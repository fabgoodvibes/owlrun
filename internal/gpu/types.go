// Package gpu detects GPU hardware and provides live utilisation stats.
package gpu

import "time"

// Info holds static GPU metadata detected at startup.
type Info struct {
	Vendor        string  // "nvidia" | "amd" | "apple" | "none"
	Name          string  // e.g. "NVIDIA GeForce RTX 4090"
	VRAMTotalMB   int     // Total VRAM in MB
	VRAMFreeMB    int     // Free VRAM at detection time
	VRAMTotalGB   float64 // TotalMB / 1024
	DriverVersion string
	Count         int  // Number of GPUs of this vendor
	VRAMExact     bool // false = WMI reported 4 GB cap (AMD); actual VRAM may be higher
}

// Stats holds a live GPU reading from the most recent poll.
type Stats struct {
	UtilizationPct int     // 0–100 (worst-case across all GPUs)
	VRAMFreeMB     int     // Least free VRAM across all GPUs
	TemperatureC   int     // Hottest GPU temperature
	PowerDrawW     float64 // Sum of power draw across all GPUs
	Timestamp      time.Time
}
