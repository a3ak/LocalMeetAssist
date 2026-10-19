package server

import (
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"localmeetassist/internal/config"
	"localmeetassist/internal/store"
)

// newModelsServer builds a server with a writable configuration file, which is
// what an applied or downloaded model needs in order to be persisted.
func newModelsServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Defaults()
	cfg.ConfigFile = filepath.Join(dir, "config.toml")
	cfg.App.DataDir = filepath.Join(dir, "data")
	cfg.Storage.DatabasePath = filepath.Join(cfg.App.DataDir, "database", "meetings.db")
	if err := os.WriteFile(cfg.ConfigFile, []byte("[app]\nlanguage = \"ru\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return New(cfg, st, log.New(io.Discard, "", 0))
}

// installModelFile puts a placeholder file where a model card expects it, so the
// Apply action sees an installed model.
func installModelFile(t *testing.T, s *Server, id string) string {
	t.Helper()
	for _, status := range s.models.Statuses() {
		if status.ID != id {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(status.Path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(status.Path, []byte("gguf"), 0o600); err != nil {
			t.Fatal(err)
		}
		return status.Path
	}
	t.Fatalf("model %q is not listed", id)
	return ""
}

// TestDownloadingLocalModelActivatesItWhenNoEndpoint covers the fresh install:
// the recommended set includes a local minutes model, and without a configured
// endpoint automatic minutes would otherwise fail on the first meeting.
func TestDownloadingLocalModelActivatesItWhenNoEndpoint(t *testing.T) {
	s := newModelsServer(t)
	if err := s.enableStepsForModels("qwen3-5-4b-q4"); err != nil {
		t.Fatalf("enable steps: %v", err)
	}
	cfg := s.config()
	if !cfg.Summary.AutoRun {
		t.Fatal("summarization was not switched on")
	}
	if !cfg.Summary.LocalSummary() {
		t.Fatalf("the local model was downloaded but the engine is %q", cfg.Summary.Engine)
	}
	if !strings.HasSuffix(cfg.Summary.ModelPath, "Qwen3.5-4B-Q4_K_M.gguf") {
		t.Fatalf("the downloaded model was not activated: %q", cfg.Summary.ModelPath)
	}
}

// TestDownloadingLocalModelKeepsConfiguredEndpoint guards a user who already
// summarizes through a server: installing a local model must not silently
// redirect the transcript away from it.
func TestDownloadingLocalModelKeepsConfiguredEndpoint(t *testing.T) {
	s := newModelsServer(t)
	if err := s.persistSettings(map[string]string{
		"summary.base_url": `"https://api.example.com/v1"`,
		"summary.model":    `"Qwen3.5"`,
	}); err != nil {
		t.Fatalf("configure endpoint: %v", err)
	}
	if err := s.enableStepsForModels("qwen3-5-4b-q4"); err != nil {
		t.Fatalf("enable steps: %v", err)
	}
	cfg := s.config()
	if cfg.Summary.LocalSummary() {
		t.Fatal("a configured endpoint was replaced by the local model")
	}
	if !cfg.Summary.AutoRun {
		t.Fatal("summarization was not switched on")
	}
}

// TestApplyingLocalModelSwitchesTheEngine covers the Apply button of the model
// card: it has to set both the file and the engine.
func TestApplyingLocalModelSwitchesTheEngine(t *testing.T) {
	s := newModelsServer(t)
	path := installModelFile(t, s, "qwen3-5-4b-q4")

	recorder := httptest.NewRecorder()
	s.applyModel(recorder, "qwen3-5-4b-q4")
	if recorder.Code != http.StatusOK {
		t.Fatalf("apply failed: %d %s", recorder.Code, recorder.Body.String())
	}
	cfg := s.config()
	if !cfg.Summary.LocalSummary() {
		t.Fatalf("the engine is %q after applying a local model", cfg.Summary.Engine)
	}
	if filepath.Clean(cfg.Summary.ModelPath) != filepath.Clean(path) {
		t.Fatalf("the card applied %q instead of %q", cfg.Summary.ModelPath, path)
	}
}
