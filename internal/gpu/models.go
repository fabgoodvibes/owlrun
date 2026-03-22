package gpu

// modelTable lists Ollama models from largest to smallest by minimum VRAM.
// vramGB = 0 means the model can run on CPU.
var modelTable = []struct {
	tag    string
	vramGB float64
}{
	{"llama3.1:70b-q4_K_M", 42},
	{"llama3.1:70b-q2_K", 28},
	{"qwen2.5-coder:32b-q4_K_M", 22},
	{"qwen2.5:14b", 10},
	{"llama3.1:8b-q8_0", 10},
	{"llama3.1:8b", 6},
	{"qwen2.5:7b", 5},
	{"qwen2.5-coder:7b", 5},
	{"llama3.2:3b", 3},
	{"qwen2.5:3b", 3},
	{"llama3.2:1b", 0},      // small enough for CPU
	{"deepseek-r1:1.5b", 0}, // reasoning/math, runs on CPU
	{"qwen2.5:1.5b", 0},    // runs well on CPU
	{"qwen2.5:0.5b", 0},    // CPU-only fallback
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
