//go:build darwin

package gpu

import (
	"os/exec"
	"runtime"
	"strconv"
	"strings"
)

// Detect queries the system for a supported GPU.
// On macOS, tries NVIDIA first (eGPU), then Apple Silicon unified memory.
func Detect() Info {
	if info, ok := detectNVIDIA(); ok {
		return info
	}
	if info, ok := detectAppleSilicon(); ok {
		return info
	}
	return Info{Vendor: "none"}
}

// detectNVIDIA queries nvidia-smi (for external NVIDIA eGPUs on macOS).
// Enumerates ALL NVIDIA GPUs.
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

// detectAppleSilicon detects Apple Silicon (M1/M2/M3/M4) and reports
// unified memory as available VRAM. Ollama uses Metal on Apple Silicon,
// so the full unified memory pool is available for model loading.
func detectAppleSilicon() (Info, bool) {
	if runtime.GOARCH != "arm64" {
		return Info{}, false
	}

	// Get total physical memory via sysctl.
	memOut, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
	if err != nil {
		return Info{}, false
	}
	memBytes, err := strconv.ParseInt(strings.TrimSpace(string(memOut)), 10, 64)
	if err != nil || memBytes == 0 {
		return Info{}, false
	}
	totalMB := int(memBytes / 1024 / 1024)

	// Get chip name (e.g. "Apple M3 Pro").
	name := "Apple Silicon"
	if chipOut, err := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output(); err == nil {
		if s := strings.TrimSpace(string(chipOut)); s != "" {
			name = s
		}
	}

	// Reserve ~25% for macOS and other apps — report 75% as free.
	freeMB := totalMB * 75 / 100

	return Info{
		Vendor:      "apple",
		Name:        name,
		VRAMTotalMB: totalMB,
		VRAMFreeMB:  freeMB,
		VRAMTotalGB: float64(totalMB) / 1024,
		Count:       1,
		GPUs:        []GPUDetail{{Name: name, VRAMTotalMB: totalMB, VRAMFreeMB: freeMB}},
		VRAMExact:   false, // unified memory, not dedicated VRAM
	}, true
}
