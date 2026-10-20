package server

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localmeetassist/internal/config"
	"localmeetassist/internal/modelmanager"
)

// TestDiarizationModelChoices lists the known models as a select, greys out the
// ones that are not downloaded and keeps a custom path configured by hand.
func TestDiarizationModelChoices(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.ConfigFile = filepath.Join(dir, "config.toml")
	config.ResolvePaths(&cfg, cfg.ConfigFile)

	resnet := filepath.Join(dir, "models", "diarization", "wespeaker-resnet34-lm", "voxceleb_resnet34_LM.onnx")
	segmentation := filepath.Join(dir, "models", "diarization", "pyannote-segmentation-3", "model.onnx")
	for _, path := range []string{resnet, segmentation} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("model"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cfg.Diarization.EmbeddingModel = resnet
	cfg.Diarization.SegmentationModel = segmentation

	server := &Server{models: modelmanager.New(cfg, log.New(io.Discard, "", 0))}
	choices := server.diarizationModelChoices(cfg)

	embedding := choices["embedding"]
	if len(embedding) != 2 {
		t.Fatalf("expected both embedding models, got %+v", embedding)
	}
	for _, option := range embedding {
		if option.Value == resnet {
			if option.Disabled || !strings.Contains(option.Label, "ResNet34") {
				t.Fatalf("the downloaded model must be selectable: %+v", option)
			}
		}
		if strings.Contains(option.Label, "CAM++") && (!option.Disabled || !strings.Contains(option.Label, "не скачана")) {
			t.Fatalf("the missing model must be greyed out: %+v", option)
		}
	}

	// Поле остаётся select и сохраняет текущее значение.
	groups := settingsSchema(cfg, choices)
	field := findSettingField(t, groups, "diarization.embedding_model")
	if field.Kind != "select" || field.Value != resnet {
		t.Fatalf("unexpected embedding field: %+v", field)
	}

	// Произвольный путь не теряется при сохранении формы.
	custom := filepath.Join(dir, "custom.onnx")
	cfg.Diarization.EmbeddingModel = custom
	customChoices := server.diarizationModelChoices(cfg)
	found := false
	for _, option := range customChoices["embedding"] {
		if option.Value == custom && strings.HasPrefix(option.Label, "Свой путь") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a custom path must stay selectable: %+v", customChoices["embedding"])
	}
}

// findSettingField returns one field of the settings schema by key.
func findSettingField(t *testing.T, groups []settingGroup, key string) settingField {
	t.Helper()
	for _, group := range groups {
		for _, field := range group.Fields {
			if field.Key == key {
				return field
			}
		}
	}
	t.Fatalf("setting %q is not listed", key)
	return settingField{}
}
