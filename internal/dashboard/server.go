// Package dashboard serves a local web UI on localhost:19131 with live GPU
// stats, earnings, and marketplace status. All data is read-only — the
// dashboard displays state, it never changes it.
package dashboard

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/fabgoodvibes/owlrun/internal/buildinfo"
	"github.com/fabgoodvibes/owlrun/internal/cashu"
	"github.com/fabgoodvibes/owlrun/internal/earnings"
)

// Status is the full snapshot returned by GET /api/status.
type Status struct {
	NodeID  string `json:"node_id"`
	Version string `json:"version"`
	Network string `json:"network"` // "beta" | "production"
	State   string `json:"state"`   // "earning" | "idle" | "paused"

	Wallet struct {
		Address    string `json:"address"`
		Warning    string `json:"warning,omitempty"`    // non-empty = user needs to set wallet
		Configured string `json:"configured,omitempty"` // non-empty = wallet configured, show green banner
	} `json:"wallet"`

	GPU struct {
		Name        string  `json:"name"`
		Vendor      string  `json:"vendor"`
		VRAMTotalMB int     `json:"vram_total_mb"`
		UtilPct     int     `json:"util_pct"`
		VRAMFreeMB  int     `json:"vram_free_mb"`
		TempC       int     `json:"temp_c"`
		PowerW      float64 `json:"power_w"`
		VRAMExact   bool    `json:"vram_exact"`
	} `json:"gpu"`

	Model        string              `json:"model"`
	ModelPricing *ModelPricingInfo    `json:"model_pricing,omitempty"`

	Earnings struct {
		TodayUSD float64 `json:"today_usd"`
		TotalUSD float64 `json:"total_usd"`
	} `json:"earnings"`

	Gateway struct {
		Connected        bool    `json:"connected"`
		GatewayStatus    string  `json:"status"`
		JobsToday        int     `json:"jobs_today"`
		TokensToday      int     `json:"tokens_today"`
		EarnedTodayUSD   float64 `json:"earned_today_usd"`
		QueueDepthGlobal int     `json:"queue_depth_global"`
	} `json:"gateway"`

	Disk struct {
		Path    string  `json:"path"`
		TotalGB float64 `json:"total_gb"`
		FreeGB  float64 `json:"free_gb"`
		FreePct float64 `json:"free_pct"`
	} `json:"disk"`

	LightningAddress string         `json:"lightning_address"`
	RedeemThreshold  int            `json:"redeem_threshold"`
	BtcPrice         BtcPriceInfo   `json:"btc_price"`
	Broadcasts       []BroadcastMsg `json:"broadcasts"`
	SatsWallet       SatsWalletInfo `json:"sats_wallet"`
}

// ModelPricingInfo holds per-model pricing from the gateway.
type ModelPricingInfo struct {
	PerMInputUSD  float64 `json:"per_m_input_usd"`
	PerMOutputUSD float64 `json:"per_m_output_usd"`
}

// BtcPriceInfo is the BTC/USD pricing snapshot for the dashboard.
type BtcPriceInfo struct {
	LiveUsd      float64 `json:"live_usd"`
	YesterdayFix float64 `json:"yesterday_fix"`
	DailyAvg     float64 `json:"daily_avg"`
	WeeklyAvg    float64 `json:"weekly_avg"`
	Status       string  `json:"status"`
}

// BroadcastMsg is a gateway notification displayed on the dashboard.
type BroadcastMsg struct {
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
}

// SatsWalletInfo is the provider's ecash wallet state for the dashboard.
type SatsWalletInfo struct {
	GatewaySats  int64              `json:"gateway_sats"`  // unclaimed on gateway
	LocalSats    int64              `json:"local_sats"`    // claimed proofs stored locally
	TotalSats    int64              `json:"total_sats"`    // gateway + local
	USDApprox    float64            `json:"usd_approx"`    // approximate USD value
	ProofCount   int                `json:"proof_count"`   // number of local proofs
	LastClaim    string             `json:"last_claim"`    // ISO timestamp
	LastToken    string             `json:"last_token"`    // most recent cashuA token (for QR)
	TokenHistory []TokenHistoryItem `json:"token_history"` // last N tokens
}

// TokenHistoryItem is a claimed ecash token with metadata for the dashboard.
type TokenHistoryItem struct {
	Token     string `json:"token"`
	Sats      uint64 `json:"sats"`
	ClaimedAt string `json:"claimed_at"`
}

// StatusProvider is a function that returns the current status snapshot.
// Set via SetProvider after the tray initialises its subsystems.
type StatusProvider func() Status

// ClaimFunc is called by the dashboard to claim ecash from the gateway.
// amountSats 0 = claim all. Returns the cashuA... token string.
type ClaimFunc func(amountSats int64) (token string, err error)

// SetLightningAddressFunc saves a Lightning address and re-registers with gateway.
type SetLightningAddressFunc func(addr string) error

// SetRedeemThresholdFunc saves a redeem threshold and re-registers with gateway.
type SetRedeemThresholdFunc func(threshold int) error

// Server is the embedded web dashboard.
type Server struct {
	port     int
	provider atomic.Pointer[StatusProvider]
	tracker  atomic.Pointer[earnings.Tracker]
	claimer  atomic.Pointer[ClaimFunc]
	setLnAddr      atomic.Pointer[SetLightningAddressFunc]
	setRedeemThr   atomic.Pointer[SetRedeemThresholdFunc]
}

// New creates a dashboard Server on the given port.
func New(port int) *Server {
	if port == 0 {
		port = 19131
	}
	return &Server{port: port}
}

// SetProvider wires the live data source into the dashboard.
// Called by the tray once subsystems are initialised.
func (s *Server) SetProvider(p StatusProvider) {
	s.provider.Store(&p)
}

// SetTracker wires the earnings database for history chart queries.
func (s *Server) SetTracker(t *earnings.Tracker) {
	s.tracker.Store(t)
}

// SetClaimer wires the ecash claim function into the dashboard.
func (s *Server) SetClaimer(c ClaimFunc) {
	s.claimer.Store(&c)
}

// SetLightningAddressSetter wires the Lightning address save function.
func (s *Server) SetLightningAddressSetter(fn SetLightningAddressFunc) {
	s.setLnAddr.Store(&fn)
}

// SetRedeemThresholdSetter wires the redeem threshold save function.
func (s *Server) SetRedeemThresholdSetter(fn SetRedeemThresholdFunc) {
	s.setRedeemThr.Store(&fn)
}

// Start launches the HTTP server in the background.
// The listener is bound before returning so the port is ready for connections.
func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/history", s.handleHistory)
	mux.HandleFunc("/api/claim-ecash", s.handleClaimEcash)
	mux.HandleFunc("/api/set-lightning-address", s.handleSetLightningAddress)
	mux.HandleFunc("/api/set-redeem-threshold", s.handleSetRedeemThreshold)
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", s.port))
	if err != nil {
		return err
	}
	go http.Serve(ln, mux) //nolint:errcheck
	return nil
}

func (s *Server) getStatus() Status {
	p := s.provider.Load()
	if p == nil {
		return Status{State: "starting", Version: buildinfo.Version}
	}
	return (*p)()
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	json.NewEncoder(w).Encode(s.getStatus())
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	t := s.tracker.Load()
	if t == nil {
		json.NewEncoder(w).Encode(map[string]any{"period": "", "buckets": []any{}})
		return
	}

	period := r.URL.Query().Get("period")
	switch period {
	case "24h", "7d", "30d", "1y":
	default:
		period = "24h"
	}

	buckets := t.History(period)
	if buckets == nil {
		buckets = []earnings.HistoryBucket{}
	}
	json.NewEncoder(w).Encode(map[string]any{
		"period":  period,
		"buckets": buckets,
	})
}

func (s *Server) handleClaimEcash(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]string{"error": "POST required"})
		return
	}

	c := s.claimer.Load()
	if c == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "wallet not ready"})
		return
	}

	var req struct {
		AmountSats int64 `json:"amount_sats"`
	}
	json.NewDecoder(r.Body).Decode(&req)

	token, err := (*c)(req.AmountSats)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	// Parse token to get actual amount (may differ from requested due to power-of-2 denominations).
	var amountSats uint64
	if parsed, err := cashu.Deserialize(token); err == nil {
		amountSats = parsed.TotalSats()
	}
	json.NewEncoder(w).Encode(map[string]any{"token": token, "amount_sats": amountSats})
}

func (s *Server) handleSetLightningAddress(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]string{"error": "POST required"})
		return
	}

	fn := s.setLnAddr.Load()
	if fn == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "not ready"})
		return
	}

	var req struct {
		Address string `json:"address"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Address == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "address is required"})
		return
	}

	// Basic Lightning address validation: must contain @
	if !strings.Contains(req.Address, "@") {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid Lightning address — expected format: user@domain"})
		return
	}

	if err := (*fn)(req.Address); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "address": req.Address})
}

func (s *Server) handleSetRedeemThreshold(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]string{"error": "POST required"})
		return
	}

	fn := s.setRedeemThr.Load()
	if fn == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "not ready"})
		return
	}

	var req struct {
		Threshold int `json:"threshold"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Threshold < 50 || req.Threshold > 1000 {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "threshold must be between 50 and 1000"})
		return
	}

	if err := (*fn)(req.Threshold); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	json.NewEncoder(w).Encode(map[string]any{"status": "ok", "threshold": req.Threshold})
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, dashboardHTML)
}

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Owlrun Dashboard</title>
<style>
  * { box-sizing: border-box; margin: 0; padding: 0; }
  body { background: #0f0f13; color: #e8e8f0; font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; font-size: 17px; padding: 28px 36px; }
  h1 { font-size: 26px; font-weight: 600; margin-bottom: 22px; color: #fff; letter-spacing: -0.3px; }
  h1 span { opacity: 0.6; font-weight: 400; font-size: 16px; margin-left: 8px; }
  .grid { display: grid; grid-template-columns: 1fr 1fr 1fr 1fr; grid-template-rows: auto auto; gap: 14px; }
  .card { background: #1a1a24; border: 1px solid #2a2a38; border-radius: 12px; padding: 20px; }
  .card-title { font-size: 14px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.8px; color: #aaaac0; margin-bottom: 16px; }
  .card-wallet { grid-row: 1 / 3; }
  .card-wide { grid-column: 1 / -1; }
  .card-notify { margin-bottom: 14px; }
  @media (max-width: 900px) { .grid { grid-template-columns: 1fr 1fr; } .card-wallet { grid-row: auto; } }
  @media (max-width: 550px) { .grid { grid-template-columns: 1fr; } .card-wallet { grid-row: auto; } }
  .stat { display: flex; justify-content: space-between; align-items: center; margin-bottom: 10px; min-height: 26px; }
  .stat:last-child { margin-bottom: 0; }
  .stat-label { color: #d0d0e0; font-size: 16px; }
  .stat-value { font-weight: 500; color: #ededf5; font-variant-numeric: tabular-nums; font-size: 16px; }
  .state-badge { display: inline-flex; align-items: center; gap: 8px; font-weight: 600; font-size: 18px; }
  .dot { width: 11px; height: 11px; border-radius: 50%; flex-shrink: 0; }
  .dot-green  { background: #22c55e; box-shadow: 0 0 8px #22c55e88; }
  .dot-yellow { background: #eab308; box-shadow: 0 0 8px #eab30888; }
  .dot-grey   { background: #6b7280; }
  .dot-blue   { background: #3b82f6; box-shadow: 0 0 8px #3b82f688; }
  .dot-red    { background: #ef4444; box-shadow: 0 0 8px #ef444488; }
  .bar-wrap { background: #2a2a38; border-radius: 4px; height: 7px; width: 110px; overflow: hidden; }
  .bar-fill { height: 100%; border-radius: 4px; transition: width 0.4s ease; }
  .bar-green  { background: #22c55e; }
  .bar-yellow { background: #eab308; }
  .bar-red    { background: #ef4444; }
  .earnings-big { font-size: 36px; font-weight: 700; color: #22c55e; font-variant-numeric: tabular-nums; margin-bottom: 4px; }
  .earnings-sub { font-size: 15px; color: #aaaabb; }
  .node-id { font-size: 14px; color: #9999b0; font-family: monospace; margin-top: 6px; }
  .connected { color: #22c55e; }
  .disconnected { color: #ef4444; }
  #updated { position: fixed; bottom: 16px; right: 24px; font-size: 13px; color: #888; }
  .charts-section { margin-top: 28px; padding-bottom: 44px; }
  .tab-bar { display: flex; gap: 0; margin-bottom: 18px; }
  .tab-bar button { background: #1a1a24; border: 1px solid #2a2a38; color: #d0d0e0; padding: 10px 20px; font-size: 15px; font-weight: 600; cursor: pointer; transition: all 0.2s; }
  .tab-bar button:first-child { border-radius: 6px 0 0 6px; }
  .tab-bar button:last-child { border-radius: 0 6px 6px 0; }
  .tab-bar button.active { background: #2a2a38; color: #f0f0f5; border-color: #3a3a4a; }
  .chart-grid { display: grid; grid-template-columns: 1fr; gap: 18px; }
  .chart-card { background: #1a1a24; border: 1px solid #2a2a38; border-radius: 12px; padding: 20px; }
  .chart-card .card-title { font-size: 14px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.8px; color: #aaaac0; margin-bottom: 14px; }
  .wallet-warn { background: #2d1f00; border: 1px solid #b45309; border-radius: 8px; padding: 16px 20px; margin-bottom: 18px; display: none; }
  .wallet-warn .warn-title { color: #f59e0b; font-weight: 600; font-size: 15px; margin-bottom: 4px; }
  .wallet-warn .warn-body { color: #e0b060; font-size: 14px; line-height: 1.5; }
  .wallet-warn.configured { background: #0d2818; border-color: #16a34a; }
  .wallet-warn.configured .warn-title { color: #4ade80; }
  .wallet-warn.configured .warn-body { color: #86efac; }
  .wallet-warn code { background: #1a1a24; padding: 2px 6px; border-radius: 4px; font-size: 13px; color: #e2e2e8; }
  .network-badge { display: inline-block; background: #b45309; color: #fff; font-size: 11px; font-weight: 600; padding: 3px 9px; border-radius: 4px; margin-left: 8px; text-transform: uppercase; vertical-align: middle; }
  .broadcast-empty { color: #9999b0; font-size: 15px; font-style: italic; padding: 8px 0; }
  .broadcast-item { display: flex; justify-content: space-between; align-items: flex-start; gap: 14px; padding: 12px 0; border-bottom: 1px solid #2a2a38; }
  .broadcast-item:last-child { border-bottom: none; }
  .broadcast-msg { color: #e8e8f0; font-size: 16px; flex: 1; }
  .broadcast-time { color: #aaaabb; font-size: 14px; white-space: nowrap; font-variant-numeric: tabular-nums; }
  .legend-row { display: inline-flex; align-items: center; gap: 6px; margin-right: 18px; font-size: 14px; color: #aaaabb; }
</style>
</head>
<body>
<h1>🦉 Owlrun <span id="version"></span><span id="network-badge" class="network-badge" style="display:none"></span></h1>
<div id="wallet-warn" class="wallet-warn">
  <div class="warn-title">Wallet not configured</div>
  <div class="warn-body" id="wallet-warn-body"></div>
</div>
<!-- ═══ Notifications (full width, above grid) ═══ -->
<div class="card card-notify" id="notify-card">
  <div class="card-title">Notifications</div>
  <div id="broadcasts">
    <div class="broadcast-empty">Gateway notifications will appear here.</div>
  </div>
</div>

<div class="grid">

  <!-- ═══ Wallet (col 1, spans 2 rows) ═══ -->
  <div class="card card-wallet" id="wallet-card">
    <div class="card-title">Wallet</div>
    <div id="wallet-setup" style="display:none">
      <div style="text-align:center;padding:8px 0 16px">
        <div style="font-size:28px;margin-bottom:8px">&#9889;</div>
        <div style="font-size:16px;color:#d0d0e0;font-weight:600;margin-bottom:12px">Set up your wallet to get paid</div>
        <div style="text-align:left;font-size:14px;color:#aaaabb;line-height:1.8">
          <div style="margin-bottom:8px"><span style="color:#f7931a;font-weight:600">1.</span> Install <a href="https://www.minibits.cash/" target="_blank" style="color:#f7931a;text-decoration:underline">Minibits</a> wallet (by Bitango Technologies)</div>
          <div style="margin-bottom:8px"><span style="color:#f7931a;font-weight:600">2.</span> Find your Lightning address in Minibits (looks like <code style="background:#0f0f13;padding:2px 6px;border-radius:4px;font-size:13px;color:#f7931a">you@minibits.cash</code>)</div>
          <div><span style="color:#f7931a;font-weight:600">3.</span> Paste it below</div>
        </div>
      </div>
      <div style="margin-top:12px">
        <input id="ln-address-input" type="text" placeholder="yourname@minibits.cash" style="width:100%;padding:12px 14px;background:#0f0f13;color:#f7931a;border:1px solid #3a3a48;border-radius:8px;font-size:15px;font-family:monospace;box-sizing:border-box" />
        <button id="btn-save-ln" onclick="saveLightningAddress()" style="width:100%;margin-top:8px;padding:12px 16px;background:#f7931a;color:#fff;border:none;border-radius:8px;cursor:pointer;font-weight:600;font-size:15px">Save &amp; Start Earning</button>
        <div id="ln-save-error" style="display:none;color:#ef4444;font-size:13px;margin-top:6px;text-align:center"></div>
      </div>
      <div style="margin-top:14px;font-size:13px;color:#666;text-align:center">
        Works with any Lightning wallet — Minibits, Phoenix, Wallet of Satoshi, etc.
      </div>
    </div>
    <div id="wallet-active" style="display:none">
      <div class="stat">
        <span class="stat-label">Lightning address</span>
        <span class="stat-value" id="ln-address-display" style="color:#f7931a;font-family:monospace;font-size:14px;max-width:200px;text-align:right;word-break:break-all"></span>
      </div>
      <div style="margin-top:10px">
        <button onclick="toggleEditLnAddress()" style="padding:6px 14px;background:#2a2a38;color:#d0d0e0;border:1px solid #3a3a48;border-radius:6px;cursor:pointer;font-size:13px">Change address</button>
      </div>
      <div id="edit-ln-section" style="display:none;margin-top:10px">
        <input id="ln-address-edit" type="text" style="width:100%;padding:10px 12px;background:#0f0f13;color:#f7931a;border:1px solid #3a3a48;border-radius:8px;font-size:14px;font-family:monospace;box-sizing:border-box" />
        <div style="display:flex;gap:6px;margin-top:6px">
          <button onclick="saveLightningAddressEdit()" style="flex:1;padding:8px 12px;background:#f7931a;color:#fff;border:none;border-radius:6px;cursor:pointer;font-weight:600;font-size:13px">Save</button>
          <button onclick="toggleEditLnAddress()" style="padding:8px 12px;background:#2a2a38;color:#d0d0e0;border:1px solid #3a3a48;border-radius:6px;cursor:pointer;font-size:13px">Cancel</button>
        </div>
        <div id="ln-edit-error" style="display:none;color:#ef4444;font-size:13px;margin-top:6px"></div>
      </div>

      <!-- Payout threshold slider -->
      <div style="margin-top:16px;padding-top:14px;border-top:1px solid #2a2a38">
        <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:6px">
          <span style="font-size:14px;color:#d0d0e0">Payout threshold</span>
          <span id="threshold-value" style="font-size:14px;color:#f7931a;font-weight:600">500 sats</span>
        </div>
        <input id="threshold-slider" type="range" min="100" max="1000" step="50" value="500" oninput="updateThresholdDisplay(this.value)" onchange="saveRedeemThreshold(this.value)" style="width:100%;accent-color:#f7931a;cursor:pointer" />
        <div style="display:flex;justify-content:space-between;font-size:12px;color:#888;margin-top:2px">
          <span>100</span><span>500</span><span>1000</span>
        </div>
        <div style="font-size:13px;color:#aaaabb;margin-top:6px" id="threshold-hint">Lower = faster payouts, higher fees. Higher = slower payouts, lower fees.</div>
        <div style="font-size:13px;margin-top:4px">
          <span style="color:#aaaabb">Est. Lightning fee: </span>
          <span id="fee-estimate" style="color:#f7931a;font-weight:600">~1%</span>
        </div>
        <div style="margin-top:8px">
          <label style="display:flex;align-items:center;gap:8px;cursor:pointer;font-size:13px;color:#888">
            <input type="checkbox" id="unlock-50" onchange="toggleLowThreshold(this.checked)" style="accent-color:#f7931a" />
            Unlock 50 sat minimum
          </label>
          <div id="low-threshold-warn" style="display:none;font-size:12px;color:#eab308;margin-top:4px;margin-left:24px">&#9888; ~10% eaten by Lightning fees at this level</div>
        </div>
      </div>

      <!-- Earnings stats -->
      <div style="margin-top:16px;padding-top:14px;border-top:1px solid #2a2a38">
        <div class="stat">
          <span class="stat-label">Pending earnings</span>
          <span class="stat-value" id="sats-gateway" style="color:#f7931a;font-weight:bold">0 sats</span>
        </div>
        <div class="stat">
          <span class="stat-label">USD approx</span>
          <span class="stat-value" id="sats-usd">$0.00</span>
        </div>
        <div class="stat">
          <span class="stat-label">Today's earnings</span>
          <span class="stat-value" id="wallet-today-sats" style="color:#22c55e">0 sats</span>
        </div>
        <div class="stat">
          <span class="stat-label">Total withdrawn</span>
          <span class="stat-value" id="wallet-withdrawn">0 sats</span>
        </div>
        <div class="stat">
          <span class="stat-label">Next payout est.</span>
          <span class="stat-value" id="wallet-next-payout" style="font-size:14px;color:#aaaabb">—</span>
        </div>
      </div>

      <!-- Fee disclaimer -->
      <div style="margin-top:12px;font-size:12px;color:#888;line-height:1.5;padding:10px 12px;background:#0f0f13;border-radius:6px">
        Lightning fees go to the Bitcoin network, not Owlrun. Our fee is always under 10%.
      </div>

      <!-- Advanced: ecash -->
      <div style="margin-top:14px">
        <details>
          <summary style="cursor:pointer;font-size:14px;color:#aaaabb;user-select:none">&#9656; Advanced: Withdraw as ecash (QR)</summary>
          <div style="margin-top:12px;padding:12px;background:#0f0f13;border-radius:8px">
            <div class="stat">
              <span class="stat-label">Local ecash</span>
              <span class="stat-value" id="ecash-local-sats" style="color:#f7931a">0 sats</span>
            </div>
            <div class="stat">
              <span class="stat-label">Proofs</span>
              <span class="stat-value" id="ecash-proof-count">0</span>
            </div>
            <div style="margin-top:8px">
              <button id="btn-claim" onclick="claimEcash()" style="padding:8px 16px;background:#f7931a;color:#fff;border:none;border-radius:6px;cursor:pointer;font-weight:600;font-size:13px">Claim All</button>
            </div>
            <div id="claim-result" style="display:none;margin-top:10px">
              <textarea id="claim-token" readonly style="width:100%;height:60px;background:#1a1a24;color:#e8e8f0;border:1px solid #2a2a38;border-radius:6px;padding:8px;font-size:12px;font-family:monospace;resize:vertical;box-sizing:border-box"></textarea>
              <div style="display:flex;justify-content:space-between;align-items:center;margin-top:4px">
                <span id="claim-amount" style="font-size:13px;color:#aaaabb"></span>
                <button onclick="copyToken()" style="padding:4px 10px;background:#2a2a38;color:#d0d0e0;border:1px solid #3a3a48;border-radius:4px;cursor:pointer;font-size:12px">Copy</button>
              </div>
            </div>
            <div id="ecash-token-history" style="margin-top:10px"></div>
          </div>
        </details>
      </div>

      <div style="font-size:13px;color:#aaaabb;margin-top:10px;padding-top:10px;border-top:1px solid #2a2a38">
        Earnings auto-sent to your Lightning wallet. No action needed.
      </div>
    </div>
  </div>

  <!-- ═══ Row 1 right: Status, Earnings, Notifications ═══ -->
  <div class="card">
    <div class="card-title">Status</div>
    <div id="state-badge" class="state-badge">—</div>
    <div class="node-id" id="node-id"></div>
    <div style="margin-top:10px;border-top:1px solid #2a2a38;padding-top:10px">
      <div class="stat">
        <span class="stat-label">Model</span>
        <span class="stat-value" id="model" style="max-width:140px;text-align:right">—</span>
      </div>
      <div class="stat" id="model-pricing-row" style="display:none;margin-top:4px">
        <span class="stat-label">Rate</span>
        <span class="stat-value" id="model-pricing" style="font-size:11px;color:#8b8b9e">—</span>
      </div>
    </div>
    <div style="margin-top:8px;padding-top:8px;border-top:1px solid #2a2a38;display:flex;flex-wrap:wrap;gap:3px 0">
      <span class="legend-row"><span class="dot dot-green"></span>Earning</span>
      <span class="legend-row"><span class="dot dot-yellow"></span>Ready</span>
      <span class="legend-row"><span class="dot dot-blue"></span>No wallet</span>
      <span class="legend-row"><span class="dot dot-red"></span>Error</span>
      <span class="legend-row"><span class="dot dot-grey"></span>Paused</span>
    </div>
  </div>

  <div class="card">
    <div class="card-title">Earnings</div>
    <div class="earnings-big" id="today">$0.00</div>
    <div class="earnings-sub">today</div>
    <div style="margin-top:10px" class="stat">
      <span class="stat-label">All time</span>
      <span class="stat-value" id="total">$0.00</span>
    </div>
  </div>

  <div class="card">
    <div class="card-title">Bitcoin Price</div>
    <div class="stat">
      <span class="stat-label">Live</span>
      <span class="stat-value" id="btc-live">—</span>
    </div>
    <div class="stat">
      <span class="stat-label">Yesterday's Fix</span>
      <span class="stat-value" id="btc-owlrun">—</span>
    </div>
    <div class="stat">
      <span class="stat-label">24h Avg</span>
      <span class="stat-value" id="btc-daily">—</span>
    </div>
    <div class="stat">
      <span class="stat-label">7d Avg</span>
      <span class="stat-value" id="btc-weekly">—</span>
    </div>
    <div class="stat">
      <span class="stat-label">Status</span>
      <span class="stat-value" id="btc-status">—</span>
    </div>
  </div>

  <!-- ═══ Row 2 right: Gateway, GPU, Disk ═══ -->
  <div class="card">
    <div class="card-title">Gateway</div>
    <div class="stat">
      <span class="stat-label">Connection</span>
      <span class="stat-value" id="gw-connected">—</span>
    </div>
    <div class="stat">
      <span class="stat-label">Jobs today</span>
      <span class="stat-value" id="gw-jobs">—</span>
    </div>
    <div class="stat">
      <span class="stat-label">Tokens today</span>
      <span class="stat-value" id="gw-tokens">—</span>
    </div>
    <div class="stat">
      <span class="stat-label">Queue depth</span>
      <span class="stat-value" id="gw-queue">—</span>
    </div>
  </div>

  <div class="card">
    <div class="card-title">GPU</div>
    <div class="stat"><span class="stat-label" id="gpu-name" style="color:#d0d0e0;font-size:15px"></span></div>
    <div class="stat">
      <span class="stat-label">Utilisation</span>
      <span class="stat-value" style="display:flex;align-items:center;gap:8px">
        <span id="util-pct">—</span>
        <div class="bar-wrap"><div class="bar-fill bar-green" id="util-bar" style="width:0%"></div></div>
      </span>
    </div>
    <div class="stat">
      <span class="stat-label">VRAM free</span>
      <span class="stat-value" id="vram-free">—</span>
    </div>
    <div class="stat">
      <span class="stat-label">Temperature</span>
      <span class="stat-value" id="temp">—</span>
    </div>
    <div class="stat">
      <span class="stat-label">Power draw</span>
      <span class="stat-value" id="power">—</span>
    </div>
  </div>

  <div class="card">
    <div class="card-title">Disk</div>
    <div class="stat">
      <span class="stat-label">Free</span>
      <span class="stat-value" style="display:flex;align-items:center;gap:8px">
        <span id="disk-free">—</span>
        <div class="bar-wrap"><div class="bar-fill bar-green" id="disk-bar" style="width:0%"></div></div>
      </span>
    </div>
    <div class="stat">
      <span class="stat-label">Total</span>
      <span class="stat-value" id="disk-total">—</span>
    </div>
    <div class="stat">
      <span class="stat-label">Path</span>
      <span class="stat-value" style="font-size:14px;color:#aaaabb;max-width:180px;text-align:right;word-break:break-all" id="disk-path">—</span>
    </div>
  </div>

</div>



</div>

<div class="charts-section" id="charts-section" style="display:none">
  <div class="tab-bar" id="period-tabs">
    <button data-period="24h" class="active">24h</button>
    <button data-period="7d">7d</button>
    <button data-period="30d">30d</button>
    <button data-period="1y">1y</button>
  </div>
  <div class="chart-grid">
    <div class="chart-card">
      <div class="card-title">Jobs</div>
      <div style="position:relative;height:220px"><canvas id="chart-jobs"></canvas></div>
    </div>
    <div class="chart-card">
      <div class="card-title">Earnings (USD)</div>
      <div style="position:relative;height:220px"><canvas id="chart-earnings"></canvas></div>
    </div>
  </div>
</div>

<div id="updated"></div>

<script>
function escapeHtml(s) { var d = document.createElement('div'); d.textContent = s; return d.innerHTML; }
function fmt2(n) { return '$' + n.toFixed(2); }
function fmtGB(mb) { return (mb/1024).toFixed(1) + ' GB'; }
function fmtMB(mb) { return mb > 1024 ? fmtGB(mb) : mb + ' MB'; }

function stateDisplay(state) {
  switch(state) {
    case 'earning': return ['dot-green',  'Connected & earning'];
    case 'ready':   return ['dot-yellow', 'Getting ready'];
    case 'idle':    return ['dot-yellow', 'Idle — waiting'];
    case 'wallet':  return ['dot-blue',   'Wallet not set'];
    case 'error':   return ['dot-red',    'Error'];
    case 'paused':  return ['dot-grey',   'Paused'];
    default:        return ['dot-grey',   state];
  }
}

function update(d) {
  // Override state: if gateway says registered+connected, node is earning
  if ((d.state === 'ready' || d.state === 'wallet') && d.gateway && d.gateway.connected && d.gateway.status === 'registered') {
    d.state = 'earning';
  }
  document.getElementById('version').textContent = 'v' + d.version;
  document.getElementById('node-id').textContent = 'node ' + d.node_id;

  // Network badge (beta/production)
  var nb = document.getElementById('network-badge');
  if (d.network === 'beta') { nb.textContent = 'BETA'; nb.style.display = 'inline-block'; }
  else { nb.style.display = 'none'; }

  // Wallet warning / configured banner
  var ww = document.getElementById('wallet-warn');
  var wwTitle = ww.querySelector('.warn-title');
  if (d.wallet && d.wallet.warning) {
    wwTitle.textContent = 'Wallet not configured';
    document.getElementById('wallet-warn-body').innerHTML = d.wallet.warning;
    ww.classList.remove('configured');
    ww.style.display = 'block';
  } else if (d.wallet && d.wallet.configured) {
    wwTitle.textContent = '\u26a1 Wallet configured';
    document.getElementById('wallet-warn-body').textContent = d.wallet.configured;
    ww.classList.add('configured');
    ww.style.display = 'block';
  } else { ww.classList.remove('configured'); ww.style.display = 'none'; }

  const [dotClass, label] = stateDisplay(d.state);
  document.getElementById('state-badge').innerHTML =
    '<span class="dot ' + dotClass + '"></span>' + label;

  document.getElementById('today').textContent = fmt2(d.earnings.today_usd);
  document.getElementById('total').textContent  = fmt2(d.earnings.total_usd);

  const g = d.gpu;
  document.getElementById('gpu-name').textContent  = g.name || 'No GPU detected';
  document.getElementById('util-pct').textContent  = g.util_pct + '%';
  document.getElementById('util-bar').style.width  = g.util_pct + '%';
  document.getElementById('vram-free').textContent = fmtMB(g.vram_free_mb);
  document.getElementById('temp').textContent      = g.temp_c ? g.temp_c + ' °C' : '—';
  document.getElementById('power').textContent     = g.power_w ? g.power_w.toFixed(0) + ' W' : '—';

  const utilBar = document.getElementById('util-bar');
  utilBar.className = 'bar-fill ' + (g.util_pct > 80 ? 'bar-red' : g.util_pct > 50 ? 'bar-yellow' : 'bar-green');

  document.getElementById('model').textContent = d.model || '—';
  const pricingRow = document.getElementById('model-pricing-row');
  if (d.model_pricing) {
    pricingRow.style.display = '';
    document.getElementById('model-pricing').textContent = '$' + d.model_pricing.per_m_input_usd.toFixed(3) + ' / $' + d.model_pricing.per_m_output_usd.toFixed(2) + ' per M tok';
  } else {
    pricingRow.style.display = 'none';
  }

  const gw = d.gateway;
  const connEl = document.getElementById('gw-connected');
  connEl.textContent = gw.connected ? 'Connected' : 'Disconnected';
  connEl.className = 'stat-value ' + (gw.connected ? 'connected' : 'disconnected');
  document.getElementById('gw-jobs').textContent   = gw.jobs_today;
  document.getElementById('gw-tokens').textContent = gw.tokens_today.toLocaleString();
  document.getElementById('gw-queue').textContent  = gw.queue_depth_global;

  const dk = d.disk;
  document.getElementById('disk-free').textContent  = dk.free_gb.toFixed(1) + ' GB (' + dk.free_pct.toFixed(0) + '%)';
  document.getElementById('disk-total').textContent = dk.total_gb.toFixed(0) + ' GB';
  document.getElementById('disk-path').textContent  = dk.path;
  const diskBar = document.getElementById('disk-bar');
  diskBar.style.width = dk.free_pct + '%';
  diskBar.className = 'bar-fill ' + (dk.free_pct < 10 ? 'bar-red' : dk.free_pct < 30 ? 'bar-yellow' : 'bar-green');

  // Wallet (Lightning address)
  var lnAddr = d.lightning_address || '';
  var sw = d.sats_wallet;
  function fmtSats(v) { return v ? v.toLocaleString() + ' sats' : '0 sats'; }
  if (lnAddr) {
    document.getElementById('wallet-setup').style.display = 'none';
    document.getElementById('wallet-active').style.display = '';
    document.getElementById('ln-address-display').textContent = lnAddr;
    document.getElementById('sats-gateway').textContent = fmtSats(sw.gateway_sats);
    document.getElementById('sats-usd').textContent = sw.usd_approx ? '$' + sw.usd_approx.toFixed(2) : '$0.00';
    // Threshold slider
    var thr = d.redeem_threshold || 500;
    var slider = document.getElementById('threshold-slider');
    if (document.activeElement !== slider) {
      slider.value = thr;
      updateThresholdDisplay(thr);
    }
    if (thr < 100) { document.getElementById('unlock-50').checked = true; slider.min = '50'; }
    // Earnings stats
    document.getElementById('wallet-today-sats').textContent = fmtSats(sw.gateway_sats);
    document.getElementById('wallet-withdrawn').textContent = fmtSats(sw.local_sats);
    if (sw.gateway_sats > 0 && thr > 0) {
      var pct = Math.min(100, Math.round(sw.gateway_sats / thr * 100));
      document.getElementById('wallet-next-payout').textContent = pct + '% to threshold (' + thr + ' sats)';
    } else {
      document.getElementById('wallet-next-payout').textContent = '—';
    }
    // Ecash advanced section
    document.getElementById('ecash-local-sats').textContent = fmtSats(sw.local_sats);
    document.getElementById('ecash-proof-count').textContent = sw.proof_count || 0;
    // Token history
    var histEl = document.getElementById('ecash-token-history');
    if (sw.token_history && sw.token_history.length > 0) {
      histEl.innerHTML = '<div style="font-size:13px;color:#aaaabb;margin-bottom:6px">Recent tokens:</div>' +
        sw.token_history.slice(0, 5).map(function(t) {
          return '<div style="font-size:12px;color:#888;margin-bottom:4px;word-break:break-all">' +
            fmtSats(t.sats) + ' — ' + new Date(t.claimed_at).toLocaleString() + '</div>';
        }).join('');
    }
  } else {
    document.getElementById('wallet-setup').style.display = '';
    document.getElementById('wallet-active').style.display = 'none';
  }

  // BTC Price
  var bp = d.btc_price;
  var btcCard = document.getElementById('btc-live').closest('.card');
  function fmtUsd(v) { return v ? '$' + v.toLocaleString(undefined, {minimumFractionDigits: 0, maximumFractionDigits: 0}) : '—'; }
  var btcActive = bp.live_usd || bp.yesterday_fix || bp.daily_avg || bp.status;
  var btcNotice = document.getElementById('btc-inactive-notice');
  if (!btcActive) {
    if (!btcNotice) {
      var n = document.createElement('div');
      n.id = 'btc-inactive-notice';
      n.style.cssText = 'color:#666;font-size:14px;font-style:italic;padding:8px 0;text-align:center';
      n.textContent = 'Bitcoin payments not yet active on this gateway';
      btcCard.querySelectorAll('.stat').forEach(function(s) { s.style.display = 'none'; });
      btcCard.appendChild(n);
    }
  } else {
    if (btcNotice) { btcNotice.remove(); btcCard.querySelectorAll('.stat').forEach(function(s) { s.style.display = ''; }); }
    document.getElementById('btc-live').textContent = fmtUsd(bp.live_usd);
    document.getElementById('btc-owlrun').textContent = fmtUsd(bp.yesterday_fix);
    document.getElementById('btc-daily').textContent = fmtUsd(bp.daily_avg);
    document.getElementById('btc-weekly').textContent = fmtUsd(bp.weekly_avg);
    var statusEl = document.getElementById('btc-status');
    statusEl.textContent = bp.status || '—';
    statusEl.style.color = bp.status === 'normal' ? '#22c55e' : bp.status === 'stale' ? '#eab308' : '#a0a0b8';
  }

  // Broadcasts
  var bcEl = document.getElementById('broadcasts');
  if (d.broadcasts && d.broadcasts.length > 0) {
    var sorted = d.broadcasts.slice().sort(function(a, b) { return b.timestamp.localeCompare(a.timestamp); });
    bcEl.innerHTML = sorted.map(function(b) {
      var t = new Date(b.timestamp);
      var ts = isNaN(t) ? b.timestamp : t.toLocaleString();
      return '<div class="broadcast-item"><span class="broadcast-msg">' + escapeHtml(b.message) + '</span><span class="broadcast-time">' + ts + '</span></div>';
    }).join('');
  } else {
    bcEl.innerHTML = '<div class="broadcast-empty">Broadcast notifications from the gateway will appear here.</div>';
  }

  document.getElementById('updated').textContent = 'updated ' + new Date().toLocaleTimeString();
}

async function saveLightningAddress() {
  var addr = document.getElementById('ln-address-input').value.trim();
  var errEl = document.getElementById('ln-save-error');
  errEl.style.display = 'none';
  if (!addr || !addr.includes('@')) {
    errEl.textContent = 'Enter a valid Lightning address (e.g. yourname@minibits.cash)';
    errEl.style.display = '';
    return;
  }
  var btn = document.getElementById('btn-save-ln');
  btn.disabled = true; btn.textContent = 'Saving...';
  try {
    var r = await fetch('/api/set-lightning-address', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({address:addr})});
    var data = await r.json();
    if (data.error) { errEl.textContent = data.error; errEl.style.display = ''; return; }
    poll(); // refresh immediately
  } catch(e) { errEl.textContent = 'Failed: ' + e.message; errEl.style.display = ''; }
  finally { btn.disabled = false; btn.textContent = 'Save & Start Earning'; }
}

async function saveLightningAddressEdit() {
  var addr = document.getElementById('ln-address-edit').value.trim();
  var errEl = document.getElementById('ln-edit-error');
  errEl.style.display = 'none';
  if (!addr || !addr.includes('@')) {
    errEl.textContent = 'Enter a valid Lightning address';
    errEl.style.display = '';
    return;
  }
  try {
    var r = await fetch('/api/set-lightning-address', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({address:addr})});
    var data = await r.json();
    if (data.error) { errEl.textContent = data.error; errEl.style.display = ''; return; }
    document.getElementById('edit-ln-section').style.display = 'none';
    poll();
  } catch(e) { errEl.textContent = 'Failed: ' + e.message; errEl.style.display = ''; }
}

function toggleEditLnAddress() {
  var sec = document.getElementById('edit-ln-section');
  if (sec.style.display === 'none') {
    sec.style.display = '';
    document.getElementById('ln-address-edit').value = document.getElementById('ln-address-display').textContent;
    document.getElementById('ln-address-edit').focus();
  } else {
    sec.style.display = 'none';
  }
}

function estimateFee(sats) {
  if (sats <= 50) return '~10%';
  if (sats <= 100) return '~5%';
  if (sats <= 200) return '~2.5%';
  if (sats <= 500) return '~1%';
  return '~0.5%';
}

function updateThresholdDisplay(val) {
  val = parseInt(val);
  document.getElementById('threshold-value').textContent = val + ' sats';
  document.getElementById('fee-estimate').textContent = estimateFee(val);
  var warn = document.getElementById('low-threshold-warn');
  if (val <= 50) { warn.style.display = ''; } else { warn.style.display = 'none'; }
}

function toggleLowThreshold(checked) {
  var slider = document.getElementById('threshold-slider');
  if (checked) {
    slider.min = '50';
  } else {
    slider.min = '100';
    if (parseInt(slider.value) < 100) { slider.value = '100'; updateThresholdDisplay(100); saveRedeemThreshold(100); }
  }
  document.getElementById('low-threshold-warn').style.display = checked ? '' : 'none';
}

async function saveRedeemThreshold(val) {
  try {
    await fetch('/api/set-redeem-threshold', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({threshold:parseInt(val)})});
  } catch(e) {}
}

async function poll() {
  try {
    const r = await fetch('/api/status');
    update(await r.json());
  } catch(e) {
    document.getElementById('updated').textContent = 'connection lost…';
  }
  fetchHistory();
}

async function claimEcash() {
  var btn = document.getElementById('btn-claim');
  btn.disabled = true; btn.textContent = 'Claiming…';
  try {
    var r = await fetch('/api/claim-ecash', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({amount_sats:0})});
    var data = await r.json();
    if (data.error) { alert('Claim failed: ' + data.error); return; }
    document.getElementById('claim-token').value = data.token;
    document.getElementById('claim-amount').textContent = data.amount_sats ? data.amount_sats.toLocaleString() + ' sats claimed' : '';
    document.getElementById('claim-result').style.display = '';
  } catch(e) { alert('Claim failed: ' + e.message); }
  finally { btn.disabled = false; btn.textContent = 'Claim All'; }
}

async function exportToken() {
  var btn = document.getElementById('btn-export');
  btn.disabled = true;
  try {
    var r = await fetch('/api/status');
    var d = await r.json();
    if (d.sats_wallet && d.sats_wallet.local_sats > 0) {
      alert('Export is available after claiming. Use Claim All first, then copy the token.');
    } else {
      alert('No local proofs to export.');
    }
  } finally { btn.disabled = false; }
}

function copyToken() {
  var ta = document.getElementById('claim-token');
  ta.select(); document.execCommand('copy');
  var btn = event.target; btn.textContent = 'Copied!';
  setTimeout(function() { btn.textContent = 'Copy'; }, 1500);
}

// ── Charts ──────────────────────────────────────────────────────────
var chartReady = false, jobsChart = null, earningsChart = null, currentPeriod = '24h';

var sc = document.createElement('script');
sc.src = 'https://cdn.jsdelivr.net/npm/chart.js@4/dist/chart.umd.min.js';
sc.onload = function() {
  chartReady = true;
  document.getElementById('charts-section').style.display = 'block';
  initCharts();
  fetchHistory();
};
sc.onerror = function() { /* graceful degradation — charts stay hidden */ };
document.head.appendChild(sc);

var chartOpts = {
  responsive: true, maintainAspectRatio: false,
  animation: false, resizeDelay: 0,
  plugins: { legend: { display: false } },
  scales: {
    x: { ticks: { color: '#a0a0b8', font: { size: 12 }, maxRotation: 45 }, grid: { color: '#2a2a38' } },
    y: { beginAtZero: true, ticks: { color: '#a0a0b8', font: { size: 12 } }, grid: { color: '#2a2a38' } }
  }
};

function initCharts() {
  jobsChart = new Chart(document.getElementById('chart-jobs').getContext('2d'), {
    type: 'bar',
    data: { labels: [], datasets: [{ data: [], backgroundColor: '#eab308cc', borderColor: '#eab308', borderWidth: 1, borderRadius: 3 }] },
    options: chartOpts
  });
  earningsChart = new Chart(document.getElementById('chart-earnings').getContext('2d'), {
    type: 'bar',
    data: { labels: [], datasets: [{ data: [], backgroundColor: '#22c55ecc', borderColor: '#22c55e', borderWidth: 1, borderRadius: 3 }] },
    options: Object.assign({}, chartOpts, {
      scales: Object.assign({}, chartOpts.scales, {
        y: Object.assign({}, chartOpts.scales.y, { ticks: Object.assign({}, chartOpts.scales.y.ticks, {
          callback: function(v) { return '$' + v.toFixed(2); }
        })})
      })
    })
  });
}

async function fetchHistory() {
  if (!chartReady) return;
  try {
    var r = await fetch('/api/history?period=' + currentPeriod);
    var d = await r.json();
    var labels = d.buckets.map(function(b) { return b.label; });
    jobsChart.data.labels = labels;
    jobsChart.data.datasets[0].data = d.buckets.map(function(b) { return b.jobs; });
    jobsChart.update('none');
    earningsChart.data.labels = labels;
    earningsChart.data.datasets[0].data = d.buckets.map(function(b) { return b.earned; });
    earningsChart.update('none');
  } catch(e) {}
}

document.getElementById('period-tabs').addEventListener('click', function(e) {
  if (e.target.tagName !== 'BUTTON') return;
  currentPeriod = e.target.dataset.period;
  document.querySelectorAll('#period-tabs button').forEach(function(b) { b.classList.remove('active'); });
  e.target.classList.add('active');
  fetchHistory();
});

poll();
setInterval(poll, 5000);
</script>
</body>
</html>`
