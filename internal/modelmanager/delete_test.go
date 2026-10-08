package modelmanager

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"

	"localmeetassist/internal/config"
)

// TestRemoveModelPathIsSafe covers what deleting a model actually does on disk:
// a plain file is removed, a dedicated directory is removed with its content,
// and a missing path is not an error.
func TestRemoveModelPathIsSafe(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "model.onnx")
	if err := os.WriteFile(file, []byte("weights"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := removeModelPath(file); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("model file must be gone, stat returned %v", err)
	}

	sub := filepath.Join(dir, "runtime")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "lib.so"), []byte("lib"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := removeModelPath(sub); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sub); !os.IsNotExist(err) {
		t.Fatalf("model directory must be gone, stat returned %v", err)
	}

	if err := removeModelPath(filepath.Join(dir, "never-existed")); err != nil {
		t.Fatalf("a missing path must not be an error: %v", err)
	}
}

// TestDeleteKeepsAppliedModels refuses to delete the model the pipeline is
// configured to use: the file under a running configuration must stay.
func TestDeleteKeepsAppliedModels(t *testing.T) {
	m := New(config.Defaults(), log.New(io.Discard, "", 0))
	applied := 0
	for _, status := range m.Statuses() {
		if !status.Selected {
			continue
		}
		applied++
		if err := m.Delete(status.ID); err == nil {
			t.Fatalf("applied model %s must not be deleted", status.ID)
		}
	}
	if applied == 0 {
		t.Fatal("the default configuration must have at least one applied model")
	}
	if err := m.Delete("no-such-model"); err == nil {
		t.Fatal("an unknown model must not be deleted")
	}
}
