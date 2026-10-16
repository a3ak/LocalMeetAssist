package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestDefaultSummaryPromptMatchesFile keeps the built-in minutes template and
// the copy users read in prompts/ in sync; otherwise they quietly drift apart.
func TestDefaultSummaryPromptMatchesFile(t *testing.T) {
	file, err := os.ReadFile(filepath.Join("..", "..", "prompts", "meeting_summary.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(file)) != strings.TrimSpace(DefaultSummaryPrompt) {
		t.Fatal("prompts/meeting_summary.md and config.DefaultSummaryPrompt differ")
	}
}

// TestExampleConfigLoads keeps the shipped template usable: a typo or a removed
// key in config.example.toml would break the documented `cp` and first run.
func TestExampleConfigLoads(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "config.example.toml"))
	if err != nil {
		t.Fatalf("config.example.toml does not load: %v", err)
	}
	if cfg.App.Theme != "dark" && cfg.App.Theme != "light" {
		t.Fatalf("example theme = %q, want dark or light", cfg.App.Theme)
	}
	if cfg.ConferenceDetection.WindowSeconds != 10 {
		t.Fatalf("conference detection window = %d, want 10", cfg.ConferenceDetection.WindowSeconds)
	}
	if !cfg.ConferenceDetection.Enabled {
		t.Fatal("conference detection must be enabled by default")
	}
}

func TestLoadOverridesDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	data := []byte("config_version = 1\n[app]\ndata_dir = \"./meetings\"\nlisten_port = 9090\nopen_browser = false\n[summary]\nauto_run = true\nbase_url = \"https://llm.local/v1\"\nmodel = \"qwen\"\ntls_verify = true\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.App.ListenPort != 9090 || cfg.App.OpenBrowser || !cfg.Summary.AutoRun || cfg.Summary.Model != "qwen" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadRejectsInvalidBoolean(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.toml")
	if err := os.WriteFile(path, []byte("[app]\nopen_browser = maybe\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestLoadRejectsRemovedEnabledKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old-config.toml")
	if err := os.WriteFile(path, []byte("[transcription]\nenabled = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("removed enabled key must not be accepted; use auto_run")
	}
}

func TestLegacyFFmpegConfigMigratesToNative(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.toml")
	data := []byte("[audio]\nbackend = \"ffmpeg\"\nffmpeg_command = \"ffmpeg\"\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Audio.Backend != "native" {
		t.Fatalf("expected native backend, got %q", cfg.Audio.Backend)
	}
}

func TestLoadSpeechPipelineSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "speech.toml")
	data := []byte("[transcription]\nauto_run = true\ntimeout_seconds = 120\nchunk_seconds = 300\nsource_mode = \"mixed\"\necho_dedup_enabled = true\necho_time_tolerance_ms = 900\necho_text_similarity = 0.81\n[diarization]\nauto_run = true\nengine = \"mock\"\ntimeout_seconds = 90\ncluster_threshold = 0.85\nnum_threads = 6\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Transcription.AutoRun || cfg.Transcription.TimeoutSeconds != 120 || cfg.Transcription.ChunkSeconds != 300 || cfg.Transcription.SourceMode != "mixed" || cfg.Transcription.EchoTimeToleranceMS != 900 || cfg.Transcription.EchoTextSimilarity != 0.81 {
		t.Fatalf("unexpected transcription config: %+v", cfg.Transcription)
	}
	if !cfg.Diarization.AutoRun || cfg.Diarization.TimeoutSeconds != 90 || cfg.Diarization.ClusterThreshold != 0.85 || cfg.Diarization.NumThreads != 6 {
		t.Fatalf("unexpected diarization config: %+v", cfg.Diarization)
	}
}

func TestLoadDesktopOwnerEchoAndWhisperSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "desktop.toml")
	data := []byte(`[app]
microphone_owner_name = "Заур Ахметов"
[audio]
echo_cancellation = "on"
echo_delay_ms = 180
[desktop]
tray_enabled = true
toggle_record = "Cmd+Shift+R"
start_recording = ""
stop_recording = ""
open_ui = "Cmd+Shift+L"
[inference]
whisper_runtime_path = "./runtime/whisper.cpp"
[transcription]
engine = "whispercpp-native"
model_path = "./models/whisper/ggml-medium-q5_0.bin"
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.App.MicrophoneOwnerName != "Заур Ахметов" || cfg.Audio.EchoCancellation != "on" || cfg.Audio.EchoDelayMS != 180 {
		t.Fatalf("unexpected owner/AEC settings: %+v %+v", cfg.App, cfg.Audio)
	}
	if !cfg.Desktop.TrayEnabled || cfg.Desktop.ToggleRecord != "Cmd+Shift+R" || cfg.Desktop.OpenUI != "Cmd+Shift+L" {
		t.Fatalf("unexpected desktop settings: %+v", cfg.Desktop)
	}
	if cfg.Transcription.Engine != "whispercpp-native" || filepath.Base(cfg.Transcription.ModelPath) != "ggml-medium-q5_0.bin" {
		t.Fatalf("unexpected Whisper settings: %+v", cfg.Transcription)
	}
}

func TestLegacyWhisperEngineMigratesToNative(t *testing.T) {
	path := filepath.Join(t.TempDir(), "whisper.toml")
	if err := os.WriteFile(path, []byte("[transcription]\nengine = \"whisper.cpp\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Transcription.Engine != "whispercpp-native" {
		t.Fatalf("legacy Whisper engine was not migrated: %q", cfg.Transcription.Engine)
	}
}

func TestLegacyGigaAMAPISettingsMigrateToDirectONNX(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gigaam.toml")
	data := []byte(`[transcription]
auto_run = true
engine = "gigaam-api"
gigaam_base_url = "https://gigaam.local/v1"
gigaam_token = "secret"
gigaam_token_env = "LOCAL_GIGAAM_KEY"
gigaam_model = "multilingual_large_ctc"
gigaam_backend = "onnx"
gigaam_onnx_provider = "coreml"
gigaam_preprocessing = "denoise"
gigaam_tls_verify = false
[diarization]
auto_run = true
engine = "gigaam-api"
gigaam_backend = "pyannote"
`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Transcription.Engine != "gigaam-onnx" {
		t.Fatalf("unexpected GigaAM transcription config: %+v", cfg.Transcription)
	}
	if cfg.Diarization.Engine != "pyannote-wespeaker-onnx" {
		t.Fatalf("unexpected GigaAM diarization config: %+v", cfg.Diarization)
	}
}

func TestLoadRejectsInvalidTranscriptionSourceMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad-source-mode.toml")
	if err := os.WriteFile(path, []byte("[transcription]\nsource_mode = \"broken\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected source_mode validation error")
	}
}

func TestLoadRejectsInvalidEchoSimilarity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad-echo.toml")
	if err := os.WriteFile(path, []byte("[transcription]\necho_text_similarity = 1.5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("expected validation error")
	}
}

func TestResolvePathsLeavesLegacyEngineCommandsUnused(t *testing.T) {
	dir := t.TempDir()
	cfg := Defaults()
	cfg.Transcription.Command = ""
	cfg.Diarization.Command = ""
	ResolvePaths(&cfg, filepath.Join(dir, "config.toml"))
	if cfg.Transcription.Command != "" {
		t.Fatalf("unexpected transcription command: %q", cfg.Transcription.Command)
	}
	if cfg.Diarization.Command != "" {
		t.Fatalf("unexpected diarization command: %q", cfg.Diarization.Command)
	}
}

func TestUpdateFilePreservesUnrelatedValuesAndValidates(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	original := "config_version = 1\n\n[app]\nlanguage = \"ru\"\ndata_dir = \"./custom-data\"\n\n[transcription]\nauto_run = false\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := UpdateFile(path, map[string]string{
		"transcription.auto_run":    "true",
		"transcription.source_mode": strconv.Quote("mixed"),
		"storage.opus_bitrate_kbps": "48",
	}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Transcription.AutoRun || cfg.Transcription.SourceMode != "mixed" || cfg.Storage.OpusBitrateKbps != 48 || cfg.App.DataDir != "./custom-data" {
		t.Fatalf("unexpected updated config: %+v", cfg)
	}
	before, _ := os.ReadFile(path)
	if err := UpdateFile(path, map[string]string{"transcription.source_mode": strconv.Quote("invalid")}); err == nil {
		t.Fatal("invalid update must fail")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("invalid update changed configuration file")
	}
}

func TestStringListRoundTrip(t *testing.T) {
	values := []string{"Zoom Workplace", "Microsoft Teams", "teams"}
	parsed, err := StringList(QuotedList(values))
	if err != nil {
		t.Fatalf("StringList(QuotedList(...)): %v", err)
	}
	if len(parsed) != len(values) {
		t.Fatalf("length mismatch: got %d want %d", len(parsed), len(values))
	}
	for index := range values {
		if parsed[index] != values[index] {
			t.Fatalf("element %d: got %q want %q", index, parsed[index], values[index])
		}
	}
	empty, err := StringList("[]")
	if err != nil || len(empty) != 0 {
		t.Fatalf("empty list: got %v err %v", empty, err)
	}
	if _, err := StringList("not-an-array"); err == nil {
		t.Fatal("a non-array literal must be rejected")
	}
}

// TestUpdateFileKeepsPromptWithQuotesAndHash covers the way prompts are stored:
// UpdateFile writes them with strconv.Quote, which escapes every quotation mark.
// Comment stripping used to count an escaped quote as a real one, so a '#' after
// an odd number of quotes was cut off as a comment. When the value ended with a
// quote right before the '#', loading failed with "invalid syntax"; other cases
// silently truncated the saved prompt.
func TestUpdateFileKeepsPromptWithQuotesAndHash(t *testing.T) {
	prompts := map[string]string{
		"цитата вплотную к решётке": "Пиши разделы как \"# Заголовок\" в ответе",
		"нечётное число кавычек":    "Ты — ассистент.\nНе пиши символ \" в ответе.\n# Правила",
		"чётное число кавычек":      "Ты — ассистент.\nОтвечай \"кратко\".\n# Правила",
		"json-пример и решётка":     "Пример: {\"a\": 1}\n# JSON в ответе",
		"решётка перед кавычкой":    "# Заголовок\nОтвечай \"да\"",
		"обратные слэши и решётка":  "Путь C:\\\\Users\\\\test\n# хвост",
	}
	for name, prompt := range prompts {
		path := filepath.Join(t.TempDir(), "config.toml")
		if err := UpdateFile(path, map[string]string{"summary.system_prompt": strconv.Quote(prompt)}); err != nil {
			t.Fatalf("%s: UpdateFile: %v", name, err)
		}
		loaded, err := Load(path)
		if err != nil {
			t.Fatalf("%s: Load: %v", name, err)
		}
		if loaded.Summary.SystemPrompt != prompt {
			t.Fatalf("%s: prompt changed:\n got %q\nwant %q", name, loaded.Summary.SystemPrompt, prompt)
		}
	}
}

func TestResolvePathsReadsPromptFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "prompts"), 0o755); err != nil {
		t.Fatal(err)
	}
	template := "Правила из файла.\n# и решётка внутри шаблона"
	if err := os.WriteFile(filepath.Join(dir, "prompts", "meeting_summary.md"), []byte(template), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := Defaults()
	ResolvePaths(&cfg, filepath.Join(dir, "config.toml"))
	if got := EffectiveSummaryPrompt(cfg.Summary); got != template {
		t.Fatalf("шаблон из файла не применён:\n got %q\nwant %q", got, template)
	}
	cfg.Summary.SystemPrompt = "пользовательский промпт"
	if got := EffectiveSummaryPrompt(cfg.Summary); got != "пользовательский промпт" {
		t.Fatalf("пользовательское значение должно побеждать файл, получено %q", got)
	}
	empty := Defaults()
	ResolvePaths(&empty, filepath.Join(t.TempDir(), "config.toml"))
	if got := EffectiveSummaryPrompt(empty.Summary); got != DefaultSummaryPrompt {
		t.Fatal("без файла должен использоваться встроенный шаблон")
	}
}
