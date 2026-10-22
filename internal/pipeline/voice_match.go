package pipeline

import (
	"localmeetassist/internal/model"
)

// speakerMatch is the pipeline-side representation of a profile match. It is
// applied to the generated Speaker cards before they are stored.
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
