//go:build darwin

package sysinfo

import (
	"os/exec"
	"strconv"
	"strings"
)

// totalRAMBytes asks sysctl for hw.memsize. The Go syscall package cannot read
// this value: Sysctl returns a truncated byte string on darwin, so the standard
// system tool is used instead. It runs once per resources detection.
func totalRAMBytes() (uint64, error) {
	output, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return 0, err
	}
	return strconv.ParseUint(strings.TrimSpace(string(output)), 10, 64)
}
