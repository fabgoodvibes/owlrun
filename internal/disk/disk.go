// Package disk checks available disk space and guards against model downloads
// that would leave the drive critically low.
package disk

import (
	"os"
	"path/filepath"
)

// Info holds disk usage for a given path.
type Info struct {
	Path    string
	TotalGB float64
	FreeGB  float64
	FreePct float64 // 0–100
}

// Default thresholds — overridden by owlrun.conf values at runtime.
const (
	DefaultWarnThresholdPct = 30 // warn if free space < 30%
	DefaultMinModelSpaceGB  = 8  // refuse download if < 8 GB free
)

// ModelSizeGB maps known Ollama model tags to approximate download sizes in GB.
// Used for pre-download space checks (Step 6). Values are compressed file sizes.
var ModelSizeGB = map[string]float64{
	"llama3.1:70b-instruct-q4_K_M": 40.0,
	"llama3.3:70b":                 40.0,
	"llama3.1:70b-instruct-q2_K":   26.0,
	"qwen2.5-coder:32b-instruct-q4_K_M": 19.0,
	"mistral-small:24b":            14.0,
	"llama3.1:8b-instruct-q8_0":    8.5,
	"llama3.1:8b":               4.7,
	"mistral:7b":                4.1,
	"qwen2.5-coder:7b":         4.5,
	"llama3.2:3b":               2.0,
	"llama3.2:1b":               1.3,
	"qwen2.5:0.5b":              0.4,
}

// ModelSize returns the known download size for a model tag, or a safe
// default (10 GB) if the model is not in the catalogue.
func ModelSize(modelTag string) float64 {
	if sz, ok := ModelSizeGB[modelTag]; ok {
		return sz
	}
	return 10.0 // conservative unknown-model default
}

// HasEnoughForModel reports whether there is sufficient space to download a
// model and still keep minFreeGB free after the download.
func HasEnoughForModel(info Info, modelSizeGB, minFreeGB float64) bool {
	return info.FreeGB >= modelSizeGB+minFreeGB
}

// OllamaModelsDir returns the directory where Ollama stores model files.
// Respects the OLLAMA_MODELS environment variable; falls back to the default
// location (~/.ollama/models on all platforms).
func OllamaModelsDir() string {
	if dir := os.Getenv("OLLAMA_MODELS"); dir != "" {
		return dir
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".ollama", "models")
}
