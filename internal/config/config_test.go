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
