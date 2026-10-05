package modelmanager

import "strings"

// modelDescriptionsEN holds the English description of every built-in model.
// The Russian text in manager.go stays the source of truth and is used as a
// fallback whenever a translation is missing.
var modelDescriptionsEN = map[string]string{
	"onnx-runtime":                    "Native ONNX execution environment for this OS and architecture. Downloaded once; no CLI applications are used.",
	"whisper-runtime":                 "Native whisper.cpp library. LocalMeetAssist calls it directly; whisper-cli is neither installed nor launched.",
	"gigaam-v3-e2e-rnnt-int8":         "Recommended Russian model: punctuation and text normalization, RNNT decoding, about 227 MB. It processes the detected VAD speech fragments directly inside LocalMeetAssist.",
	"whisper-small-q5":                "Compact multilingual Whisper: faster than medium, about 181 MB. Good for quick drafts and weaker CPUs.",
	"whisper-medium-q5":               "More accurate multilingual Whisper, about 514 MB. A good balance for difficult speech and terminology.",
	"whisper-large-v3-turbo-q5":       "The highest quality of the offered Whisper models at a noticeably lower cost than full large-v3; about 574 MB.",
	"silero-vad":                      "Finds speech and splits a long meeting into fragments of up to 20 seconds. Reduces repetitions and invented text over silence.",
	"diarization-segmentation":        "Detects local speech tracks and voice overlaps in 10-second windows. Works without Python or a Hugging Face token.",
	"diarization-embedding-wespeaker": "Builds 256-dimensional voice embeddings for global clustering across the whole meeting. The meeting participant count is used as an exact constraint.",
}

// english reports whether the configured interface language selects English.
func english(language string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(language)), "en")
}
