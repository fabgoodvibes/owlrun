package gpu

// Category classifies a model by its primary use case.
type Category string

const (
	CatGeneral   Category = "general"   // chat, Q&A, summarization
	CatCode      Category = "code"      // code generation, debugging
	CatReasoning Category = "reasoning" // chain-of-thought, math, logic
	CatSmall     Category = "small"     // lightweight, high-throughput
)

// AllCategories returns the valid category values for UI display.
func AllCategories() []Category {
	return []Category{CatGeneral, CatCode, CatReasoning, CatSmall}
}

// CategoryLabel returns a human-friendly label for the category.
func CategoryLabel(c Category) string {
	switch c {
	case CatGeneral:
		return "General Purpose"
	case CatCode:
		return "Code Assistant"
	case CatReasoning:
		return "Reasoning & Math"
	case CatSmall:
		return "Small & Fast"
	default:
		return string(c)
	}
}

// modelEntry is a single row in the model table.
type modelEntry struct {
	tag      string
	vramGB   float64
	category Category
}

// modelTable lists Ollama models from largest to smallest by minimum VRAM.
// vramGB = 0 means the model can run on CPU.
var modelTable = []modelEntry{
	{"llama3.1:70b-instruct-q4_K_M", 42, CatGeneral},
	{"llama3.3:70b", 42, CatGeneral},
	{"llama3.1:70b-instruct-q2_K", 28, CatGeneral},
	{"qwen2.5-coder:32b-instruct-q4_K_M", 22, CatCode},
	{"mistral-small:24b", 16, CatGeneral},
	{"qwen2.5:14b", 10, CatGeneral},
	{"llama3.1:8b-instruct-q8_0", 10, CatGeneral},
	{"qwen3.5:9b", 7, CatGeneral},       // multimodal, 262K ctx, best sub-10B
	{"llama3.1:8b", 6, CatGeneral},
	{"qwen3:8b", 5, CatCode},            // best coding at 8B (76% HumanEval)
	{"qwen2.5:7b", 5, CatGeneral},
	{"qwen2.5-coder:7b", 5, CatCode},
	{"deepseek-r1:8b", 5, CatReasoning}, // reasoning/chain-of-thought
	{"qwen3.5:4b", 3, CatGeneral},       // multimodal, 262K ctx
	{"gemma3:4b", 3, CatCode},           // Google, 128K ctx, 71% HumanEval
	{"phi4-mini", 3, CatGeneral},        // Microsoft, function calling
	{"llama3.2:3b", 3, CatGeneral},
	{"qwen2.5:3b", 3, CatGeneral},
	{"llama3.2:1b", 0, CatSmall},
	{"deepseek-r1:1.5b", 0, CatReasoning}, // reasoning/math, runs on CPU
	{"qwen2.5:1.5b", 0, CatSmall},
	{"tinyllama:1.1b", 0, CatSmall},
	{"qwen3.5:0.8b", 0, CatSmall},      // ultra-light multimodal
	{"qwen2.5:0.5b", 0, CatSmall},
	{"smollm2:360m", 0, CatSmall},
	{"smollm2:135m", 0, CatSmall},       // smallest viable model
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

// RecommendByCategory returns the best model for the given category and VRAM.
// Falls back to the best model of any category if no category match fits.
func RecommendByCategory(vramGB float64, maxVRAMPct int, cat Category) string {
	usable := vramGB * float64(maxVRAMPct) / 100
	for _, m := range modelTable {
		if m.category == cat && usable >= m.vramGB {
			return m.tag
		}
	}
	// Fallback: best model of any category that fits
	return RecommendModel(vramGB, maxVRAMPct)
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

// RankedByCategory returns models matching the category that fit, best first.
// If cat is empty, returns all fitting models (same as RankedModels).
func RankedByCategory(vramGB float64, maxVRAMPct int, cat Category) []string {
	if cat == "" {
		return RankedModels(vramGB, maxVRAMPct)
	}
	usable := vramGB * float64(maxVRAMPct) / 100
	var out []string
	for _, m := range modelTable {
		if m.category == cat && usable >= m.vramGB {
			out = append(out, m.tag)
		}
	}
	return out
}

// ModelInfo describes a model from the model table.
type ModelInfo struct {
	Tag      string   `json:"tag"`
	VramGB   float64  `json:"vram_gb"`  // 0 = CPU-capable
	Fits     bool     `json:"fits"`     // true if model fits in available VRAM
	Category Category `json:"category"` // primary use case
}

// RankedModelInfos returns model info for all models that fit, best first.
func RankedModelInfos(vramGB float64, maxVRAMPct int) []ModelInfo {
	usable := vramGB * float64(maxVRAMPct) / 100
	var out []ModelInfo
	for _, m := range modelTable {
		if usable >= m.vramGB {
			out = append(out, ModelInfo{Tag: m.tag, VramGB: m.vramGB, Fits: true, Category: m.category})
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
		out[i] = ModelInfo{Tag: m.tag, VramGB: m.vramGB, Fits: usable >= m.vramGB, Category: m.category}
	}
	return out
}

// ModelCategory returns the category of a model by tag, or empty if unknown.
func ModelCategory(tag string) Category {
	for _, m := range modelTable {
		if m.tag == tag {
			return m.category
		}
	}
	return ""
}
