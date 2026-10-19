package server

import (
	"strings"
	"testing"
	"time"
)

// TestAudioWarningText keeps the meeting card message short and localized, and
// makes sure the event code from the native layer maps to real text.
func TestAudioWarningText(t *testing.T) {
	at := time.Date(2026, 10, 19, 14, 32, 0, 0, time.Local)
	lost := audioWarningText("ru", "lost", at)
	if !strings.Contains(lost, "14:32") || !strings.Contains(lost, "потерян") {
		t.Fatalf("unexpected lost message: %q", lost)
	}
	restored := audioWarningText("ru", "restored", at)
	if !strings.Contains(restored, "14:32") || !strings.Contains(restored, "пересоздан") {
		t.Fatalf("unexpected restored message: %q", restored)
	}
	english := audioWarningText("en", "lost", at)
	if !strings.Contains(english, "14:32") || !strings.Contains(english, "lost") {
		t.Fatalf("unexpected english message: %q", english)
	}
	if got := audioWarningText("en", "", at); got != "" {
		t.Fatalf("an empty event must not produce a message: %q", got)
	}
}
