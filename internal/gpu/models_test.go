package gpu

import "testing"

func TestModelTable_SmallModelsPresent(t *testing.T) {
	// Models that must be in the table for CPU-only demo nodes.
	required := []string{
		"smollm2:135m",
		"smollm2:360m",
		"tinyllama:1.1b",
		"qwen2.5:0.5b",
		"qwen3.5:0.8b",
		"deepseek-r1:1.5b",
	}
	have := map[string]bool{}
	for _, m := range modelTable {
		have[m.tag] = true
	}
	for _, tag := range required {
		if !have[tag] {
			t.Errorf("model %q missing from modelTable", tag)
		}
	}
}

func TestRankedModels_CPUOnly(t *testing.T) {
	// vramGB=0 → only CPU models (vramGB==0) should be returned.
	models := RankedModels(0, 80)
	if len(models) == 0 {
		t.Fatal("RankedModels(0, 80) returned no models — CPU nodes need at least one")
	}
	for _, tag := range models {
		var found bool
		for _, m := range modelTable {
			if m.tag == tag {
				if m.vramGB != 0 {
					t.Errorf("CPU-only list includes GPU model %q (vramGB=%.1f)", tag, m.vramGB)
				}
				found = true
				break
			}
		}
		if !found {
			t.Errorf("RankedModels returned %q which is not in modelTable", tag)
		}
	}
}

func TestRecommendModel_CPU(t *testing.T) {
	got := RecommendModel(0, 80)
	if got == "" {
		t.Fatal("RecommendModel(0, 80) returned empty string")
	}
}

func TestRecommendModel_HighVRAM(t *testing.T) {
	// 48GB × 80% = 38.4GB usable → 70b-q2_K (28GB) fits, 70b-q4_K_M (42GB) doesn't.
	got := RecommendModel(48, 80)
	if got != "llama3.1:70b-q2_K" {
		t.Errorf("RecommendModel(48, 80) = %q, want llama3.1:70b-q2_K", got)
	}
}

func TestModelTable_SortedDescending(t *testing.T) {
	for i := 1; i < len(modelTable); i++ {
		if modelTable[i].vramGB > modelTable[i-1].vramGB {
			t.Errorf("modelTable not sorted descending at index %d: %s (%.1f) > %s (%.1f)",
				i, modelTable[i].tag, modelTable[i].vramGB, modelTable[i-1].tag, modelTable[i-1].vramGB)
		}
	}
}
