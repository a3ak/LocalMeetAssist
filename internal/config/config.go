// Package config loads, validates and persists the TOML configuration.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Config is the complete application configuration.
type Config struct {
	// ConfigFile is the absolute path used to load the configuration. It is
	// runtime metadata and is never parsed as a TOML setting.
	ConfigFile    string
	ConfigVersion int
	App           App
	Audio         Audio
	Inference     Inference
	Transcription Transcription
	Diarization   Diarization
	Models        Models
	Summary       Summary
	Storage       Storage
	Logging       Logging
	Desktop       Desktop
	Integrations  Integrations
}

// UpdateFile replaces selected section.key values while preserving unrelated
// settings and comments. Values must already be valid TOML literals. The
// resulting file is parsed and validated before it atomically replaces the
// previous configuration.
func UpdateFile(path string, updates map[string]string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("configuration file path is unavailable")
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(data) == 0 {
		data = []byte("config_version = 1\n")
	}

	// Validate keys and literal types before touching the file.
	probe := Defaults()
	for full, raw := range updates {
		section, key, ok := strings.Cut(full, ".")
		if !ok || section == "" || key == "" {
			return fmt.Errorf("invalid setting key %q", full)
		}
		if err := apply(&probe, section, key, raw); err != nil {
			return fmt.Errorf("%s: %w", full, err)
		}
	}

	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	found := make(map[string]bool, len(updates))
	section := ""
	for index, original := range lines {
		trimmed := strings.TrimSpace(stripComment(original))
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section = strings.TrimSpace(trimmed[1 : len(trimmed)-1])
			continue
		}
		parts := strings.SplitN(trimmed, "=", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		full := section + "." + key
		raw, ok := updates[full]
		if !ok {
			continue
		}
		indent := original[:len(original)-len(strings.TrimLeft(original, " \t"))]
		lines[index] = indent + key + " = " + raw
		found[full] = true
	}

	missing := make(map[string][]string)
	for full, raw := range updates {
		if found[full] {
			continue
		}
		section, key, _ := strings.Cut(full, ".")
		missing[section] = append(missing[section], key+" = "+raw)
	}
	sections := make([]string, 0, len(missing))
	for name := range missing {
		sections = append(sections, name)
	}
	sort.Strings(sections)
	for _, name := range sections {
		lines = append(lines, "", "["+name+"]")
		sort.Strings(missing[name])
		lines = append(lines, missing[name]...)
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(strings.TrimRight(strings.Join(lines, "\n"), "\n")+"\n"), 0o600); err != nil {
		return err
	}
	if _, err := Load(tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// App holds interface, storage location and local HTTP server settings.
type App struct {
	Language            string
	Theme               string
	DataDir             string
	ListenHost          string
	ListenPort          int
	OpenBrowser         bool
	MicrophoneOwnerName string
}

// Audio describes the native capture format and device selection.
type Audio struct {
	Backend          string
	SampleRate       int
	Channels         int
	BlockMS          int
	InputMode        string
	InputDeviceID    string
	InputDeviceName  string
	OutputMode       string
	OutputDeviceID   string
	OutputDeviceName string
	MicrophoneGain   float64
	SystemGain       float64
	Normalize        bool
	EchoCancellation string
	EchoDelayMS      int
}

// Desktop configures the optional system-tray controller and global shortcuts.
// An empty shortcut disables that action. ToggleRecord can be used instead of
// separate StartRecording/StopRecording shortcuts.
type Desktop struct {
	TrayEnabled    bool
	ToggleRecord   string
	StartRecording string
	StopRecording  string
	OpenUI         string
}

// Inference configures the native ONNX Runtime loaded by the Go process.
// RuntimePath may point to a manually supplied library. When AutoDownload is
// enabled, the model manager downloads the matching official CPU runtime when
// the file is absent.
type Inference struct {
	RuntimePath        string
	AutoDownload       bool
	RuntimeVersion     string
	WhisperRuntimePath string
}

// Transcription configures the ASR engine, VAD and echo handling.
type Transcription struct {
	AutoRun              bool
	Engine               string
	Command              string
	ModelPath            string
	Language             string
	Threads              int
	TimeoutSeconds       int
	ChunkSeconds         int
	SourceMode           string
	EchoDedupEnabled     bool
	EchoTimeToleranceMS  int
	EchoTextSimilarity   float64
	VADEnabled           bool
	VADModelPath         string
	VADThreshold         float64
	VADMinSpeechMS       int
	VADMinSilenceMS      int
	SuppressNonSpeech    bool
	NoFallback           bool
	SilenceFilter        bool
	MinSegmentRMS        float64
	MixedFallbackEnabled bool
	// Deprecated GigaAMGUI HTTP fields are retained only so an existing
	// config.toml can be read during migration. LocalMeetAssist no longer calls that
	// API; Engine=gigaam-onnx always executes the model in-process.
	GigaAMBaseURL       string
	GigaAMToken         string
	GigaAMTokenEnv      string
	GigaAMModel         string
	GigaAMBackend       string
	GigaAMONNXProvider  string
	GigaAMPreprocessing string
	GigaAMTLSVerify     bool
	GigaAMTLSCAFile     string
	GigaAMTLSServerName string
}

// Diarization configures speaker segmentation, embeddings and clustering.
type Diarization struct {
	AutoRun             bool
	Engine              string
	Command             string
	SegmentationModel   string
	EmbeddingModel      string
	NumSpeakers         int
	ClusterThreshold    float64
	MicrophoneEnabled   bool
	TimeoutSeconds      int
	MaxAutoSpeakers     int
	NumThreads          int
	ChunkSeconds        int
	ChunkOverlapSeconds int
	GigaAMBackend       string
}

// Models contains download locations for model files only. Executables are
// deliberately not downloaded by the application.
type Models struct {
	DownloadTimeoutSeconds int
	GigaAMEncoderURL       string
	GigaAMEncoderSHA256    string
	GigaAMDecoderURL       string
	GigaAMDecoderSHA256    string
	GigaAMJointURL         string
	GigaAMJointSHA256      string
	GigaAMVocabURL         string
	GigaAMVocabSHA256      string
	WhisperURL             string
	WhisperSHA256          string
	WhisperSmallURL        string
	WhisperSmallSHA256     string
	WhisperMediumURL       string
	WhisperMediumSHA256    string
	WhisperTurboURL        string
	WhisperTurboSHA256     string
	VADURL                 string
	VADSHA256              string
	SegmentationURL        string
	SegmentationSHA256     string
	EmbeddingURL           string
	EmbeddingSHA256        string
}

// Summary configures the OpenAI-compatible minutes endpoint.
type Summary struct {
	AutoRun        bool
	BaseURL        string
	Model          string
	Token          string
	TokenEnv       string
	TimeoutSeconds int
	MaxRetries     int
	SystemPrompt   string
	PromptFile     string
	Language       string
	TLSVerify      bool
	TLSCAFile      string
	TLSServerName  string
}

// DefaultSummaryPrompt is the built-in minutes template. The response
// language directive is prepended at runtime by the summarizer.
const DefaultSummaryPrompt = `You write accurate meeting minutes.

Use only facts from the transcript. Do not invent decisions, action items, deadlines, names, or titles. If a fragment is recognized uncertainly or is contradictory, mark it explicitly. Keep the user-assigned speaker names; do not replace technical SPEAKER_XX labels with invented names.

Return Markdown with strictly the following structure:

# Brief summary
Briefly describe the meeting purpose and main outcomes in 3-7 bullet points.

## Participants
List only the participants that can be identified from the transcript.

## Discussed topics
Group the main themes and key arguments without repetition.

## Decisions
List only decisions that were explicitly made. If there are none, write "No explicit decisions were recorded".

## Action items
Format as a table: Task | Assignee | Deadline. Use "not specified" for unknown values.

## Open questions and risks
List unresolved questions, dependencies, risks and clarifications needed.

Do not add introductory comments before the first heading and do not repeat the whole transcript.`

// Storage controls artifact naming, audio retention and the database path.
type Storage struct {
	FileNameTemplate     string
	DateFormat           string
	TimeFormat           string
	AudioAfterProcessing string
	MP3BitrateKbps       int
	KeepSourceOnFailure  bool
	DatabasePath         string
	BackupCount          int
	EncoderCommand       string
	OpusBitrateKbps      int
}

// Logging controls the log level and file rotation.
type Logging struct {
	Level     string
	Directory string
	MaxFileMB int
	MaxFiles  int
}

// Integrations configures external API access: tokens, browser recording and
// the usage audit.
type Integrations struct {
	AuditEnabled bool
	Browser      BrowserIntegration
}

// BrowserIntegration configures recording triggered by matching browser pages.
// Masks is a newline-separated list of "Name = glob-pattern" entries.
type BrowserIntegration struct {
	Enabled              bool
	Masks                string
	Mode                 string
	TitleTemplate        string
	StopAfterMissedPolls int
	PollIntervalSeconds  int
}

// Localized returns the Russian or English variant based on the interface
// language. It is used for generated default data such as meeting titles and
// the default microphone owner name, not for error messages.
func Localized(language, ru, en string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(language)), "en") {
		return en
	}
	return ru
}

// DefaultMicrophoneOwnerName returns the localized default name of the local
// participant, used when app.microphone_owner_name is empty.
func DefaultMicrophoneOwnerName(language string) string {
	return Localized(language, "Владелец микрофона", "Microphone owner")
}

// Defaults returns the configuration used when no config.toml is present.
func Defaults() Config {
	return Config{
		ConfigVersion: 1,
		App:           App{Language: "ru", Theme: "dark", DataDir: "./data", ListenHost: "127.0.0.1", ListenPort: 0, OpenBrowser: true, MicrophoneOwnerName: ""},
		Audio:         Audio{Backend: "native", SampleRate: 16000, Channels: 1, BlockMS: 250, InputMode: "auto", OutputMode: "native", MicrophoneGain: 1, SystemGain: 1, Normalize: true, EchoCancellation: "auto", EchoDelayMS: 100},
		Desktop:       Desktop{TrayEnabled: true},
		Inference:     Inference{RuntimePath: "./runtime/onnxruntime", AutoDownload: true, RuntimeVersion: "1.23.2", WhisperRuntimePath: "./runtime/whisper.cpp"},
		Transcription: Transcription{AutoRun: false, Engine: "gigaam-onnx", ModelPath: "./models/asr/gigaam-v3-e2e-rnnt-int8", Language: "ru", TimeoutSeconds: 3600, ChunkSeconds: 20, SourceMode: "auto", EchoDedupEnabled: true, EchoTimeToleranceMS: 1500, EchoTextSimilarity: 0.72, VADEnabled: true, VADModelPath: "./models/vad/silero-v6.2/silero_vad.onnx", VADThreshold: 0.5, VADMinSpeechMS: 250, VADMinSilenceMS: 300, SuppressNonSpeech: true, NoFallback: true, SilenceFilter: true, MinSegmentRMS: 0.0015, MixedFallbackEnabled: true, GigaAMModel: "v3_e2e_rnnt_int8", GigaAMBackend: "onnx", GigaAMONNXProvider: "cpu"},
		Diarization: Diarization{
			AutoRun: false, Engine: "pyannote-wespeaker-onnx",
			SegmentationModel: "./models/diarization/pyannote-segmentation-3/model.onnx", EmbeddingModel: "./models/diarization/wespeaker-resnet34-lm/voxceleb_resnet34_LM.onnx",
			NumSpeakers: 0, ClusterThreshold: 0.60, TimeoutSeconds: 3600, MaxAutoSpeakers: 16, NumThreads: 4, ChunkSeconds: 0, ChunkOverlapSeconds: 0, GigaAMBackend: "onnx",
		},
		Models: Models{DownloadTimeoutSeconds: 7200,
			GigaAMEncoderURL:    "https://huggingface.co/istupakov/gigaam-v3-onnx/resolve/main/v3_e2e_rnnt_encoder.int8.onnx",
			GigaAMEncoderSHA256: "4e0e076a6076cd110277e529b8ac8f32cd5297f7fbebad5341ae8ddb7d00817b",
			GigaAMDecoderURL:    "https://huggingface.co/istupakov/gigaam-v3-onnx/resolve/main/v3_e2e_rnnt_decoder.int8.onnx",
			GigaAMDecoderSHA256: "89014e134865615b91e037157e46e389b1271e6072460efc010ea08e61e23146",
			GigaAMJointURL:      "https://huggingface.co/istupakov/gigaam-v3-onnx/resolve/main/v3_e2e_rnnt_joint.int8.onnx",
			GigaAMJointSHA256:   "ade116563dbf66e503b0994efab6b5861412743e52bf31c39fc3fffa3783d5d1",
			GigaAMVocabURL:      "https://huggingface.co/istupakov/gigaam-v3-onnx/resolve/main/v3_e2e_rnnt_vocab.txt",
			WhisperURL:          "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-small.bin",
			WhisperSHA256:       "1be3a9b2063867b937e64e2ec7483364a79917e157fa98c5d94b5c1fffea987b",
			WhisperSmallURL:     "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-small-q5_1.bin",
			WhisperSmallSHA256:  "ae85e4a935d7a567bd102fe55afc16bb595bdb618e11b2fc7591bc08120411bb",
			WhisperMediumURL:    "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-medium-q5_0.bin",
			WhisperMediumSHA256: "19fea4b380c3a618ec4723c3eef2eb785ffba0d0538cf43f8f235e7b3b34220f",
			WhisperTurboURL:     "https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-large-v3-turbo-q5_0.bin",
			WhisperTurboSHA256:  "394221709cd5ad1f40c46e6031ca61bce88931e6e088c188294c6d5a55ffa7e2",
			VADURL:              "https://huggingface.co/istupakov/silero-vad-onnx/resolve/main/silero_vad.onnx",
			VADSHA256:           "1a153a22f4509e292a94e67d6f9b85e8deb25b4988682b7e174c65279d8788e3",
			SegmentationURL:     "https://huggingface.co/onnx-community/pyannote-segmentation-3.0/resolve/main/onnx/model.onnx",
			SegmentationSHA256:  "057ee564753071c0b09b5b611648b50ac188d50846bff5f01e9f7bbf1591ea25",
			EmbeddingURL:        "https://huggingface.co/Wespeaker/wespeaker-voxceleb-resnet34-LM/resolve/main/voxceleb_resnet34_LM.onnx",
			EmbeddingSHA256:     "7bb2f06e9df17cdf1ef14ee8a15ab08ed28e8d0ef5054ee135741560df2ec068"},
		Summary:      Summary{AutoRun: false, TimeoutSeconds: 300, MaxRetries: 2, TokenEnv: "MEETING_LLM_TOKEN", TLSVerify: true},
		Storage:      Storage{FileNameTemplate: "{{date}}_{{time}}_{{title_slug}}_{{uid_short}}_{{artifact_type}}", DateFormat: "2006-01-02", TimeFormat: "15-04-05", AudioAfterProcessing: "wav", MP3BitrateKbps: 96, OpusBitrateKbps: 32, KeepSourceOnFailure: true, DatabasePath: "./data/database/meetings.db", BackupCount: 3},
		Logging:      Logging{Level: "info", Directory: "./data/logs", MaxFileMB: 20, MaxFiles: 5},
		Integrations: Integrations{AuditEnabled: false, Browser: BrowserIntegration{Mode: "auto", TitleTemplate: "{{title}} — {{date}} {{time}}", StopAfterMissedPolls: 4, PollIntervalSeconds: 20}},
	}
}

// Load reads and validates the TOML file at path.
func Load(path string) (Config, error) {
	cfg := Defaults()
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, nil
	}
	if err != nil {
		return cfg, err
	}
	defer f.Close()
	section := ""
	s := bufio.NewScanner(f)
	lineNo := 0
	for s.Scan() {
		lineNo++
		line := strings.TrimSpace(stripComment(s.Text()))
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			return cfg, fmt.Errorf("config line %d: expected key=value", lineNo)
		}
		key, value := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if err := apply(&cfg, section, key, value); err != nil {
			return cfg, fmt.Errorf("config line %d: %w", lineNo, err)
		}
	}
	if err := s.Err(); err != nil {
		return cfg, err
	}
	if cfg.App.DataDir == "" {
		return cfg, errors.New("app.data_dir must not be empty")
	}
	switch cfg.Audio.SampleRate {
	case 8000, 16000, 24000, 48000:
	default:
		return cfg, errors.New("audio.sample_rate must be 8000, 16000, 24000 or 48000")
	}
	if cfg.Audio.Channels != 1 && cfg.Audio.Channels != 2 {
		return cfg, errors.New("audio.channels must be 1 or 2")
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Logging.Level)) {
	case "debug", "info", "warn", "error":
	default:
		return cfg, errors.New("logging.level must be debug, info, warn or error")
	}
	if cfg.Transcription.TimeoutSeconds <= 0 {
		return cfg, errors.New("transcription.timeout_seconds must be positive")
	}
	legacyTranscriptionEngine := false
	switch strings.ToLower(strings.TrimSpace(cfg.Transcription.Engine)) {
	case "", "gigaam-onnx", "whispercpp-native", "mock":
	case "whisper.cpp":
		cfg.Transcription.Engine = "whispercpp-native"
	case "gigaam-api":
		// Automatic migration from pre-0.8 configurations. The obsolete CLI/API
		// values no longer select an external process.
		cfg.Transcription.Engine = "gigaam-onnx"
		legacyTranscriptionEngine = true
	default:
		return cfg, errors.New("transcription.engine must be gigaam-onnx or whispercpp-native")
	}
	if legacyTranscriptionEngine {
		defaults := Defaults()
		if strings.EqualFold(filepath.Ext(cfg.Transcription.ModelPath), ".bin") || strings.Contains(strings.ToLower(cfg.Transcription.ModelPath), "whisper") {
			cfg.Transcription.ModelPath = defaults.Transcription.ModelPath
		}
		if strings.EqualFold(filepath.Ext(cfg.Transcription.VADModelPath), ".bin") || strings.Contains(strings.ToLower(cfg.Transcription.VADModelPath), "ggml") {
			cfg.Transcription.VADModelPath = defaults.Transcription.VADModelPath
		}
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Transcription.GigaAMBackend)) {
	case "", "auto", "pytorch", "onnx", "mlx":
	default:
		return cfg, errors.New("transcription.gigaam_backend must be auto, pytorch, onnx or mlx")
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Transcription.GigaAMPreprocessing)) {
	case "", "off", "auto", "light", "denoise":
	default:
		return cfg, errors.New("transcription.gigaam_preprocessing must be off, auto, light or denoise")
	}
	if cfg.Transcription.ChunkSeconds < 0 || cfg.Transcription.ChunkSeconds > 7200 {
		return cfg, errors.New("transcription.chunk_seconds must be between 0 and 7200")
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Transcription.SourceMode)) {
	case "auto", "separate", "mixed":
	default:
		return cfg, errors.New("transcription.source_mode must be auto, separate or mixed")
	}
	if cfg.Transcription.EchoTimeToleranceMS < 0 || cfg.Transcription.EchoTimeToleranceMS > 10000 {
		return cfg, errors.New("transcription.echo_time_tolerance_ms must be between 0 and 10000")
	}
	if cfg.Transcription.EchoTextSimilarity < 0 || cfg.Transcription.EchoTextSimilarity > 1 {
		return cfg, errors.New("transcription.echo_text_similarity must be between 0 and 1")
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Audio.EchoCancellation)) {
	case "", "off", "auto", "on":
	default:
		return cfg, errors.New("audio.echo_cancellation must be off, auto or on")
	}
	if cfg.Audio.EchoDelayMS < 0 || cfg.Audio.EchoDelayMS > 2000 {
		return cfg, errors.New("audio.echo_delay_ms must be between 0 and 2000")
	}
	if cfg.Transcription.VADThreshold <= 0 || cfg.Transcription.VADThreshold > 1 {
		return cfg, errors.New("transcription.vad_threshold must be in (0, 1]")
	}
	if cfg.Transcription.MinSegmentRMS < 0 || cfg.Transcription.MinSegmentRMS > 1 {
		return cfg, errors.New("transcription.min_segment_rms must be between 0 and 1")
	}
	if cfg.Diarization.TimeoutSeconds <= 0 {
		return cfg, errors.New("diarization.timeout_seconds must be positive")
	}
	legacyDiarizationEngine := false
	switch strings.ToLower(strings.TrimSpace(cfg.Diarization.Engine)) {
	case "", "pyannote-wespeaker-onnx", "mock":
	case "sherpa-onnx-cli", "gigaam-api":
		cfg.Diarization.Engine = "pyannote-wespeaker-onnx"
		legacyDiarizationEngine = true
	default:
		return cfg, errors.New("diarization.engine must be pyannote-wespeaker-onnx")
	}
	if legacyDiarizationEngine {
		defaults := Defaults()
		cfg.Diarization.SegmentationModel = defaults.Diarization.SegmentationModel
		cfg.Diarization.EmbeddingModel = defaults.Diarization.EmbeddingModel
		cfg.Diarization.ClusterThreshold = defaults.Diarization.ClusterThreshold
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Diarization.GigaAMBackend)) {
	case "", "onnx", "pyannote", "sortformer":
	default:
		return cfg, errors.New("diarization.gigaam_backend must be onnx, pyannote or sortformer")
	}
	if cfg.Diarization.ClusterThreshold <= 0 || cfg.Diarization.ClusterThreshold > 1 {
		return cfg, errors.New("diarization.cluster_threshold must be in (0, 1]")
	}
	if cfg.Diarization.MaxAutoSpeakers < 0 {
		return cfg, errors.New("diarization.max_auto_speakers must not be negative")
	}
	if cfg.Diarization.NumThreads < 0 || cfg.Diarization.NumThreads > 128 {
		return cfg, errors.New("diarization.num_threads must be between 0 and 128")
	}
	if cfg.Diarization.ChunkSeconds < 0 || cfg.Diarization.ChunkSeconds > 7200 {
		return cfg, errors.New("diarization.chunk_seconds must be between 0 and 7200")
	}
	if cfg.Diarization.ChunkOverlapSeconds < 0 || cfg.Diarization.ChunkOverlapSeconds > 1800 {
		return cfg, errors.New("diarization.chunk_overlap_seconds must be between 0 and 1800")
	}
	if cfg.Diarization.ChunkSeconds > 0 && cfg.Diarization.ChunkOverlapSeconds*2 >= cfg.Diarization.ChunkSeconds {
		return cfg, errors.New("diarization.chunk_overlap_seconds must be less than half of chunk_seconds")
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Storage.AudioAfterProcessing)) {
	case "wav", "delete", "mp3", "opus":
	default:
		return cfg, errors.New("storage.audio_after_processing must be wav, opus, mp3 or delete")
	}
	if cfg.Storage.OpusBitrateKbps < 6 || cfg.Storage.OpusBitrateKbps > 256 {
		return cfg, errors.New("storage.opus_bitrate_kbps must be between 6 and 256")
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Audio.Backend)) {
	case "", "native", "ffmpeg":
		// ffmpeg принимается только для бесшовного обновления старого config.toml.
		cfg.Audio.Backend = "native"
	default:
		return cfg, errors.New("audio.backend must be native")
	}
	return cfg, nil
}

// ResolvePaths converts relative paths into absolute ones anchored at the
// directory of configPath.
func ResolvePaths(cfg *Config, configPath string) {
	base, _ := filepath.Abs(filepath.Dir(configPath))
	resolve := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(base, p)
	}
	cfg.App.DataDir = resolve(cfg.App.DataDir)
	cfg.Storage.DatabasePath = resolve(cfg.Storage.DatabasePath)
	cfg.Logging.Directory = resolve(cfg.Logging.Directory)
	cfg.Inference.RuntimePath = resolve(cfg.Inference.RuntimePath)
	cfg.Inference.WhisperRuntimePath = resolve(cfg.Inference.WhisperRuntimePath)
	cfg.Transcription.ModelPath = resolve(cfg.Transcription.ModelPath)
	cfg.Transcription.VADModelPath = resolve(cfg.Transcription.VADModelPath)
	cfg.Diarization.SegmentationModel = resolve(cfg.Diarization.SegmentationModel)
	cfg.Diarization.EmbeddingModel = resolve(cfg.Diarization.EmbeddingModel)
	cfg.Summary.PromptFile = resolve(cfg.Summary.PromptFile)
	cfg.Summary.TLSCAFile = resolve(cfg.Summary.TLSCAFile)
	cfg.Storage.EncoderCommand = resolve(cfg.Storage.EncoderCommand)
}

func stripComment(s string) string {
	inQuote := false
	for i, r := range s {
		if r == '"' {
			inQuote = !inQuote
		}
		if r == '#' && !inQuote {
			return s[:i]
		}
	}
	return s
}

func text(v string) (string, error) {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		return strconv.Unquote(v)
	}
	return v, nil
}
func boolean(v string) (bool, error)    { return strconv.ParseBool(strings.TrimSpace(v)) }
func integer(v string) (int, error)     { return strconv.Atoi(strings.TrimSpace(v)) }
func decimal(v string) (float64, error) { return strconv.ParseFloat(strings.TrimSpace(v), 64) }

func apply(c *Config, sec, key, raw string) error {
	full := sec + "." + key
	s, err := text(raw)
	if err != nil {
		return err
	}
	intKeys := map[string]bool{".config_version": true, "app.listen_port": true, "audio.sample_rate": true, "audio.channels": true, "audio.block_ms": true, "audio.echo_delay_ms": true, "transcription.threads": true, "transcription.timeout_seconds": true, "transcription.chunk_seconds": true, "transcription.echo_time_tolerance_ms": true, "transcription.vad_min_speech_ms": true, "transcription.vad_min_silence_ms": true, "diarization.num_speakers": true, "diarization.timeout_seconds": true, "diarization.max_auto_speakers": true, "diarization.num_threads": true, "diarization.chunk_seconds": true, "diarization.chunk_overlap_seconds": true, "models.download_timeout_seconds": true, "summary.timeout_seconds": true, "summary.max_retries": true, "storage.mp3_bitrate_kbps": true, "storage.opus_bitrate_kbps": true, "storage.backup_count": true, "logging.max_file_mb": true, "logging.max_files": true, "integrations.browser.stop_after_missed_polls": true, "integrations.browser.poll_interval_seconds": true}
	boolKeys := map[string]bool{"app.open_browser": true, "audio.normalize": true, "desktop.tray_enabled": true, "inference.auto_download": true, "transcription.auto_run": true, "transcription.echo_dedup_enabled": true, "transcription.vad_enabled": true, "transcription.suppress_non_speech": true, "transcription.no_fallback": true, "transcription.silence_filter": true, "transcription.mixed_fallback_enabled": true, "transcription.gigaam_tls_verify": true, "diarization.auto_run": true, "diarization.microphone_enabled": true, "summary.auto_run": true, "summary.tls_verify": true, "storage.keep_source_on_failure": true, "integrations.audit_enabled": true, "integrations.browser.enabled": true}
	floatKeys := map[string]bool{"audio.microphone_gain": true, "audio.system_gain": true, "transcription.echo_text_similarity": true, "transcription.vad_threshold": true, "transcription.min_segment_rms": true, "diarization.cluster_threshold": true}
	if intKeys[full] {
		if _, err := integer(raw); err != nil {
			return err
		}
	}
	if boolKeys[full] {
		if _, err := boolean(raw); err != nil {
			return err
		}
	}
	if floatKeys[full] {
		if _, err := decimal(raw); err != nil {
			return err
		}
	}
	switch full {
	case ".config_version":
		c.ConfigVersion, _ = integer(raw)
	case "app.language":
		c.App.Language = s
	case "app.theme":
		if s != "dark" && s != "light" {
			return fmt.Errorf("app.theme must be dark or light")
		}
		c.App.Theme = s
	case "app.data_dir":
		c.App.DataDir = s
	case "app.listen_host":
		c.App.ListenHost = s
	case "app.listen_port":
		c.App.ListenPort, _ = integer(raw)
	case "app.open_browser":
		c.App.OpenBrowser, _ = boolean(raw)
	case "app.microphone_owner_name":
		c.App.MicrophoneOwnerName = s
	case "audio.sample_rate":
		c.Audio.SampleRate, _ = integer(raw)
	case "audio.backend":
		c.Audio.Backend = s
	case "audio.ffmpeg_command":
		// Совместимость со старой конфигурацией: параметр больше не используется.
	case "audio.channels":
		c.Audio.Channels, _ = integer(raw)
	case "audio.block_ms":
		c.Audio.BlockMS, _ = integer(raw)
	case "audio.input_mode":
		c.Audio.InputMode = s
	case "audio.input_device_id":
		c.Audio.InputDeviceID = s
	case "audio.input_device_name":
		c.Audio.InputDeviceName = s
	case "audio.output_mode":
		c.Audio.OutputMode = s
	case "audio.output_device_id":
		c.Audio.OutputDeviceID = s
	case "audio.output_device_name":
		c.Audio.OutputDeviceName = s
	case "audio.microphone_gain":
		c.Audio.MicrophoneGain, _ = decimal(raw)
	case "audio.system_gain":
		c.Audio.SystemGain, _ = decimal(raw)
	case "audio.normalize":
		c.Audio.Normalize, _ = boolean(raw)
	case "audio.echo_cancellation":
		c.Audio.EchoCancellation = s
	case "audio.echo_delay_ms":
		c.Audio.EchoDelayMS, _ = integer(raw)
	case "desktop.tray_enabled":
		c.Desktop.TrayEnabled, _ = boolean(raw)
	case "desktop.toggle_record":
		c.Desktop.ToggleRecord = s
	case "desktop.start_recording":
		c.Desktop.StartRecording = s
	case "desktop.stop_recording":
		c.Desktop.StopRecording = s
	case "desktop.open_ui":
		c.Desktop.OpenUI = s
	case "inference.runtime_path":
		c.Inference.RuntimePath = s
	case "inference.auto_download":
		c.Inference.AutoDownload, _ = boolean(raw)
	case "inference.runtime_version":
		c.Inference.RuntimeVersion = s
	case "inference.whisper_runtime_path":
		c.Inference.WhisperRuntimePath = s
	case "transcription.auto_run":
		c.Transcription.AutoRun, _ = boolean(raw)
	case "transcription.engine":
		c.Transcription.Engine = s
	case "transcription.command":
		c.Transcription.Command = s
	case "transcription.model_path":
		c.Transcription.ModelPath = s
	case "transcription.language":
		c.Transcription.Language = s
	case "transcription.threads":
		c.Transcription.Threads, _ = integer(raw)
	case "transcription.timeout_seconds":
		c.Transcription.TimeoutSeconds, _ = integer(raw)
	case "transcription.chunk_seconds":
		c.Transcription.ChunkSeconds, _ = integer(raw)
	case "transcription.source_mode":
		c.Transcription.SourceMode = s
	case "transcription.echo_dedup_enabled":
		c.Transcription.EchoDedupEnabled, _ = boolean(raw)
	case "transcription.echo_time_tolerance_ms":
		c.Transcription.EchoTimeToleranceMS, _ = integer(raw)
	case "transcription.echo_text_similarity":
		c.Transcription.EchoTextSimilarity, _ = decimal(raw)
	case "transcription.vad_enabled":
		c.Transcription.VADEnabled, _ = boolean(raw)
	case "transcription.vad_model_path":
		c.Transcription.VADModelPath = s
	case "transcription.vad_threshold":
		c.Transcription.VADThreshold, _ = decimal(raw)
	case "transcription.vad_min_speech_ms":
		c.Transcription.VADMinSpeechMS, _ = integer(raw)
	case "transcription.vad_min_silence_ms":
		c.Transcription.VADMinSilenceMS, _ = integer(raw)
	case "transcription.suppress_non_speech":
		c.Transcription.SuppressNonSpeech, _ = boolean(raw)
	case "transcription.no_fallback":
		c.Transcription.NoFallback, _ = boolean(raw)
	case "transcription.silence_filter":
		c.Transcription.SilenceFilter, _ = boolean(raw)
	case "transcription.min_segment_rms":
		c.Transcription.MinSegmentRMS, _ = decimal(raw)
	case "transcription.mixed_fallback_enabled":
		c.Transcription.MixedFallbackEnabled, _ = boolean(raw)
	case "transcription.gigaam_base_url":
		c.Transcription.GigaAMBaseURL = s
	case "transcription.gigaam_token":
		c.Transcription.GigaAMToken = s
	case "transcription.gigaam_token_env":
		c.Transcription.GigaAMTokenEnv = s
	case "transcription.gigaam_model":
		c.Transcription.GigaAMModel = s
	case "transcription.gigaam_backend":
		c.Transcription.GigaAMBackend = s
	case "transcription.gigaam_onnx_provider":
		c.Transcription.GigaAMONNXProvider = s
	case "transcription.gigaam_preprocessing":
		c.Transcription.GigaAMPreprocessing = s
	case "transcription.gigaam_tls_verify":
		c.Transcription.GigaAMTLSVerify, _ = boolean(raw)
	case "transcription.gigaam_tls_ca_file":
		c.Transcription.GigaAMTLSCAFile = s
	case "transcription.gigaam_tls_server_name":
		c.Transcription.GigaAMTLSServerName = s
	case "diarization.auto_run":
		c.Diarization.AutoRun, _ = boolean(raw)
	case "diarization.engine":
		c.Diarization.Engine = s
	case "diarization.command":
		c.Diarization.Command = s
	case "diarization.segmentation_model":
		c.Diarization.SegmentationModel = s
	case "diarization.embedding_model":
		c.Diarization.EmbeddingModel = s
	case "diarization.num_speakers":
		c.Diarization.NumSpeakers, _ = integer(raw)
	case "diarization.cluster_threshold":
		c.Diarization.ClusterThreshold, _ = decimal(raw)
	case "diarization.microphone_enabled":
		c.Diarization.MicrophoneEnabled, _ = boolean(raw)
	case "diarization.timeout_seconds":
		c.Diarization.TimeoutSeconds, _ = integer(raw)
	case "diarization.max_auto_speakers":
		c.Diarization.MaxAutoSpeakers, _ = integer(raw)
	case "diarization.num_threads":
		c.Diarization.NumThreads, _ = integer(raw)
	case "diarization.chunk_seconds":
		c.Diarization.ChunkSeconds, _ = integer(raw)
	case "diarization.chunk_overlap_seconds":
		c.Diarization.ChunkOverlapSeconds, _ = integer(raw)
	case "diarization.gigaam_backend":
		c.Diarization.GigaAMBackend = s
	case "models.download_timeout_seconds":
		c.Models.DownloadTimeoutSeconds, _ = integer(raw)
	case "models.gigaam_encoder_url":
		c.Models.GigaAMEncoderURL = s
	case "models.gigaam_encoder_sha256":
		c.Models.GigaAMEncoderSHA256 = s
	case "models.gigaam_decoder_url":
		c.Models.GigaAMDecoderURL = s
	case "models.gigaam_decoder_sha256":
		c.Models.GigaAMDecoderSHA256 = s
	case "models.gigaam_joint_url":
		c.Models.GigaAMJointURL = s
	case "models.gigaam_joint_sha256":
		c.Models.GigaAMJointSHA256 = s
	case "models.gigaam_vocab_url":
		c.Models.GigaAMVocabURL = s
	case "models.gigaam_vocab_sha256":
		c.Models.GigaAMVocabSHA256 = s
	case "models.whisper_url":
		c.Models.WhisperURL = s
	case "models.whisper_sha256":
		c.Models.WhisperSHA256 = s
	case "models.whisper_small_url":
		c.Models.WhisperSmallURL = s
	case "models.whisper_small_sha256":
		c.Models.WhisperSmallSHA256 = s
	case "models.whisper_medium_url":
		c.Models.WhisperMediumURL = s
	case "models.whisper_medium_sha256":
		c.Models.WhisperMediumSHA256 = s
	case "models.whisper_turbo_url":
		c.Models.WhisperTurboURL = s
	case "models.whisper_turbo_sha256":
		c.Models.WhisperTurboSHA256 = s
	case "models.vad_url":
		c.Models.VADURL = s
	case "models.vad_sha256":
		c.Models.VADSHA256 = s
	case "models.segmentation_url":
		c.Models.SegmentationURL = s
	case "models.segmentation_sha256":
		c.Models.SegmentationSHA256 = s
	case "models.embedding_url":
		c.Models.EmbeddingURL = s
	case "models.embedding_sha256":
		c.Models.EmbeddingSHA256 = s
	case "summary.auto_run":
		c.Summary.AutoRun, _ = boolean(raw)
	case "summary.base_url":
		c.Summary.BaseURL = s
	case "summary.model":
		c.Summary.Model = s
	case "summary.token":
		c.Summary.Token = s
	case "summary.token_env":
		c.Summary.TokenEnv = s
	case "summary.timeout_seconds":
		c.Summary.TimeoutSeconds, _ = integer(raw)
	case "summary.max_retries":
		c.Summary.MaxRetries, _ = integer(raw)
	case "summary.system_prompt":
		c.Summary.SystemPrompt = s
	case "summary.prompt_file":
		c.Summary.PromptFile = s
	case "summary.language":
		c.Summary.Language = s
	case "summary.tls_verify":
		c.Summary.TLSVerify, _ = boolean(raw)
	case "summary.tls_ca_file":
		c.Summary.TLSCAFile = s
	case "summary.tls_server_name":
		c.Summary.TLSServerName = s
	case "storage.file_name_template":
		c.Storage.FileNameTemplate = s
	case "storage.date_format":
		c.Storage.DateFormat = s
	case "storage.time_format":
		c.Storage.TimeFormat = s
	case "storage.audio_after_processing":
		c.Storage.AudioAfterProcessing = s
	case "storage.mp3_bitrate_kbps":
		c.Storage.MP3BitrateKbps, _ = integer(raw)
	case "storage.keep_source_on_failure":
		c.Storage.KeepSourceOnFailure, _ = boolean(raw)
	case "storage.database_path":
		c.Storage.DatabasePath = s
	case "storage.backup_count":
		c.Storage.BackupCount, _ = integer(raw)
	case "storage.encoder_command":
		c.Storage.EncoderCommand = s
	case "storage.opus_bitrate_kbps":
		c.Storage.OpusBitrateKbps, _ = integer(raw)
	case "logging.level":
		c.Logging.Level = s
	case "logging.directory":
		c.Logging.Directory = s
	case "logging.max_file_mb":
		c.Logging.MaxFileMB, _ = integer(raw)
	case "logging.max_files":
		c.Logging.MaxFiles, _ = integer(raw)
	case "integrations.audit_enabled":
		c.Integrations.AuditEnabled, _ = boolean(raw)
	case "integrations.browser.enabled":
		c.Integrations.Browser.Enabled, _ = boolean(raw)
	case "integrations.browser.masks":
		c.Integrations.Browser.Masks = s
	case "integrations.browser.mode":
		c.Integrations.Browser.Mode = s
	case "integrations.browser.title_template":
		c.Integrations.Browser.TitleTemplate = s
	case "integrations.browser.stop_after_missed_polls":
		c.Integrations.Browser.StopAfterMissedPolls, _ = integer(raw)
	case "integrations.browser.poll_interval_seconds":
		c.Integrations.Browser.PollIntervalSeconds, _ = integer(raw)
	default:
		return fmt.Errorf("unknown key %s.%s", sec, key)
	}
	return nil
}
