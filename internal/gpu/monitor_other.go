//go:build !windows

package gpu

import (
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Monitor polls the GPU on a fixed interval and caches the latest Stats.
type Monitor struct {
	info     Info
	interval time.Duration
	mu       sync.RWMutex
	latest   Stats
}

func NewMonitor(info Info, interval time.Duration) *Monitor {
	return &Monitor{info: info, interval: interval}
}

// Start polls the GPU until the process exits. Run in a goroutine.
func (m *Monitor) Start() {
	m.poll()
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
func (m *Monitor) UtilizationPct() int {
	return m.Latest().UtilizationPct
}

func (m *Monitor) poll() {
	var s Stats
	if m.info.Vendor == "nvidia" {
		s = pollNVIDIA()
	} else {
		s = Stats{Timestamp: time.Now()}
	}
	m.mu.Lock()
	m.latest = s
	m.mu.Unlock()
}

// pollNVIDIA queries nvidia-smi for live stats across all GPUs.
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

		s.PerGPU = append(s.PerGPU, GPUStat{
			UtilizationPct: util, VRAMFreeMB: vramFree, TemperatureC: temp, PowerDrawW: power,
		})

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
