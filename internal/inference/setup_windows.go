package inference

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/fabgoodvibes/owlrun/internal/gpu"
)

const (
	ollamaDownloadURL = "https://ollama.com/download/OllamaSetup.exe"
	ollamaSetupName   = "OllamaSetup.exe"
)

// ensureInstalled finds Ollama or downloads it.
func ensureInstalled() error {
	if _, err := findOllama(); err == nil {
		return nil // already present
	}
	return downloadOllama()
}

// findOllama locates the ollama executable. Checks (in order):
//  1. PATH
//  2. %LOCALAPPDATA%\Programs\Ollama\ollama.exe  (default installer location)
//  3. Owlrun's own data dir: %LOCALAPPDATA%\Owlrun\ollama.exe
func findOllama() (string, error) {
	if p, err := exec.LookPath("ollama"); err == nil {
		return p, nil
	}

	localApp := os.Getenv("LOCALAPPDATA")
	candidates := []string{
		filepath.Join(localApp, "Programs", "Ollama", "ollama.exe"),
		filepath.Join(localApp, "Owlrun", "ollama.exe"),
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}
	return "", fmt.Errorf("ollama.exe not found")
}

// downloadOllama fetches OllamaSetup.exe and runs it silently.
// Falls back to placing ollama.exe directly in the Owlrun data dir if the
// installer fails (the Ollama binary is self-contained for "serve" usage).
func downloadOllama() error {
	dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "Owlrun")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create owlrun dir: %w", err)
	}

	setupPath := filepath.Join(dir, ollamaSetupName)

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Get(ollamaDownloadURL)
	if err != nil {
		return fmt.Errorf("download ollama: %w", err)
	}
	defer resp.Body.Close()

	f, err := os.Create(setupPath)
	if err != nil {
		return fmt.Errorf("create setup file: %w", err)
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return fmt.Errorf("write setup file: %w", err)
	}
	f.Close()

	// Run the installer silently (Ollama uses /S for NSIS silent install).
	cmd := exec.Command(setupPath, "/S")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ollama installer: %w", err)
	}
	return nil
}

// ollamaEnv returns environment variables for the Ollama subprocess.
// Sets GPU routing for NVIDIA (CUDA) and AMD (ROCm/HIP) cards.
func ollamaEnv(info gpu.Info) []string {
	env := os.Environ()

	// Use all available GPUs.
	env = append(env, "OLLAMA_NUM_GPU=99")

	switch info.Vendor {
	case "nvidia":
		// Enable FlashAttention for faster inference on Ampere+.
		env = append(env, "OLLAMA_FLASH_ATTENTION=1")
		// CUDA: expose all GPUs (Ollama selects best by VRAM).
		env = append(env, "CUDA_VISIBLE_DEVICES=all")

	case "amd":
		// ROCm/HIP: expose all GPUs.
		env = append(env, "HIP_VISIBLE_DEVICES=all")
		// Disable Flash Attention — not universally supported on ROCm.
		env = append(env, "OLLAMA_FLASH_ATTENTION=0")
	}

	return env
}

// killProcess terminates an Ollama subprocess and its children.
// Uses taskkill /F /T so child processes (GPU workers) are also cleaned up.
func killProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pid := cmd.Process.Pid
	kill := exec.Command("taskkill", "/F", "/T", "/PID", fmt.Sprintf("%d", pid))
	kill.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := kill.Run(); err != nil {
		// Fall back to direct process termination.
		return cmd.Process.Kill()
	}
	return nil
}
