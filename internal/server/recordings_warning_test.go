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
	lost := audioWarningText("ru", "system", "lost", at)
	if !strings.Contains(lost, "14:32") || !strings.Contains(lost, "потерян") {
		t.Fatalf("unexpected lost message: %q", lost)
	}
	restored := audioWarningText("ru", "system", "restored", at)
	if !strings.Contains(restored, "14:32") || !strings.Contains(restored, "пересоздан") {
		t.Fatalf("unexpected restored message: %q", restored)
	}
	english := audioWarningText("en", "system", "lost", at)
	if !strings.Contains(english, "14:32") || !strings.Contains(english, "lost") {
		t.Fatalf("unexpected english message: %q", english)
	}
	if got := audioWarningText("en", "system", "", at); got != "" {
		t.Fatalf("an empty event must not produce a message: %q", got)
	}
	microphone := audioWarningText("ru", "microphone", "lost", at)
	if !strings.Contains(microphone, "Микрофон потерян") {
		t.Fatalf("unexpected microphone message: %q", microphone)
	}
	microphoneRestored := audioWarningText("en", "microphone", "restored", at)
	if !strings.Contains(microphoneRestored, "microphone was recreated") {
		t.Fatalf("unexpected microphone message: %q", microphoneRestored)
	}
}
