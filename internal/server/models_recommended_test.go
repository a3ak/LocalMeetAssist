package server

import "testing"

// TestModelListMarksRecommended guards the recommended set the UI offers to
// install in one click: the pipeline needs exactly these models, and their
// groups decide which processing step is switched on automatically.
func TestModelListMarksRecommended(t *testing.T) {
	s, _ := newIntegrationServer(t)

	recommended := map[string]string{}
	for _, status := range s.models.Statuses() {
		if status.Recommended {
			recommended[status.ID] = status.Group
		}
	}
	want := map[string]string{
		"onnx-runtime":                    "runtime",
		"gigaam-v3-e2e-rnnt-int8":         "transcription",
		"silero-vad":                      "speech_filter",
		"diarization-segmentation":        "diarization",
		"diarization-embedding-wespeaker": "diarization",
	}
	for id, group := range want {
		if recommended[id] != group {
			t.Fatalf("model %s must be recommended for group %s, got %q (recommended: %v)", id, group, recommended[id], recommended)
		}
	}
	if len(recommended) != len(want) {
		t.Fatalf("recommended set changed: %v", recommended)
	}
}
