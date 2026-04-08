//go:build darwin

package inference

import (
	"os"
	"os/exec"

	"github.com/fabgoodvibes/owlrun/internal/gpu"
)

// ensureInstalled is a no-op on macOS — Ollama is installed via
// the install script or the user's own Homebrew/app install.
func ensureInstalled() error { return nil }

// findOllama checks PATH first, then common macOS install locations.
func findOllama() (string, error) {
	// 1. Check PATH (covers manual installs and properly configured Homebrew).
	if path, err := exec.LookPath("ollama"); err == nil {
		return path, nil
	}

	// 2. Check known macOS locations in order of likelihood.
	candidates := []string{
		"/opt/homebrew/bin/ollama",                        // Homebrew on Apple Silicon
		"/usr/local/bin/ollama",                           // Homebrew on Intel Mac
		"/Applications/Ollama.app/Contents/Resources/ollama", // Ollama.app bundle
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c, nil
		}
	}

	return "", exec.ErrNotFound
}

// ollamaEnv returns the environment for Ollama on macOS.
// Apple Silicon uses Metal/unified memory; gpuSplit is a no-op (always one
// integrated GPU). Multi-GPU env vars are still emitted in the rare eGPU case.
func ollamaEnv(info gpu.Info, gpuSplit bool) []string {
	env := os.Environ()
	env = append(env, gpuLayerEnv(info, gpuSplit)...)
	return env
}

// killProcess sends SIGKILL on macOS.
func killProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
