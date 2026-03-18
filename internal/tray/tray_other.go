//go:build !windows && !linux

// Package tray provides the primary runtime loop.
// On Linux/macOS, Owlrun runs as a headless daemon — no system tray,
// state is logged to stdout. Blocks until SIGINT or SIGTERM.
package tray

import (
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/fabgoodvibes/owlrun/internal/buildinfo"
	"github.com/fabgoodvibes/owlrun/internal/config"
	"github.com/fabgoodvibes/owlrun/internal/dashboard"
	"github.com/fabgoodvibes/owlrun/internal/disk"
	"github.com/fabgoodvibes/owlrun/internal/earnings"
	"github.com/fabgoodvibes/owlrun/internal/gpu"
	"github.com/fabgoodvibes/owlrun/internal/idle"
	"github.com/fabgoodvibes/owlrun/internal/inference"
	"github.com/fabgoodvibes/owlrun/internal/marketplace"
)

type state int

const (
	stateIdle          state = iota
	stateStarting
	stateReady
	stateEarning
	stateMissingWallet
	stateError
)

type daemon struct {
	cfg       config.Config
	nodeID    string
	gpuInfo   gpu.Info
	monitor   *gpu.Monitor
	tracker   *earnings.Tracker
	ollamaMgr *inference.Manager
	gateway   *marketplace.Router

	mu      sync.Mutex
	st      state
	model   string
	jobMode string // "never", "idle", "always"
}

// Run starts Owlrun in headless daemon mode. Blocks until SIGINT/SIGTERM.
func Run(cfg config.Config, dash *dashboard.Server) {
	nodeID := config.EnsureNodeID(&cfg)
	info := gpu.Detect()
	monitor := gpu.NewMonitor(info, 10*time.Second)
	tracker := earnings.New()

	gw := marketplace.New(
		cfg.Marketplace.Gateway,
		cfg.Marketplace.ProxyBase,
		cfg.Account.APIKey,
		nodeID,
		cfg.Account.Wallet,
		cfg.Account.ReferralCode,
		cfg.Marketplace.Region,
		buildinfo.Version,
		info,
		func() (int, int, int, float64) {
			stats := monitor.Latest()
			return stats.UtilizationPct, stats.VRAMFreeMB, stats.TemperatureC, stats.PowerDrawW
		},
		func(model string, tokens int, earnedUSD float64) {
			tracker.Record(model, tokens, earnedUSD)
		},
	)

	d := &daemon{
		cfg:       cfg,
		nodeID:    nodeID,
		gpuInfo:   info,
		monitor:   monitor,
		tracker:   tracker,
		ollamaMgr: inference.New(info),
		gateway:   gw,
		jobMode:   cfg.Idle.JobMode,
	}

	if dash != nil {
		dash.SetProvider(d.statusSnapshot)
		dash.SetTracker(tracker)
	}

	log.Printf("owlrun: node %s | gpu %s %s (%.0f GB VRAM)",
		nodeID, info.Vendor, info.Name, info.VRAMTotalGB)

	go monitor.Start()
	go d.idleLoop()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Println("owlrun: shutting down")
	gw.Disconnect()
	if err := d.ollamaMgr.Stop(); err != nil {
		log.Printf("owlrun: stop ollama: %v", err)
	}
	tracker.Close()
}

// idleLoop checks every 30s whether earning conditions are met and transitions state.
func (d *daemon) idleLoop() {
	d.check()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		d.check()
	}
}

func (d *daemon) check() {
	d.mu.Lock()
	st := d.st
	mode := d.jobMode
	d.mu.Unlock()

	if mode == "never" {
		return
	}

	gpuUtil := d.monitor.UtilizationPct()

	if st == stateEarning || st == stateReady || st == stateMissingWallet {
		if mode == "always" {
			return
		}
		userBack := idle.IdleDuration() < time.Duration(d.cfg.Idle.TriggerMinutes)*time.Minute
		gameRunning := d.cfg.Idle.WatchProcesses && idle.IsGameRunning()
		if userBack || gameRunning {
			d.mu.Lock()
			d.st = stateIdle
			d.mu.Unlock()
			log.Println("owlrun: conditions no longer met — stopping")
			go d.stopEarning()
		}
		return
	}

	shouldStart := mode == "always" || idle.IsSystemIdle(d.cfg.Idle, gpuUtil)

	if shouldStart && st == stateIdle {
		d.mu.Lock()
		d.st = stateStarting
		d.mu.Unlock()
		go d.startEarning()
	}
}

func (d *daemon) startEarning() {
	for _, s := range []struct {
		name string
		fn   func() error
	}{
		{"install ollama", d.ollamaMgr.EnsureInstalled},
		{"start ollama", d.ollamaMgr.Start},
	} {
		if err := s.fn(); err != nil {
			log.Printf("owlrun: %s: %v", s.name, err)
			d.mu.Lock()
			d.st = stateError
			d.mu.Unlock()
			return
		}
	}

	model := d.cfg.Inference.Model
	if model == "" {
		chosen, suggestions := d.ollamaMgr.SelectModel(d.gpuInfo.VRAMTotalGB, d.cfg.Inference.MaxVRAMPct)
		if chosen == "" {
			log.Printf("owlrun: no models installed — install one first, then restart")
			for _, s := range suggestions {
				log.Printf("  ollama pull %s", s)
			}
			_ = d.ollamaMgr.Stop()
			d.mu.Lock()
			d.st = stateError
			d.mu.Unlock()
			return
		}
		model = chosen
		log.Printf("owlrun: using installed model %s", model)
	} else {
		log.Printf("owlrun: starting — model %s", model)
	}

	d.mu.Lock()
	d.model = model
	if config.NeedsWallet(&d.cfg) {
		d.st = stateMissingWallet
	} else {
		d.st = stateReady
	}
	d.mu.Unlock()

	d.gateway.SetModel(model)
	d.gateway.Connect()
	log.Printf("owlrun: ready — connecting to gateway")
}

func (d *daemon) loadOrPull(model string) error {
	if !d.ollamaMgr.ModelInstalled(model) {
		log.Printf("owlrun: pulling model %s …", model)
		for p := range d.ollamaMgr.PullModel(model) {
			if p.Err != nil {
				return p.Err
			}
			if p.Total > 0 {
				pct := int(100 * p.Completed / p.Total)
				log.Printf("owlrun: pull %s: %s %d%%", model, p.Status, pct)
			}
		}
	}
	log.Printf("owlrun: loading model %s into VRAM …", model)
	return d.ollamaMgr.LoadModel(model)
}

func (d *daemon) stopEarning() {
	d.gateway.Disconnect()
	if err := d.ollamaMgr.Stop(); err != nil {
		log.Printf("owlrun: stop ollama: %v", err)
	}
}

func (d *daemon) statusSnapshot() dashboard.Status {
	d.mu.Lock()
	st := d.st
	model := d.model
	d.mu.Unlock()

	var s dashboard.Status
	s.NodeID = d.nodeID
	s.Version = buildinfo.Version
	s.Network = buildinfo.Network
	s.Wallet.Address = d.cfg.Account.Wallet
	if config.NeedsWallet(&d.cfg) {
		s.Wallet.Warning = "Set your Solana wallet in <code>~/.owlrun/owlrun.conf</code> under <code>[account]</code> → <code>wallet = YOUR_SOLANA_PUBKEY</code> to receive payouts."
	}
	switch st {
	case stateEarning:
		s.State = "earning"
	case stateReady, stateStarting:
		s.State = "ready"
	case stateMissingWallet:
		s.State = "wallet"
	case stateError:
		s.State = "error"
	default:
		s.State = "idle"
	}

	gpuStats := d.monitor.Latest()
	s.GPU.Name = d.gpuInfo.Name
	s.GPU.Vendor = d.gpuInfo.Vendor
	s.GPU.VRAMTotalMB = d.gpuInfo.VRAMTotalMB
	s.GPU.VRAMExact = d.gpuInfo.VRAMExact
	s.GPU.UtilPct = gpuStats.UtilizationPct
	s.GPU.VRAMFreeMB = gpuStats.VRAMFreeMB
	s.GPU.TempC = gpuStats.TemperatureC
	s.GPU.PowerW = gpuStats.PowerDrawW

	s.Model = model

	snap := d.tracker.Get()
	s.Earnings.TodayUSD = snap.Today
	s.Earnings.TotalUSD = snap.Total

	gwStats := d.gateway.Stats()
	s.Gateway.Connected = gwStats.Connected
	s.Gateway.GatewayStatus = gwStats.Status
	s.Gateway.JobsToday = gwStats.JobsToday
	s.Gateway.TokensToday = gwStats.TokensToday
	s.Gateway.EarnedTodayUSD = gwStats.EarnedTodayUSD
	s.Gateway.QueueDepthGlobal = gwStats.QueueDepthGlobal
	s.Gateway.NextPayoutEpoch = gwStats.NextPayoutEpoch

	diskInfo, err := disk.Check(disk.OllamaModelsDir())
	if err == nil {
		s.Disk.Path = diskInfo.Path
		s.Disk.TotalGB = diskInfo.TotalGB
		s.Disk.FreeGB = diskInfo.FreeGB
		s.Disk.FreePct = diskInfo.FreePct
	}

	return s
}
