package pipeline

import (
	"context"
	"strings"

	"localmeetassist/internal/model"
)

// Transcriber converts one audio file into timestamped segments.
type Transcriber interface {
	Transcribe(context.Context, string, string) ([]model.Segment, error)
}

type mockTranscriber struct{}

// Transcribe is the test implementation of Transcriber.
func (mockTranscriber) Transcribe(_ context.Context, _ string, source string) ([]model.Segment, error) {
	return []model.Segment{{StartMS: 0, EndMS: 1000, Source: source, Text: "Test fragment " + source}}, nil
}

func tail(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) <= limit {
		return value
	}
	return value[len(value)-limit:]
}
