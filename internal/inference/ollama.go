// Package inference manages the Ollama subprocess: find/install, start, health
// check, model pull, model load, and stop. The Manager is the single owner of
// the Ollama process — no other package may start or kill it.
//
// Lifecycle (happy path):
//  1. EnsureInstalled  — find or download ollama executable
//  2. Start            — launch "ollama serve", wait for :11434 to respond
//  3. PullModel        — POST /api/pull (streaming progress), skip if present
//  4. LoadModel        — POST /api/generate with empty prompt to warm VRAM
//  5. Stop             — terminate subprocess on pause / shutdown
package inference

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"sync"
	"time"

	"github.com/fabgoodvibes/owlrun/internal/gpu"
)

const (
	ollamaHost    = "http://localhost:11434"
	startTimeout  = 30 * time.Second
	pullTimeout   = 30 * time.Minute
	loadTimeout   = 2 * time.Minute
	healthTimeout = 5 * time.Second
)

// PullProgress is sent on the channel returned by PullModel.
type PullProgress struct {
	Status    string // e.g. "pulling manifest", "downloading", "success"
	Total     int64
	Completed int64
	Err       error // non-nil on failure; channel closes after
}

// Manager controls the Ollama subprocess.
type Manager struct {
	mu      sync.Mutex
	gpuInfo gpu.Info
	cmd     *exec.Cmd // nil if not running
}

// New creates an inference Manager for the given GPU.
func New(info gpu.Info) *Manager {
	return &Manager{gpuInfo: info}
}

// EnsureInstalled verifies ollama is available; downloads it if not.
// On non-Windows the stub always returns nil.
func (m *Manager) EnsureInstalled() error {
	return ensureInstalled()
}

// Start launches "ollama serve" and blocks until the API is healthy or the
// timeout elapses. Returns an error if it fails to start.
func (m *Manager) Start() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cmd != nil {
		return nil // already running
	}

	ollamaPath, err := findOllama()
	if err != nil {
		return fmt.Errorf("ollama not found: %w", err)
	}

	cmd := exec.Command(ollamaPath, "serve")
	cmd.Env = ollamaEnv(m.gpuInfo) // platform-specific GPU env vars
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start ollama serve: %w", err)
	}
	m.cmd = cmd

	// Wait for the API to become healthy.
	deadline := time.Now().Add(startTimeout)
	for time.Now().Before(deadline) {
		if m.healthyUnlocked() {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Timed out — kill the process we just started.
	_ = killProcess(cmd)
	m.cmd = nil
	return fmt.Errorf("ollama did not start within %s", startTimeout)
}

// Stop terminates the Ollama subprocess. Safe to call when not running.
func (m *Manager) Stop() error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.cmd == nil {
		return nil
	}
	err := killProcess(m.cmd)
	_ = m.cmd.Wait()
	m.cmd = nil
	return err
}

// IsRunning reports whether the Ollama HTTP API is reachable.
func (m *Manager) IsRunning() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.healthyUnlocked()
}

// healthyUnlocked performs the health check. Caller must hold m.mu.
func (m *Manager) healthyUnlocked() bool {
	ctx, cancel := context.WithTimeout(context.Background(), healthTimeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ollamaHost+"/api/tags", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// ListInstalled returns all model tags currently installed in Ollama.
// Ollama must already be running.
func (m *Manager) ListInstalled() []string {
	ctx, cancel := context.WithTimeout(context.Background(), healthTimeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ollamaHost+"/api/tags", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()

	var payload struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil
	}
	out := make([]string, 0, len(payload.Models))
	for _, m := range payload.Models {
		out = append(out, m.Name)
	}
	return out
}

// SelectModel picks the best already-installed model for the available
// resources. Returns (model, nil) if a suitable installed model is found.
// Returns ("", suggestions) if nothing is installed — suggestions is an
// ordered list of models the user could pull.
func (m *Manager) SelectModel(vramGB float64, maxVRAMPct int) (string, []string) {
	ranked := gpu.RankedModels(vramGB, maxVRAMPct)
	installed := m.ListInstalled()

	installedSet := make(map[string]bool, len(installed))
	for _, name := range installed {
		installedSet[name] = true
	}

	for _, candidate := range ranked {
		if installedSet[candidate] {
			return candidate, nil
		}
	}
	return "", ranked
}

// SelectModels returns ALL installed models that fit in VRAM, best first.
// The first element is the primary model (loaded into VRAM). Others are
// registered with the gateway so it can route multiple model requests.
// Returns (models, nil) if at least one found, or (nil, suggestions).
func (m *Manager) SelectModels(vramGB float64, maxVRAMPct int) ([]string, []string) {
	ranked := gpu.RankedModels(vramGB, maxVRAMPct)
	installed := m.ListInstalled()

	installedSet := make(map[string]bool, len(installed))
	for _, name := range installed {
		installedSet[name] = true
	}

	var matched []string
	for _, candidate := range ranked {
		if installedSet[candidate] {
			matched = append(matched, candidate)
		}
	}
	if len(matched) == 0 {
		return nil, ranked
	}
	return matched, nil
}

// ModelInstalled reports whether the given tag already exists locally.
func (m *Manager) ModelInstalled(modelTag string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), healthTimeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ollamaHost+"/api/tags", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()

	var payload struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return false
	}
	for _, m := range payload.Models {
		if m.Name == modelTag {
			return true
		}
	}
	return false
}

// PullModel streams a model pull via /api/pull.
// Progress is sent on the returned channel; the channel is closed when done.
// Callers should check PullProgress.Err on the final message.
func (m *Manager) PullModel(modelTag string) <-chan PullProgress {
	ch := make(chan PullProgress, 8)
	go func() {
		defer close(ch)
		if err := m.pullModel(modelTag, ch); err != nil {
			ch <- PullProgress{Err: err}
		}
	}()
	return ch
}

func (m *Manager) pullModel(modelTag string, ch chan<- PullProgress) error {
	body, _ := json.Marshal(map[string]any{
		"name":   modelTag,
		"stream": true,
	})
	ctx, cancel := context.WithTimeout(context.Background(), pullTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		ollamaHost+"/api/pull", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("pull request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("pull: HTTP %d: %s", resp.StatusCode, b)
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		var line struct {
			Status    string `json:"status"`
			Total     int64  `json:"total"`
			Completed int64  `json:"completed"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}
		ch <- PullProgress{
			Status:    line.Status,
			Total:     line.Total,
			Completed: line.Completed,
		}
		if line.Status == "success" {
			return nil
		}
	}
	return scanner.Err()
}

// LoadModel sends a no-op generate request to load the model into VRAM.
// This ensures the first real inference request is fast.
func (m *Manager) LoadModel(modelTag string) error {
	body, _ := json.Marshal(map[string]any{
		"model":  modelTag,
		"prompt": "",
		"stream": false,
	})
	ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		ollamaHost+"/api/generate", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("load model: %w", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("load model: HTTP %d", resp.StatusCode)
	}
	return nil
}
