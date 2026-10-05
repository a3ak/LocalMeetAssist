package modelmanager

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"localmeetassist/internal/config"
)

func TestExtractRuntimeArchives(t *testing.T) {
	payload := []byte("runtime-library")
	for _, kind := range []string{"tgz", "zip"} {
		t.Run(kind, func(t *testing.T) {
			dir := t.TempDir()
			archivePath := filepath.Join(dir, "runtime."+kind)
			var data bytes.Buffer
			if kind == "tgz" {
				gz := gzip.NewWriter(&data)
				tw := tar.NewWriter(gz)
				if err := tw.WriteHeader(&tar.Header{Name: "package/lib/libonnxruntime.so.1.23.2", Mode: 0o755, Size: int64(len(payload))}); err != nil {
					t.Fatal(err)
				}
				if _, err := tw.Write(payload); err != nil {
					t.Fatal(err)
				}
				if err := tw.Close(); err != nil {
					t.Fatal(err)
				}
				if err := gz.Close(); err != nil {
					t.Fatal(err)
				}
			} else {
				zw := zip.NewWriter(&data)
				entry, err := zw.Create("package/lib/onnxruntime.dll")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := entry.Write(payload); err != nil {
					t.Fatal(err)
				}
				if err := zw.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(archivePath, data.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			output := filepath.Join(dir, "out")
			wanted := "libonnxruntime.so.1.23.2"
			if kind == "zip" {
				wanted = "onnxruntime.dll"
			}
			if err := extractArchiveFile(archivePath, output, kind+":"+wanted); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(output)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, payload) {
				t.Fatalf("unexpected payload %q", got)
			}
		})
	}
}

func TestExtractMacRuntimeUsesVersionedRegularFile(t *testing.T) {
	payload := []byte("versioned-runtime-library")
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "runtime.tgz")
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "onnxruntime/lib/libonnxruntime.dylib", Typeflag: tar.TypeSymlink, Linkname: "libonnxruntime.1.23.2.dylib"}); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "onnxruntime/lib/libonnxruntime.1.23.2.dylib", Mode: 0o755, Size: int64(len(payload))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "libonnxruntime.dylib")
	if err := extractArchiveFile(archivePath, output, "tgz:libonnxruntime.1.23.2.dylib"); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("unexpected payload %q", got)
	}
}

func TestExtractWhisperRuntimeOmitsCLIPrograms(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "whisper.zip")
	var data bytes.Buffer
	zw := zip.NewWriter(&data)
	for name, payload := range map[string]string{
		"Release/whisper.dll":     "runtime",
		"Release/ggml-base.dll":   "dependency",
		"Release/whisper-cli.exe": "must-not-be-installed",
	} {
		entry, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(payload)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath, data.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "runtime")
	if err := extractArchiveFile(archivePath, output, "ziplibs:Release/"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"whisper.dll", "ggml-base.dll"} {
		if _, err := os.Stat(filepath.Join(output, name)); err != nil {
			entries, _ := os.ReadDir(output)
			t.Fatalf("expected runtime library %s: %v; extracted=%v", name, err, entries)
		}
	}
	if _, err := os.Stat(filepath.Join(output, "whisper-cli.exe")); !os.IsNotExist(err) {
		t.Fatalf("CLI executable must not be installed: %v", err)
	}
}

func TestExtractWhisperRuntimeTGZOmitCLIPrograms(t *testing.T) {
	dir := t.TempDir()
	archivePath := filepath.Join(dir, "whisper.tar.gz")
	f, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, payload := range map[string]string{
		"whisper-bin-ubuntu-x64/libwhisper.so.1.9.4": "runtime",
		"whisper-bin-ubuntu-x64/libggml-base.so":     "dependency",
		"whisper-bin-ubuntu-x64/whisper-cli":         "must-not-be-installed",
	} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(payload))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(payload)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "runtime")
	if err := extractArchiveFile(archivePath, output, "tgzlibs:whisper-bin-ubuntu-x64/"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"libwhisper.so.1.9.4", "libggml-base.so"} {
		if _, err := os.Stat(filepath.Join(output, name)); err != nil {
			t.Fatalf("expected runtime library %s: %v", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(output, "whisper-cli")); !os.IsNotExist(err) {
		t.Fatalf("CLI executable must not be installed: %v", err)
	}
}

func TestDownloadModelAtomically(t *testing.T) {
	payload := []byte("localmeetassist-test-model")
	hash := sha256.Sum256(payload)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Transcription.VADModelPath = filepath.Join(dir, "silero_vad.onnx")
	cfg.Models.VADURL = server.URL
	cfg.Models.VADSHA256 = hex.EncodeToString(hash[:])
	m := New(cfg, log.New(io.Discard, "", 0))
	if err := m.Start("silero-vad"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		var status Status
		for _, item := range m.Statuses() {
			if item.ID == "silero-vad" {
				status = item
			}
		}
		if !status.Downloading {
			if status.LastError != "" {
				t.Fatal(status.LastError)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("model download did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	got, err := os.ReadFile(cfg.Transcription.VADModelPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(payload) {
		t.Fatalf("download mismatch: %q", got)
	}
	if _, err := os.Stat(cfg.Transcription.VADModelPath + ".part"); !os.IsNotExist(err) {
		t.Fatalf("temporary file was not removed: %v", err)
	}
}

func TestDownloadContinuesCompletedPartAfterHTTP416(t *testing.T) {
	payload := []byte("already-complete-model")
	hash := sha256.Sum256(payload)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Range") != "" {
			w.Header().Set("Content-Range", "bytes */22")
			w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
			return
		}
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	dir := t.TempDir()
	target := filepath.Join(dir, "model.onnx")
	if err := os.WriteFile(target+".download.part", payload, 0o600); err != nil {
		t.Fatal(err)
	}
	m := &Manager{
		client: &http.Client{Timeout: time.Second},
		logger: log.New(io.Discard, "", 0),
		state:  map[string]*Status{"test": {Spec: Spec{ID: "test"}, TotalBytes: int64(len(payload))}},
	}
	component := Component{Name: "test", Path: target, URL: server.URL, SHA256: hex.EncodeToString(hash[:])}
	if err := m.downloadFile(t.Context(), "test", component, "", "", false); err != nil {
		t.Fatal(err)
	}
	if requests != 1 {
		t.Fatalf("completed part should not be downloaded again, requests=%d", requests)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatalf("unexpected installed payload %q", got)
	}
}

func TestApplyTargetRequiresInstalledSelectableModel(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.Transcription.ModelPath = filepath.Join(dir, "gigaam")
	m := New(cfg, log.New(io.Discard, "", 0))
	if _, err := m.ApplyTarget("gigaam-v3-e2e-rnnt-int8"); err == nil {
		t.Fatal("expected missing model error")
	}
	for _, name := range []string{"v3_e2e_rnnt_encoder.int8.onnx", "v3_e2e_rnnt_decoder.int8.onnx", "v3_e2e_rnnt_joint.int8.onnx", "v3_e2e_rnnt_vocab.txt"} {
		if err := os.MkdirAll(cfg.Transcription.ModelPath, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(cfg.Transcription.ModelPath, name), []byte("model"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	spec, err := m.ApplyTarget("gigaam-v3-e2e-rnnt-int8")
	if err != nil {
		t.Fatal(err)
	}
	if spec.SettingKey != "transcription.model_path" || spec.Path != cfg.Transcription.ModelPath {
		t.Fatalf("unexpected apply target: %+v", spec)
	}
	if _, err := m.ApplyTarget("silero-vad"); err == nil {
		t.Fatal("support model must not be selectable")
	}
}

func TestApplyWhisperModelSelectsNativeEngine(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.ConfigFile = filepath.Join(dir, "config.toml")
	modelPath := filepath.Join(dir, "models", "whisper", "ggml-small-q5_1.bin")
	if err := os.MkdirAll(filepath.Dir(modelPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(modelPath, []byte("model"), 0o600); err != nil {
		t.Fatal(err)
	}
	m := New(cfg, log.New(io.Discard, "", 0))
	spec, err := m.ApplyTarget("whisper-small-q5")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Engine != "whispercpp-native" || filepath.Clean(spec.Path) != filepath.Clean(modelPath) {
		t.Fatalf("unexpected Whisper target: %+v", spec)
	}
}
