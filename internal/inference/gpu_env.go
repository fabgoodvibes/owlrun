package inference

import (
	"fmt"
	"strings"

	"github.com/fabgoodvibes/owlrun/internal/gpu"
)

// gpuLayerEnv returns the cross-platform Ollama environment variables that
// control GPU offload and multi-GPU behaviour. The OS-specific ollamaEnv
// wrappers append these to os.Environ().
//
// gpuSplit=true sets OLLAMA_SCHED_SPREAD=1 so a single model is spread across
// every detected GPU. With gpuSplit=false (default), Ollama consolidates onto
// the fewest cards, which means a model only fits the largest single GPU.
func gpuLayerEnv(info gpu.Info, gpuSplit bool) []string {
	var env []string

	// Offload all layers to GPU when one is present.
	env = append(env, "OLLAMA_NUM_GPU=99")

	if info.Count > 1 && gpuSplit {
		env = append(env, "OLLAMA_SCHED_SPREAD=1")
	}

	// Decide which device indices Ollama is allowed to see.
	// Default (split=false): expose ONLY the GPU with the most VRAM, so the
	// fastest single-card inference path is used and we don't accidentally
	// pin a model to a tiny secondary card just because it has index 0.
	// split=true: expose every detected GPU.
	devices := ""
	if info.Count > 1 {
		if gpuSplit {
			devices = deviceIndexList(info.Count)
		} else {
			devices = fmt.Sprintf("%d", info.LargestGPUIndex())
		}
	}

	switch info.Vendor {
	case "nvidia":
		env = append(env, "OLLAMA_FLASH_ATTENTION=1")
		if devices != "" {
			env = append(env, "CUDA_VISIBLE_DEVICES="+devices)
		}

	case "amd":
		env = append(env, "OLLAMA_FLASH_ATTENTION=0")
		if devices != "" {
			env = append(env, "HIP_VISIBLE_DEVICES="+devices)
			env = append(env, "ROCR_VISIBLE_DEVICES="+devices)
		}
	}

	return env
}

// deviceIndexList returns "0,1,...,n-1".
func deviceIndexList(n int) string {
	parts := make([]string, 0, n)
	for i := 0; i < n; i++ {
		parts = append(parts, fmt.Sprintf("%d", i))
	}
	return strings.Join(parts, ",")
}
