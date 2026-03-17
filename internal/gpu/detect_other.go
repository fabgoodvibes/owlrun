//go:build !windows && !darwin

package gpu

import (
	"os/exec"
	"strconv"
	"strings"
)

// Detect queries the system for a supported GPU.
// On Linux, tries NVIDIA via nvidia-smi (AMD rocm-smi is Phase 2).
func Detect() Info {
	if info, ok := detectNVIDIA(); ok {
		return info
	}
	return Info{Vendor: "none"}
}

// detectNVIDIA queries nvidia-smi, which ships with all NVIDIA drivers.
func detectNVIDIA() (Info, bool) {
	out, err := exec.Command("nvidia-smi",
		"--query-gpu=name,memory.total,memory.free,driver_version",
		"--format=csv,noheader,nounits",
	).Output()
	if err != nil {
		return Info{}, false
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	parts := strings.Split(lines[0], ", ")
	if len(parts) < 4 {
		return Info{}, false
	}

	totalMB, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
	freeMB, _ := strconv.Atoi(strings.TrimSpace(parts[2]))

	return Info{
		Vendor:        "nvidia",
		Name:          strings.TrimSpace(parts[0]),
		VRAMTotalMB:   totalMB,
		VRAMFreeMB:    freeMB,
		VRAMTotalGB:   float64(totalMB) / 1024,
		DriverVersion: strings.TrimSpace(parts[3]),
		Count:         len(lines),
		VRAMExact:     true,
	}, true
}
