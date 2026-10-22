package server

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"localmeetassist/internal/model"
	"localmeetassist/internal/voice"
)

// diarizationArtifact is the subset of diarization_json needed by enrollment.
// The candidates are time codes only; no audio or biometric data is stored in
// the artifact.
type diarizationArtifact struct {
	SchemaVersion  int                    `json:"schema_version"`
	AudioSource    string                 `json:"audio_source"`
	EmbeddingModel string                 `json:"embedding_model"`
	Turns          []model.SpeakerTurn    `json:"turns"`
	Candidates     []model.VoiceCandidate `json:"candidates"`
	Centroids      map[string][]float32   `json:"centroids"`
}

// voiceEnrollmentSummary is returned to the UI. Reason and Warnings are stable
// codes translated by the Web UI.
type voiceEnrollmentSummary struct {
	Added    int      `json:"added,omitempty"`
	Replaced int      `json:"replaced,omitempty"`
	Skipped  bool     `json:"skipped,omitempty"`
	Reason   string   `json:"reason,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

type voiceEnrollmentResult struct {
	Summary  voiceEnrollmentSummary
	Added    int
	Speakers map[string]model.Speaker
	Updated  map[string]bool
}

// enrollSpeakers runs after speaker edits are committed. It never fails the
// request: a missing archive, a short sample or a model error becomes a
// warning. Enrollment is idempotent per speaker and interval.
func (s *Server) enrollSpeakers(ctx context.Context, meeting model.Meeting, segments []model.Segment, speakers []model.Speaker, manual map[string]bool, personIDs map[string]string, previousPersonIDs map[string]string) voiceEnrollmentResult {
	result := voiceEnrollmentResult{
		Speakers: make(map[string]model.Speaker, len(manual)),
		Updated:  make(map[string]bool, len(manual)),
	}
	if s.voiceprints == nil || !s.config().Diarization.RememberSpeakers {
		result.Summary.Skipped = true
		result.Summary.Reason = "disabled"
		return result
	}
	diarization, err := s.diarizationData(meeting.UID)
	if err != nil {
		result.Summary.Skipped = true
		result.Summary.Reason = "no_diarization"
		return result
	}
	source := strings.TrimSpace(diarization.AudioSource)
	if source == "" {
		source = strings.TrimSpace(meeting.Metadata["diarization_source"])
	}
	if source == "" {
		source = "system"
	}
	audioPath, err := s.voiceAudioPath(meeting, source)
	if err != nil {
		result.Summary.Skipped = true
		result.Summary.Reason = "audio_archived"
		result.Summary.Warnings = append(result.Summary.Warnings, voice.WarnAudioUnavailable)
		return result
	}
	cfg := s.config()
	for index := range speakers {
		speaker := &speakers[index]
		if !manual[speaker.ID] {
			continue
		}
		name := strings.TrimSpace(speaker.DisplayName)
		if name == "" || name == speaker.ID || strings.HasPrefix(name, "SPEAKER_") {
			continue
		}
		personID := strings.TrimSpace(personIDs[speaker.ID])
		if personID == "" {
			personID = strings.TrimSpace(previousPersonIDs[speaker.ID])
		}
		if personID == "" {
			personID = speaker.PersonID
		}
		enrolled, err := s.voiceprints.Enroll(ctx, cfg, voice.EnrollRequest{
			MeetingUID:       meeting.UID,
			SpeakerID:        speaker.ID,
			SpeakerName:      name,
			Source:           source,
			AudioPath:        audioPath,
			ExistingPersonID: personID,
			ExplicitPersonID: personIDs[speaker.ID],
			Candidates:       diarization.Candidates,
			Turns:            diarization.Turns,
			Segments:         segments,
		})
		if errors.Is(err, voice.ErrAudioUnavailable) {
			result.Summary.Skipped = true
			result.Summary.Reason = "audio_archived"
			result.Summary.Warnings = append(result.Summary.Warnings, voice.WarnAudioUnavailable)
			break
		}
		if err != nil {
			result.Summary.Warnings = append(result.Summary.Warnings, "enrollment_failed")
			s.logger.Printf("voice enrollment failed meeting_uid=%s speaker=%s error=%v", meeting.UID, speaker.ID, err)
			continue
		}
		result.Added += enrolled.Added
		result.Summary.Added += enrolled.Added
		result.Summary.Replaced += enrolled.Replaced
		result.Summary.Warnings = append(result.Summary.Warnings, enrolled.Warnings...)
		if enrolled.PersonID != "" {
			speaker.PersonID = enrolled.PersonID
			speaker.NameSource = "manual"
			speaker.MatchScore = 1
			speaker.MatchCandidates = nil
		}
		if saveErr := s.store.SaveSpeaker(*speaker); saveErr != nil {
			s.logger.Printf("voice enrollment: save speaker meeting_uid=%s speaker=%s error=%v", meeting.UID, speaker.ID, saveErr)
			continue
		}
		result.Speakers[speaker.ID] = *speaker
		result.Updated[speaker.ID] = true
	}
	result.Summary.Warnings = uniqueStrings(result.Summary.Warnings)
	s.logger.Printf("voice enrollment finished meeting_uid=%s speakers=%d added=%d skipped=%t", meeting.UID, len(manual), result.Added, result.Summary.Skipped)
	return result
}

// diarizationData reads the stored diarization result of a meeting.
func (s *Server) diarizationData(uid string) (diarizationArtifact, error) {
	artifacts, err := s.store.Artifacts(uid)
	if err != nil {
		return diarizationArtifact{}, err
	}
	for _, artifact := range artifacts {
		if artifact.Type != "diarization_json" || !s.safeManagedPath(artifact.Path) {
			continue
		}
		data, readErr := os.ReadFile(artifact.Path)
		if readErr != nil {
			return diarizationArtifact{}, readErr
		}
		var parsed diarizationArtifact
		if err := json.Unmarshal(data, &parsed); err != nil {
			return diarizationArtifact{}, err
		}
		return parsed, nil
	}
	return diarizationArtifact{}, os.ErrNotExist
}

// voiceAudioPath returns the canonical source stream used by diarization. It
// never falls back to the mixed file for a system speaker: the mixed stream
// contains other voices and would poison the profile.
func (s *Server) voiceAudioPath(meeting model.Meeting, source string) (string, error) {
	types := []string{"system_wav"}
	filename := "system.wav"
	if source == "mixed" {
		types = []string{"mixed_wav", "imported_mixed_wav"}
		filename = "mixed.wav"
	}
	artifacts, _ := s.store.Artifacts(meeting.UID)
	for _, artifactType := range types {
		for _, artifact := range artifacts {
			if artifact.Type != artifactType || !s.safeManagedPath(artifact.Path) {
				continue
			}
			if info, err := os.Stat(artifact.Path); err == nil && info.Size() > 44 {
				return artifact.Path, nil
			}
		}
	}
	path := filepath.Join(s.meetingDir(meeting), "audio", filename)
	if info, err := os.Stat(path); err == nil && info.Size() > 44 {
		return path, nil
	}
	return "", os.ErrNotExist
}

func voiceEnrollmentJobMessage(summary voiceEnrollmentSummary) string {
	if summary.Skipped {
		switch summary.Reason {
		case "audio_archived":
			return "audio is archived; voice enrollment is unavailable"
		case "disabled":
			return "speaker memory is disabled"
		case "no_diarization":
			return "no diarization result; enrollment is unavailable"
		case "":
			return ""
		default:
			return summary.Reason
		}
	}
	return strings.Join(summary.Warnings, ", ")
}
