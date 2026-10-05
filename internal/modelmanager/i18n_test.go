package modelmanager

import (
	"io"
	"log"
	"testing"

	"localmeetassist/internal/config"
)

func containsCyrillic(value string) bool {
	for _, r := range value {
		if r >= 0x0400 && r <= 0x04FF {
			return true
		}
	}
	return false
}

// TestModelDescriptionsAreLocalizedWhenEnglish guards the English overlay: no
// model description may keep Russian text when the interface is English.
func TestModelDescriptionsAreLocalizedWhenEnglish(t *testing.T) {
	cfg := config.Defaults()
	cfg.App.Language = "en"
	statuses := New(cfg, log.New(io.Discard, "", 0)).Statuses()
	if len(statuses) == 0 {
		t.Fatal("expected at least one model")
	}
	for _, status := range statuses {
		if containsCyrillic(status.Description) {
			t.Fatalf("model %s description is not localized: %q", status.ID, status.Description)
		}
	}
}

// TestModelDescriptionsStayRussianByDefault checks the inline Russian text is
// still used when the interface language is Russian.
func TestModelDescriptionsStayRussianByDefault(t *testing.T) {
	cfg := config.Defaults()
	found := false
	for _, status := range New(cfg, log.New(io.Discard, "", 0)).Statuses() {
		if status.ID == "onnx-runtime" {
			found = containsCyrillic(status.Description)
		}
	}
	if !found {
		t.Fatal("expected the Russian ONNX runtime description by default")
	}
}
