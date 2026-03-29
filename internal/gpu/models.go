package gpu

// modelTable lists Ollama models from largest to smallest by minimum VRAM.
// vramGB = 0 means the model can run on CPU.
var modelTable = []struct {
	tag    string
	vramGB float64
}{
	{"llama3.1:70b-instruct-q4_K_M", 42},
	{"llama3.3:70b", 42},    // Llama 3.3 is 70b only
	{"llama3.1:70b-instruct-q2_K", 28},
	{"qwen2.5-coder:32b-instruct-q4_K_M", 22},
	{"mistral-small:24b", 16}, // Mistral Small 22b/24b
	{"qwen2.5:14b", 10},
	{"llama3.1:8b-instruct-q8_0", 10},
	{"qwen3.5:9b", 7},       // multimodal, 262K ctx, best sub-10B (Mar 2026)
	{"llama3.1:8b", 6},
	{"qwen3:8b", 5},         // best coding at 8B (76% HumanEval)
	{"qwen2.5:7b", 5},
	{"qwen2.5-coder:7b", 5},
	{"deepseek-r1:8b", 5},   // reasoning/chain-of-thought
	{"qwen3.5:4b", 3},       // multimodal, 262K ctx, great for 8GB Mac (Mar 2026)
	{"gemma3:4b", 3},        // Google, 128K ctx, 71% HumanEval
	{"phi4-mini", 3},        // Microsoft, function calling, stable on 8GB
	{"llama3.2:3b", 3},
	{"qwen2.5:3b", 3},
	{"llama3.2:1b", 0},      // small enough for CPU
	{"deepseek-r1:1.5b", 0}, // reasoning/math, runs on CPU
	{"qwen2.5:1.5b", 0},     // runs well on CPU
	{"tinyllama:1.1b", 0},   // 637MB, fast CPU inference
	{"qwen3.5:0.8b", 0},     // ultra-light multimodal (Mar 2026)
	{"qwen2.5:0.5b", 0},     // CPU-only fallback
	{"smollm2:360m", 0},     // HuggingFace, 230MB, ultra-light
	{"smollm2:135m", 0},     // HuggingFace, 270MB, smallest viable model
}

// RecommendModel returns the single best model tag for the available VRAM.
func RecommendModel(vramGB float64, maxVRAMPct int) string {
	usable := vramGB * float64(maxVRAMPct) / 100
	for _, m := range modelTable {
		if usable >= m.vramGB {
			return m.tag
		}
	}
	return modelTable[len(modelTable)-1].tag
}

// RankedModels returns all models that fit within available resources,
// best first. Models with vramGB == 0 are always included (CPU fallback).
func RankedModels(vramGB float64, maxVRAMPct int) []string {
	usable := vramGB * float64(maxVRAMPct) / 100
	var out []string
	for _, m := range modelTable {
		if usable >= m.vramGB {
			out = append(out, m.tag)
		}
	}
	return out
}

// ModelInfo describes a model from the model table.
type ModelInfo struct {
	Tag    string  `json:"tag"`
	VramGB float64 `json:"vram_gb"` // 0 = CPU-capable
	Fits   bool    `json:"fits"`    // true if model fits in available VRAM
}

// RankedModelInfos returns model info for all models that fit, best first.
func RankedModelInfos(vramGB float64, maxVRAMPct int) []ModelInfo {
	usable := vramGB * float64(maxVRAMPct) / 100
	var out []ModelInfo
	for _, m := range modelTable {
		if usable >= m.vramGB {
			out = append(out, ModelInfo{Tag: m.tag, VramGB: m.vramGB, Fits: true})
		}
	}
	return out
}

// AllModelInfos returns every model in the table with a flag indicating
// whether it fits in the available VRAM.
func AllModelInfos(vramGB float64, maxVRAMPct int) []ModelInfo {
	usable := vramGB * float64(maxVRAMPct) / 100
	out := make([]ModelInfo, len(modelTable))
	for i, m := range modelTable {
		out[i] = ModelInfo{Tag: m.tag, VramGB: m.vramGB, Fits: usable >= m.vramGB}
	}
	return out
}
