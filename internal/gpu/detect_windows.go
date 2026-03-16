package gpu

import (
	"encoding/json"
	"os/exec"
	"strconv"
	"strings"
)

// Detect queries the system for a supported GPU.
// Tries NVIDIA first (primary gaming GPU vendor), then AMD.
// Returns Info with Vendor="none" if no supported GPU is found.
func Detect() Info {
	if info, ok := detectNVIDIA(); ok {
		return info
	}
	if info, ok := detectAMD(); ok {
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
		Count:         len(lines), // one line per GPU
		VRAMExact:     true,
	}, true
}

// detectAMD queries Win32_VideoController via PowerShell.
// Note: WMI caps AdapterRAM at 4 GB (uint32 max) for high-VRAM cards.
// We flag VRAMExact=false and use the WMI value as a conservative floor.
func detectAMD() (Info, bool) {
	// PowerShell is available on all Windows 7+ systems.
	script := `Get-WmiObject Win32_VideoController |` +
		` Where-Object {$_.Name -match 'AMD|Radeon|ATI'} |` +
		` Select-Object -First 1 Name,AdapterRAM,DriverVersion |` +
		` ConvertTo-Json`

	out, err := exec.Command("powershell", "-NoProfile", "-Command", script).Output()
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		return Info{}, false
	}

	var result struct {
		Name          string `json:"Name"`
		AdapterRAM    int64  `json:"AdapterRAM"`
		DriverVersion string `json:"DriverVersion"`
	}
	if err := json.Unmarshal(out, &result); err != nil || result.Name == "" {
		return Info{}, false
	}

	totalMB := int(result.AdapterRAM / 1024 / 1024)
	exact := true

	// WMI reports 4294967295 bytes (uint32 max) when VRAM >= 4 GB.
	// Use 4096 MB as a conservative floor; actual VRAM is likely higher.
	if result.AdapterRAM == 4294967295 {
		totalMB = 4096
		exact = false
	}

	return Info{
		Vendor:        "amd",
		Name:          strings.TrimSpace(result.Name),
		VRAMTotalMB:   totalMB,
		VRAMFreeMB:    0, // AMD has no equivalent of nvidia-smi on Windows without HIP SDK
		VRAMTotalGB:   float64(totalMB) / 1024,
		DriverVersion: strings.TrimSpace(result.DriverVersion),
		Count:         1,
		VRAMExact:     exact,
	}, true
}
