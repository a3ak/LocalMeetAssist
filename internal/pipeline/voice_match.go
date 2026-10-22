package pipeline

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"localmeetassist/internal/atomicfile"
	"localmeetassist/internal/model"
	"localmeetassist/internal/transcript"
)

// diarizationVoiceData is the part of diarization_json needed by the speaker
// recognition and enrollment stages.
type diarizationVoiceData struct {
	AudioSource    string                 `json:"audio_source"`
	EmbeddingModel string                 `json:"embedding_model"`
	Turns          []model.SpeakerTurn    `json:"turns"`
	Candidates     []model.VoiceCandidate `json:"candidates"`
	Centroids      map[string][]float32   `json:"centroids"`
}

// speakerMatch is the pipeline-side representation of a profile match. It is
// applied to the stored Speaker cards by applySpeakerMatches.
type speakerMatch struct {
	PersonID   string
	Name       string
	Score      float64
	Candidates []model.SpeakerMatch
	Auto       bool
}

// matchVoiceProfiles compares the cluster centroids with the saved profiles
// when voice memory is enabled. Matching is best-effort: a failure never fails
// the meeting.
func (r *Runner) matchVoiceProfiles(centroids map[string][]float32) map[string]speakerMatch {
	if r.voiceprints == nil || !r.cfg.Diarization.RememberSpeakers || len(centroids) == 0 {
		return nil
	}
	results := r.voiceprints.Match(r.cfg, centroids)
	if len(results) == 0 {
		return nil
	}
	out := make(map[string]speakerMatch, len(results))
	for speaker, result := range results {
		out[speaker] = speakerMatch{
			PersonID:   result.PersonID,
			Name:       result.Name,
			Score:      result.Score,
			Candidates: result.Candidates,
			Auto:       result.Auto,
		}
		if result.Auto {
			r.logger.Printf("voice match: automatic name meeting_speaker=%s person=%s score=%.3f", speaker, result.Name, result.Score)
		}
	}
	return out
}

// applySpeakerMatches updates the stored speaker cards after recognition. A
// manual name is never changed. An automatic name that no longer has a match is
// returned to SPEAKER_NN; suggestions are kept separately.
func (r *Runner) applySpeakerMatches(m *model.Meeting, matches map[string]speakerMatch) error {
	if len(matches) == 0 {
		return nil
	}
	speakers, err := r.store.Speakers(m.UID)
	if err != nil {
		return err
	}
	segments, err := r.store.TranscriptSegments(m.UID)
	if err != nil {
		segments = nil
	}
	changed := false
	for index := range speakers {
		speaker := &speakers[index]
		if speaker.ID == "microphone_owner" || speaker.Source == "microphone" || isManualSpeaker(*speaker) {
			continue
		}
		match, ok := matches[speaker.ID]
		switch {
		case ok && match.Auto:
			if match.Name != "" {
				speaker.DisplayName = match.Name
			}
			speaker.PersonID = match.PersonID
			speaker.MatchScore = match.Score
			speaker.MatchCandidates = match.Candidates
			speaker.NameSource = "auto"
			changed = true
		case ok && len(match.Candidates) > 0:
			speaker.MatchCandidates = match.Candidates
			speaker.MatchScore = match.Score
			if speaker.NameSource == "auto" {
				resetAutomaticName(speaker)
			}
			changed = true
		case speaker.NameSource == "auto":
			resetAutomaticName(speaker)
			changed = true
		case len(speaker.MatchCandidates) > 0 || speaker.MatchScore != 0:
			speaker.MatchCandidates = nil
			speaker.MatchScore = 0
			changed = true
		}
	}
	if !changed {
		return nil
	}
	if len(segments) > 0 {
		ordered := append([]model.Segment(nil), segments...)
		sort.SliceStable(ordered, func(i, j int) bool {
			if ordered[i].StartMS == ordered[j].StartMS {
				return ordered[i].EndMS < ordered[j].EndMS
			}
			return ordered[i].StartMS < ordered[j].StartMS
		})
		names := make(map[string]string, len(speakers))
		for _, speaker := range speakers {
			names[speaker.ID] = speaker.DisplayName
		}
		m.Transcript = transcript.Format(ordered, names)
	}
	m.SpeakerCount = len(speakers)
	if strings.TrimSpace(m.Summary) != "" {
		if m.Metadata == nil {
			m.Metadata = make(map[string]string)
		}
		m.Metadata["summary_stale"] = "true"
	}
	m.UpdatedAt = time.Now()
	if err := r.store.ReplaceSpeakerState(*m, segments, speakers); err != nil {
		return err
	}
	r.rewriteTranscriptArtifact(*m)
	return nil
}

func resetAutomaticName(speaker *model.Speaker) {
	speaker.DisplayName = speaker.ID
	speaker.PersonID = ""
	speaker.MatchScore = 0
	speaker.MatchCandidates = nil
	speaker.NameSource = ""
}

// isManualSpeaker treats a custom display name from an older version as a
// manual edit even when NameSource was not stored yet.
func isManualSpeaker(speaker model.Speaker) bool {
	if speaker.NameSource == "manual" {
		return true
	}
	if speaker.NameSource != "" {
		return false
	}
	name := strings.TrimSpace(speaker.DisplayName)
	return name != "" && name != speaker.ID && !strings.HasPrefix(name, "SPEAKER_") && speaker.ID != "microphone_owner"
}

func (r *Runner) rewriteTranscriptArtifact(m model.Meeting) {
	artifacts, err := r.store.Artifacts(m.UID)
	if err != nil {
		return
	}
	for _, artifact := range artifacts {
		if artifact.Type != "transcript" {
			continue
		}
		if err := writeTextArtifact(artifact.Path, m.UID, "transcript", m.Transcript); err != nil {
			r.logger.Printf("recognition: rewrite transcript artifact: %v", err)
			continue
		}
		if _, err := r.addArtifact(m.UID, "transcript", artifact.Path, "text/markdown", "generated"); err != nil {
			r.logger.Printf("recognition: register transcript artifact: %v", err)
		}
	}
}

// loadDiarizationVoiceData reads centroids, turns and clean candidates from the
// stored diarization result.
func (r *Runner) loadDiarizationVoiceData(uid string) (diarizationVoiceData, error) {
	artifacts, err := r.store.Artifacts(uid)
	if err != nil {
		return diarizationVoiceData{}, err
	}
	for _, artifact := range artifacts {
		if artifact.Type != "diarization_json" {
			continue
		}
		raw, err := os.ReadFile(artifact.Path)
		if err != nil {
			return diarizationVoiceData{}, err
		}
		var data diarizationVoiceData
		if err := json.Unmarshal(raw, &data); err != nil {
			return diarizationVoiceData{}, err
		}
		return data, nil
	}
	return diarizationVoiceData{}, os.ErrNotExist
}

// diarizationAudioPath returns the canonical source used by diarization. It
// never falls back to mixed for a system speaker.
func (r *Runner) diarizationAudioPath(m model.Meeting, source string) (string, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		source = strings.TrimSpace(m.Metadata["diarization_source"])
	}
	types := []string{"system_wav"}
	filename := "system.wav"
	if source == "mixed" {
		types = []string{"mixed_wav", "imported_mixed_wav"}
		filename = "mixed.wav"
	}
	artifacts, _ := r.store.Artifacts(m.UID)
	for _, artifactType := range types {
		for _, artifact := range artifacts {
			if artifact.Type != artifactType {
				continue
			}
			if info, err := os.Stat(artifact.Path); err == nil && info.Size() > 44 {
				return artifact.Path, nil
			}
		}
	}
	path := filepath.Join(r.meetingDir(m), "audio", filename)
	if info, err := os.Stat(path); err == nil && info.Size() > 44 {
		return path, nil
	}
	return "", os.ErrNotExist
}

// runRecognition is shared by the automatic chain and the manual stage. It
// never marks the meeting as failed; the caller decides how to surface an
// error.
func (r *Runner) runRecognition(ctx context.Context, m *model.Meeting, data diarizationVoiceData) error {
	if !r.cfg.Diarization.RememberSpeakers {
		r.job(m.UID, "matching", "skipped", "speaker memory is disabled")
		return nil
	}
	if r.voiceprints == nil {
		r.job(m.UID, "matching", "skipped", "voice service is unavailable")
		return nil
	}
	centroids := data.Centroids
	if data.EmbeddingModel != "" && data.EmbeddingModel != r.cfg.Diarization.EmbeddingModel {
		// Centroids from another embedding model are not comparable with the
		// current profiles. Try to recompute them from the WAV.
		centroids = nil
	}
	if len(centroids) == 0 {
		// Meetings processed by an older version have no stored centroids. If
		// the WAV is still present, compute them from the clean intervals
		// without re-running segmentation and clustering.
		audioPath, err := r.diarizationAudioPath(*m, data.AudioSource)
		if err != nil {
			r.job(m.UID, "matching", "skipped", "no stored speaker embeddings; re-run diarization while audio is available")
			return nil
		}
		speakers, _ := r.store.Speakers(m.UID)
		speakerIDs := make([]string, 0, len(speakers))
		for _, speaker := range speakers {
			if speaker.ID != "microphone_owner" {
				speakerIDs = append(speakerIDs, speaker.ID)
			}
		}
		computed, err := r.voiceprints.Centroids(ctx, r.cfg, audioPath, speakerIDs, data.Candidates, data.Turns)
		if err != nil {
			r.job(m.UID, "matching", "skipped", "could not compute speaker embeddings: "+err.Error())
			return nil
		}
		centroids = computed
		// Persist the computed centroids so a later run still works after the
		// source WAV is archived.
		r.persistCentroids(m.UID, centroids)
	}
	matches := r.matchVoiceProfiles(centroids)
	if len(matches) == 0 {
		r.job(m.UID, "matching", "completed", "")
		return nil
	}
	if err := r.applySpeakerMatches(m, matches); err != nil {
		r.job(m.UID, "matching", "failed", err.Error())
		return err
	}
	r.job(m.UID, "matching", "completed", "")
	return nil
}

// persistCentroids writes computed centroids back into diarization_json.
func (r *Runner) persistCentroids(uid string, centroids map[string][]float32) {
	if len(centroids) == 0 {
		return
	}
	artifacts, err := r.store.Artifacts(uid)
	if err != nil {
		return
	}
	for _, artifact := range artifacts {
		if artifact.Type != "diarization_json" {
			continue
		}
		raw, err := os.ReadFile(artifact.Path)
		if err != nil {
			return
		}
		var data map[string]any
		if err := json.Unmarshal(raw, &data); err != nil {
			return
		}
		data["centroids"] = centroids
		data["embedding_model"] = r.cfg.Diarization.EmbeddingModel
		payload, err := json.MarshalIndent(data, "", "  ")
		if err != nil {
			return
		}
		if err := atomicfile.Write(artifact.Path, payload, 0o600); err != nil {
			r.logger.Printf("recognition: persist centroids: %v", err)
			return
		}
		if _, err := r.addArtifact(uid, "diarization_json", artifact.Path, "application/json", "generated"); err != nil {
			r.logger.Printf("recognition: register persisted centroids: %v", err)
		}
		return
	}
}

// retryRecognition re-runs speaker recognition against the current profiles.
func (r *Runner) retryRecognition(ctx context.Context, uid string) error {
	m, err := r.store.Meeting(uid)
	if err != nil {
		return err
	}
	m.Status, m.ProcessingStage, m.LastError, m.UpdatedAt = "processing", "matching", "", time.Now()
	_ = r.store.SaveMeeting(m)
	r.job(uid, "matching", "processing", "")
	data, err := r.loadDiarizationVoiceData(uid)
	if err != nil {
		r.job(uid, "matching", "skipped", "no diarization result; run diarization first")
		m.Status, m.ProcessingStage, m.UpdatedAt = "completed", "completed", time.Now()
		return r.store.SaveMeeting(m)
	}
	if err := r.runRecognition(ctx, &m, data); err != nil {
		m.Status, m.ProcessingStage, m.LastError, m.UpdatedAt = "warning", "matching", err.Error(), time.Now()
		return r.store.SaveMeeting(m)
	}
	m.Status, m.ProcessingStage, m.LastError, m.UpdatedAt = "completed", "completed", "", time.Now()
	return r.store.SaveMeeting(m)
}
