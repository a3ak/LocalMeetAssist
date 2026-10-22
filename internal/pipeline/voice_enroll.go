package pipeline

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"localmeetassist/internal/model"
	"localmeetassist/internal/voice"
)

// manualSpeakerIDs returns the speakers the user named by hand. Automatic names
// are deliberately excluded: they never add voice samples.
func manualSpeakerIDs(speakers []model.Speaker) []string {
	ids := make([]string, 0, len(speakers))
	for _, speaker := range speakers {
		if speaker.ID == "microphone_owner" || speaker.Source == "microphone" {
			continue
		}
		if isManualSpeaker(speaker) {
			ids = append(ids, speaker.ID)
		}
	}
	sort.Strings(ids)
	return ids
}

func diarizationSource(m model.Meeting, data diarizationVoiceData) string {
	source := strings.TrimSpace(data.AudioSource)
	if source == "" {
		source = strings.TrimSpace(m.Metadata["diarization_source"])
	}
	if source == "" {
		source = "system"
	}
	return source
}

// runEnrollmentStage is shared by the automatic chain and the manual stage. It
// reports skip conditions through the job row and returns an error only for a
// real failure.
func (r *Runner) runEnrollmentStage(ctx context.Context, m *model.Meeting, data diarizationVoiceData) (int, error) {
	r.job(m.UID, "enrolling", "processing", "")
	if !r.cfg.Diarization.RememberSpeakers {
		r.job(m.UID, "enrolling", "skipped", "speaker memory is disabled")
		return 0, nil
	}
	if r.voiceprints == nil {
		r.job(m.UID, "enrolling", "skipped", "voice service is unavailable")
		return 0, nil
	}
	if len(data.Turns) == 0 && len(data.Candidates) == 0 {
		r.job(m.UID, "enrolling", "skipped", "no diarization result; enrollment is unavailable")
		return 0, nil
	}
	speakers, err := r.store.Speakers(m.UID)
	if err != nil {
		r.job(m.UID, "enrolling", "failed", err.Error())
		return 0, err
	}
	if len(manualSpeakerIDs(speakers)) == 0 {
		r.job(m.UID, "enrolling", "skipped", "no speakers named by hand")
		return 0, nil
	}
	audioPath, err := r.diarizationAudioPath(*m, diarizationSource(*m, data))
	if err != nil {
		r.job(m.UID, "enrolling", "skipped", "audio is archived; enrollment is unavailable")
		return 0, nil
	}
	added, warnings, err := r.runEnrollment(ctx, m, data, audioPath)
	if errors.Is(err, voice.ErrAudioUnavailable) {
		r.job(m.UID, "enrolling", "skipped", "audio is archived; enrollment is unavailable")
		return added, nil
	}
	if err != nil {
		r.job(m.UID, "enrolling", "failed", err.Error())
		return added, err
	}
	if added == 0 && len(warnings) > 0 {
		r.job(m.UID, "enrolling", "skipped", strings.Join(warnings, ", "))
	} else {
		r.job(m.UID, "enrolling", "completed", "")
	}
	r.logger.Printf("speaker enrollment completed uid=%s added=%d warnings=%v", m.UID, added, warnings)
	return added, nil
}

func (r *Runner) runEnrollment(ctx context.Context, m *model.Meeting, data diarizationVoiceData, audioPath string) (int, []string, error) {
	speakers, err := r.store.Speakers(m.UID)
	if err != nil {
		return 0, nil, err
	}
	segments, _ := r.store.TranscriptSegments(m.UID)
	ids := manualSpeakerIDs(speakers)
	if len(ids) == 0 {
		return 0, nil, nil
	}
	byID := make(map[string]*model.Speaker, len(speakers))
	for index := range speakers {
		byID[speakers[index].ID] = &speakers[index]
	}
	source := diarizationSource(*m, data)
	added := 0
	var warnings []string
	for _, id := range ids {
		speaker := byID[id]
		if speaker == nil {
			continue
		}
		name := strings.TrimSpace(speaker.DisplayName)
		if name == "" || name == speaker.ID || strings.HasPrefix(name, "SPEAKER_") {
			continue
		}
		result, err := r.voiceprints.Enroll(ctx, r.cfg, voice.EnrollRequest{
			MeetingUID:       m.UID,
			SpeakerID:        speaker.ID,
			SpeakerName:      name,
			Source:           source,
			AudioPath:        audioPath,
			ExistingPersonID: speaker.PersonID,
			Candidates:       data.Candidates,
			Turns:            data.Turns,
			Segments:         segments,
		})
		if err != nil {
			if errors.Is(err, voice.ErrAudioUnavailable) {
				return added, warnings, err
			}
			warnings = append(warnings, "enrollment_failed")
			r.logger.Printf("speaker enrollment failed uid=%s speaker=%s error=%v", m.UID, speaker.ID, err)
			continue
		}
		added += result.Added
		warnings = append(warnings, result.Warnings...)
		if result.PersonID != "" {
			speaker.PersonID = result.PersonID
			speaker.NameSource = "manual"
			speaker.MatchScore = 1
			speaker.MatchCandidates = nil
		}
		if err := r.store.SaveSpeaker(*speaker); err != nil {
			warnings = append(warnings, "save_failed")
			r.logger.Printf("speaker enrollment: save speaker uid=%s speaker=%s error=%v", m.UID, speaker.ID, err)
		}
	}
	return added, warnings, nil
}

// retryEnrollment re-runs voice enrollment for a meeting whose speakers were
// named by hand. It is also the post-summary stage of the automatic chain.
func (r *Runner) retryEnrollment(ctx context.Context, uid string) error {
	m, err := r.store.Meeting(uid)
	if err != nil {
		return err
	}
	m.Status, m.ProcessingStage, m.LastError, m.UpdatedAt = "processing", "enrolling", "", time.Now()
	_ = r.store.SaveMeeting(m)
	data, err := r.loadDiarizationVoiceData(uid)
	if err != nil {
		r.job(uid, "enrolling", "skipped", "no diarization result; enrollment is unavailable")
		m.Status, m.ProcessingStage, m.UpdatedAt = "completed", "completed", time.Now()
		return r.store.SaveMeeting(m)
	}
	if _, err := r.runEnrollmentStage(ctx, &m, data); err != nil {
		m.Status, m.ProcessingStage, m.LastError, m.UpdatedAt = "warning", "enrolling", err.Error(), time.Now()
		return r.store.SaveMeeting(m)
	}
	m.Status, m.ProcessingStage, m.LastError, m.UpdatedAt = "completed", "completed", "", time.Now()
	return r.store.SaveMeeting(m)
}
