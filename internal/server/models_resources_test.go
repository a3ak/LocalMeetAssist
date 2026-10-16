package server

import (
	"io"
	"log"
	"path/filepath"
	"testing"

	"localmeetassist/internal/config"
	"localmeetassist/internal/modelmanager"
	"localmeetassist/internal/store"
	"localmeetassist/internal/sysinfo"
)

func testServerWithModels(t *testing.T) *Server {
	t.Helper()
	cfg := config.Defaults()
	cfg.App.DataDir = t.TempDir()
	cfg.Storage.DatabasePath = filepath.Join(cfg.App.DataDir, "database", "meetings.db")
	st, err := store.Open(cfg.Storage.DatabasePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return New(cfg, st, log.New(io.Discard, "", 0))
}

// TestModelRequirementsAreDeclared guards the catalogue: a model big enough to
// matter must state what it needs, otherwise the download gate would silently
// let it through on any machine.
func TestModelRequirementsAreDeclared(t *testing.T) {
	s := testServerWithModels(t)
	for _, status := range s.models.Statuses() {
		if status.ApproxBytes < 50<<20 {
			continue
		}
		if status.MinRAMBytes <= 0 || status.MinDiskBytes <= 0 {
			t.Fatalf("модель %s (%d байт) не заявляет требования", status.ID, status.ApproxBytes)
		}
	}
}

// TestResourceShortfallReportsUnknownModel keeps the gate from blocking models
// the catalogue does not know about.
func TestResourceShortfallReportsUnknownModel(t *testing.T) {
	s := testServerWithModels(t)
	if _, _, found, _ := s.resourceShortfall("does-not-exist"); found {
		t.Fatal("неизвестная модель не должна давать требований")
	}
	resources, spec, found, short := s.resourceShortfall("gigaam-v3-e2e-rnnt-int8")
	if !found {
		t.Fatal("требования модели GigaAM должны находиться")
	}
	if spec.MinRAMBytes <= 0 || spec.MinDiskBytes <= 0 {
		t.Fatalf("у GigaAM не заданы требования: %+v", spec)
	}
	if resources.TotalRAMBytes == 0 {
		t.Fatal("ресурсы машины не определились")
	}
	if short {
		t.Skipf("на этой машине действительно не хватает ресурсов: %+v", resources)
	}
}

// TestResourceShortfallFor covers both directions with synthetic values: the
// shortage branch cannot be reached on a machine that is large enough.
func TestResourceShortfallFor(t *testing.T) {
	spec := modelmanager.Spec{MinRAMBytes: 16 << 30, MinDiskBytes: 8 << 30}
	enough := sysinfo.Resources{TotalRAMBytes: 32 << 30, FreeDiskBytes: 100 << 30}
	if resourceShortfallFor(enough, spec) {
		t.Fatal("машины достаточно, отказа быть не должно")
	}
	lowRAM := sysinfo.Resources{TotalRAMBytes: 8 << 30, FreeDiskBytes: 100 << 30}
	if !resourceShortfallFor(lowRAM, spec) {
		t.Fatal("нехватка памяти должна приводить к отказу")
	}
	lowDisk := sysinfo.Resources{TotalRAMBytes: 32 << 30, FreeDiskBytes: 2 << 30}
	if !resourceShortfallFor(lowDisk, spec) {
		t.Fatal("нехватка места должна приводить к отказу")
	}
	noRequirements := modelmanager.Spec{}
	if resourceShortfallFor(sysinfo.Resources{}, noRequirements) {
		t.Fatal("модель без требований не должна блокироваться")
	}
}
