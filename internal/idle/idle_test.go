package idle

import (
	"testing"

	"github.com/fabgoodvibes/owlrun/internal/config"
)

// idleCfg returns a config where only the GPU threshold matters.
// TriggerMinutes = 0 means IdleDuration() (which is platform-specific) always
// satisfies the threshold on Linux stubs (returns 0 >= 0).
func idleCfg(gpuThreshold int, watchProcesses bool) config.IdleConfig {
	return config.IdleConfig{
		TriggerMinutes: 0, // always passes on Linux stub (IdleDuration returns 0)
		GPUThreshold:   gpuThreshold,
		WatchProcesses: watchProcesses,
	}
}

func TestIsSystemIdle_GPUBusy_NotIdle(t *testing.T) {
	cfg := idleCfg(15, false)
	// GPU at 50% exceeds threshold of 15% — not idle.
	if IsSystemIdle(cfg, 50) {
		t.Error("expected not idle when GPU util (50%) >= threshold (15%)")
	}
}

func TestIsSystemIdle_GPUAtThreshold_NotIdle(t *testing.T) {
	cfg := idleCfg(15, false)
	// Exactly at threshold is not idle (condition is >=).
	if IsSystemIdle(cfg, 15) {
		t.Error("expected not idle when GPU util == threshold")
	}
}

func TestIsSystemIdle_GPUBelowThreshold_Idle(t *testing.T) {
	cfg := idleCfg(15, false)
	// 14% < 15% threshold and no process watch.
	if !IsSystemIdle(cfg, 14) {
		t.Error("expected idle when GPU util (14%) < threshold (15%) and WatchProcesses=false")
	}
}

func TestIsSystemIdle_ZeroGPU_Idle(t *testing.T) {
	cfg := idleCfg(15, false)
	if !IsSystemIdle(cfg, 0) {
		t.Error("expected idle when GPU util = 0 and WatchProcesses=false")
	}
}

func TestIsSystemIdle_WatchProcesses_NoGame(t *testing.T) {
	cfg := idleCfg(15, true)
	// With WatchProcesses=true, IsGameRunning() is called.
	// On a CI/test runner, no game processes exist, so this should pass.
	// GPU = 0 to ensure GPU gate passes.
	result := IsSystemIdle(cfg, 0)
	// We can't assert true here since IsGameRunning depends on the live process
	// list. We just verify it doesn't panic and returns a bool.
	_ = result
}

func TestIsGameRunning_NoGames_False(t *testing.T) {
	// On any CI/test machine there should be no game processes.
	// This mostly validates that go-ps works cross-platform.
	got := IsGameRunning()
	// We can't assert false unconditionally (someone might be running Steam)
	// but we can assert it returns without panic.
	_ = got
}

func TestKnownGameExes_NotEmpty(t *testing.T) {
	if len(KnownGameExes) == 0 {
		t.Error("KnownGameExes is empty")
	}
}

func TestKnownGameExes_AllLowercase(t *testing.T) {
	for _, exe := range KnownGameExes {
		for _, c := range exe {
			if c >= 'A' && c <= 'Z' {
				t.Errorf("KnownGameExes entry %q contains uppercase — must be lowercase for comparison", exe)
				break
			}
		}
	}
}

func TestKnownGameExes_ContainsExpected(t *testing.T) {
	must := []string{"steam.exe", "cs2.exe", "valorant.exe", "cyberpunk2077.exe"}
	set := make(map[string]bool, len(KnownGameExes))
	for _, e := range KnownGameExes {
		set[e] = true
	}
	for _, want := range must {
		if !set[want] {
			t.Errorf("KnownGameExes missing expected entry %q", want)
		}
	}
}
