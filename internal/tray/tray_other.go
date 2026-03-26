//go:build !windows && !linux

// Package tray provides the primary runtime loop.
// On Linux/macOS, Owlrun runs as a headless daemon — no system tray,
// state is logged to stdout. Blocks until SIGINT or SIGTERM.
package tray

import (
	"fmt"
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
	"github.com/fabgoodvibes/owlrun/internal/wallet"
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
	ecash     *wallet.Wallet
	mockMode  bool

	mu          sync.Mutex
	st          state
	errorDetail string // user-facing error message when state=stateError
	model       string
	jobMode     string // "never", "idle", "always"
}

// Run starts Owlrun in headless daemon mode. Blocks until SIGINT/SIGTERM.
func Run(cfg config.Config, dash *dashboard.Server, mockMode bool) {
	nodeID := config.EnsureNodeID(&cfg)
	config.EnsureAPIKey(&cfg)
	info := gpu.Detect()
	monitor := gpu.NewMonitor(info, 10*time.Second)
	tracker := earnings.New()

	w := wallet.New(cfg.Marketplace.Gateway, cfg.Account.APIKey)

	d := &daemon{
		cfg:       cfg,
		nodeID:    nodeID,
		gpuInfo:   info,
		monitor:   monitor,
		tracker:   tracker,
		ecash:     w,
		ollamaMgr: inference.New(info),
		jobMode:   cfg.Idle.JobMode,
		mockMode:  mockMode,
	}

	gw := marketplace.New(
		cfg.Marketplace.Gateway,
		cfg.Marketplace.ProxyBase,
		cfg.Account.APIKey,
		nodeID,
		cfg.Account.Wallet,
		cfg.Account.ReferralCode,
		cfg.Account.LightningAddress,
		cfg.Account.RedeemThreshold,
		cfg.Marketplace.Region,
		buildinfo.Version,
		info,
		func() (int, int, int, float64) {
			stats := monitor.Latest()
			return stats.UtilizationPct, stats.VRAMFreeMB, stats.TemperatureC, stats.PowerDrawW
		},
		func(model string, tokens int, earnedUSD float64) {
			if err := tracker.Record(model, tokens, earnedUSD); err != nil {
				log.Printf("owlrun: failed to record earnings: %v", err)
			}
		},
		func() {
			d.mu.Lock()
			d.st = stateEarning
			d.mu.Unlock()
		},
		func(balanceSats int64) {
			d.ecash.AutoClaim(balanceSats)
		},
	)
	d.gateway = gw

	if cfg.Inference.ContextLength > 0 {
		d.ollamaMgr.SetContextLength(cfg.Inference.ContextLength)
		gw.SetContextLength(cfg.Inference.ContextLength)
	}

	if dash != nil {
		dash.SetProvider(d.statusSnapshot)
		dash.SetTracker(tracker)
		dash.SetClaimer(func(amountSats int64) (string, error) {
			return d.ecash.Claim(amountSats)
		})
		dash.SetLightningAddressSetter(func(addr string) error {
			if err := config.SaveLightningAddress(addr); err != nil {
				return err
			}
			d.mu.Lock()
			d.cfg.Account.LightningAddress = addr
			d.mu.Unlock()
			d.gateway.SetLightningAddress(addr)
			return nil
		})
		dash.SetRedeemThresholdSetter(func(threshold int) error {
			if err := config.SaveRedeemThreshold(threshold); err != nil {
				return err
			}
			d.mu.Lock()
			d.cfg.Account.RedeemThreshold = threshold
			d.mu.Unlock()
			d.gateway.SetRedeemThreshold(threshold)
			return nil
		})
		dash.SetJobModeSetter(func(mode string) error {
			if err := config.SaveJobMode(mode); err != nil {
				return err
			}
			d.setJobMode(mode)
			return nil
		})
		dash.SetContextLengthSetter(func(ctxLen int) error {
			if err := config.SaveContextLength(ctxLen); err != nil {
				return err
			}
			d.mu.Lock()
			d.cfg.Inference.ContextLength = ctxLen
			model := d.model
			d.mu.Unlock()
			d.ollamaMgr.SetContextLength(ctxLen)
			d.gateway.SetContextLength(ctxLen)
			log.Printf("owlrun: context length changed to %d", ctxLen)
			if model != "" {
				if err := d.ollamaMgr.LoadModel(model); err != nil {
					log.Printf("owlrun: reload model with new context length: %v", err)
				}
			}
			return nil
		})
		dash.SetModelSwitcher(func(model string) error {
			if !d.ollamaMgr.ModelInstalled(model) {
				return fmt.Errorf("model %s not installed — download it first", model)
			}
			log.Printf("owlrun: switching primary model to %s", model)
			if err := d.ollamaMgr.LoadModel(model); err != nil {
				return fmt.Errorf("failed to load model: %w", err)
			}
			d.mu.Lock()
			d.model = model
			d.mu.Unlock()
			models, _ := d.ollamaMgr.SelectModels(d.gpuInfo.VRAMTotalGB, d.cfg.Inference.MaxVRAMPct)
			d.gateway.SetModels(models)
			go d.gateway.Reconnect()
			return nil
		})
		dash.SetModelRemover(func(model string) error {
			d.mu.Lock()
			active := d.model
			d.mu.Unlock()
			if model == active {
				return fmt.Errorf("cannot remove the active model — switch to another first")
			}
			if err := d.ollamaMgr.DeleteModel(model); err != nil {
				return err
			}
			log.Printf("owlrun: removed model %s", model)
			models, _ := d.ollamaMgr.SelectModels(d.gpuInfo.VRAMTotalGB, d.cfg.Inference.MaxVRAMPct)
			d.gateway.SetModels(models)
			go d.gateway.Reconnect()
			return nil
		})
		dash.SetModelPuller(func(model string) <-chan dashboard.PullModelProgress {
			out := make(chan dashboard.PullModelProgress, 8)
			go func() {
				defer close(out)
				for p := range d.ollamaMgr.PullModel(model) {
					pp := dashboard.PullModelProgress{
						Status:    p.Status,
						Total:     p.Total,
						Completed: p.Completed,
					}
					if p.Err != nil {
						pp.Error = p.Err.Error()
					}
					out <- pp
				}
			}()
			return out
		})
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

func (d *daemon) setJobMode(mode string) {
	d.mu.Lock()
	old := d.jobMode
	d.jobMode = mode
	st := d.st
	d.mu.Unlock()
	log.Printf("owlrun: job mode changed: %s -> %s", old, mode)

	if mode == "never" && (old == "idle" || old == "always") {
		if st == stateEarning || st == stateReady || st == stateMissingWallet {
			d.mu.Lock()
			d.st = stateIdle
			d.mu.Unlock()
			go d.stopEarning()
		}
	}
}

func (d *daemon) startEarning() {
	if d.mockMode {
		mockSrv, err := inference.StartMockOllama()
		if err != nil {
			log.Printf("owlrun: mock ollama failed: %v", err)
			d.mu.Lock()
			d.st = stateError
			d.errorDetail = "Mock Ollama failed to start: " + err.Error()
			d.mu.Unlock()
			return
		}
		mockAddr := "http://" + mockSrv.Addr
		d.ollamaMgr.SetHost(mockAddr)
		d.gateway.SetOllamaBase(mockAddr)
		log.Printf("owlrun: mock mode — using fake model %s on %s", inference.MockModel(), mockAddr)

		d.mu.Lock()
		d.model = inference.MockModel()
		if config.NeedsWallet(&d.cfg) {
			d.st = stateMissingWallet
		} else {
			d.st = stateReady
		}
		d.mu.Unlock()
		d.gateway.SetModels([]string{inference.MockModel()})
		d.gateway.Connect()
		log.Printf("owlrun: mock mode ready — connecting to gateway")
		return
	}

	if err := d.ollamaMgr.EnsureInstalled(); err != nil {
		log.Printf("owlrun: install ollama: %v", err)
		d.mu.Lock()
		d.st = stateError
		d.errorDetail = "Ollama is not installed. Download it from ollama.com/download, install it, then restart Owlrun."
		d.mu.Unlock()
		return
	}
	if err := d.ollamaMgr.Start(); err != nil {
		log.Printf("owlrun: start ollama: %v", err)
		d.mu.Lock()
		d.st = stateError
		d.errorDetail = "Ollama failed to start. Make sure Ollama is installed (ollama.com/download) and try restarting Owlrun."
		d.mu.Unlock()
		return
	}

	var models []string
	if d.cfg.Inference.Model != "" {
		models = []string{d.cfg.Inference.Model}
		log.Printf("owlrun: starting — model %s", d.cfg.Inference.Model)
	} else {
		var suggestions []string
		models, suggestions = d.ollamaMgr.SelectModels(d.gpuInfo.VRAMTotalGB, d.cfg.Inference.MaxVRAMPct)
		if len(models) == 0 {
			log.Printf("owlrun: no models installed — install one first, then restart")
			for _, s := range suggestions {
				log.Printf("  ollama pull %s", s)
			}
			_ = d.ollamaMgr.Stop()
			d.mu.Lock()
			d.st = stateError
			d.errorDetail = "No AI models installed. Open the dashboard at localhost:19131 and download a model, or run: ollama pull qwen2.5:0.5b"
			d.mu.Unlock()
			return
		}
		log.Printf("owlrun: found %d installed models, primary: %s", len(models), models[0])
	}
	model := models[0]

	d.mu.Lock()
	d.model = model
	if config.NeedsWallet(&d.cfg) {
		d.st = stateMissingWallet
	} else {
		d.st = stateReady
	}
	d.mu.Unlock()

	d.gateway.SetModels(models)
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
	jobMode := d.jobMode
	d.mu.Unlock()

	var s dashboard.Status
	s.JobMode = jobMode
	s.NodeID = d.nodeID
	s.ProviderKey = d.cfg.Account.APIKey
	s.Version = buildinfo.Version
	s.Network = buildinfo.Network
	s.Wallet.Address = d.cfg.Account.Wallet
	if config.NeedsWallet(&d.cfg) {
		s.Wallet.Warning = "Set your Lightning address in the Wallet section to start earning Bitcoin."
	} else if d.cfg.Account.LightningAddress != "" {
		s.Wallet.Configured = "Wallet configured at " + d.cfg.Account.LightningAddress
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
		s.ErrorDetail = d.errorDetail
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
	installed := d.ollamaMgr.ListInstalled()
	installedSet := make(map[string]bool, len(installed))
	for _, m := range installed {
		installedSet[m] = true
	}
	for _, mi := range gpu.AllModelInfos(d.gpuInfo.VRAMTotalGB, d.cfg.Inference.MaxVRAMPct) {
		s.AvailableModels = append(s.AvailableModels, dashboard.AvailableModel{
			Tag: mi.Tag, VramGB: mi.VramGB, Installed: installedSet[mi.Tag], Active: mi.Tag == model, Fits: mi.Fits,
		})
	}

	snap := d.tracker.Get()
	s.Earnings.TodayUSD = snap.Today
	s.Earnings.TotalUSD = snap.Total

	gwStats := d.gateway.Stats()
	s.Models = gwStats.Models
	s.Gateway.Connected = gwStats.Connected
	s.Gateway.GatewayStatus = gwStats.Status
	s.Gateway.JobsToday = gwStats.JobsToday
	s.Gateway.TokensToday = gwStats.TokensToday
	s.Gateway.EarnedTodayUSD = gwStats.EarnedTodayUSD
	s.Gateway.EarnedTodaySats = gwStats.EarnedTodaySats
	s.Gateway.EarnedTotalSats = gwStats.EarnedTotalSats
	s.Gateway.QueueDepthGlobal = gwStats.QueueDepthGlobal
	s.LightningAddress = d.cfg.Account.LightningAddress
	s.RedeemThreshold = d.cfg.Account.RedeemThreshold
	s.ContextLength = d.cfg.Inference.ContextLength

	// Model pricing from gateway
	if gwStats.ModelPricing != nil {
		s.ModelPricing = &dashboard.ModelPricingInfo{
			PerMInputUSD:  gwStats.ModelPricing.PerMInputUSD,
			PerMOutputUSD: gwStats.ModelPricing.PerMOutputUSD,
		}
	}
	if len(gwStats.AllModelPricing) > 0 {
		s.AllModelPricing = make(map[string]*dashboard.ModelPricingInfo, len(gwStats.AllModelPricing))
		for tag, p := range gwStats.AllModelPricing {
			s.AllModelPricing[tag] = &dashboard.ModelPricingInfo{
				PerMInputUSD:  p.PerMInputUSD,
				PerMOutputUSD: p.PerMOutputUSD,
			}
		}
	}

	// BTC price from gateway
	s.BtcPrice = dashboard.BtcPriceInfo{
		LiveUsd:    gwStats.BtcPrice.LiveUsd,
		YesterdayFix: gwStats.BtcPrice.YesterdayFix,
		DailyAvg:   gwStats.BtcPrice.DailyAvg,
		WeeklyAvg:  gwStats.BtcPrice.WeeklyAvg,
		Status:     gwStats.BtcPrice.Status,
	}

	// Map broadcasts from gateway to dashboard
	for _, b := range gwStats.Broadcasts {
		s.Broadcasts = append(s.Broadcasts, dashboard.BroadcastMsg{
			Title:     b.Title,
			Message:   b.Message,
			Severity:  b.Severity,
			Timestamp: b.Timestamp,
		})
	}

	// Sats wallet
	if d.ecash != nil {
		ws := d.ecash.GetStats(gwStats.BalanceSats, gwStats.BtcPrice.LiveUsd)
		var hist []dashboard.TokenHistoryItem
		for _, t := range ws.TokenHistory {
			hist = append(hist, dashboard.TokenHistoryItem{Token: t.Token, Sats: t.Sats, ClaimedAt: t.ClaimedAt})
		}
		var wdHist []dashboard.WithdrawHistoryItem
		for _, w := range gwStats.WithdrawHistory {
			wdHist = append(wdHist, dashboard.WithdrawHistoryItem{
				AmountSats:  w.AmountSats,
				PaymentHash: w.PaymentHash,
				Timestamp:   w.Timestamp,
			})
		}
		s.SatsWallet = dashboard.SatsWalletInfo{
			GatewaySats:     ws.GatewaySats,
			LocalSats:       ws.LocalSats,
			TotalSats:       ws.TotalSats,
			USDApprox:       ws.USDApprox,
			ProofCount:      ws.ProofCount,
			LastClaim:       ws.LastClaim,
			LastToken:       ws.LastToken,
			TokenHistory:    hist,
			WithdrawHistory: wdHist,
		}
	}

	diskInfo, err := disk.Check(disk.OllamaModelsDir())
	if err == nil {
		s.Disk.Path = diskInfo.Path
		s.Disk.TotalGB = diskInfo.TotalGB
		s.Disk.FreeGB = diskInfo.FreeGB
		s.Disk.FreePct = diskInfo.FreePct
	}

	return s
}
