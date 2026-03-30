package gpu

import (
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Monitor polls the GPU on a fixed interval and caches the latest Stats.
// It is the single source of truth for live GPU utilisation — both the idle
// detector and the dashboard read from here rather than calling nvidia-smi
// independently.
type Monitor struct {
	info     Info
	interval time.Duration
	mu       sync.RWMutex
	latest   Stats
}

// NewMonitor creates a Monitor for the detected GPU.
// Call Start() in a goroutine to begin polling.
func NewMonitor(info Info, interval time.Duration) *Monitor {
	return &Monitor{info: info, interval: interval}
}

// Start polls the GPU until the process exits. Run in a goroutine.
func (m *Monitor) Start() {
	m.poll() // immediate first reading so stats are ready before first idle check
	ticker := time.NewTicker(m.interval)
	defer ticker.Stop()
	for range ticker.C {
		m.poll()
	}
}

// Latest returns the most recent GPU stats snapshot.
func (m *Monitor) Latest() Stats {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.latest
}

// UtilizationPct returns the cached GPU utilisation (0–100).
// Returns 0 (not blocking) if GPU stats are unavailable.
func (m *Monitor) UtilizationPct() int {
	return m.Latest().UtilizationPct
}

func (m *Monitor) poll() {
	var s Stats
	switch m.info.Vendor {
	case "nvidia":
		s = pollNVIDIA()
	case "amd":
		// AMD on Windows has no standard CLI for live stats without the HIP SDK.
		// Utilisation defaults to 0 → GPU threshold check is skipped (safe: won't
		// block earning, but also won't detect a busy AMD GPU). Dashboard will show N/A.
		// TODO: integrate AMD ADL SDK or parse GPU-Z sensor output.
		s = Stats{Timestamp: time.Now()}
	default:
		s = Stats{Timestamp: time.Now()}
	}
	m.mu.Lock()
	m.latest = s
	m.mu.Unlock()
}

// pollNVIDIA queries nvidia-smi for live stats across all GPUs.
// Returns worst-case values: highest util, lowest free VRAM, hottest temp.
func pollNVIDIA() Stats {
	out, err := exec.Command("nvidia-smi",
		"--query-gpu=utilization.gpu,memory.free,temperature.gpu,power.draw",
		"--format=csv,noheader,nounits",
	).Output()
	if err != nil {
		return Stats{Timestamp: time.Now()}
	}

	s := Stats{Timestamp: time.Now()}
	for i, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.Split(line, ", ")
		if len(parts) < 4 {
			continue
		}
		util, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
		vramFree, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
		temp, _ := strconv.Atoi(strings.TrimSpace(parts[2]))
		power, _ := strconv.ParseFloat(strings.TrimSpace(parts[3]), 64)

		if util > s.UtilizationPct {
			s.UtilizationPct = util
		}
		if vramFree < s.VRAMFreeMB || i == 0 {
			s.VRAMFreeMB = vramFree
		}
		if temp > s.TemperatureC {
			s.TemperatureC = temp
		}
		s.PowerDrawW += power
	}
	return s
}
