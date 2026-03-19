// Package dashboard serves a local web UI on localhost:19131 with live GPU
// stats, earnings, and marketplace status. All data is read-only — the
// dashboard displays state, it never changes it.
package dashboard

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync/atomic"

	"github.com/fabgoodvibes/owlrun/internal/buildinfo"
	"github.com/fabgoodvibes/owlrun/internal/earnings"
)

// Status is the full snapshot returned by GET /api/status.
type Status struct {
	NodeID  string `json:"node_id"`
	Version string `json:"version"`
	Network string `json:"network"` // "beta" | "production"
	State   string `json:"state"`   // "earning" | "idle" | "paused"

	Wallet struct {
		Address string `json:"address"`
		Warning string `json:"warning,omitempty"` // non-empty = user needs to set wallet
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

	Model string `json:"model"`

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
		NextPayoutEpoch  string  `json:"next_payout_epoch"`
	} `json:"gateway"`

	Disk struct {
		Path    string  `json:"path"`
		TotalGB float64 `json:"total_gb"`
		FreeGB  float64 `json:"free_gb"`
		FreePct float64 `json:"free_pct"`
	} `json:"disk"`

	BtcPrice   BtcPriceInfo   `json:"btc_price"`
	Broadcasts []BroadcastMsg `json:"broadcasts"`
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

// StatusProvider is a function that returns the current status snapshot.
// Set via SetProvider after the tray initialises its subsystems.
type StatusProvider func() Status

// Server is the embedded web dashboard.
type Server struct {
	port     int
	provider atomic.Pointer[StatusProvider]
	tracker  atomic.Pointer[earnings.Tracker]
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

// Start launches the HTTP server in the background.
// The listener is bound before returning so the port is ready for connections.
func (s *Server) Start() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/history", s.handleHistory)
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
  body { background: #0f0f13; color: #f0f0f5; font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; font-size: 16px; padding: 24px; }
  h1 { font-size: 24px; font-weight: 600; margin-bottom: 20px; color: #fff; letter-spacing: -0.3px; }
  h1 span { opacity: 0.6; font-weight: 400; font-size: 15px; margin-left: 8px; }
  .grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(280px, 1fr)); gap: 16px; }
  .card { background: #1a1a24; border: 1px solid #2a2a38; border-radius: 10px; padding: 20px; }
  .card-title { font-size: 13px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.8px; color: #8888a0; margin-bottom: 14px; }
  .stat { display: flex; justify-content: space-between; align-items: center; margin-bottom: 12px; }
  .stat:last-child { margin-bottom: 0; }
  .stat-label { color: #a0a0b8; }
  .stat-value { font-weight: 500; color: #f0f0f5; font-variant-numeric: tabular-nums; }
  .state-badge { display: inline-flex; align-items: center; gap: 7px; font-weight: 600; font-size: 17px; }
  .dot { width: 10px; height: 10px; border-radius: 50%; flex-shrink: 0; }
  .dot-green  { background: #22c55e; box-shadow: 0 0 8px #22c55e88; }
  .dot-yellow { background: #eab308; box-shadow: 0 0 8px #eab30888; }
  .dot-grey   { background: #6b7280; }
  .dot-blue   { background: #3b82f6; box-shadow: 0 0 8px #3b82f688; }
  .dot-red    { background: #ef4444; box-shadow: 0 0 8px #ef444488; }
  .bar-wrap { background: #2a2a38; border-radius: 4px; height: 6px; width: 100px; overflow: hidden; }
  .bar-fill { height: 100%; border-radius: 4px; transition: width 0.4s ease; }
  .bar-green  { background: #22c55e; }
  .bar-yellow { background: #eab308; }
  .bar-red    { background: #ef4444; }
  .earnings-big { font-size: 32px; font-weight: 700; color: #22c55e; font-variant-numeric: tabular-nums; margin-bottom: 4px; }
  .earnings-sub { font-size: 14px; color: #777; }
  .node-id { font-size: 12px; color: #666; font-family: monospace; margin-top: 6px; }
  .connected { color: #22c55e; }
  .disconnected { color: #ef4444; }
  #updated { position: fixed; bottom: 16px; right: 20px; font-size: 12px; color: #555; }
  .charts-section { margin-top: 24px; padding-bottom: 40px; }
  .tab-bar { display: flex; gap: 0; margin-bottom: 16px; }
  .tab-bar button { background: #1a1a24; border: 1px solid #2a2a38; color: #a0a0b8; padding: 8px 18px; font-size: 14px; font-weight: 600; cursor: pointer; transition: all 0.2s; }
  .tab-bar button:first-child { border-radius: 6px 0 0 6px; }
  .tab-bar button:last-child { border-radius: 0 6px 6px 0; }
  .tab-bar button.active { background: #2a2a38; color: #f0f0f5; border-color: #3a3a4a; }
  .chart-grid { display: grid; grid-template-columns: 1fr; gap: 16px; }
  .chart-card { background: #1a1a24; border: 1px solid #2a2a38; border-radius: 10px; padding: 18px; }
  .chart-card .card-title { font-size: 13px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.8px; color: #8888a0; margin-bottom: 14px; }
  .wallet-warn { background: #2d1f00; border: 1px solid #b45309; border-radius: 8px; padding: 14px 18px; margin-bottom: 16px; display: none; }
  .wallet-warn .warn-title { color: #f59e0b; font-weight: 600; font-size: 13px; margin-bottom: 4px; }
  .wallet-warn .warn-body { color: #d4a04a; font-size: 12px; line-height: 1.5; }
  .wallet-warn code { background: #1a1a24; padding: 2px 6px; border-radius: 4px; font-size: 11px; color: #e2e2e8; }
  .network-badge { display: inline-block; background: #b45309; color: #fff; font-size: 10px; font-weight: 600; padding: 2px 8px; border-radius: 4px; margin-left: 8px; text-transform: uppercase; vertical-align: middle; }
  .broadcast-empty { color: #666; font-size: 14px; font-style: italic; padding: 8px 0; }
  .broadcast-item { display: flex; justify-content: space-between; align-items: flex-start; gap: 12px; padding: 10px 0; border-bottom: 1px solid #2a2a38; }
  .broadcast-item:last-child { border-bottom: none; }
  .broadcast-msg { color: #f0f0f5; font-size: 15px; flex: 1; }
  .broadcast-time { color: #777; font-size: 13px; white-space: nowrap; font-variant-numeric: tabular-nums; }
</style>
</head>
<body>
<h1>🦉 Owlrun <span id="version"></span><span id="network-badge" class="network-badge" style="display:none"></span></h1>
<div id="wallet-warn" class="wallet-warn">
  <div class="warn-title">Wallet not configured</div>
  <div class="warn-body" id="wallet-warn-body"></div>
</div>
<div class="grid">

  <div class="card">
    <div class="card-title">Status</div>
    <div id="state-badge" class="state-badge">—</div>
    <div class="node-id" id="node-id"></div>
  </div>

  <div class="card">
    <div class="card-title">Earnings</div>
    <div class="earnings-big" id="today">$0.00</div>
    <div class="earnings-sub">today</div>
    <div style="margin-top:12px" class="stat">
      <span class="stat-label">All time</span>
      <span class="stat-value" id="total">$0.00</span>
    </div>
  </div>

  <div class="card">
    <div class="card-title">GPU</div>
    <div class="stat"><span class="stat-label" id="gpu-name" style="color:#aaa;font-size:12px"></span></div>
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
    <div class="card-title">Model</div>
    <div class="stat">
      <span class="stat-label">Loaded</span>
      <span class="stat-value" id="model" style="font-size:12px;max-width:160px;text-align:right">—</span>
    </div>
  </div>

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
    <div class="stat">
      <span class="stat-label">Next payout</span>
      <span class="stat-value" id="gw-payout">—</span>
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
      <span class="stat-value" style="font-size:11px;color:#666;max-width:160px;text-align:right;word-break:break-all" id="disk-path">—</span>
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

  <div class="card" style="grid-column:1/-1">
    <div class="card-title">Notifications</div>
    <div id="broadcasts">
      <div class="broadcast-empty">Broadcast notifications from the gateway will appear here.</div>
    </div>
  </div>

  <div class="card">
    <div class="card-title">Status Colors</div>
    <div class="stat"><span class="state-badge"><span class="dot dot-green"></span>Connected & earning</span></div>
    <div class="stat"><span class="state-badge"><span class="dot dot-yellow"></span>Getting ready</span></div>
    <div class="stat"><span class="state-badge"><span class="dot dot-blue"></span>Wallet not set</span></div>
    <div class="stat"><span class="state-badge"><span class="dot dot-red"></span>Error</span></div>
    <div class="stat"><span class="state-badge"><span class="dot dot-grey"></span>Paused</span></div>
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
    case 'earning': return ['dot-green',  '● Connected & earning'];
    case 'ready':   return ['dot-yellow', '◑ Getting ready'];
    case 'idle':    return ['dot-yellow', '◑ Idle — waiting'];
    case 'wallet':  return ['dot-blue',   '◇ Wallet not set'];
    case 'error':   return ['dot-red',    '✕ Error'];
    case 'paused':  return ['dot-grey',   '○ Paused'];
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

  // Wallet warning
  var ww = document.getElementById('wallet-warn');
  if (d.wallet && d.wallet.warning) {
    document.getElementById('wallet-warn-body').innerHTML = d.wallet.warning;
    ww.style.display = 'block';
  } else { ww.style.display = 'none'; }

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

  const gw = d.gateway;
  const connEl = document.getElementById('gw-connected');
  connEl.textContent = gw.connected ? 'Connected' : 'Disconnected';
  connEl.className = 'stat-value ' + (gw.connected ? 'connected' : 'disconnected');
  document.getElementById('gw-jobs').textContent   = gw.jobs_today;
  document.getElementById('gw-tokens').textContent = gw.tokens_today.toLocaleString();
  document.getElementById('gw-queue').textContent  = gw.queue_depth_global;
  document.getElementById('gw-payout').textContent = gw.next_payout_epoch
    ? new Date(gw.next_payout_epoch).toLocaleDateString() : '—';

  const dk = d.disk;
  document.getElementById('disk-free').textContent  = dk.free_gb.toFixed(1) + ' GB (' + dk.free_pct.toFixed(0) + '%)';
  document.getElementById('disk-total').textContent = dk.total_gb.toFixed(0) + ' GB';
  document.getElementById('disk-path').textContent  = dk.path;
  const diskBar = document.getElementById('disk-bar');
  diskBar.style.width = dk.free_pct + '%';
  diskBar.className = 'bar-fill ' + (dk.free_pct < 10 ? 'bar-red' : dk.free_pct < 30 ? 'bar-yellow' : 'bar-green');

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

async function poll() {
  try {
    const r = await fetch('/api/status');
    update(await r.json());
  } catch(e) {
    document.getElementById('updated').textContent = 'connection lost…';
  }
  fetchHistory();
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
