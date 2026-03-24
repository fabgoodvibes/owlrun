// Package config loads owlrun.conf and provides typed access to all settings.
// Defaults are applied for any missing key so the agent works out-of-the-box
// with no config file present.
package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
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
	betaGateway = "https://node.owlrun.me"
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
	NodeID           string // stable UUID generated once, persisted to conf
	APIKey           string
	Wallet           string // Legacy payout address (deprecated — use LightningAddress)
	ReferralCode     string // affiliate referral code (owlr_ref_<code>), optional
	LightningAddress string // Lightning address for BTC payouts (e.g. user@walletofsatoshi.com), optional
	RedeemThreshold  int    // sats threshold for auto-payout via Lightning (default 500)
}

type MarketplaceConfig struct {
	// Gateway is the Owlrun Gateway endpoint. This is the only place the
	// node connects to — buyer routing happens on the gateway side.
	Gateway       string
	// ProxyBase is the direct-to-VPS base URL for job proxy connections.
	// Bypasses Cloudflare (which buffers request bodies) for HTTP/2 full-duplex.
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
	TriggerMinutes int    // no-input duration before earning starts
	GPUThreshold   int    // GPU utilisation % below which earning is allowed
	WatchProcesses bool   // pause when game processes detected
	JobMode        string // "never", "idle" (default), or "always"
}

type DiskConfig struct {
	WarnThresholdPct int
	MinModelSpaceGB  int
}

// defaults returns a Config with all values set to the shipped defaults.
// Beta builds include a testnet wallet so operators can start immediately.
func defaults() Config {
	gateway := "https://node.owlrun.me"
	var wallet string

	if buildinfo.IsBeta() {
		gateway = betaGateway
		wallet = betaWallet
	}

	return Config{
		Account: AccountConfig{
			Wallet:          wallet,
			RedeemThreshold: 500,
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
			JobMode:        "idle",
		},
		Disk: DiskConfig{
			WarnThresholdPct: 30,
			MinModelSpaceGB:  8,
		},
	}
}

// NeedsWallet returns true if the user hasn't set their own payout wallet.
// This is the case when there's no Lightning address AND the legacy wallet
// is empty or still the beta default.
func NeedsWallet(cfg *Config) bool {
	if cfg.Account.LightningAddress != "" {
		return false
	}
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
	persistKey("node_id", id)
	return id
}

// EnsureAPIKey returns the provider API key from cfg, generating and persisting
// a new owlr_prov_<48 hex chars> key if the config doesn't have one yet.
// Normally bootstrap() handles this, but this is a safety net for existing
// configs that were created before auto-generation was added.
func EnsureAPIKey(cfg *Config) string {
	if cfg.Account.APIKey != "" {
		return cfg.Account.APIKey
	}
	key := generateAPIKey()
	cfg.Account.APIKey = key
	persistKey("api_key", key)
	return key
}

// persistKey writes a single key to the [account] section of the config file.
func persistKey(key, value string) {
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f, err := ini.LooseLoad(path)
	if err != nil {
		f = ini.Empty()
	}
	f.Section("account").Key(key).SetValue(value)
	_ = f.SaveTo(path)
}

// SaveLightningAddress persists a Lightning address to the config file.
func SaveLightningAddress(addr string) error {
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := ini.LooseLoad(path)
	if err != nil {
		f = ini.Empty()
	}
	f.Section("account").Key("lightning_address").SetValue(addr)
	return f.SaveTo(path)
}

// SaveRedeemThreshold persists a redeem threshold (sats) to the config file.
func SaveRedeemThreshold(threshold int) error {
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := ini.LooseLoad(path)
	if err != nil {
		f = ini.Empty()
	}
	f.Section("account").Key("redeem_threshold").SetValue(fmt.Sprintf("%d", threshold))
	return f.SaveTo(path)
}

// Path returns the default config file location: ~/.owlrun/owlrun.conf
func Path() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.Getenv("HOME")
	}
	if home == "" {
		home = os.TempDir()
	}
	return filepath.Join(home, ".owlrun", "owlrun.conf")
}

// generateAPIKey creates a new owlr_prov_<48 hex chars> provider key.
func generateAPIKey() string {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		b = []byte(uuid.New().String() + uuid.New().String())[:24]
	}
	return "owlr_prov_" + hex.EncodeToString(b)
}

// bootstrap writes a fresh config file with defaults, a random node ID,
// and a random provider API key. Called automatically when no config exists.
func bootstrap(cfg *Config) {
	cfg.Account.NodeID = uuid.New().String()
	cfg.Account.APIKey = generateAPIKey()

	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	f := ini.Empty()
	sec := f.Section("account")
	sec.Key("node_id").SetValue(cfg.Account.NodeID)
	sec.Key("api_key").SetValue(cfg.Account.APIKey)

	mkt := f.Section("marketplace")
	mkt.Key("gateway").SetValue(cfg.Marketplace.Gateway)
	mkt.Key("allow_override").SetValue("true")

	inf := f.Section("inference")
	inf.Key("model_auto").SetValue("true")
	inf.Key("max_vram_pct").SetValue("80")

	idl := f.Section("idle")
	idl.Key("trigger_minutes").SetValue("10")
	idl.Key("gpu_threshold").SetValue("15")
	idl.Key("watch_processes").SetValue("true")

	dsk := f.Section("disk")
	dsk.Key("warn_threshold_pct").SetValue("30")
	dsk.Key("min_model_space_gb").SetValue("8")

	_ = f.SaveTo(path)
}

// Load reads owlrun.conf and returns a Config. If the file does not exist,
// a default config is created with auto-generated node ID and provider key.
// Partial files are also safe — missing keys use defaults.
func Load() (Config, error) {
	cfg := defaults()
	path := Path()

	if _, err := os.Stat(path); os.IsNotExist(err) {
		bootstrap(&cfg)
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
		cfg.Account.LightningAddress = sec.Key("lightning_address").String()
		cfg.Account.RedeemThreshold = sec.Key("redeem_threshold").MustInt(500)
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
		if jm := sec.Key("job_mode").String(); jm == "never" || jm == "always" {
			cfg.Idle.JobMode = jm
		}
	}

	if sec, err := f.GetSection("disk"); err == nil {
		cfg.Disk.WarnThresholdPct = sec.Key("warn_threshold_pct").MustInt(30)
		cfg.Disk.MinModelSpaceGB = sec.Key("min_model_space_gb").MustInt(8)
	}

	return cfg, nil
}
