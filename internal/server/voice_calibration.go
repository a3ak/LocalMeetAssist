package server

import (
	"math"
	"net/http"
	"sort"

	"localmeetassist/internal/model"
)

const (
	calibrationAutoPrecision    = 0.95
	calibrationSuggestPrecision = 0.90
	calibrationMinPositives     = 10
	calibrationMinNegatives     = 5
)

type calibrationPoint struct {
	score    float64
	positive bool
}

type calibrationResponse struct {
	Reliable         bool    `json:"reliable"`
	Positives        int     `json:"positives"`
	Negatives        int     `json:"negatives"`
	Meetings         int     `json:"meetings"`
	Auto             float64 `json:"auto"`
	Suggest          float64 `json:"suggest"`
	CurrentAuto      float64 `json:"current_auto"`
	CurrentSuggest   float64 `json:"current_suggest"`
	TargetPrecision  float64 `json:"target_precision"`
	SuggestPrecision float64 `json:"suggest_precision"`
}

// voiceCalibration derives thresholds from explicit user decisions:
// confirmations and "not this person" rejections, plus suggestions that were
// left unconfirmed. Auto-applied names are not used as their own ground truth.
func (s *Server) voiceCalibration(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		methodNotAllowed(w)
		return
	}
	meetings, err := s.store.ListMeetings(nil, nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	points := make([]calibrationPoint, 0)
	seenMeetings := make(map[string]bool)
	for _, meeting := range meetings {
		if meeting.Status != "completed" && meeting.Status != "warning" {
			continue
		}
		speakers, err := s.store.Speakers(meeting.UID)
		if err != nil {
			continue
		}
		added := false
		for _, speaker := range speakers {
			candidates := uniqueCandidates(speaker.MatchCandidates)
			switch speaker.NameSource {
			case "auto":
				// The pipeline's own automatic pick is not an independent label.
				continue
			case "manual":
				if len(candidates) > 0 {
					for _, candidate := range candidates {
						if candidate.Score <= 0 || candidate.Score > 1 {
							continue
						}
						points = append(points, calibrationPoint{score: candidate.Score, positive: candidate.PersonID == speaker.PersonID && speaker.PersonID != ""})
						added = true
					}
				} else if speaker.PersonID != "" && speaker.MatchScore > 0 && speaker.MatchScore <= 1 {
					points = append(points, calibrationPoint{score: speaker.MatchScore, positive: true})
					added = true
				}
			default:
				// A candidate that was neither confirmed nor linked is an ignored
				// suggestion: the user explicitly asked to count it as negative.
				if len(candidates) > 0 && speaker.PersonID == "" {
					for _, candidate := range candidates {
						if candidate.Score > 0 && candidate.Score <= 1 {
							points = append(points, calibrationPoint{score: candidate.Score, positive: false})
							added = true
						}
					}
				}
			}
		}
		if added {
			seenMeetings[meeting.UID] = true
		}
	}
	response := calibrationResponse{
		Meetings:         len(seenMeetings),
		CurrentAuto:      s.config().Diarization.VoiceAutoThreshold,
		CurrentSuggest:   s.config().Diarization.VoiceSuggestThreshold,
		TargetPrecision:  calibrationAutoPrecision,
		SuggestPrecision: calibrationSuggestPrecision,
	}
	for _, point := range points {
		if point.positive {
			response.Positives++
		} else {
			response.Negatives++
		}
	}
	response.Auto = calibrationThreshold(points, calibrationAutoPrecision)
	response.Suggest = calibrationThreshold(points, calibrationSuggestPrecision)
	if response.Suggest <= 0 {
		response.Suggest = response.CurrentSuggest
	}
	if response.Auto <= 0 {
		response.Auto = response.CurrentAuto
	}
	if response.Suggest > response.Auto {
		response.Suggest = response.Auto
	}
	if response.Suggest == response.Auto {
		response.Suggest = math.Max(0.50, response.Auto-0.05)
	}
	response.Reliable = response.Positives >= calibrationMinPositives && response.Negatives >= calibrationMinNegatives
	writeJSON(w, http.StatusOK, response)
}

func uniqueCandidates(candidates []model.SpeakerMatch) []model.SpeakerMatch {
	seen := make(map[string]bool, len(candidates))
	out := make([]model.SpeakerMatch, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.PersonID == "" || seen[candidate.PersonID] {
			continue
		}
		seen[candidate.PersonID] = true
		out = append(out, candidate)
	}
	return out
}

// calibrationThreshold finds the lowest score that still keeps precision at or
// above the target. Scores are grouped so a threshold never splits equal
// scores. Returns 0 when there is no qualifying group.
func calibrationThreshold(points []calibrationPoint, target float64) float64 {
	if len(points) == 0 {
		return 0
	}
	sort.SliceStable(points, func(i, j int) bool { return points[i].score > points[j].score })
	positives, negatives := 0, 0
	best := 0.0
	for index := 0; index < len(points); {
		score := points[index].score
		for index < len(points) && points[index].score == score {
			if points[index].positive {
				positives++
			} else {
				negatives++
			}
			index++
		}
		if positives == 0 {
			continue
		}
		precision := float64(positives) / float64(positives+negatives)
		if precision+1e-9 < target {
			continue
		}
		best = math.Floor(score*100) / 100
	}
	return best
}
