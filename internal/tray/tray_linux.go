//go:build linux

// Package tray provides the primary runtime loop and, on Linux desktops,
// a StatusNotifierItem tray icon via D-Bus (no CGO required).
//
// Display detection:
//   - DISPLAY or WAYLAND_DISPLAY set  → try D-Bus SNI tray
//   - Neither set, or D-Bus unavailable → headless daemon, no errors
package tray

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image/png"
	"log"
	"os"
	"os/signal"
	"sync"
	"syscall"
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
	"github.com/fabgoodvibes/owlrun/internal/wallet"
	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
)

// Run is the Linux entry point. Tries a StatusNotifierItem tray widget when
// a graphical display is available; falls back to headless daemon silently.
func Run(cfg config.Config, dash *dashboard.Server) {
	if hasDisplay() {
		if err := runSNI(cfg, dash); err != nil {
			log.Printf("owlrun: tray unavailable (%v), running headless", err)
			runHeadless(cfg, dash)
		}
		return
	}
	runHeadless(cfg, dash)
}

func hasDisplay() bool {
	return os.Getenv("DISPLAY") != "" || os.Getenv("WAYLAND_DISPLAY") != ""
}

// ─── D-Bus paths and interface names ─────────────────────────────────────────

const (
	sniPath   = dbus.ObjectPath("/StatusNotifierItem")
	menuPath  = dbus.ObjectPath("/MenuBar")
	sniIface  = "org.kde.StatusNotifierItem"
	menuIface = "com.canonical.dbusmenu"
)

// ─── D-Bus value types ────────────────────────────────────────────────────────

// iconPixmap is (iiay) — one entry in the a(iiay) icon pixmap property.
type iconPixmap struct {
	Width  int32
	Height int32
	Data   []byte // ARGB32, network byte order
}

// toolTip is (sa(iiay)ss) — the ToolTip property of StatusNotifierItem.
type toolTip struct {
	IconName string
	IconData []iconPixmap
	Title    string
	Desc     string
}

// layoutItem is (ia{sv}av) — a node in the dbusmenu tree.
type layoutItem struct {
	ID       int32
	Props    map[string]dbus.Variant
	Children []dbus.Variant
}

// menuProps is (ia{sv}) — used in GetGroupProperties.
type menuProps struct {
	ID    int32
	Props map[string]dbus.Variant
}

// ─── Daemon state ─────────────────────────────────────────────────────────────

type linuxState int

const (
	linuxIdle          linuxState = iota
	linuxStarting                 // Ollama startup in progress
	linuxReady                    // Ollama up, connecting to gateway
	linuxEarning                  // Gateway connected, serving jobs
	linuxMissingWallet            // No payout wallet configured
	linuxError                    // Hard error (Ollama crash, etc.)
	linuxPaused                   // User manually paused
)

// ─── sniDaemon ────────────────────────────────────────────────────────────────
// Shared by both the SNI tray path and the headless fallback.
// When conn == nil, all tray-specific calls are no-ops.

type sniDaemon struct {
	cfg       config.Config
	nodeID    string
	gpuInfo   gpu.Info
	monitor   *gpu.Monitor
	tracker   *earnings.Tracker
	ollamaMgr *inference.Manager
	gateway   *marketplace.Router
	ecash     *wallet.Wallet
	dash      *dashboard.Server

	// Precomputed icon pixmaps (decoded from ICO at startup).
	iconGreen  []iconPixmap
	iconYellow []iconPixmap
	iconGrey   []iconPixmap
	iconRed    []iconPixmap
	iconBlue   []iconPixmap

	mu             sync.Mutex
	st             linuxState
	manuallyPaused bool
	starting       bool
	model          string
	jobMode        string // "never", "idle", "always"

	// D-Bus fields — nil in headless mode.
	conn      *dbus.Conn
	sniProps  *prop.Properties
	menuObj   *dbusMenu
	menuRev   uint32
}

// ─── SNI tray entry point ─────────────────────────────────────────────────────

func runSNI(cfg config.Config, dash *dashboard.Server) error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("dbus session: %w", err)
	}

	d := buildDaemon(cfg, dash)
	d.conn = conn
	d.menuRev = 1

	if err := d.setupDBus(); err != nil {
		conn.Close()
		return err
	}

	log.Printf("owlrun: node %s | gpu %s %s (%.0f GB VRAM) | tray active",
		d.nodeID, d.gpuInfo.Vendor, d.gpuInfo.Name, d.gpuInfo.VRAMTotalGB)

	d.run()
	conn.Close()
	return nil
}

func runHeadless(cfg config.Config, dash *dashboard.Server) {
	d := buildDaemon(cfg, dash)

	log.Printf("owlrun: node %s | gpu %s %s (%.0f GB VRAM)",
		d.nodeID, d.gpuInfo.Vendor, d.gpuInfo.Name, d.gpuInfo.VRAMTotalGB)

	d.run()
}

// buildDaemon constructs the shared daemon (tray and headless both use this).
func buildDaemon(cfg config.Config, dash *dashboard.Server) *sniDaemon {
	nodeID := config.EnsureNodeID(&cfg)
	info := gpu.Detect()
	monitor := gpu.NewMonitor(info, 10*time.Second)
	tracker := earnings.New()

	w := wallet.New(cfg.Marketplace.Gateway, cfg.Account.APIKey)

	d := &sniDaemon{
		cfg:        cfg,
		nodeID:     nodeID,
		gpuInfo:    info,
		monitor:    monitor,
		tracker:    tracker,
		ecash:      w,
		ollamaMgr:  inference.New(info),
		dash:       dash,
		jobMode:    cfg.Idle.JobMode,
		iconGreen:  safeIco(assets.IconGreen),
		iconYellow: safeIco(assets.IconYellow),
		iconGrey:   safeIco(assets.IconGrey),
		iconRed:    safeIco(assets.IconRed),
		iconBlue:   safeIco(assets.IconBlue),
	}

	d.gateway = marketplace.New(
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
			tracker.Record(model, tokens, earnedUSD)
		},
		func() {
			d.mu.Lock()
			d.st = linuxEarning
			d.mu.Unlock()
		},
		func(balanceSats int64) {
			d.ecash.AutoClaim(balanceSats)
		},
	)

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
	}

	return d
}

// run starts the monitor and idle loop, then blocks until SIGINT/SIGTERM.
func (d *sniDaemon) run() {
	go d.monitor.Start()
	go d.idleLoop()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	<-sig

	log.Println("owlrun: shutting down")
	d.gateway.Disconnect()
	if err := d.ollamaMgr.Stop(); err != nil {
		log.Printf("owlrun: stop ollama: %v", err)
	}
	d.tracker.Close()
}

// ─── D-Bus setup ──────────────────────────────────────────────────────────────

func (d *sniDaemon) setupDBus() error {
	propsMap := prop.Map{
		sniIface: {
			"Category":            {Value: "ApplicationStatus", Writable: false},
			"Id":                  {Value: "owlrun", Writable: false},
			"Title":               {Value: "Owlrun", Writable: false},
			"Status":              {Value: "Active", Writable: false, Emit: prop.EmitFalse},
			"WindowId":            {Value: uint32(0), Writable: false},
			"IconName":            {Value: "", Writable: false},
			"IconPixmap":          {Value: d.iconYellow, Writable: false, Emit: prop.EmitFalse},
			"OverlayIconName":     {Value: "", Writable: false},
			"OverlayIconPixmap":   {Value: []iconPixmap{}, Writable: false},
			"AttentionIconName":   {Value: "", Writable: false},
			"AttentionIconPixmap": {Value: []iconPixmap{}, Writable: false},
			"AttentionMovieName":  {Value: "", Writable: false},
			"ToolTip": {Value: toolTip{
				Title: "Owlrun",
				Desc:  "Idle — waiting",
			}, Writable: false, Emit: prop.EmitFalse},
			"Menu": {Value: menuPath, Writable: false},
		},
	}

	sniPropsObj, err := prop.Export(d.conn, sniPath, propsMap)
	if err != nil {
		return fmt.Errorf("prop.Export: %w", err)
	}
	d.sniProps = sniPropsObj

	if err := d.conn.Export(&sniItem{d: d}, sniPath, sniIface); err != nil {
		return fmt.Errorf("export SNI methods: %w", err)
	}

	d.menuObj = &dbusMenu{d: d, statusLabel: "◑ Idle", toggleLabel: "Pause"}
	if err := d.conn.Export(d.menuObj, menuPath, menuIface); err != nil {
		return fmt.Errorf("export menu: %w", err)
	}

	svcName := fmt.Sprintf("org.kde.StatusNotifierItem-%d-1", os.Getpid())
	reply, err := d.conn.RequestName(svcName, dbus.NameFlagDoNotQueue)
	if err != nil {
		return fmt.Errorf("RequestName: %w", err)
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		return fmt.Errorf("D-Bus name %s already owned", svcName)
	}

	// Register with the StatusNotifierWatcher (best-effort; may not exist).
	watcher := d.conn.Object("org.kde.StatusNotifierWatcher", "/StatusNotifierWatcher")
	if err := watcher.Call("org.kde.StatusNotifierWatcher.RegisterStatusNotifierItem",
		0, svcName).Err; err != nil {
		log.Printf("owlrun: StatusNotifierWatcher unavailable (%v) — icon may not appear on all DEs", err)
	}

	return nil
}

// applyStateLocked updates the tray icon, tooltip, and menu to match current
// state. No-op in headless mode (conn == nil). Must be called with d.mu held.
func (d *sniDaemon) applyStateLocked() {
	if d.conn == nil {
		return
	}

	var icon []iconPixmap
	var label, desc string

	switch d.st {
	case linuxEarning:
		icon = d.iconGreen
		label = "🟢 Earning"
		desc = "Connected and serving inference"
	case linuxReady, linuxStarting:
		icon = d.iconYellow
		label = "🟡 Getting ready"
		desc = "Setting up Ollama / connecting to gateway"
	case linuxMissingWallet:
		icon = d.iconBlue
		label = "🔵 Wallet not set"
		desc = "Set your Lightning address to start earning"
	case linuxError:
		icon = d.iconRed
		label = "🔴 Error"
		desc = "Something went wrong — check logs"
	case linuxPaused:
		icon = d.iconGrey
		label = "⚪ Paused"
		desc = "Manually paused"
	default: // idle
		icon = d.iconYellow
		label = "🟡 Idle"
		desc = "Waiting for idle conditions"
	}

	d.sniProps.SetMust(sniIface, "IconPixmap", icon)
	d.sniProps.SetMust(sniIface, "ToolTip", toolTip{Title: "Owlrun", Desc: desc})
	_ = d.conn.Emit(sniPath, sniIface+".NewIcon")
	_ = d.conn.Emit(sniPath, sniIface+".NewToolTip")

	d.menuObj.statusLabel = label
	if d.manuallyPaused {
		d.menuObj.toggleLabel = "Resume"
	} else {
		d.menuObj.toggleLabel = "Pause"
	}
	d.menuObj.jobMode = d.jobMode
	d.menuRev++
	_ = d.conn.Emit(menuPath, menuIface+".LayoutUpdated", d.menuRev, int32(0))
}

// ─── SNI method object ────────────────────────────────────────────────────────

type sniItem struct{ d *sniDaemon }

func (s *sniItem) Activate(x, y int32) *dbus.Error {
	openBrowser("http://localhost:19131")
	return nil
}

func (s *sniItem) SecondaryActivate(x, y int32) *dbus.Error { return nil }
func (s *sniItem) Scroll(delta int32, orientation string) *dbus.Error { return nil }
func (s *sniItem) ContextMenu(x, y int32) *dbus.Error { return nil }

// ─── dbusmenu method object ───────────────────────────────────────────────────

type dbusMenu struct {
	d           *sniDaemon
	statusLabel string
	toggleLabel string
	jobMode     string // synced from sniDaemon on layout build
}

func (m *dbusMenu) GetLayout(parentId, recursionDepth int32, propertyNames []string) (uint32, layoutItem, *dbus.Error) {
	m.d.mu.Lock()
	rev := m.d.menuRev
	sl := m.statusLabel
	tl := m.toggleLabel
	jm := m.jobMode
	snap := m.d.tracker.Get()
	trigMin := m.d.cfg.Idle.TriggerMinutes
	m.d.mu.Unlock()

	sep := func(id int32) dbus.Variant {
		return dbus.MakeVariant(layoutItem{
			ID:    id,
			Props: map[string]dbus.Variant{"type": dbus.MakeVariant("separator")},
		})
	}
	item := func(id int32, label string, enabled bool) dbus.Variant {
		return dbus.MakeVariant(layoutItem{
			ID: id,
			Props: map[string]dbus.Variant{
				"label":   dbus.MakeVariant(label),
				"enabled": dbus.MakeVariant(enabled),
			},
		})
	}
	radioItem := func(id int32, label string, checked bool) dbus.Variant {
		state := int32(0)
		if checked {
			state = 1
		}
		return dbus.MakeVariant(layoutItem{
			ID: id,
			Props: map[string]dbus.Variant{
				"label":        dbus.MakeVariant(label),
				"enabled":      dbus.MakeVariant(true),
				"toggle-type":  dbus.MakeVariant("radio"),
				"toggle-state": dbus.MakeVariant(state),
			},
		})
	}

	// Job mode submenu (ID 20 = parent, 21-23 = children)
	jobModeParent := dbus.MakeVariant(layoutItem{
		ID: 20,
		Props: map[string]dbus.Variant{
			"label":       dbus.MakeVariant("Accept Jobs"),
			"children-display": dbus.MakeVariant("submenu"),
		},
		Children: []dbus.Variant{
			radioItem(21, "Never", jm == "never"),
			radioItem(22, fmt.Sprintf("After idle %dm", trigMin), jm == "idle"),
			radioItem(23, "Always", jm == "always"),
		},
	})

	root := layoutItem{
		ID:    0,
		Props: map[string]dbus.Variant{},
		Children: []dbus.Variant{
			item(1, sl, false),
			sep(2),
			item(3, fmt.Sprintf("Today:  $%.2f", snap.Today), false),
			item(4, fmt.Sprintf("Total:  $%.2f", snap.Total), false),
			sep(5),
			item(6, "Open Dashboard", true),
			item(7, tl, true),
			jobModeParent,
			sep(8),
			item(10, "🟢 Green  — Connected & earning", false),
			item(11, "🟡 Yellow — Getting ready", false),
			item(12, "🔵 Blue   — Wallet not set", false),
			item(13, "🔴 Red    — Error", false),
			item(14, "⚪ Grey   — Paused", false),
			sep(15),
			item(9, "Quit", true),
		},
	}
	return rev, root, nil
}

func (m *dbusMenu) Event(id int32, eventId string, data dbus.Variant, timestamp uint32) *dbus.Error {
	if eventId != "clicked" {
		return nil
	}
	switch id {
	case 6:
		openBrowser("http://localhost:19131")
	case 7:
		m.d.togglePause()
	case 9:
		_ = syscall.Kill(os.Getpid(), syscall.SIGTERM)
	case 21:
		m.d.setJobMode("never")
	case 22:
		m.d.setJobMode("idle")
	case 23:
		m.d.setJobMode("always")
	}
	return nil
}

func (m *dbusMenu) AboutToShow(id int32) (bool, *dbus.Error) { return false, nil }

func (m *dbusMenu) GetGroupProperties(ids []int32, propertyNames []string) ([]menuProps, *dbus.Error) {
	return nil, nil
}

// ─── Daemon logic ─────────────────────────────────────────────────────────────

func (d *sniDaemon) togglePause() {
	d.mu.Lock()
	d.manuallyPaused = !d.manuallyPaused
	if d.manuallyPaused {
		d.st = linuxPaused
		d.starting = false
	} else {
		d.st = linuxIdle
	}
	d.applyStateLocked()
	becamePaused := d.manuallyPaused
	d.mu.Unlock()

	if becamePaused {
		go d.stopEarning()
	}
}

func (d *sniDaemon) setJobMode(mode string) {
	d.mu.Lock()
	old := d.jobMode
	d.jobMode = mode
	wasEarning := d.st == linuxEarning || d.st == linuxReady || d.st == linuxMissingWallet
	if mode == "never" && wasEarning {
		d.st = linuxIdle
	}
	d.applyStateLocked()
	d.mu.Unlock()
	log.Printf("owlrun: job mode changed: %s → %s", old, mode)
	if mode == "never" && wasEarning {
		go d.stopEarning()
	}
}

func (d *sniDaemon) idleLoop() {
	d.check()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		d.check()
	}
}

func (d *sniDaemon) check() {
	d.mu.Lock()
	paused := d.manuallyPaused
	st := d.st
	mode := d.jobMode
	d.mu.Unlock()

	if paused {
		return
	}

	if mode == "never" {
		return
	}

	if st == linuxEarning || st == linuxReady || st == linuxMissingWallet {
		if mode == "always" {
			return
		}
		userBack := idle.IdleDuration() < time.Duration(d.cfg.Idle.TriggerMinutes)*time.Minute
		gameRunning := d.cfg.Idle.WatchProcesses && idle.IsGameRunning()
		if userBack || gameRunning {
			d.mu.Lock()
			d.st = linuxIdle
			d.applyStateLocked()
			d.mu.Unlock()
			go d.stopEarning()
		}
		return
	}

	shouldStart := mode == "always" || idle.IsSystemIdle(d.cfg.Idle, d.monitor.UtilizationPct())

	d.mu.Lock()
	defer d.mu.Unlock()

	if shouldStart && d.st == linuxIdle && !d.starting {
		d.starting = true
		go d.startEarning()
	}
}

func (d *sniDaemon) startEarning() {
	// Phase 1: get Ollama running.
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
			d.starting = false
			d.st = linuxError
			d.applyStateLocked()
			d.mu.Unlock()
			return
		}
	}

	// Phase 2: select models — all installed that fit, best first.
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
			d.starting = false
			d.st = linuxError
			d.applyStateLocked()
			d.mu.Unlock()
			return
		}
		log.Printf("owlrun: found %d installed models, primary: %s", len(models), models[0])
	}
	model := models[0]

	d.mu.Lock()
	d.model = model
	d.starting = false
	if config.NeedsWallet(&d.cfg) {
		d.st = linuxMissingWallet
	} else {
		d.st = linuxReady
	}
	d.applyStateLocked()
	d.mu.Unlock()

	d.gateway.SetModels(models)
	d.gateway.Connect()
	log.Printf("owlrun: ready — connecting to gateway")
}

func (d *sniDaemon) loadOrPull(model string) error {
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

func (d *sniDaemon) stopEarning() {
	d.gateway.Disconnect()
	if err := d.ollamaMgr.Stop(); err != nil {
		log.Printf("owlrun: stop ollama: %v", err)
	}
}

func (d *sniDaemon) statusSnapshot() dashboard.Status {
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
		s.Wallet.Warning = "Set your Lightning address in the Wallet section to start earning Bitcoin."
	} else if d.cfg.Account.LightningAddress != "" {
		s.Wallet.Configured = "Wallet configured at " + d.cfg.Account.LightningAddress
	}
	switch st {
	case linuxEarning:
		s.State = "earning"
	case linuxReady, linuxStarting:
		s.State = "ready"
	case linuxMissingWallet:
		s.State = "wallet"
	case linuxError:
		s.State = "error"
	case linuxPaused:
		s.State = "paused"
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
	s.Models = gwStats.Models
	s.Gateway.Connected = gwStats.Connected
	s.Gateway.GatewayStatus = gwStats.Status
	s.Gateway.JobsToday = gwStats.JobsToday
	s.Gateway.TokensToday = gwStats.TokensToday
	s.Gateway.EarnedTodayUSD = gwStats.EarnedTodayUSD
	s.Gateway.QueueDepthGlobal = gwStats.QueueDepthGlobal
	s.LightningAddress = d.cfg.Account.LightningAddress
	s.RedeemThreshold = d.cfg.Account.RedeemThreshold

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
		s.SatsWallet = dashboard.SatsWalletInfo{
			GatewaySats:  ws.GatewaySats,
			LocalSats:    ws.LocalSats,
			TotalSats:    ws.TotalSats,
			USDApprox:    ws.USDApprox,
			ProofCount:   ws.ProofCount,
			LastClaim:    ws.LastClaim,
			LastToken:    ws.LastToken,
			TokenHistory: hist,
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

// ─── ICO → ARGB32 ────────────────────────────────────────────────────────────

// safeIco calls icoToArgb32 and falls back to an empty slice so the D-Bus
// property type signature stays consistent ([]iconPixmap, never nil).
func safeIco(data []byte) []iconPixmap {
	if p := icoToArgb32(data); p != nil {
		return p
	}
	return []iconPixmap{}
}

// icoToArgb32 parses a Windows ICO file and returns the largest image as
// ARGB32 pixel data for the SNI IconPixmap property. Handles both
// PNG-in-ICO (modern) and BMP/DIB-in-ICO (classic) formats.
func icoToArgb32(data []byte) []iconPixmap {
	if len(data) < 6 {
		return nil
	}
	count := int(binary.LittleEndian.Uint16(data[4:6]))
	if count == 0 {
		return nil
	}

	var bestData []byte
	var bestArea int

	for i := 0; i < count; i++ {
		base := 6 + i*16
		if base+16 > len(data) {
			break
		}
		e := data[base:]
		w := int(e[0])
		if w == 0 {
			w = 256
		}
		h := int(e[1])
		if h == 0 {
			h = 256
		}
		length := int(binary.LittleEndian.Uint32(e[8:12]))
		offset := int(binary.LittleEndian.Uint32(e[12:16]))
		if offset+length > len(data) || length < 8 {
			continue
		}
		if w*h > bestArea {
			bestData = data[offset : offset+length]
			bestArea = w * h
		}
	}

	if bestData == nil {
		return nil
	}

	// PNG-in-ICO (modern format).
	if bytes.HasPrefix(bestData, []byte("\x89PNG\r\n\x1a\n")) {
		decoded, err := png.Decode(bytes.NewReader(bestData))
		if err != nil {
			return nil
		}
		b := decoded.Bounds()
		w := b.Max.X - b.Min.X
		h := b.Max.Y - b.Min.Y
		argb := make([]byte, w*h*4)
		for y := b.Min.Y; y < b.Max.Y; y++ {
			for x := b.Min.X; x < b.Max.X; x++ {
				r32, g32, b32, a32 := decoded.At(x, y).RGBA()
				idx := ((y-b.Min.Y)*w + (x-b.Min.X)) * 4
				argb[idx+0] = byte(a32 >> 8)
				argb[idx+1] = byte(r32 >> 8)
				argb[idx+2] = byte(g32 >> 8)
				argb[idx+3] = byte(b32 >> 8)
			}
		}
		return []iconPixmap{{Width: int32(w), Height: int32(h), Data: argb}}
	}

	// BMP/DIB-in-ICO (classic format). Header is BITMAPINFOHEADER (40 bytes).
	if len(bestData) < 40 {
		return nil
	}
	dibW := int(int32(binary.LittleEndian.Uint32(bestData[4:8])))
	dibH := int(int32(binary.LittleEndian.Uint32(bestData[8:12]))) / 2 // biHeight includes AND mask
	bpp := int(binary.LittleEndian.Uint16(bestData[14:16]))
	if dibW <= 0 || dibH <= 0 {
		return nil
	}

	// Color table size: only present for ≤8bpp.
	colorTableSize := 0
	if bpp <= 8 {
		colorTableSize = (1 << bpp) * 4
	}
	pixelOffset := 40 + colorTableSize
	rowBytes := (dibW*bpp + 31) / 32 * 4 // each row padded to 4-byte boundary

	if pixelOffset+rowBytes*dibH > len(bestData) {
		return nil
	}

	argb := make([]byte, dibW*dibH*4)
	for row := 0; row < dibH; row++ {
		// DIB rows are stored bottom-up.
		srcRow := bestData[pixelOffset+(dibH-1-row)*rowBytes:]
		dstOff := row * dibW * 4

		switch bpp {
		case 32: // BGRA
			for x := 0; x < dibW; x++ {
				argb[dstOff+x*4+0] = srcRow[x*4+3] // A
				argb[dstOff+x*4+1] = srcRow[x*4+2] // R
				argb[dstOff+x*4+2] = srcRow[x*4+1] // G
				argb[dstOff+x*4+3] = srcRow[x*4+0] // B
			}
		case 24: // BGR, no alpha
			for x := 0; x < dibW; x++ {
				b := srcRow[x*3+0]
				g := srcRow[x*3+1]
				r := srcRow[x*3+2]
				argb[dstOff+x*4+0] = 0xff
				argb[dstOff+x*4+1] = r
				argb[dstOff+x*4+2] = g
				argb[dstOff+x*4+3] = b
			}
		default:
			return nil // 1/4/8bpp palettized — not worth implementing for tray icons
		}
	}
	// Old-style 32bpp ICOs store alpha=0 everywhere and use the AND mask for
	// transparency instead. Detect this by checking if all alpha bytes are zero;
	// if so, set all pixels fully opaque (the icon outline is still correct).
	allZeroAlpha := true
	for i := 0; i < len(argb); i += 4 {
		if argb[i] != 0 {
			allZeroAlpha = false
			break
		}
	}
	if allZeroAlpha {
		for i := 0; i < len(argb); i += 4 {
			argb[i] = 0xff
		}
	}

	return []iconPixmap{{Width: int32(dibW), Height: int32(dibH), Data: argb}}
}

