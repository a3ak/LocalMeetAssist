package sysinfo

import (
	"path/filepath"
	"testing"
)

func TestDetectReportsThisMachine(t *testing.T) {
	resources := Detect(".")
	if resources.TotalRAMBytes == 0 {
		t.Fatal("total RAM was not detected")
	}
	if resources.FreeDiskBytes == 0 {
		t.Fatal("free disk space was not detected")
	}
	if resources.CPUCores == 0 {
		t.Fatal("cpu cores were not detected")
	}
	t.Logf("RAM %.1f GiB, free disk %.1f GiB, %d cores",
		float64(resources.TotalRAMBytes)/(1<<30), float64(resources.FreeDiskBytes)/(1<<30), resources.CPUCores)
}

func TestDetectResolvesMissingDirectory(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "models", "asr", "not-created-yet")
	if got := freeDiskBytes(missing); got == 0 {
		t.Fatalf("free disk space for a missing path = %d, want a real value", got)
	}
	if got := freeDiskBytes(""); got != 0 {
		t.Fatalf("free disk space for an empty path = %d, want 0", got)
	}
}

func TestGigaBytesRoundsUp(t *testing.T) {
	cases := []struct {
		bytes uint64
		want  uint64
	}{
		{0, 0},
		{1, 1},
		{1 << 30, 1},
		{(1 << 30) + 1, 2},
		{8 << 30, 8},
		{(8 << 30) - 1, 8},
	}
	for _, item := range cases {
		if got := GigaBytes(item.bytes); got != item.want {
			t.Fatalf("GigaBytes(%d) = %d, want %d", item.bytes, got, item.want)
		}
	}
}
