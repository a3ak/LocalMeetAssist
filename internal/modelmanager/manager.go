// Package modelmanager downloads, verifies and activates models and native runtimes.
package modelmanager

import (
	"archive/tar"
	"archive/zip"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"localmeetassist/internal/config"
	"localmeetassist/internal/inference"
	appLogging "localmeetassist/internal/logging"
	"localmeetassist/internal/whispercpp"
)

// Component is one downloadable file of a model.
type Component struct {
	Name        string `json:"name"`
	Path        string `json:"path"`
	URL         string `json:"-"`
	SHA256      string `json:"sha256,omitempty"`
	ApproxBytes int64  `json:"approx_bytes,omitempty"`
}

// Spec describes a downloadable model or runtime.
type Spec struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Path   string `json:"path"`
	URL    string `json:"-"`
	SHA256 string `json:"sha256,omitempty"`
	// DownloadSHA256 verifies an archive before extraction. SHA256 verifies the
	// installed file and remains compatible with older model specifications.
	DownloadSHA256 string      `json:"-"`
	Archive        string      `json:"-"`
	RequiredFiles  []string    `json:"-"`
	Group          string      `json:"group"`
	Description    string      `json:"description"`
	ApproxBytes    int64       `json:"approx_bytes,omitempty"`
	SettingKey     string      `json:"-"`
	Engine         string      `json:"-"`
	Selectable     bool        `json:"selectable,omitempty"`
	Selected       bool        `json:"selected,omitempty"`
	License        string      `json:"license,omitempty"`
	Components     []Component `json:"components,omitempty"`
}

// Status is a Spec plus its current installation and download state.
type Status struct {
	Spec
	Exists          bool   `json:"exists"`
	SizeBytes       int64  `json:"size_bytes"`
	Downloading     bool   `json:"downloading"`
	DownloadedBytes int64  `json:"downloaded_bytes"`
	TotalBytes      int64  `json:"total_bytes"`
	LastError       string `json:"last_error,omitempty"`
}

// TestResult reports the outcome of a model verification.
type TestResult struct {
	OK         bool     `json:"ok"`
	ID         string   `json:"id"`
	Message    string   `json:"message"`
	Path       string   `json:"path"`
	DurationMS int64    `json:"duration_ms"`
	Checks     []string `json:"checks,omitempty"`
}

type progressReader struct {
	r  io.Reader
	n  *int64
	mu *sync.Mutex
}

func (r progressReader) Read(p []byte) (int, error) {
	n, err := r.r.Read(p)
	r.mu.Lock()
	*r.n += int64(n)
	r.mu.Unlock()
	return n, err
}

// Manager tracks the state of every known model.
type Manager struct {
	specs    map[string]Spec
	order    []string
	client   *http.Client
	logger   *log.Logger
	language string
	mu       sync.Mutex
	state    map[string]*Status
}

// RuntimeLibraryPath resolves the configured directory/stem to the actual
// platform library. An explicit .dll/.dylib/.so path is left untouched so a
// developer can provide a locally built runtime while using go run.
func RuntimeLibraryPath(configured, version string) string {
	clean := filepath.Clean(configured)
	lower := strings.ToLower(clean)
	if strings.HasSuffix(lower, ".dll") || strings.HasSuffix(lower, ".dylib") || strings.Contains(filepath.Base(lower), ".so") {
		return clean
	}
	name := "libonnxruntime.so." + version
	if runtime.GOOS == "darwin" {
		name = "libonnxruntime.dylib"
	} else if runtime.GOOS == "windows" {
		name = "onnxruntime.dll"
	}
	return filepath.Join(clean, runtime.GOOS+"-"+runtime.GOARCH, name)
}

// ResolveRuntimeLibrary also recognizes the versioned dylib shipped in the
// official macOS archive. This makes a manual installation work even when the
// unversioned compatibility symlink was not preserved while copying files.
func ResolveRuntimeLibrary(configured, version string) string {
	expected := RuntimeLibraryPath(configured, version)
	if regularFile(expected) {
		return expected
	}
	if runtime.GOOS == "darwin" {
		dir := filepath.Dir(expected)
		versioned := filepath.Join(dir, "libonnxruntime."+version+".dylib")
		if regularFile(versioned) {
			return versioned
		}
		matches, _ := filepath.Glob(filepath.Join(dir, "libonnxruntime.*.dylib"))
		for _, candidate := range matches {
			if regularFile(candidate) {
				return candidate
			}
		}
	}
	return expected
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Size() > 0
}

func onnxRuntimeSpec(cfg config.Config) Spec {
	version := strings.TrimSpace(cfg.Inference.RuntimeVersion)
	if version == "" {
		version = "1.23.2"
	}
	path := ResolveRuntimeLibrary(cfg.Inference.RuntimePath, version)
	type platformAsset struct {
		archive, wanted, sha string
		size                 int64
	}
	assets := map[string]platformAsset{
		"linux/amd64": {"onnxruntime-linux-x64-" + version + ".tgz", "libonnxruntime.so." + version, "1fa4dcaef22f6f7d5cd81b28c2800414350c10116f5fdd46a2160082551c5f9b", 8_309_231},
		"linux/arm64": {"onnxruntime-linux-aarch64-" + version + ".tgz", "libonnxruntime.so." + version, "7c63c73560ed76b1fac6cff8204ffe34fe180e70d6582b5332ec094810241e5c", 7_254_068},
		// In the official macOS archives libonnxruntime.dylib is a symlink.
		// Extract the real regular file so installation also works on file
		// systems and archive readers that do not preserve symlinks.
		"darwin/arm64":  {"onnxruntime-osx-arm64-" + version + ".tgz", "libonnxruntime." + version + ".dylib", "b4d513ab2b26f088c66891dbbc1408166708773d7cc4163de7bdca0e9bbb7856", 9_999_931},
		"darwin/amd64":  {"onnxruntime-osx-x86_64-" + version + ".tgz", "libonnxruntime." + version + ".dylib", "d10359e16347b57d9959f7e80a225a5b4a66ed7d7e007274a15cae86836485a6", 11_676_322},
		"windows/amd64": {"onnxruntime-win-x64-" + version + ".zip", "onnxruntime.dll", "0b38df9af21834e41e73d602d90db5cb06dbd1ca618948b8f1d66d607ac9f3cd", 78_127_794},
		"windows/arm64": {"onnxruntime-win-arm64-" + version + ".zip", "onnxruntime.dll", "1cfe88b6435df3b5fb0e9f6bd7d6f5df1e887b6174de7f6e2a47bab956f3f168", 78_932_411},
	}
	asset, ok := assets[runtime.GOOS+"/"+runtime.GOARCH]
	spec := Spec{ID: "onnx-runtime", Name: "ONNX Runtime " + version, Path: path, Group: "runtime", Description: "Нативная среда выполнения ONNX для этой ОС и архитектуры. Загружается один раз; CLI-приложения не используются.", Selected: true, License: "MIT"}
	if !ok || version != "1.23.2" {
		return spec
	}
	spec.URL = "https://github.com/microsoft/onnxruntime/releases/download/v" + version + "/" + asset.archive
	spec.DownloadSHA256 = asset.sha
	spec.ApproxBytes = asset.size
	if strings.HasSuffix(asset.archive, ".zip") {
		spec.Archive = "zip:" + asset.wanted
	} else {
		spec.Archive = "tgz:" + asset.wanted
	}
	return spec
}

// WhisperRuntimeLibraryPath returns the platform path of the whisper.cpp library.
func WhisperRuntimeLibraryPath(configured string) string {
	base := filepath.Join(filepath.Clean(configured), runtime.GOOS+"-"+runtime.GOARCH)
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(base, "Versions", "A", "whisper")
	case "windows":
		return filepath.Join(base, "whisper.dll")
	default:
		return filepath.Join(base, "libwhisper.so.1.9.4")
	}
}

func whisperRuntimeSpec(cfg config.Config) Spec {
	const tag = "b5130"
	base := filepath.Join(filepath.Clean(cfg.Inference.WhisperRuntimePath), runtime.GOOS+"-"+runtime.GOARCH)
	type asset struct {
		name, archive, prefix, runtimeFile, sha string
		size                                    int64
	}
	assets := map[string]asset{
		"darwin/amd64":  {"whisper-b5130-xcframework.zip", "zipdir", "build-apple/whisper.xcframework/macos-arm64_x86_64/whisper.framework/", filepath.Join("Versions", "A", "whisper"), "033a43b0174e8cf9b366f72e4a428cdcf126f93ad1c87d3fa119a96bed6f231a", 57_180_543},
		"darwin/arm64":  {"whisper-b5130-xcframework.zip", "zipdir", "build-apple/whisper.xcframework/macos-arm64_x86_64/whisper.framework/", filepath.Join("Versions", "A", "whisper"), "033a43b0174e8cf9b366f72e4a428cdcf126f93ad1c87d3fa119a96bed6f231a", 57_180_543},
		"linux/amd64":   {"whisper-bin-ubuntu-x64.tar.gz", "tgzlibs", "whisper-bin-ubuntu-x64/", "libwhisper.so.1.9.4", "53e7fd8b5764edad916b8848dd0af6abb1ff1d3b86c899e79c78652412536c32", 9_793_438},
		"linux/arm64":   {"whisper-bin-ubuntu-arm64.tar.gz", "tgzlibs", "whisper-bin-ubuntu-arm64/", "libwhisper.so.1.9.4", "93532a0e3777f26f041ffa358ee77dd88b1a33a86847c1990745327ff335a5d6", 4_605_905},
		"windows/amd64": {"whisper-bin-x64.zip", "ziplibs", "Release/", "whisper.dll", "f9ec6c52a2e949b62ab51fa21d0d497958f9e41c3010c157c4e42932d5316f3c", 8_573_270},
		"windows/arm64": {"whisper-bin-win-cpu-arm64.zip", "ziplibs", "Release/", "whisper.dll", "799543b926ab5b6c2d60cab269a2092e0ae8d27820e9e15429e59de3699546fc", 4_361_895},
	}
	a, ok := assets[runtime.GOOS+"/"+runtime.GOARCH]
	spec := Spec{ID: "whisper-runtime", Name: "Whisper.cpp native runtime", Path: base, Group: "runtime", Description: "Нативная библиотека whisper.cpp. LocalMeetAssist вызывает её напрямую; whisper-cli не устанавливается и не запускается.", Selected: strings.EqualFold(cfg.Transcription.Engine, "whispercpp-native"), License: "MIT"}
	if !ok {
		return spec
	}
	spec.URL = "https://github.com/ggml-org/whisper.cpp/releases/download/" + tag + "/" + a.name
	spec.DownloadSHA256 = a.sha
	spec.Archive = a.archive + ":" + a.prefix
	spec.RequiredFiles = []string{a.runtimeFile}
	spec.ApproxBytes = a.size
	return spec
}

// New builds a manager from the current configuration.
func New(cfg config.Config, logger *log.Logger) *Manager {
	timeout := time.Duration(cfg.Models.DownloadTimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 2 * time.Hour
	}
	samePath := func(left, right string) bool { return filepath.Clean(left) == filepath.Clean(right) }
	gigaDir := cfg.Transcription.ModelPath
	modelsBase := filepath.Join(cfg.App.DataDir, "..", "models", "whisper")
	if cfg.ConfigFile != "" {
		modelsBase = filepath.Join(filepath.Dir(cfg.ConfigFile), "models", "whisper")
	}
	runtimeSpec := onnxRuntimeSpec(cfg)
	whisperSpec := whisperRuntimeSpec(cfg)
	specs := []Spec{
		runtimeSpec,
		whisperSpec,
		{ID: "gigaam-v3-e2e-rnnt-int8", Name: "GigaAM v3 E2E RNNT INT8", Path: gigaDir, Group: "transcription", Description: "Рекомендуемая русскоязычная модель: пунктуация и нормализация текста, RNNT-декодирование, около 227 МБ. Обрабатывает найденные VAD речевые фрагменты непосредственно в LocalMeetAssist.", ApproxBytes: 226_500_000, SettingKey: "transcription.model_path", Engine: "gigaam-onnx", Selectable: true, Selected: samePath(cfg.Transcription.ModelPath, gigaDir) && strings.EqualFold(cfg.Transcription.Engine, "gigaam-onnx"), License: "MIT", Components: []Component{
			{Name: "encoder", Path: filepath.Join(gigaDir, "v3_e2e_rnnt_encoder.int8.onnx"), URL: cfg.Models.GigaAMEncoderURL, SHA256: cfg.Models.GigaAMEncoderSHA256, ApproxBytes: 224_570_477},
			{Name: "decoder", Path: filepath.Join(gigaDir, "v3_e2e_rnnt_decoder.int8.onnx"), URL: cfg.Models.GigaAMDecoderURL, SHA256: cfg.Models.GigaAMDecoderSHA256, ApproxBytes: 1_159_170},
			{Name: "joint", Path: filepath.Join(gigaDir, "v3_e2e_rnnt_joint.int8.onnx"), URL: cfg.Models.GigaAMJointURL, SHA256: cfg.Models.GigaAMJointSHA256, ApproxBytes: 687_791},
			{Name: "vocabulary", Path: filepath.Join(gigaDir, "v3_e2e_rnnt_vocab.txt"), URL: cfg.Models.GigaAMVocabURL, SHA256: cfg.Models.GigaAMVocabSHA256, ApproxBytes: 13_400},
		}},
		{ID: "whisper-small-q5", Name: "Whisper small Q5_1", Path: filepath.Join(modelsBase, "ggml-small-q5_1.bin"), URL: cfg.Models.WhisperSmallURL, SHA256: cfg.Models.WhisperSmallSHA256, Group: "transcription", Description: "Компактный мультиязычный Whisper: быстрее medium, около 181 МБ. Подходит для быстрых черновиков и слабых CPU.", ApproxBytes: 190_085_487, SettingKey: "transcription.model_path", Engine: "whispercpp-native", Selectable: true, Selected: strings.EqualFold(cfg.Transcription.Engine, "whispercpp-native") && samePath(cfg.Transcription.ModelPath, filepath.Join(modelsBase, "ggml-small-q5_1.bin")), License: "MIT"},
		{ID: "whisper-medium-q5", Name: "Whisper medium Q5_0", Path: filepath.Join(modelsBase, "ggml-medium-q5_0.bin"), URL: cfg.Models.WhisperMediumURL, SHA256: cfg.Models.WhisperMediumSHA256, Group: "transcription", Description: "Более точный мультиязычный Whisper, около 514 МБ. Хороший баланс для сложной речи и терминов.", ApproxBytes: 539_212_467, SettingKey: "transcription.model_path", Engine: "whispercpp-native", Selectable: true, Selected: strings.EqualFold(cfg.Transcription.Engine, "whispercpp-native") && samePath(cfg.Transcription.ModelPath, filepath.Join(modelsBase, "ggml-medium-q5_0.bin")), License: "MIT"},
		{ID: "whisper-large-v3-turbo-q5", Name: "Whisper large-v3-turbo Q5_0", Path: filepath.Join(modelsBase, "ggml-large-v3-turbo-q5_0.bin"), URL: cfg.Models.WhisperTurboURL, SHA256: cfg.Models.WhisperTurboSHA256, Group: "transcription", Description: "Наиболее качественный из предлагаемых Whisper при заметно меньшей цене, чем full large-v3; около 574 МБ.", ApproxBytes: 574_041_195, SettingKey: "transcription.model_path", Engine: "whispercpp-native", Selectable: true, Selected: strings.EqualFold(cfg.Transcription.Engine, "whispercpp-native") && samePath(cfg.Transcription.ModelPath, filepath.Join(modelsBase, "ggml-large-v3-turbo-q5_0.bin")), License: "MIT"},
		{ID: "silero-vad", Name: "Silero VAD 6.2 ONNX", Path: cfg.Transcription.VADModelPath, URL: cfg.Models.VADURL, SHA256: cfg.Models.VADSHA256, Group: "speech_filter", Description: "Находит речь и делит длинную встречу на фрагменты до 20 секунд. Снижает повторы и выдуманный текст на тишине.", ApproxBytes: 2_330_000, Selected: true, License: "MIT"},
		{ID: "diarization-segmentation", Name: "PyAnnote Segmentation 3.0 ONNX", Path: cfg.Diarization.SegmentationModel, URL: cfg.Models.SegmentationURL, SHA256: cfg.Models.SegmentationSHA256, Group: "diarization", Description: "Определяет локальные дорожки речи и перекрытия голосов в окнах по 10 секунд. Работает без Python и Hugging Face token.", ApproxBytes: 5_986_908, Selected: true, License: "MIT"},
		{ID: "diarization-embedding-wespeaker", Name: "WeSpeaker ResNet34-LM VoxCeleb", Path: cfg.Diarization.EmbeddingModel, URL: cfg.Models.EmbeddingURL, SHA256: cfg.Models.EmbeddingSHA256, Group: "diarization", Description: "Строит 256-мерные признаки голосов для глобальной кластеризации по всей встрече. Количество участников встречи используется как точное ограничение.", ApproxBytes: 26_530_309, SettingKey: "diarization.embedding_model", Selectable: true, Selected: true, License: "CC BY 4.0"},
	}
	m := &Manager{client: &http.Client{Timeout: timeout}, logger: logger, language: cfg.App.Language, specs: make(map[string]Spec), state: make(map[string]*Status)}
	for _, spec := range specs {
		if english(m.language) {
			if description, ok := modelDescriptionsEN[spec.ID]; ok {
				spec.Description = description
			}
		}
		m.specs[spec.ID] = spec
		m.order = append(m.order, spec.ID)
		m.state[spec.ID] = &Status{Spec: spec}
	}
	return m
}

// ApplyTarget returns an installed selectable model and the configuration key
// that activates it. Fixed support models (VAD and segmentation) are used by
// their own configured paths and therefore do not expose an Apply action.
func (m *Manager) ApplyTarget(id string) (Spec, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	spec, ok := m.specs[id]
	if !ok {
		return Spec{}, errors.New("unknown model")
	}
	if !spec.Selectable || spec.SettingKey == "" {
		return Spec{}, errors.New("this support model does not need to be applied separately")
	}
	if status := m.state[id]; status != nil && status.Downloading {
		return Spec{}, errors.New("wait for the model download to finish")
	}
	if ok, _ := installed(spec); !ok {
		return Spec{}, errors.New("download the model first")
	}
	return spec, nil
}

// UpdateConfig rebuilds the model specs from a new configuration.
func (m *Manager) UpdateConfig(cfg config.Config) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, status := range m.state {
		if status.Downloading {
			return errors.New("cannot change model settings while a download is running")
		}
	}
	updated := New(cfg, m.logger)
	m.specs = updated.specs
	m.order = updated.order
	m.client = updated.client
	m.state = updated.state
	return nil
}

// AnyDownloading reports whether at least one download is running.
func (m *Manager) AnyDownloading() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, status := range m.state {
		if status.Downloading {
			return true
		}
	}
	return false
}

// Statuses returns the current state of every known model.
func (m *Manager) Statuses() []Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	items := make([]Status, 0, len(m.order))
	for _, id := range m.order {
		status := *m.state[id]
		if id == "onnx-runtime" {
			status.Path = ResolveRuntimeLibrary(status.Path, "1.23.2")
			status.Spec.Path = status.Path
		}
		status.Exists, status.SizeBytes = installed(status.Spec)
		items = append(items, status)
	}
	return items
}

// Test verifies files, checksums and that the runtime can load the model.
func (m *Manager) Test(id string) TestResult {
	started := time.Now()
	result := TestResult{ID: id}
	m.mu.Lock()
	spec, ok := m.specs[id]
	runtimeSpec := m.specs["onnx-runtime"]
	whisperRuntime := m.specs["whisper-runtime"]
	m.mu.Unlock()
	if !ok {
		result.Message = "unknown model"
		return result
	}
	result.Path = spec.Path
	if id == "onnx-runtime" {
		spec.Path = ResolveRuntimeLibrary(spec.Path, "1.23.2")
		result.Path = spec.Path
	}
	runtimeSpec.Path = ResolveRuntimeLibrary(runtimeSpec.Path, "1.23.2")
	finish := func(err error) TestResult {
		result.DurationMS = time.Since(started).Milliseconds()
		if err != nil {
			result.Message = err.Error()
			return result
		}
		result.OK = true
		result.Message = "Verification completed successfully"
		return result
	}
	if installed, _ := installed(spec); !installed {
		return finish(errors.New("model files are not fully installed"))
	}
	for _, component := range filesFor(spec) {
		if expected := strings.ToLower(strings.TrimSpace(component.SHA256)); expected != "" {
			actual, err := fileSHA256(component.Path)
			if err != nil {
				return finish(err)
			}
			if actual != expected {
				return finish(fmt.Errorf("SHA-256 mismatch for %s", component.Name))
			}
		}
	}
	result.Checks = append(result.Checks, "Files and checksums")
	if id == "whisper-runtime" {
		if len(spec.RequiredFiles) > 0 {
			result.Path = filepath.Join(spec.Path, spec.RequiredFiles[0])
		}
		if err := whispercpp.Probe(result.Path); err != nil {
			return finish(err)
		}
		result.Checks = append(result.Checks, "Native library loads")
		return finish(nil)
	}
	if strings.HasPrefix(id, "whisper-") {
		if ok, _ := installed(whisperRuntime); !ok {
			return finish(errors.New("install the Whisper.cpp native runtime first"))
		}
		runtimePath := filepath.Join(whisperRuntime.Path, whisperRuntime.RequiredFiles[0])
		runtime, err := whispercpp.Open(runtimePath, spec.Path, "ru", 1)
		if err != nil {
			return finish(err)
		}
		runtime.Close()
		result.Checks = append(result.Checks, "Whisper.cpp loaded the model")
		return finish(nil)
	}
	runtimePath := runtimeSpec.Path
	if id == "onnx-runtime" {
		runtimePath = spec.Path
	}
	if err := inference.EnsureRuntime(runtimePath); err != nil {
		return finish(err)
	}
	result.Checks = append(result.Checks, "ONNX Runtime loaded")
	var probes []struct {
		path    string
		inputs  []string
		outputs []string
	}
	switch id {
	case "onnx-runtime":
		return finish(nil)
	case "gigaam-v3-e2e-rnnt-int8":
		probes = []struct {
			path    string
			inputs  []string
			outputs []string
		}{
			{filepath.Join(spec.Path, "v3_e2e_rnnt_encoder.int8.onnx"), []string{"audio_signal", "length"}, []string{"encoded", "encoded_len"}},
			{filepath.Join(spec.Path, "v3_e2e_rnnt_decoder.int8.onnx"), []string{"x", "h.1", "c.1"}, []string{"dec", "h", "c"}},
			{filepath.Join(spec.Path, "v3_e2e_rnnt_joint.int8.onnx"), []string{"enc", "dec"}, []string{"joint"}},
		}
	case "silero-vad":
		probes = []struct {
			path    string
			inputs  []string
			outputs []string
		}{{spec.Path, []string{"input", "state", "sr"}, []string{"output", "stateN"}}}
	case "diarization-segmentation":
		probes = []struct {
			path    string
			inputs  []string
			outputs []string
		}{{spec.Path, []string{"input_values"}, []string{"logits"}}}
	case "diarization-embedding-wespeaker":
		probes = []struct {
			path    string
			inputs  []string
			outputs []string
		}{{spec.Path, []string{"feats"}, []string{"embs"}}}
	}
	for _, probe := range probes {
		if err := inference.ProbeModel(probe.path, probe.inputs, probe.outputs); err != nil {
			return finish(fmt.Errorf("open ONNX session %s: %w", filepath.Base(probe.path), err))
		}
	}
	if len(probes) > 0 {
		result.Checks = append(result.Checks, "ONNX sessions open")
	}
	return finish(nil)
}

// Start begins a background download and verification of one model.
func (m *Manager) Start(id string) error {
	m.mu.Lock()
	status, ok := m.state[id]
	if !ok {
		m.mu.Unlock()
		return errors.New("unknown model")
	}
	if status.Downloading {
		m.mu.Unlock()
		return errors.New("model download is already running")
	}
	if len(status.Components) == 0 && (strings.TrimSpace(status.URL) == "" || strings.TrimSpace(status.Path) == "") {
		m.mu.Unlock()
		return errors.New("model URL or target path is empty")
	}
	for _, file := range filesFor(status.Spec) {
		parsed, err := url.Parse(file.URL)
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			m.mu.Unlock()
			return errors.New("model URL must use http or https")
		}
	}
	status.Downloading = true
	status.DownloadedBytes = 0
	status.TotalBytes = status.ApproxBytes
	status.LastError = ""
	spec := status.Spec
	m.mu.Unlock()
	go m.download(spec)
	return nil
}

func (m *Manager) download(spec Spec) {
	ctx, cancel := context.WithTimeout(context.Background(), m.client.Timeout)
	defer cancel()
	var err error
	for _, file := range filesFor(spec) {
		if err = m.downloadFile(ctx, spec.ID, file, spec.Archive, spec.DownloadSHA256, len(spec.Components) > 0); err != nil {
			break
		}
	}
	m.mu.Lock()
	status := m.state[spec.ID]
	status.Downloading = false
	if err != nil {
		status.LastError = err.Error()
		appLogging.Errorf(m.logger, "model download failed id=%s error=%v", spec.ID, err)
	} else {
		status.LastError = ""
		m.logger.Printf("model download completed id=%s path=%q", spec.ID, spec.Path)
	}
	m.mu.Unlock()
}

func filesFor(spec Spec) []Component {
	if len(spec.Components) > 0 {
		return spec.Components
	}
	return []Component{{Name: spec.Name, Path: spec.Path, URL: spec.URL, SHA256: spec.SHA256, ApproxBytes: spec.ApproxBytes}}
}

func installed(spec Spec) (bool, int64) {
	if len(spec.RequiredFiles) > 0 {
		var total int64
		for _, relative := range spec.RequiredFiles {
			info, err := os.Stat(filepath.Join(spec.Path, relative))
			if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
				return false, total
			}
			total += info.Size()
		}
		return true, total
	}
	var total int64
	for _, file := range filesFor(spec) {
		info, err := os.Stat(file.Path)
		if err != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			return false, total
		}
		total += info.Size()
	}
	return true, total
}

func (m *Manager) downloadFile(ctx context.Context, id string, file Component, archive, downloadSHA string, skipInstalled bool) error {
	if skipInstalled {
		if info, err := os.Stat(file.Path); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
			valid := true
			if expected := strings.ToLower(strings.TrimSpace(file.SHA256)); expected != "" {
				actual, hashErr := fileSHA256(file.Path)
				valid = hashErr == nil && actual == expected
			}
			if valid {
				m.mu.Lock()
				m.state[id].DownloadedBytes += info.Size()
				m.mu.Unlock()
				return nil
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(file.Path), 0o700); err != nil {
		return err
	}
	downloadPart := file.Path + ".download.part"
	var offset int64
	if info, err := os.Stat(downloadPart); err == nil {
		offset = info.Size()
	}
	completePart := false
	for attempt := 0; attempt < 2 && !completePart; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, file.URL, nil)
		if err != nil {
			return err
		}
		if offset > 0 {
			req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
		}
		resp, err := m.client.Do(req)
		if err != nil {
			return err
		}
		// GitHub correctly returns 416 when a previous run downloaded the
		// complete archive but was interrupted before extraction. Verify that
		// file and continue instead of retrying forever from EOF.
		if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && offset > 0 {
			_ = resp.Body.Close()
			expected := downloadSHA
			if archive == "" {
				expected = file.SHA256
			}
			if validFileSHA256(downloadPart, expected) {
				m.mu.Lock()
				m.state[id].DownloadedBytes = offset
				if m.state[id].TotalBytes < offset {
					m.state[id].TotalBytes = offset
				}
				m.mu.Unlock()
				completePart = true
				break
			}
			_ = os.Remove(downloadPart)
			offset = 0
			continue
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			_ = resp.Body.Close()
			return fmt.Errorf("download HTTP %d", resp.StatusCode)
		}
		if offset > 0 && resp.StatusCode != http.StatusPartialContent {
			offset = 0
			_ = os.Remove(downloadPart)
		}
		flags := os.O_CREATE | os.O_WRONLY
		if offset == 0 {
			flags |= os.O_TRUNC
		} else {
			flags |= os.O_APPEND
		}
		f, err := os.OpenFile(downloadPart, flags, 0o600)
		if err != nil {
			_ = resp.Body.Close()
			return err
		}
		m.mu.Lock()
		progress := &m.state[id].DownloadedBytes
		*progress += offset
		if m.state[id].TotalBytes <= 0 && resp.ContentLength > 0 {
			m.state[id].TotalBytes = offset + resp.ContentLength
		}
		m.mu.Unlock()
		reader := progressReader{r: resp.Body, n: progress, mu: &m.mu}
		_, copyErr := io.Copy(f, reader)
		_ = resp.Body.Close()
		if copyErr != nil {
			_ = f.Close()
			return copyErr
		}
		if err := f.Sync(); err != nil {
			_ = f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		completePart = true
	}
	if !completePart {
		return errors.New("download could not be restarted after HTTP 416")
	}
	if expected := strings.ToLower(strings.TrimSpace(downloadSHA)); expected != "" {
		actual, hashErr := fileSHA256(downloadPart)
		if hashErr != nil {
			return hashErr
		}
		if actual != expected {
			return fmt.Errorf("SHA-256 mismatch: got %s, want %s", actual, expected)
		}
	}
	part := file.Path + ".part"
	if strings.HasPrefix(archive, "zipdir:") || strings.HasPrefix(archive, "tgzdir:") ||
		strings.HasPrefix(archive, "ziplibs:") || strings.HasPrefix(archive, "tgzlibs:") {
		_ = os.RemoveAll(part)
	} else {
		_ = os.Remove(part)
	}
	if archive == "" {
		if err := os.Rename(downloadPart, part); err != nil {
			return err
		}
	} else if err := extractArchiveFile(downloadPart, part, archive); err != nil {
		return err
	}
	if expected := strings.ToLower(strings.TrimSpace(file.SHA256)); expected != "" {
		actual, hashErr := fileSHA256(part)
		if hashErr != nil {
			return hashErr
		}
		if actual != expected {
			return fmt.Errorf("SHA-256 mismatch for %s: got %s, want %s", file.Name, actual, expected)
		}
	}
	if err := replaceFile(part, file.Path); err != nil {
		return err
	}
	_ = os.Remove(downloadPart)
	return nil
}

func validFileSHA256(path, expected string) bool {
	expected = strings.ToLower(strings.TrimSpace(expected))
	if expected == "" {
		return false
	}
	actual, err := fileSHA256(path)
	return err == nil && actual == expected
}

func replaceFile(source, target string) error {
	backup := target + ".old"
	_ = os.RemoveAll(backup)
	if _, err := os.Stat(target); err == nil {
		if err := os.Rename(target, backup); err != nil {
			return err
		}
	}
	if err := os.Rename(source, target); err != nil {
		_ = os.Rename(backup, target)
		return err
	}
	_ = os.RemoveAll(backup)
	return nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func extractArchiveFile(archivePath, outputPath, descriptor string) error {
	kind, wanted, ok := strings.Cut(descriptor, ":")
	if !ok || strings.TrimSpace(wanted) == "" {
		return fmt.Errorf("invalid archive descriptor %q", descriptor)
	}
	wanted = filepath.ToSlash(strings.TrimSpace(wanted))
	kind = strings.ToLower(strings.TrimSpace(kind))
	librariesOnly := strings.HasSuffix(kind, "libs")
	directoryMode := strings.HasSuffix(kind, "dir") || librariesOnly
	if directoryMode {
		if librariesOnly {
			kind = strings.TrimSuffix(kind, "libs")
		} else {
			kind = strings.TrimSuffix(kind, "dir")
		}
		if err := os.MkdirAll(outputPath, 0o700); err != nil {
			return err
		}
	}
	copyEntry := func(name string, r io.Reader) error {
		clean := filepath.ToSlash(filepath.Clean(name))
		if directoryMode {
			if !strings.HasPrefix(clean, wanted) {
				return os.ErrNotExist
			}
			relative := strings.TrimPrefix(clean, wanted)
			if relative == "" || strings.HasPrefix(relative, "../") || filepath.IsAbs(relative) {
				return os.ErrNotExist
			}
			if librariesOnly {
				baseName := strings.ToLower(filepath.Base(relative))
				if !strings.HasPrefix(baseName, "libggml") && !strings.HasPrefix(baseName, "libwhisper") &&
					!strings.HasPrefix(baseName, "ggml") && baseName != "whisper.dll" {
					return os.ErrNotExist
				}
			}
			target := filepath.Join(outputPath, filepath.FromSlash(relative))
			base := filepath.Clean(outputPath) + string(os.PathSeparator)
			if !strings.HasPrefix(filepath.Clean(target)+string(os.PathSeparator), base) {
				return errors.New("unsafe archive path")
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(out, r)
			closeErr := out.Close()
			if copyErr != nil {
				return copyErr
			}
			return closeErr
		}
		if clean != wanted && filepath.Base(clean) != filepath.Base(wanted) {
			return os.ErrNotExist
		}
		out, err := os.OpenFile(outputPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o700)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, r)
		closeErr := out.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	}
	found := false
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "zip":
		zr, err := zip.OpenReader(archivePath)
		if err != nil {
			return err
		}
		defer zr.Close()
		for _, entry := range zr.File {
			if entry.FileInfo().IsDir() {
				continue
			}
			r, openErr := entry.Open()
			if openErr != nil {
				return openErr
			}
			copyErr := copyEntry(entry.Name, r)
			_ = r.Close()
			if copyErr == nil {
				found = true
				if !directoryMode {
					return nil
				}
				continue
			}
			if !errors.Is(copyErr, os.ErrNotExist) {
				return copyErr
			}
		}
	case "tgz", "tar.gz", "tar.bz2":
		f, err := os.Open(archivePath)
		if err != nil {
			return err
		}
		defer f.Close()
		var reader io.Reader = f
		if kind == "tar.bz2" {
			reader = bzip2.NewReader(f)
		} else {
			gz, gzipErr := gzip.NewReader(f)
			if gzipErr != nil {
				return gzipErr
			}
			defer gz.Close()
			reader = gz
		}
		tr := tar.NewReader(reader)
		for {
			header, nextErr := tr.Next()
			if errors.Is(nextErr, io.EOF) {
				break
			}
			if nextErr != nil {
				return nextErr
			}
			if header.Typeflag != tar.TypeReg {
				continue
			}
			copyErr := copyEntry(header.Name, tr)
			if copyErr == nil {
				found = true
				if !directoryMode {
					return nil
				}
				continue
			}
			if !errors.Is(copyErr, os.ErrNotExist) {
				return copyErr
			}
		}
	default:
		return fmt.Errorf("unsupported archive type %q", kind)
	}
	if found {
		return nil
	}
	return fmt.Errorf("%s not found in %s", wanted, filepath.Base(archivePath))
}
