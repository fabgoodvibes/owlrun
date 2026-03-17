//go:build windows

// Package tray owns the Windows system tray icon, menu, and state display.
// It is the primary UI for Owlrun — everything the user sees lives here.
//
// State machine:
//   StatePaused  — user clicked Pause; idle monitor is suppressed
//   StateIdle    — not paused, but idle conditions not yet met (yellow)
//   StateEarning — not paused, all idle conditions met (green)
//
// The idle monitor runs every 30 s and drives StateIdle ↔ StateEarning.
// The user toggle drives StatePaused ↔ (Idle|Earning).
package tray

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/fabgoodvibes/owlrun/internal/assets"
	"github.com/fabgoodvibes/owlrun/internal/buildinfo"
	"github.com/fabgoodvibes/owlrun/internal/config"
	"github.com/fabgoodvibes/owlrun/internal/dashboard"
	"github.com/fabgoodvibes/owlrun/internal/disk"
	"github.com/fabgoodvibes/owlrun/internal/earnings"
	"github.com/fabgoodvibes/owlrun/internal/gpu"
	"github.com/fabgoodvibes/owlrun/internal/idle"
	"github.com/fabgoodvibes/owlrun/internal/inference"
	"github.com/fabgoodvibes/owlrun/internal/marketplace"
	"github.com/getlantern/systray"
)

// State represents the agent's operating state.
type State int

const (
	StateEarning       State = iota // 🟢 Connected and serving jobs
	StateIdle                       // 🟡 Waiting for idle conditions
	StateReady                      // 🟡 Ollama up, connecting to gateway
	StateMissingWallet              // 🔵 No payout wallet configured
	StateError                      // 🔴 Hard error
	StatePaused                     // ⚫ Manually paused by user
)

// Agent is the top-level controller that drives the tray and all subsystems.
type Agent struct {
	mu             sync.Mutex
	state          State
	manuallyPaused bool // true = user explicitly paused; overrides idle monitor
	starting       bool // true = Ollama startup goroutine is in progress
	model          string // currently loaded Ollama model tag
	jobMode        string // "never", "idle", "always"
	cfg            config.Config
	nodeID         string
	gpuInfo        gpu.Info
	gpuMonitor     *gpu.Monitor
	ollamaMgr      *inference.Manager
	tracker        *earnings.Tracker
	gateway        *marketplace.Router
	dash           *dashboard.Server

	// Tray menu items updated at runtime
	mStatus    *systray.MenuItem
	mToday     *systray.MenuItem
	mTotal     *systray.MenuItem
	mToggle    *systray.MenuItem
	mDashboard *systray.MenuItem
	mWalletWarn *systray.MenuItem // shown when wallet not configured
	mDiskWarn   *systray.MenuItem // shown only when disk is low
	mQuit       *systray.MenuItem

	// Job mode submenu
	mJobMode      *systray.MenuItem
	mJobNever     *systray.MenuItem
	mJobIdle      *systray.MenuItem
	mJobAlways    *systray.MenuItem
}

// Run detects the GPU, then starts the system tray. Blocks until Quit.
// Must be called from the main goroutine.
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

	a := &Agent{
		cfg:        cfg,
		nodeID:     nodeID,
		gpuInfo:    info,
		gpuMonitor: monitor,
		state:      StateIdle,
		jobMode:    cfg.Idle.JobMode,
		tracker:    tracker,
		ollamaMgr:  inference.New(info),
		gateway:    gw,
		dash:       dash,
	}
	systray.Run(a.onReady, a.onExit)
}

// onReady is called by systray once the tray is initialized.
func (a *Agent) onReady() {
	a.applyIcon()
	systray.SetTooltip("Owlrun — idle GPU earning")

	// Status line (read-only)
	a.mStatus = systray.AddMenuItem(a.stateLabel(), "")
	a.mStatus.Disable()
	systray.AddSeparator()

	// Earnings (read-only)
	snap := a.tracker.Get()
	a.mToday = systray.AddMenuItem(fmtToday(snap.Today), "Earnings today")
	a.mToday.Disable()
	a.mTotal = systray.AddMenuItem(fmtTotal(snap.Total), "All-time earnings")
	a.mTotal.Disable()
	systray.AddSeparator()

	// Wallet warning
	a.mWalletWarn = systray.AddMenuItem("⚠ Set your payout wallet in ~/.owlrun/owlrun.conf", "")
	a.mWalletWarn.Disable()
	if !config.NeedsWallet(&a.cfg) {
		a.mWalletWarn.Hide()
	}
	systray.AddSeparator()

	// Actions
	a.mToggle = systray.AddMenuItem(a.toggleLabel(), "Pause or resume Owlrun")
	a.mDashboard = systray.AddMenuItem("Open Dashboard", "Open localhost:19131")

	// Job mode submenu
	a.mJobMode = systray.AddMenuItem("Accept Jobs", "")
	a.mJobNever = a.mJobMode.AddSubMenuItem("Never", "Never accept jobs")
	a.mJobIdle = a.mJobMode.AddSubMenuItem(fmt.Sprintf("After idle %dm", a.cfg.Idle.TriggerMinutes), "Accept jobs after idle timeout")
	a.mJobAlways = a.mJobMode.AddSubMenuItem("Always", "Always accept jobs")
	a.applyJobModeChecks()

	systray.AddSeparator()
	a.mDiskWarn = systray.AddMenuItem("", "")
	a.mDiskWarn.Disable()
	a.mDiskWarn.Hide()

	// Color legend
	systray.AddSeparator()
	for _, l := range []string{
		"🟢 Green  — Connected & earning",
		"🟡 Yellow — Getting ready",
		"🔵 Blue   — Wallet not set",
		"🔴 Red    — Error",
		"⚪ Grey   — Paused",
	} {
		m := systray.AddMenuItem(l, "")
		m.Disable()
	}
	systray.AddSeparator()
	a.mQuit = systray.AddMenuItem("Quit", "Exit Owlrun")

	if a.dash != nil {
		a.dash.SetProvider(a.statusSnapshot)
	}

	go a.gpuMonitor.Start()
	go a.handleClicks()
	go a.earningsRefreshLoop()
	go a.idleMonitorLoop()
	go a.diskMonitorLoop()
}

func (a *Agent) onExit() {
	a.gateway.Disconnect()
	if err := a.ollamaMgr.Stop(); err != nil {
		log.Printf("owlrun: stop ollama: %v", err)
	}
	a.tracker.Close()
}

// handleClicks processes menu click events.
func (a *Agent) handleClicks() {
	for {
		select {
		case <-a.mToggle.ClickedCh:
			a.togglePause()
		case <-a.mDashboard.ClickedCh:
			openBrowser("http://localhost:19131")
		case <-a.mJobNever.ClickedCh:
			a.setJobMode("never")
		case <-a.mJobIdle.ClickedCh:
			a.setJobMode("idle")
		case <-a.mJobAlways.ClickedCh:
			a.setJobMode("always")
		case <-a.mQuit.ClickedCh:
			systray.Quit()
		}
	}
}

// togglePause flips the manual pause state.
func (a *Agent) togglePause() {
	a.mu.Lock()
	a.manuallyPaused = !a.manuallyPaused
	if a.manuallyPaused {
		a.state = StatePaused
		a.starting = false
	} else {
		// Hand control back to idle monitor; start in Idle until conditions met.
		a.state = StateIdle
	}
	a.refreshMenuLocked()
	becomingPaused := a.manuallyPaused
	a.mu.Unlock()

	if becomingPaused {
		go a.stopEarning()
	}
}

// idleMonitorLoop checks idle conditions every 30 s and updates state.
func (a *Agent) idleMonitorLoop() {
	// Check immediately at startup so the icon is correct from the first second.
	a.checkAndUpdateState()

	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		a.checkAndUpdateState()
	}
}

// checkAndUpdateState runs one idle-detection cycle and updates state + icon.
func (a *Agent) checkAndUpdateState() {
	a.mu.Lock()
	paused := a.manuallyPaused
	mode := a.jobMode
	a.mu.Unlock()

	if paused {
		return // user has manually paused; do not touch state
	}

	// "never" mode: never start earning.
	if mode == "never" {
		return
	}

	a.mu.Lock()
	st := a.state
	a.mu.Unlock()

	if st == StateEarning || st == StateReady || st == StateMissingWallet {
		// In "always" mode, never stop due to user activity.
		if mode == "always" {
			return
		}
		// Ollama is running. Only stop if the user returns or a game launches.
		userBack := idle.IdleDuration() < time.Duration(a.cfg.Idle.TriggerMinutes)*time.Minute
		gameRunning := a.cfg.Idle.WatchProcesses && idle.IsGameRunning()
		if userBack || gameRunning {
			a.mu.Lock()
			a.state = StateIdle
			a.refreshMenuLocked()
			a.mu.Unlock()
			go a.stopEarning()
		}
		return
	}

	// "always" mode: skip idle check, start immediately.
	shouldStart := mode == "always" || idle.IsSystemIdle(a.cfg.Idle, a.gpuMonitor.UtilizationPct())

	a.mu.Lock()
	defer a.mu.Unlock()

	if shouldStart && a.state == StateIdle && !a.starting {
		a.starting = true
		go a.startEarning()
	}
}

// startEarning runs the full Ollama startup pipeline in a background goroutine.
// On success it transitions to Ready/MissingWallet; on failure it goes to Error.
func (a *Agent) startEarning() {
	for _, s := range []struct {
		name string
		fn   func() error
	}{
		{"install ollama", a.ollamaMgr.EnsureInstalled},
		{"start ollama", a.ollamaMgr.Start},
	} {
		if err := s.fn(); err != nil {
			log.Printf("owlrun: %s: %v", s.name, err)
			a.mu.Lock()
			a.starting = false
			a.state = StateError
			a.refreshMenuLocked()
			a.mu.Unlock()
			return
		}
	}

	model := a.cfg.Inference.Model
	if model == "" {
		chosen, suggestions := a.ollamaMgr.SelectModel(a.gpuInfo.VRAMTotalGB, a.cfg.Inference.MaxVRAMPct)
		if chosen == "" {
			log.Printf("owlrun: no models installed — install one first, then restart")
			for _, s := range suggestions {
				log.Printf("  ollama pull %s", s)
			}
			_ = a.ollamaMgr.Stop()
			a.mu.Lock()
			a.starting = false
			a.state = StateError
			a.refreshMenuLocked()
			a.mu.Unlock()
			return
		}
		model = chosen
		log.Printf("owlrun: using installed model %s", model)
	} else {
		log.Printf("owlrun: starting — model %s", model)
	}

	a.mu.Lock()
	a.model = model
	a.starting = false
	if config.NeedsWallet(&a.cfg) {
		a.state = StateMissingWallet
	} else {
		a.state = StateReady
	}
	a.refreshMenuLocked()
	a.mu.Unlock()
	a.gateway.SetModel(model)
	a.gateway.Connect()
	log.Printf("owlrun: ready — connecting to gateway")
}

// loadOrPull pulls the model if not yet present, then warms it into VRAM.
func (a *Agent) loadOrPull(model string) error {
	if !a.ollamaMgr.ModelInstalled(model) {
		log.Printf("owlrun: pulling model %s …", model)
		for p := range a.ollamaMgr.PullModel(model) {
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
	return a.ollamaMgr.LoadModel(model)
}

// stopEarning disconnects from the gateway and shuts down Ollama.
func (a *Agent) stopEarning() {
	a.gateway.Disconnect()
	if err := a.ollamaMgr.Stop(); err != nil {
		log.Printf("owlrun: stop ollama: %v", err)
	}
}

// SetState allows external subsystems (marketplace, Ollama manager) to push
// state changes into the tray. Respects manuallyPaused.
func (a *Agent) SetState(s State) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.manuallyPaused {
		return
	}
	if a.state == s {
		return
	}
	a.state = s
	a.refreshMenuLocked()
}

// refreshMenuLocked updates icon + all dynamic menu text.
// Must be called with a.mu held.
func (a *Agent) refreshMenuLocked() {
	a.applyIcon()
	a.mStatus.SetTitle(a.stateLabel())
	a.mToggle.SetTitle(a.toggleLabel())
}

// applyIcon sets the tray icon to match the current state.
// Must be called with a.mu held (or before menu is live).
func (a *Agent) applyIcon() {
	switch a.state {
	case StateEarning:
		systray.SetIcon(assets.IconGreen)
	case StateIdle, StateReady:
		systray.SetIcon(assets.IconYellow)
	case StateMissingWallet:
		systray.SetIcon(assets.IconBlue)
	case StateError:
		systray.SetIcon(assets.IconRed)
	case StatePaused:
		systray.SetIcon(assets.IconGrey)
	}
}

func (a *Agent) stateLabel() string {
	switch a.state {
	case StateEarning:
		return "🟢 Earning"
	case StateIdle:
		return "🟡 Idle — waiting"
	case StateReady:
		return "🟡 Getting ready"
	case StateMissingWallet:
		return "🔵 Wallet not set"
	case StateError:
		return "🔴 Error"
	case StatePaused:
		return "⚪ Paused"
	}
	return ""
}

func (a *Agent) toggleLabel() string {
	if a.manuallyPaused {
		return "Resume"
	}
	return "Pause"
}

// earningsRefreshLoop polls the earnings tracker and updates menu items.
func (a *Agent) earningsRefreshLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		snap := a.tracker.Get()
		a.mToday.SetTitle(fmtToday(snap.Today))
		a.mTotal.SetTitle(fmtTotal(snap.Total))
	}
}

// diskMonitorLoop checks disk space at startup and then every 5 minutes.
// Shows/hides a tray warning item and shows a blocking dialog if critical.
func (a *Agent) diskMonitorLoop() {
	a.checkDisk(true) // true = show blocking dialog if critical
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		a.checkDisk(false)
	}
}

func (a *Agent) checkDisk(showDialogIfCritical bool) {
	modelsDir := disk.OllamaModelsDir()
	info, err := disk.Check(modelsDir)
	if err != nil {
		// Drive not accessible yet (models dir may not exist) — not an error
		return
	}

	warnPct := float64(a.cfg.Disk.WarnThresholdPct)
	minGB := float64(a.cfg.Disk.MinModelSpaceGB)

	switch {
	case info.FreeGB < minGB:
		// Critical: not enough space even for the smallest model.
		a.mDiskWarn.SetTitle(fmt.Sprintf("⚠  Only %.1f GB free — downloads blocked", info.FreeGB))
		a.mDiskWarn.Show()
		if showDialogIfCritical {
			disk.CriticalDialog(info, minGB)
		}

	case info.FreePct < warnPct:
		// Warning: below threshold but still workable.
		a.mDiskWarn.SetTitle(fmt.Sprintf("⚠  Low disk: %.1f GB free (%.0f%%)", info.FreeGB, info.FreePct))
		a.mDiskWarn.Show()
		if showDialogIfCritical {
			disk.WarnDialog(info, a.cfg.Disk.WarnThresholdPct)
		}

	default:
		// Healthy — hide the warning item.
		a.mDiskWarn.Hide()
	}
}

// setJobMode switches the runtime job acceptance mode and updates the tray.
func (a *Agent) setJobMode(mode string) {
	a.mu.Lock()
	old := a.jobMode
	a.jobMode = mode
	a.mu.Unlock()
	a.applyJobModeChecks()
	log.Printf("owlrun: job mode changed: %s → %s", old, mode)

	// If switching to "never", stop earning immediately.
	if mode == "never" && (old == "idle" || old == "always") {
		a.mu.Lock()
		wasEarning := a.state == StateEarning || a.state == StateReady || a.state == StateMissingWallet
		if wasEarning {
			a.state = StateIdle
			a.refreshMenuLocked()
		}
		a.mu.Unlock()
		if wasEarning {
			go a.stopEarning()
		}
	}
}

// applyJobModeChecks updates the check marks on the job mode submenu.
func (a *Agent) applyJobModeChecks() {
	a.mu.Lock()
	mode := a.jobMode
	a.mu.Unlock()

	a.mJobNever.Uncheck()
	a.mJobIdle.Uncheck()
	a.mJobAlways.Uncheck()
	switch mode {
	case "never":
		a.mJobNever.Check()
	case "always":
		a.mJobAlways.Check()
	default:
		a.mJobIdle.Check()
	}
}

func fmtToday(v float64) string { return fmt.Sprintf("Today:  $%.2f", v) }
func fmtTotal(v float64) string { return fmt.Sprintf("Total:  $%.2f", v) }

// statusSnapshot assembles a dashboard.Status from live subsystem data.
// This is the StatusProvider function wired into the dashboard in onReady.
func (a *Agent) statusSnapshot() dashboard.Status {
	a.mu.Lock()
	state := a.state
	model := a.model
	a.mu.Unlock()

	var s dashboard.Status
	s.NodeID = a.nodeID
	s.Version = buildinfo.Version
	s.Network = buildinfo.Network
	s.Wallet.Address = a.cfg.Account.Wallet
	if config.NeedsWallet(&a.cfg) {
		s.Wallet.Warning = "Set your Solana wallet in <code>~/.owlrun/owlrun.conf</code> under <code>[account]</code> → <code>wallet = YOUR_SOLANA_PUBKEY</code> to receive payouts."
	}
	switch state {
	case StateEarning:
		s.State = "earning"
	case StateReady:
		s.State = "ready"
	case StateMissingWallet:
		s.State = "wallet"
	case StateError:
		s.State = "error"
	case StatePaused:
		s.State = "paused"
	default:
		s.State = "idle"
	}

	// GPU
	gpuStats := a.gpuMonitor.Latest()
	s.GPU.Name = a.gpuInfo.Name
	s.GPU.Vendor = a.gpuInfo.Vendor
	s.GPU.VRAMTotalMB = a.gpuInfo.VRAMTotalMB
	s.GPU.VRAMExact = a.gpuInfo.VRAMExact
	s.GPU.UtilPct = gpuStats.UtilizationPct
	s.GPU.VRAMFreeMB = gpuStats.VRAMFreeMB
	s.GPU.TempC = gpuStats.TemperatureC
	s.GPU.PowerW = gpuStats.PowerDrawW

	// Model
	s.Model = model

	// Earnings
	snap := a.tracker.Get()
	s.Earnings.TodayUSD = snap.Today
	s.Earnings.TotalUSD = snap.Total

	// Gateway
	gwStats := a.gateway.Stats()
	s.Gateway.Connected = gwStats.Connected
	s.Gateway.GatewayStatus = gwStats.Status
	s.Gateway.JobsToday = gwStats.JobsToday
	s.Gateway.TokensToday = gwStats.TokensToday
	s.Gateway.EarnedTodayUSD = gwStats.EarnedTodayUSD
	s.Gateway.QueueDepthGlobal = gwStats.QueueDepthGlobal
	s.Gateway.NextPayoutEpoch = gwStats.NextPayoutEpoch

	// Disk
	diskInfo, err := disk.Check(disk.OllamaModelsDir())
	if err == nil {
		s.Disk.Path = diskInfo.Path
		s.Disk.TotalGB = diskInfo.TotalGB
		s.Disk.FreeGB = diskInfo.FreeGB
		s.Disk.FreePct = diskInfo.FreePct
	}

	return s
}
