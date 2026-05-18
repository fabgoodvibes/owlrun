package gpu

import (
	"encoding/json"
	"os/exec"
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

// detectAMD queries Win32_VideoController via PowerShell.
// Enumerates ALL AMD GPUs. Note: WMI caps AdapterRAM at 4 GB (uint32 max)
// for high-VRAM cards; VRAMExact=false in that case.
func detectAMD() (Info, bool) {
	// PowerShell is available on all Windows 7+ systems.
	// ConvertTo-Json -AsArray ensures we always get a JSON array even for one GPU.
	script := `Get-WmiObject Win32_VideoController |` +
		` Where-Object {$_.Name -match 'AMD|Radeon|ATI'} |` +
		` Select-Object Name,AdapterRAM,DriverVersion |` +
		` ConvertTo-Json -AsArray`

	out, err := exec.Command("powershell", "-NoProfile", "-Command", script).Output()
	if err != nil || len(strings.TrimSpace(string(out))) == 0 {
		return Info{}, false
	}

	type wmiGPU struct {
		Name          string `json:"Name"`
		AdapterRAM    int64  `json:"AdapterRAM"`
		DriverVersion string `json:"DriverVersion"`
	}
	var results []wmiGPU
	// -AsArray was added in PowerShell 6+; on Windows PowerShell 5 a single
	// object is emitted as a bare object. Handle both shapes.
	if err := json.Unmarshal(out, &results); err != nil {
		var single wmiGPU
		if err2 := json.Unmarshal(out, &single); err2 != nil || single.Name == "" {
			return Info{}, false
		}
		results = []wmiGPU{single}
	}
	if len(results) == 0 {
		return Info{}, false
	}

	info := Info{Vendor: "amd", VRAMExact: true}
	for _, r := range results {
		if r.Name == "" {
			continue
		}
		totalMB := int(r.AdapterRAM / 1024 / 1024)
		// WMI reports 4294967295 bytes (uint32 max) when VRAM >= 4 GB.
		// Use 4096 MB as a conservative floor; actual VRAM is likely higher.
		if r.AdapterRAM == 4294967295 {
			totalMB = 4096
			info.VRAMExact = false
		}
		info.GPUs = append(info.GPUs, GPUDetail{
			Name:        strings.TrimSpace(r.Name),
			VRAMTotalMB: totalMB,
		})
		info.VRAMTotalMB += totalMB
		if info.DriverVersion == "" {
			info.DriverVersion = strings.TrimSpace(r.DriverVersion)
		}
	}
	if len(info.GPUs) == 0 {
		return Info{}, false
	}
	info.Count = len(info.GPUs)
	info.VRAMTotalGB = float64(info.VRAMTotalMB) / 1024
	info.Name = info.GPUs[info.LargestGPUIndex()].Name
	return info, true
}
