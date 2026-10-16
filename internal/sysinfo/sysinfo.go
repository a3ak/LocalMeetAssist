// Package sysinfo reports the machine resources that decide whether a local
// model can actually run here.
package sysinfo

import (
	"os"
	"path/filepath"
	"runtime"
)

// Resources is a point-in-time snapshot of the machine.
type Resources struct {
	TotalRAMBytes uint64 `json:"total_ram_bytes"`
	FreeDiskBytes uint64 `json:"free_disk_bytes"`
	CPUCores      int    `json:"cpu_cores"`
}

// Detect measures the machine. dataDir is where models are stored; the free
// space of the filesystem holding it is reported.
func Detect(dataDir string) Resources {
	resources := Resources{CPUCores: runtime.NumCPU()}
	if bytes, err := totalRAMBytes(); err == nil {
		resources.TotalRAMBytes = bytes
	}
	resources.FreeDiskBytes = freeDiskBytes(dataDir)
	return resources
}

// freeDiskBytes reports the free space of the filesystem holding dir. A path
// that does not exist yet is resolved through its closest existing parent.
func freeDiskBytes(dir string) uint64 {
	if dir == "" {
		return 0
	}
	probe := filepath.Clean(dir)
	for {
		if _, err := os.Stat(probe); err == nil {
			bytes, err := diskFreeBytes(probe)
			if err != nil {
				return 0
			}
			return bytes
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return 0
		}
		probe = parent
	}
}

// GigaBytes rounds a byte count up to whole gibibytes, which is how system
// requirements are shown to people.
func GigaBytes(bytes uint64) uint64 {
	if bytes == 0 {
		return 0
	}
	return (bytes + (1 << 30) - 1) >> 30
}
