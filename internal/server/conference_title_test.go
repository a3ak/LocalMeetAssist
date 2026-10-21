package server

import (
	"strings"
	"testing"
)

// TestConferenceTitleTemplate renders the meeting title from the configured
// template and keeps the default title when the application is unknown.
func TestConferenceTitleTemplate(t *testing.T) {
	s := newBrowserServer(t, "auto")
	cfg := s.config()
	cfg.Storage.DateFormat = "2006-01-02"
	cfg.Storage.TimeFormat = "15:04"
	cfg.ConferenceDetection.TitleTemplate = "{{app}} — {{backend}} — {{date}} {{time}}"
	s.setConfig(cfg)

	title := s.buildConferenceTitle("Zoom", "CoreAudio")
	if !strings.HasPrefix(title, "Zoom — CoreAudio — ") {
		t.Fatalf("unexpected title: %q", title)
	}
	if strings.Contains(title, "{{") {
		t.Fatalf("placeholders were not replaced: %q", title)
	}
	if got := s.buildConferenceTitle("", "CoreAudio"); got != "" {
		t.Fatalf("an unknown application must keep the default title, got %q", got)
	}

	cfg.ConferenceDetection.TitleTemplate = "Встреча: {{app}}"
	s.setConfig(cfg)
	if got := s.buildConferenceTitle("Teams", "WASAPI"); got != "Встреча: Teams" {
		t.Fatalf("custom template was not applied: %q", got)
	}
}
