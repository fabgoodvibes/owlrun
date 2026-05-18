//go:build !windows && !darwin

package gpu

import (
	"os/exec"
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
// Enumerates ALL NVIDIA GPUs and aggregates VRAM.
func detectNVIDIA() (Info, bool) {
	out, err := exec.Command("nvidia-smi",
		"--query-gpu=name,memory.total,memory.free,driver_version",
		"--format=csv,noheader,nounits",
	).Output()
	if err != nil {
		return Info{}, false
	}
	return parseNvidiaSmi(string(out))
}

