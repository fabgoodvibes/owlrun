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

// ollamaEnv returns the current environment unchanged on macOS.
func ollamaEnv(_ gpu.Info) []string { return os.Environ() }

// killProcess sends SIGKILL on macOS.
func killProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
