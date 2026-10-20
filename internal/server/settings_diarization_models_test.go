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

	embedding := choices["diarization.embedding_model"]
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

	// Конфиг хранит относительный путь: список обязан узнавать в нём обычную
	// модель, а не показывать лишний «Свой путь».
	cfg.Diarization.EmbeddingModel = "./models/diarization/wespeaker-resnet34-lm/voxceleb_resnet34_LM.onnx"
	relative := server.diarizationModelChoices(cfg)
	matched := false
	for _, option := range relative["diarization.embedding_model"] {
		if strings.HasPrefix(option.Label, "Свой путь") {
			t.Fatalf("a relative path to a known model produced a custom option: %+v", option)
		}
		if strings.Contains(option.Label, "ResNet34") {
			matched = true
			if option.Value != cfg.Diarization.EmbeddingModel {
				t.Fatalf("the option value must match the stored path: %q != %q", option.Value, cfg.Diarization.EmbeddingModel)
			}
		}
	}
	if !matched {
		t.Fatalf("the ResNet34 option is missing for a relative path: %+v", relative["diarization.embedding_model"])
	}

	// Транскрибация и VAD тоже списки: GigaAM, Whisper и Silero видны, чужие
	// модели попадают в список как «Свой путь».
	transcription := choices["transcription.model_path"]
	if len(transcription) < 4 {
		t.Fatalf("expected GigaAM and Whisper models, got %+v", transcription)
	}
	vad := choices["transcription.vad_model_path"]
	if len(vad) != 1 || !strings.Contains(vad[0].Label, "Silero") {
		t.Fatalf("unexpected VAD choices: %+v", vad)
	}
	cfg.Transcription.ModelPath = filepath.Join(dir, "custom-asr", "model.onnx")
	customASR := server.diarizationModelChoices(cfg)
	foundCustom := false
	for _, option := range customASR["transcription.model_path"] {
		if option.Value == cfg.Transcription.ModelPath && strings.HasPrefix(option.Label, "Свой путь") {
			foundCustom = true
		}
	}
	if !foundCustom {
		t.Fatalf("a custom transcription model must be selectable: %+v", customASR["transcription.model_path"])
	}

	// Произвольный путь не теряется при сохранении формы.
	custom := filepath.Join(dir, "custom.onnx")
	cfg.Diarization.EmbeddingModel = custom
	customChoices := server.diarizationModelChoices(cfg)
	found := false
	for _, option := range customChoices["diarization.embedding_model"] {
		if option.Value == custom && strings.HasPrefix(option.Label, "Свой путь") {
			found = true
		}
	}
	if !found {
		t.Fatalf("a custom path must stay selectable: %+v", customChoices["diarization.embedding_model"])
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

// TestTranscriptionEngineFollowsModel keeps the model and its engine a pair:
// choosing Whisper in the list must switch the engine, otherwise recognition
// would use the wrong runtime.
func TestTranscriptionEngineFollowsModel(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.ConfigFile = filepath.Join(dir, "config.toml")
	config.ResolvePaths(&cfg, cfg.ConfigFile)
	server := &Server{models: modelmanager.New(cfg, log.New(io.Discard, "", 0))}

	cases := map[string]string{
		"./models/whisper/ggml-small-q5_1.bin":     "whispercpp-native",
		"./models/asr/gigaam-v3-e2e-rnnt-int8":     "gigaam-onnx",
		"./models/custom/unknown-model/model.onnx": "",
	}
	for path, want := range cases {
		if got := server.transcriptionEngineForPath(cfg, path); got != want {
			t.Fatalf("engine for %q = %q, want %q", path, got, want)
		}
	}
}
