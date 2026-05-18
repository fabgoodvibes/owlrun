// Package gpu detects GPU hardware and provides live utilisation stats.
package gpu

import (
	"fmt"
	"strings"
	"time"
)

// GPUDetail describes a single physical GPU within a multi-GPU rig.
type GPUDetail struct {
	Name        string
	VRAMTotalMB int
	VRAMFreeMB  int
}

// Info holds static GPU metadata detected at startup.
//
// VRAMTotalMB / VRAMTotalGB are the SUM across all GPUs of the same vendor.
// To know whether a single model can fit on one card, use LargestSingleVRAMGB
// or EffectiveVRAMGB(info, split).
type Info struct {
	Vendor        string  // "nvidia" | "amd" | "apple" | "none"
	Name          string  // primary GPU name (first detected)
	VRAMTotalMB   int     // SUM of VRAM across all detected GPUs of this vendor (MB)
	VRAMFreeMB    int     // SUM of free VRAM across all detected GPUs (MB)
	VRAMTotalGB   float64 // TotalMB / 1024
	DriverVersion string
	Count         int         // Number of GPUs of this vendor
	GPUs          []GPUDetail // Per-GPU breakdown (len == Count when populated)
	VRAMExact     bool        // false = WMI reported 4 GB cap (AMD); actual VRAM may be higher
}

// LargestGPUIndex returns the index of the GPU with the most VRAM.
// Returns 0 if there are no per-GPU details.
func (i Info) LargestGPUIndex() int {
	idx, maxMB := 0, 0
	for k, g := range i.GPUs {
		if g.VRAMTotalMB > maxMB {
			maxMB = g.VRAMTotalMB
			idx = k
		}
	}
	return idx
}

// Describe returns a human-readable summary of the detected GPU(s).
// Single GPU: "NVIDIA RTX 4090 (24 GB)"
// Multi GPU:  "2x nvidia: RTX 4090 (24 GB) + GTX 1050 Ti (4 GB), 28 GB total"
func (i Info) Describe() string {
	if len(i.GPUs) <= 1 {
		return fmt.Sprintf("%s (%.0f GB)", i.Name, i.VRAMTotalGB)
	}
	parts := make([]string, len(i.GPUs))
	for k, g := range i.GPUs {
		parts[k] = fmt.Sprintf("%s (%.0f GB)", g.Name, float64(g.VRAMTotalMB)/1024)
	}
	return fmt.Sprintf("%dx %s: %s, %.0f GB total",
		len(i.GPUs), i.Vendor, strings.Join(parts, " + "), i.VRAMTotalGB)
}

// LargestSingleVRAMGB returns the VRAM (GB) of the single biggest GPU.
// This is the safe upper bound for a model that does not split across cards.
// Falls back to VRAMTotalGB if per-GPU details aren't available.
func (i Info) LargestSingleVRAMGB() float64 {
	if len(i.GPUs) == 0 {
		return i.VRAMTotalGB
	}
	maxMB := 0
	for _, g := range i.GPUs {
		if g.VRAMTotalMB > maxMB {
			maxMB = g.VRAMTotalMB
		}
	}
	return float64(maxMB) / 1024
}

// EffectiveVRAMGB returns the VRAM budget the model selector should plan for.
// When split=false (default), models must fit on a single card → returns the
// largest single GPU. When split=true, the user has opted into tensor splitting
// (OLLAMA_SCHED_SPREAD=1) so the full pooled VRAM is usable.
func EffectiveVRAMGB(info Info, split bool) float64 {
	if split {
		return info.VRAMTotalGB
	}
	return info.LargestSingleVRAMGB()
}

// GPUStat is a live reading for a single physical GPU.
type GPUStat struct {
	UtilizationPct int     `json:"util_pct"`
	VRAMFreeMB     int     `json:"vram_free_mb"`
	TemperatureC   int     `json:"temp_c"`
	PowerDrawW     float64 `json:"power_w"`
}

// Stats holds a live GPU reading from the most recent poll.
// Aggregate fields are worst-case across all GPUs (so a single busy card
// still trips the idle threshold). PerGPU has one entry per detected card,
// in the same order as Info.GPUs.
type Stats struct {
	UtilizationPct int     // 0–100 (worst-case across all GPUs)
	VRAMFreeMB     int     // Least free VRAM across all GPUs
	TemperatureC   int     // Hottest GPU temperature
	PowerDrawW     float64 // Sum of power draw across all GPUs
	PerGPU         []GPUStat
	Timestamp      time.Time
}
