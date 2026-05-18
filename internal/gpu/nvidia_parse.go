package gpu

import (
	"strconv"
	"strings"
)

// parseNvidiaSmi parses multi-line nvidia-smi CSV output (one GPU per line)
// into an Info that aggregates VRAM across all detected NVIDIA GPUs.
//
// Expected format: "name, memory.total, memory.free, driver_version" with
// --format=csv,noheader,nounits.
func parseNvidiaSmi(raw string) (Info, bool) {
	lines := strings.Split(strings.TrimSpace(raw), "\n")
	if len(lines) == 0 || strings.TrimSpace(lines[0]) == "" {
		return Info{}, false
	}

	info := Info{Vendor: "nvidia", VRAMExact: true}
	for _, line := range lines {
		parts := strings.Split(line, ", ")
		if len(parts) < 4 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		totalMB, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
		freeMB, _ := strconv.Atoi(strings.TrimSpace(parts[2]))
		driver := strings.TrimSpace(parts[3])

		info.GPUs = append(info.GPUs, GPUDetail{
			Name:        name,
			VRAMTotalMB: totalMB,
			VRAMFreeMB:  freeMB,
		})
		info.VRAMTotalMB += totalMB
		info.VRAMFreeMB += freeMB
		if info.DriverVersion == "" {
			info.DriverVersion = driver
		}
	}
	if len(info.GPUs) == 0 {
		return Info{}, false
	}
	info.Count = len(info.GPUs)
	info.VRAMTotalGB = float64(info.VRAMTotalMB) / 1024
	// Primary "Name" defaults to the largest GPU — that's the one Ollama
	// will pin to when multi-GPU split is disabled.
	info.Name = info.GPUs[info.LargestGPUIndex()].Name
	return info, true
}
