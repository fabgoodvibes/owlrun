// Package config loads owlrun.conf and provides typed access to all settings.
// Defaults are applied for any missing key so the agent works out-of-the-box
// with no config file present.
package config

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/fabgoodvibes/owlrun/internal/buildinfo"
	"github.com/google/uuid"
	"gopkg.in/ini.v1"
)

// Beta testnet defaults — hardcoded into beta builds so operators can
// start earning on testnet without any configuration.
const (
	betaGateway = "https://gateway.owlrun.me"
	betaWallet  = "OwLt3st1111111111111111111111111111111111111" // testnet payout wallet
)

// Config is the fully-parsed owlrun.conf.
type Config struct {
	Account     AccountConfig
	Marketplace MarketplaceConfig
	Inference   InferenceConfig
	Idle        IdleConfig
	Disk        DiskConfig
}

type AccountConfig struct {
	NodeID      string // stable UUID generated once, persisted to conf
	APIKey      string
	Wallet      string // Solana pubkey (base58) or EVM address (0x...)
	ReferralCode string // affiliate referral code (owlr_ref_<code>), optional
}

type MarketplaceConfig struct {
	// Gateway is the Owlrun Gateway endpoint. This is the only place the
	// node connects to — buyer routing happens on the gateway side.
	Gateway       string
	// ProxyBase is an optional alternate base URL for proxy connections.
	// If empty, falls back to Gateway.
	ProxyBase     string
	ExtraGateways []string // additional Owlrun-operated endpoints for redundancy
	AllowOverride bool
	Region        string // self-reported region; "auto" if unset (gateway resolves from IP)
}

type InferenceConfig struct {
	ModelAuto  bool
	MaxVRAMPct int
	Model      string // override: pin a specific model tag; empty = auto-select
}

type IdleConfig struct {
	TriggerMinutes int  // no-input duration before earning starts
	GPUThreshold   int  // GPU utilisation % below which earning is allowed
	WatchProcesses bool // pause when game processes detected
}

type DiskConfig struct {
	WarnThresholdPct int
	MinModelSpaceGB  int
}

// defaults returns a Config with all values set to the shipped defaults.
// Beta builds include a testnet wallet so operators can start immediately.
func defaults() Config {
	gateway := "https://gateway.owlrun.me"
	var wallet string

	if buildinfo.IsBeta() {
		gateway = betaGateway
		wallet = betaWallet
	}

	return Config{
		Account: AccountConfig{
			Wallet: wallet,
		},
		Marketplace: MarketplaceConfig{
			Gateway:       gateway,
			AllowOverride: true,
		},
		Inference: InferenceConfig{
			ModelAuto:  true,
			MaxVRAMPct: 80,
		},
		Idle: IdleConfig{
			TriggerMinutes: 10,
			GPUThreshold:   15,
			WatchProcesses: true,
		},
		Disk: DiskConfig{
			WarnThresholdPct: 30,
			MinModelSpaceGB:  8,
		},
	}
}

// NeedsWallet returns true if the user hasn't set their own payout wallet.
// This is the case when the wallet is empty or still the beta default.
func NeedsWallet(cfg *Config) bool {
	return cfg.Account.Wallet == "" || cfg.Account.Wallet == betaWallet
}

// EnsureNodeID returns the stable node UUID from cfg, generating and persisting
// a new one if the config file doesn't have one yet.
func EnsureNodeID(cfg *Config) string {
	if cfg.Account.NodeID != "" {
		return cfg.Account.NodeID
	}
	id := uuid.New().String()
	cfg.Account.NodeID = id
	// Best-effort persist — if it fails the node gets a new ID next restart,
	// which is acceptable until the installer sets up the conf file properly.
	persistNodeID(id)
	return id
}

func persistNodeID(id string) {
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := ini.LooseLoad(path)
	if err != nil {
		f = ini.Empty()
	}
	f.Section("account").Key("node_id").SetValue(id)
	_ = f.SaveTo(path)
}

// Path returns the default config file location: ~/.owlrun/owlrun.conf
func Path() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".owlrun", "owlrun.conf")
}

// Load reads owlrun.conf and returns a Config. If the file does not exist,
// all defaults are used. Partial files are also safe — missing keys use defaults.
func Load() (Config, error) {
	cfg := defaults()
	path := Path()

	if _, err := os.Stat(path); os.IsNotExist(err) {
		return cfg, nil
	}

	f, err := ini.Load(path)
	if err != nil {
		return cfg, err
	}

	if sec, err := f.GetSection("account"); err == nil {
		cfg.Account.NodeID = sec.Key("node_id").String()
		cfg.Account.APIKey = sec.Key("api_key").String()
		cfg.Account.Wallet = sec.Key("wallet").String()
		cfg.Account.ReferralCode = sec.Key("referral_code").String()
	}

	if sec, err := f.GetSection("marketplace"); err == nil {
		if v := sec.Key("gateway").String(); v != "" {
			cfg.Marketplace.Gateway = v
		}
		if extras := sec.Key("extra_gateways").String(); extras != "" {
			for _, e := range strings.Split(extras, ",") {
				if t := strings.TrimSpace(e); t != "" {
					cfg.Marketplace.ExtraGateways = append(cfg.Marketplace.ExtraGateways, t)
				}
			}
		}
		cfg.Marketplace.ProxyBase = sec.Key("proxy_base").String()
		cfg.Marketplace.Region = sec.Key("region").String()
		cfg.Marketplace.AllowOverride = sec.Key("allow_override").MustBool(true)
	}

	if sec, err := f.GetSection("inference"); err == nil {
		cfg.Inference.ModelAuto = sec.Key("model_auto").MustBool(true)
		cfg.Inference.MaxVRAMPct = sec.Key("max_vram_pct").MustInt(80)
		cfg.Inference.Model = sec.Key("model").String()
	}

	if sec, err := f.GetSection("idle"); err == nil {
		cfg.Idle.TriggerMinutes = sec.Key("trigger_minutes").MustInt(10)
		cfg.Idle.GPUThreshold = sec.Key("gpu_threshold").MustInt(15)
		cfg.Idle.WatchProcesses = sec.Key("watch_processes").MustBool(true)
	}

	if sec, err := f.GetSection("disk"); err == nil {
		cfg.Disk.WarnThresholdPct = sec.Key("warn_threshold_pct").MustInt(30)
		cfg.Disk.MinModelSpaceGB = sec.Key("min_model_space_gb").MustInt(8)
	}

	return cfg, nil
}
