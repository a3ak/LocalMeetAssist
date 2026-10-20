package modelmanager

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"localmeetassist/internal/config"
)

// findStatus returns one model card by id.
func findStatus(t *testing.T, manager *Manager, id string) Status {
	t.Helper()
	for _, status := range manager.Statuses() {
		if status.ID == id {
			return status
		}
	}
	t.Fatalf("model %q is not listed", id)
	return Status{}
}

// TestLocalSummaryCardsApplyTheLocalEngine guards the wiring between the model
// cards and the configuration: the card of a local minutes model has to be
// selectable, apply into summary.model_path and switch summary.engine.
func TestLocalSummaryCardsApplyTheLocalEngine(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.ConfigFile = filepath.Join(dir, "config.toml")
	cfg.Inference.WhisperRuntimePath = filepath.Join(dir, "runtime", "whisper.cpp")
	cfg.Summary.Engine = config.EngineLocalLlama
	manager := New(cfg, log.New(os.Stderr, "", 0))

	modelPath := filepath.Join(dir, "models", "llm", "Qwen3.5-4B-Q4_K_M.gguf")
	cfg.Summary.ModelPath = modelPath
	manager = New(cfg, log.New(os.Stderr, "", 0))

	card := findStatus(t, manager, "qwen3-5-4b-q4")
	if !card.Selectable {
		t.Fatal("the local minutes model is not selectable")
	}
	if card.SettingKey != "summary.model_path" {
		t.Fatalf("the card applies %q instead of summary.model_path", card.SettingKey)
	}
	if card.Engine != config.EngineLocalLlama {
		t.Fatalf("the card sets engine %q", card.Engine)
	}
	if !card.Selected {
		t.Fatal("the applied local model is not marked as selected")
	}
	if card.Group != "summarization" {
		t.Fatalf("the card is in group %q", card.Group)
	}

	// A network endpoint means no local card is applied.
	cfg.Summary.Engine = config.EngineOpenAI
	manager = New(cfg, log.New(os.Stderr, "", 0))
	if findStatus(t, manager, "qwen3-5-4b-q4").Selected {
		t.Fatal("a local model is marked as applied while the endpoint engine is selected")
	}
}

// TestLlamaRuntimePathMatchesSpec guards the single source of the runtime
// location: the summarizer loads the file the runtime card installs.
func TestLlamaRuntimePathMatchesSpec(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Inference.WhisperRuntimePath = filepath.Join(dir, "runtime", "whisper.cpp")
	spec := llamaRuntimeSpec(cfg)
	library := filepath.Join(spec.Path, spec.RequiredFiles[0])
	if got := LlamaRuntimePath(cfg); got != library {
		t.Fatalf("the summarizer would load %q while the runtime card installs %q", got, library)
	}
}

// TestVulkanRuntimeCard covers the GPU build card: it installs into its own
// directory, applies the same runtime setting, and is absent where the release
// has no Vulkan build.
func TestVulkanRuntimeCard(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Inference.LlamaRuntimePath = filepath.Join(dir, "runtime", "llama.cpp")
	spec, ok := llamaVulkanRuntimeSpec(cfg)
	supported := runtime.GOOS == "linux" || runtime.GOOS == "windows"
	if ok != supported {
		t.Fatalf("Vulkan card presence on %s is %t, want %t", runtime.GOOS, ok, supported)
	}
	if !ok {
		return
	}
	if spec.SettingKey != "inference.llama_runtime_path" {
		t.Fatalf("the Vulkan card applies %q", spec.SettingKey)
	}
	if spec.SettingValue != LlamaVulkanRuntimeBase(cfg) {
		t.Fatalf("the Vulkan card applies %q", spec.SettingValue)
	}
	cpu := llamaRuntimeSpec(cfg)
	if filepath.Clean(spec.SettingValue) == filepath.Clean(cpu.SettingValue) {
		t.Fatal("the Vulkan and regular runtimes install into the same directory")
	}
	if !strings.HasSuffix(filepath.Clean(spec.Path), runtime.GOOS+"-"+runtime.GOARCH) {
		t.Fatalf("the Vulkan card extracts into %q", spec.Path)
	}
	// Selecting the Vulkan card deselects the regular one.
	cfg.Inference.LlamaRuntimePath = spec.SettingValue
	if llamaRuntimeSpec(cfg).Selected {
		t.Fatal("the regular runtime is still marked as applied")
	}
	if !mustVulkanSpec(t, cfg).Selected {
		t.Fatal("the applied Vulkan runtime is not marked as selected")
	}
}

func mustVulkanSpec(t *testing.T, cfg config.Config) Spec {
	t.Helper()
	spec, ok := llamaVulkanRuntimeSpec(cfg)
	if !ok {
		t.Skip("no Vulkan build for this platform")
	}
	return spec
}

// TestDiarizationEmbeddingCards lets the user switch the voice-embedding model
// from the models window: both cards must be selectable and apply the same
// setting, and exactly one of them has to look applied.
func TestDiarizationEmbeddingCards(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.ConfigFile = filepath.Join(dir, "config.toml")
	cfg.Inference.WhisperRuntimePath = filepath.Join(dir, "runtime", "whisper.cpp")
	cfg.Diarization.EmbeddingModel = filepath.Join(dir, "models", "diarization", "wespeaker-resnet34-lm", "voxceleb_resnet34_LM.onnx")
	manager := New(cfg, log.New(os.Stderr, "", 0))

	resnet := findStatus(t, manager, "diarization-embedding-wespeaker")
	campplus := findStatus(t, manager, "diarization-embedding-campplus")
	for _, card := range []Status{resnet, campplus} {
		if !card.Selectable || card.SettingKey != "diarization.embedding_model" {
			t.Fatalf("card %s is not selectable through diarization.embedding_model: %+v", card.ID, card)
		}
	}
	if !resnet.Selected || campplus.Selected {
		t.Fatalf("the applied embedding model is wrong: resnet=%t campplus=%t", resnet.Selected, campplus.Selected)
	}

	// Applying CAM++ points the setting at its file and deselects ResNet34.
	value := campplus.Path
	if err := config.UpdateFile(cfg.ConfigFile, map[string]string{"diarization.embedding_model": strconv.Quote(value)}); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(cfg.ConfigFile)
	if err != nil {
		t.Fatal(err)
	}
	loaded.ConfigFile = cfg.ConfigFile
	config.ResolvePaths(&loaded, cfg.ConfigFile)
	manager = New(loaded, log.New(os.Stderr, "", 0))
	if findStatus(t, manager, "diarization-embedding-wespeaker").Selected {
		t.Fatal("ResNet34 is still marked as applied after switching to CAM++")
	}
	if !findStatus(t, manager, "diarization-embedding-campplus").Selected {
		t.Fatal("CAM++ is not marked as applied after the switch")
	}
}

// TestEmbeddingCardVerifiesItsOwnFile reproduces a reported bug: with CAM++
// applied, testing the ResNet34 card hashed the configured file and reported
// "SHA-256 mismatch", while applying the card wrote the already configured path
// back, so nothing changed. Each embedding card must own its file.
func TestEmbeddingCardVerifiesItsOwnFile(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.ConfigFile = filepath.Join(dir, "config.toml")
	cfg.Inference.WhisperRuntimePath = filepath.Join(dir, "runtime", "whisper.cpp")

	resnetPath := filepath.Join(dir, "models", "diarization", "wespeaker-resnet34-lm", "voxceleb_resnet34_LM.onnx")
	campPath := filepath.Join(dir, "models", "diarization", "wespeaker-campplus", "voxceleb_campplus.onnx")
	writeModelFile(t, campPath, "campplus")
	writeModelFile(t, resnetPath, "resnet34")
	cfg.Diarization.EmbeddingModel = campPath
	// Ожидаемый хэш относится к файлу ResNet34, а не к применённому CAM++.
	cfg.Models.EmbeddingSHA256 = sha256Text("resnet34")
	manager := New(cfg, log.New(io.Discard, "", 0))

	resnet := findStatus(t, manager, "diarization-embedding-wespeaker")
	if resnet.Path != resnetPath {
		t.Fatalf("the ResNet34 card points at %q, want its own file %q", resnet.Path, resnetPath)
	}
	if resnet.Selected {
		t.Fatal("the ResNet34 card must not be applied while CAM++ is configured")
	}
	if !resnet.Exists {
		t.Fatal("the ResNet34 file exists but the card does not see it")
	}
	// Полная проверка требует установленного ONNX Runtime, поэтому здесь важно
	// именно то, что контрольная сумма прошла: иначе проверялся бы чужой файл.
	result := manager.Test("diarization-embedding-wespeaker")
	if strings.Contains(result.Message, "SHA-256") {
		t.Fatalf("the intact ResNet34 file failed the checksum: %s", result.Message)
	}
	if checks := strings.Join(result.Checks, ", "); !strings.Contains(checks, "Files and checksums") {
		t.Fatalf("checksums were not verified: message=%q checks=%q", result.Message, checks)
	}
	spec, err := manager.ApplyTarget("diarization-embedding-wespeaker")
	if err != nil {
		t.Fatalf("the card cannot be applied: %v", err)
	}
	if spec.Path != resnetPath {
		t.Fatalf("applying the card would write %q, want %q", spec.Path, resnetPath)
	}
}

// writeModelFile creates a file with the given content, as an installed model.
func writeModelFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// sha256Text returns the hex checksum of a short string used as a fake model.
func sha256Text(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}
