package config

import (
	"os"
	"path/filepath"
	"testing"
)

// withTempHome sets HOME to a temp dir for the duration of the test,
// restoring it afterwards. Returns the temp dir path.
func withTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	t.Cleanup(func() { os.Setenv("HOME", orig) })
	return dir
}

func TestDefaults_NoFile(t *testing.T) {
	withTempHome(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Marketplace.Gateway != "https://node.owlrun.me" {
		t.Errorf("Gateway = %q, want https://node.owlrun.me", cfg.Marketplace.Gateway)
	}
	if !cfg.Marketplace.AllowOverride {
		t.Error("AllowOverride should default to true")
	}
	if cfg.Inference.MaxVRAMPct != 80 {
		t.Errorf("MaxVRAMPct = %d, want 80", cfg.Inference.MaxVRAMPct)
	}
	if !cfg.Inference.ModelAuto {
		t.Error("ModelAuto should default to true")
	}
	if cfg.Idle.TriggerMinutes != 10 {
		t.Errorf("TriggerMinutes = %d, want 10", cfg.Idle.TriggerMinutes)
	}
	if cfg.Idle.GPUThreshold != 15 {
		t.Errorf("GPUThreshold = %d, want 15", cfg.Idle.GPUThreshold)
	}
	if !cfg.Idle.WatchProcesses {
		t.Error("WatchProcesses should default to true")
	}
	if cfg.Disk.WarnThresholdPct != 30 {
		t.Errorf("WarnThresholdPct = %d, want 30", cfg.Disk.WarnThresholdPct)
	}
	if cfg.Disk.MinModelSpaceGB != 8 {
		t.Errorf("MinModelSpaceGB = %d, want 8", cfg.Disk.MinModelSpaceGB)
	}
	if cfg.Account.NodeID != "" {
		t.Errorf("NodeID should be empty without a file, got %q", cfg.Account.NodeID)
	}
}

func TestLoad_FullFile(t *testing.T) {
	home := withTempHome(t)

	confDir := filepath.Join(home, ".owlrun")
	os.MkdirAll(confDir, 0o755)
	conf := filepath.Join(confDir, "owlrun.conf")

	ini := `[account]
node_id = test-node-123
api_key  = sk-test
wallet   = Abc123TestWallet

[marketplace]
gateway        = https://custom.gateway.example/v1
extra_gateways = https://backup1.example, https://backup2.example
allow_override = false

[inference]
model_auto   = false
max_vram_pct = 70

[idle]
trigger_minutes = 5
gpu_threshold   = 20
watch_processes = false

[disk]
warn_threshold_pct = 20
min_model_space_gb = 16
`
	if err := os.WriteFile(conf, []byte(ini), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Account.NodeID != "test-node-123" {
		t.Errorf("NodeID = %q", cfg.Account.NodeID)
	}
	if cfg.Account.APIKey != "sk-test" {
		t.Errorf("APIKey = %q", cfg.Account.APIKey)
	}
	if cfg.Account.Wallet != "Abc123TestWallet" {
		t.Errorf("Wallet = %q", cfg.Account.Wallet)
	}
	if cfg.Marketplace.Gateway != "https://custom.gateway.example/v1" {
		t.Errorf("Gateway = %q", cfg.Marketplace.Gateway)
	}
	if cfg.Marketplace.AllowOverride {
		t.Error("AllowOverride should be false")
	}
	if len(cfg.Marketplace.ExtraGateways) != 2 {
		t.Errorf("ExtraGateways len = %d, want 2", len(cfg.Marketplace.ExtraGateways))
	} else {
		if cfg.Marketplace.ExtraGateways[0] != "https://backup1.example" {
			t.Errorf("ExtraGateways[0] = %q", cfg.Marketplace.ExtraGateways[0])
		}
		if cfg.Marketplace.ExtraGateways[1] != "https://backup2.example" {
			t.Errorf("ExtraGateways[1] = %q", cfg.Marketplace.ExtraGateways[1])
		}
	}
	if cfg.Inference.ModelAuto {
		t.Error("ModelAuto should be false")
	}
	if cfg.Inference.MaxVRAMPct != 70 {
		t.Errorf("MaxVRAMPct = %d, want 70", cfg.Inference.MaxVRAMPct)
	}
	if cfg.Idle.TriggerMinutes != 5 {
		t.Errorf("TriggerMinutes = %d, want 5", cfg.Idle.TriggerMinutes)
	}
	if cfg.Idle.GPUThreshold != 20 {
		t.Errorf("GPUThreshold = %d, want 20", cfg.Idle.GPUThreshold)
	}
	if cfg.Idle.WatchProcesses {
		t.Error("WatchProcesses should be false")
	}
	if cfg.Disk.WarnThresholdPct != 20 {
		t.Errorf("WarnThresholdPct = %d, want 20", cfg.Disk.WarnThresholdPct)
	}
	if cfg.Disk.MinModelSpaceGB != 16 {
		t.Errorf("MinModelSpaceGB = %d, want 16", cfg.Disk.MinModelSpaceGB)
	}
}

func TestLoad_PartialFile_FallsBackToDefaults(t *testing.T) {
	home := withTempHome(t)

	confDir := filepath.Join(home, ".owlrun")
	os.MkdirAll(confDir, 0o755)
	// Only set one value — everything else should stay at defaults.
	ini := "[idle]\ntrigger_minutes = 3\n"
	if err := os.WriteFile(filepath.Join(confDir, "owlrun.conf"), []byte(ini), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Idle.TriggerMinutes != 3 {
		t.Errorf("TriggerMinutes = %d, want 3", cfg.Idle.TriggerMinutes)
	}
	// Everything else must be default.
	if cfg.Marketplace.Gateway != "https://node.owlrun.me" {
		t.Errorf("Gateway should be default, got %q", cfg.Marketplace.Gateway)
	}
	if cfg.Inference.MaxVRAMPct != 80 {
		t.Errorf("MaxVRAMPct should be default 80, got %d", cfg.Inference.MaxVRAMPct)
	}
}

func TestEnsureNodeID_GeneratesAndPersists(t *testing.T) {
	withTempHome(t)

	cfg, _ := Load()
	if cfg.Account.NodeID != "" {
		t.Fatal("expected empty NodeID before EnsureNodeID")
	}

	id1 := EnsureNodeID(&cfg)
	if id1 == "" {
		t.Fatal("EnsureNodeID returned empty string")
	}

	// Should persist: reload and check.
	cfg2, err := Load()
	if err != nil {
		t.Fatalf("reload error: %v", err)
	}
	if cfg2.Account.NodeID != id1 {
		t.Errorf("persisted NodeID = %q, want %q", cfg2.Account.NodeID, id1)
	}
}

func TestEnsureNodeID_ExistingIDUnchanged(t *testing.T) {
	withTempHome(t)

	cfg := Config{Account: AccountConfig{NodeID: "existing-id"}}
	id := EnsureNodeID(&cfg)
	if id != "existing-id" {
		t.Errorf("EnsureNodeID changed existing ID: got %q", id)
	}
}
