//go:build !windows && !darwin

package inference

import (
	"os"
	"os/exec"

	"github.com/fabgoodvibes/owlrun/internal/gpu"
)

// ensureInstalled is a no-op stub on non-Windows platforms.
func ensureInstalled() error { return nil }

// findOllama checks PATH on non-Windows platforms.
func findOllama() (string, error) {
	return exec.LookPath("ollama")
}

// ollamaEnv returns the environment for the Ollama subprocess on Linux.
// Sets GPU layer offload and (when gpuSplit) multi-GPU model splitting.
func ollamaEnv(info gpu.Info, gpuSplit bool) []string {
	env := os.Environ()
	env = append(env, gpuLayerEnv(info, gpuSplit)...)
	return env
}

// killProcess sends SIGKILL on non-Windows platforms.
func killProcess(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
