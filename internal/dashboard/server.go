// Package dashboard serves a local web UI on localhost:19131 with live GPU
// stats, earnings, and marketplace status. All data is read-only — the
// dashboard displays state, it never changes it.
package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fabgoodvibes/owlrun/internal/buildinfo"
	"github.com/fabgoodvibes/owlrun/internal/cashu"
	"github.com/fabgoodvibes/owlrun/internal/earnings"
)

// Status is the full snapshot returned by GET /api/status.
type Status struct {
	NodeID      string `json:"node_id"`
	ProviderKey string `json:"provider_key"`
	Version     string `json:"version"`
	Network     string `json:"network"` // "beta" | "production"
	State       string `json:"state"`        // "earning" | "idle" | "paused" | "error"
	ErrorDetail string `json:"error_detail,omitempty"` // user-facing error message when state=error

	JobMode string `json:"job_mode"` // "never", "idle", "always"

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

	Model           string                        `json:"model"`
	Models          []string                      `json:"models,omitempty"`
	ModelPricing    *ModelPricingInfo              `json:"model_pricing,omitempty"`
	AllModelPricing map[string]*ModelPricingInfo   `json:"all_model_pricing,omitempty"`

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
		EarnedTodaySats  int64   `json:"earned_today_sats"`  // authoritative, 1-sat min applied (from gateway)
		EarnedTotalSats  int64   `json:"earned_total_sats"`  // lifetime sats earned (from gateway)
		QueueDepthGlobal int     `json:"queue_depth_global"`
	} `json:"gateway"`

	Disk struct {
		Path    string  `json:"path"`
		TotalGB float64 `json:"total_gb"`
		FreeGB  float64 `json:"free_gb"`
		FreePct float64 `json:"free_pct"`
	} `json:"disk"`

	AvailableModels  []AvailableModel `json:"available_models,omitempty"`
	Pulling          bool             `json:"pulling"` // true if download in progress
	LightningAddress string           `json:"lightning_address"`
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
	Title     string `json:"title"`
	Message   string `json:"message"`
	Severity  string `json:"severity"`
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
	TokenHistory    []TokenHistoryItem    `json:"token_history"`    // last N tokens
	WithdrawHistory []WithdrawHistoryItem `json:"withdraw_history"` // last N Lightning payouts
}

// WithdrawHistoryItem is a Lightning payout record for the dashboard.
type WithdrawHistoryItem struct {
	AmountSats  int64  `json:"amount_sats"`
	PaymentHash string `json:"payment_hash"`
	Timestamp   string `json:"timestamp"`
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

// SetJobModeFunc switches the job acceptance mode ("never", "idle", "always").
type SetJobModeFunc func(mode string) error

// SwitchModelFunc switches the active primary model. If the model is already
// installed, it loads it into VRAM and re-registers. Returns error if not installed.
type SwitchModelFunc func(model string) error

// PullModelProgress is a download progress event.
type PullModelProgress struct {
	Status    string `json:"status"`    // "pulling manifest", "downloading", "success", "error"
	Total     int64  `json:"total"`
	Completed int64  `json:"completed"`
	Error     string `json:"error,omitempty"`
}

// PullModelFunc starts downloading a model and returns a channel of progress events.
type PullModelFunc func(model string) <-chan PullModelProgress

// RemoveModelFunc deletes a model from Ollama and re-registers with the gateway.
type RemoveModelFunc func(model string) error

// AvailableModel describes a model the node could run.
type AvailableModel struct {
	Tag       string             `json:"tag"`
	VramGB    float64            `json:"vram_gb"`
	Installed bool               `json:"installed"`
	Active    bool               `json:"active"`
	Fits      bool               `json:"fits"`    // fits in VRAM (false = CPU fallback / slow)
	Pricing   *ModelPricingInfo  `json:"pricing,omitempty"`
}

// Server is the embedded web dashboard.
type Server struct {
	port     int
	provider atomic.Pointer[StatusProvider]
	tracker  atomic.Pointer[earnings.Tracker]
	claimer  atomic.Pointer[ClaimFunc]
	setLnAddr      atomic.Pointer[SetLightningAddressFunc]
	setRedeemThr   atomic.Pointer[SetRedeemThresholdFunc]
	switchModel    atomic.Pointer[SwitchModelFunc]
	pullModel      atomic.Pointer[PullModelFunc]
	removeModel    atomic.Pointer[RemoveModelFunc]
	pulling        atomic.Bool // true while a pull is in progress
	setJobMode     atomic.Pointer[SetJobModeFunc]
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

// SetModelSwitcher wires the model switch function.
func (s *Server) SetModelSwitcher(fn SwitchModelFunc) {
	s.switchModel.Store(&fn)
}

// SetModelPuller wires the model download function.
func (s *Server) SetModelPuller(fn PullModelFunc) {
	s.pullModel.Store(&fn)
}

// SetModelRemover wires the model delete function.
func (s *Server) SetModelRemover(fn RemoveModelFunc) {
	s.removeModel.Store(&fn)
}

// SetRedeemThresholdSetter wires the redeem threshold save function.
func (s *Server) SetRedeemThresholdSetter(fn SetRedeemThresholdFunc) {
	s.setRedeemThr.Store(&fn)
}

// SetJobModeSetter wires the job mode switch function.
func (s *Server) SetJobModeSetter(fn SetJobModeFunc) {
	s.setJobMode.Store(&fn)
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
	mux.HandleFunc("/api/switch-model", s.handleSwitchModel)
	mux.HandleFunc("/api/pull-model", s.handlePullModel)
	mux.HandleFunc("/api/model-size", s.handleModelSize)
	mux.HandleFunc("/api/remove-model", s.handleRemoveModel)
	mux.HandleFunc("/api/set-job-mode", s.handleSetJobMode)
	addr := fmt.Sprintf("127.0.0.1:%d", s.port)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		// Port likely held by a crashed previous instance (TIME_WAIT or zombie).
		// Try SO_REUSEADDR via ListenConfig.
		lc := net.ListenConfig{
			Control: setReuseAddr,
		}
		ln, err = lc.Listen(context.Background(), "tcp", addr)
		if err != nil {
			log.Printf("owlrun: dashboard port %d unavailable: %v", s.port, err)
			return err
		}
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
	st := s.getStatus()
	st.Pulling = s.pulling.Load()
	json.NewEncoder(w).Encode(st)
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

	// Lightning address validation: user@domain with valid parts.
	if !isValidLightningAddress(req.Address) {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid Lightning address — expected format: user@domain.tld"})
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

func (s *Server) handleSetJobMode(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]string{"error": "POST required"})
		return
	}

	fn := s.setJobMode.Load()
	if fn == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "not ready"})
		return
	}

	var req struct {
		Mode string `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "invalid request"})
		return
	}
	if req.Mode != "never" && req.Mode != "idle" && req.Mode != "always" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "mode must be never, idle, or always"})
		return
	}

	if err := (*fn)(req.Mode); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}

	json.NewEncoder(w).Encode(map[string]any{"status": "ok", "mode": req.Mode})
}

func (s *Server) handleSwitchModel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]string{"error": "POST required"})
		return
	}
	if s.pulling.Load() {
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{"error": "download in progress"})
		return
	}

	var req struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Model == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "model required"})
		return
	}

	fn := s.switchModel.Load()
	if fn == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "not ready"})
		return
	}
	if err := (*fn)(req.Model); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "model": req.Model})
}

func (s *Server) handlePullModel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.Method != http.MethodPost {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]string{"error": "POST required"})
		return
	}
	if s.pulling.Load() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{"error": "download already in progress"})
		return
	}

	var req struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Model == "" {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "model required"})
		return
	}

	// Disk space check is done client-side with model-aware sizing.
	// Server just validates the pull function is available.
	fn := s.pullModel.Load()
	if fn == nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "not ready"})
		return
	}

	// SSE stream for download progress.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher, canFlush := w.(http.Flusher)
	if canFlush {
		flusher.Flush()
	}

	s.pulling.Store(true)
	defer s.pulling.Store(false)

	ch := (*fn)(req.Model)
	for p := range ch {
		data, _ := json.Marshal(p)
		fmt.Fprintf(w, "data: %s\n\n", data)
		if canFlush {
			flusher.Flush()
		}
	}
	fmt.Fprintf(w, "data: {\"status\":\"done\"}\n\n")
	if canFlush {
		flusher.Flush()
	}
}

func (s *Server) handleRemoveModel(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		json.NewEncoder(w).Encode(map[string]string{"error": "POST required"})
		return
	}

	var req struct {
		Model string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Model == "" {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": "model required"})
		return
	}

	fn := s.removeModel.Load()
	if fn == nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]string{"error": "not ready"})
		return
	}
	if err := (*fn)(req.Model); err != nil {
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "ok", "model": req.Model})
}

// handleModelSize queries the Ollama registry for the model's download size.
// Falls back to vram_gb * 1.5 GB estimate if the registry is unreachable.
func (s *Server) handleModelSize(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	model := r.URL.Query().Get("model")
	if model == "" {
		json.NewEncoder(w).Encode(map[string]any{"error": "model param required"})
		return
	}

	// Parse "name:tag" — registry expects /v2/library/{name}/manifests/{tag}
	parts := strings.SplitN(model, ":", 2)
	name := parts[0]
	tag := "latest"
	if len(parts) == 2 {
		tag = parts[1]
	}

	// Try Ollama registry with 10s timeout.
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()

	url := fmt.Sprintf("https://registry.ollama.ai/v2/library/%s/manifests/%s", name, tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err == nil {
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				var manifest struct {
					Layers []struct {
						Size int64 `json:"size"`
					} `json:"layers"`
				}
				if json.Unmarshal(body, &manifest) == nil && len(manifest.Layers) > 0 {
					var total int64
					for _, l := range manifest.Layers {
						total += l.Size
					}
					json.NewEncoder(w).Encode(map[string]any{
						"model":    model,
						"size_mb":  total / 1048576,
						"source":   "registry",
					})
					return
				}
			}
		}
	}

	// Fallback: estimate from VRAM table.
	json.NewEncoder(w).Encode(map[string]any{
		"model":   model,
		"size_mb": 0, // unknown — JS will use vram_gb * 1.5 * 1024
		"source":  "estimate",
	})
}

// isValidLightningAddress checks that addr is a well-formed user@domain.tld.
func isValidLightningAddress(addr string) bool {
	parts := strings.SplitN(addr, "@", 2)
	if len(parts) != 2 {
		return false
	}
	user, domain := parts[0], parts[1]
	if user == "" || domain == "" {
		return false
	}
	// Domain must have at least one dot, not start/end with dot, no consecutive dots.
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") || strings.Contains(domain, "..") {
		return false
	}
	// No spaces or control characters in either part.
	if strings.ContainsAny(addr, " \t\n\r") {
		return false
	}
	// Must not have multiple @ signs.
	if strings.Count(addr, "@") != 1 {
		return false
	}
	return true
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, dashboardHTML)
}

const dashboardHTML = `<!DOCTYPE html>
<html lang="en" data-theme="dark">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Owlrun Dashboard</title>
<style>
  :root {
    --accent: #f59e0b;
    --accent-hover: #d97706;
    --green: #22c55e;
    --yellow: #eab308;
    --red: #ef4444;
    --blue: #3b82f6;
    --transition: 0.35s cubic-bezier(0.4, 0, 0.2, 1);
  }
  [data-theme="dark"] {
    --bg: #0f0f13;
    --bg-card: #1a1a24;
    --bg-card-hover: #222230;
    --border: #2a2a38;
    --border-active: #3a3a4a;
    --text: #e8e8f0;
    --text-dim: #aaaac0;
    --text-muted: #9999b0;
    --text-heading: #fff;
    --bar-bg: #2a2a38;
    --wallet-warn-bg: #2d1f00;
    --wallet-warn-border: #b45309;
    --wallet-ok-bg: #0d2818;
    --wallet-ok-border: #16a34a;
    --code-bg: #1a1a24;
  }
  [data-theme="light"] {
    --bg: #f5f5f7;
    --bg-card: #ffffff;
    --bg-card-hover: #f0f0f4;
    --border: #e0e0ea;
    --border-active: #ccccd8;
    --text: #1a1a2e;
    --text-dim: #6b6b80;
    --text-muted: #8888a0;
    --text-heading: #111;
    --bar-bg: #e0e0ea;
    --wallet-warn-bg: #fff8eb;
    --wallet-warn-border: #d97706;
    --wallet-ok-bg: #ecfdf5;
    --wallet-ok-border: #16a34a;
    --code-bg: #f0f0f4;
  }
  *, *::before, *::after { box-sizing: border-box; margin: 0; padding: 0; }
  body { background: var(--bg); color: var(--text); font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; font-size: 17px; padding: 28px 36px; transition: background var(--transition), color var(--transition); }
  h1 { font-size: 26px; font-weight: 600; margin-bottom: 22px; color: var(--text-heading); letter-spacing: -0.3px; display: flex; align-items: center; gap: 12px; }
  h1 span { opacity: 0.6; font-weight: 400; font-size: 16px; }
  .grid { display: grid; grid-template-columns: 1fr 1fr 1fr 1fr; grid-template-rows: auto auto; gap: 14px; }
  .card { background: var(--bg-card); border: 1px solid var(--border); border-radius: 12px; padding: 20px; transition: background var(--transition), border-color var(--transition); }
  .card-title { font-size: 14px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.8px; color: var(--text-dim); margin-bottom: 16px; }
  .card-wallet { grid-row: 1 / 3; }
  .card-wide { grid-column: 1 / -1; }
  .card-notify { margin-bottom: 14px; }
  @media (max-width: 900px) { .grid { grid-template-columns: 1fr 1fr; } .card-wallet { grid-row: auto; } }
  @media (max-width: 550px) { .grid { grid-template-columns: 1fr; } .card-wallet { grid-row: auto; } }
  .stat { display: flex; justify-content: space-between; align-items: center; margin-bottom: 10px; min-height: 26px; }
  .stat:last-child { margin-bottom: 0; }
  .stat-label { color: var(--text); font-size: 16px; }
  .stat-value { font-weight: 500; color: var(--text); font-variant-numeric: tabular-nums; font-size: 16px; }
  .state-badge { display: inline-flex; align-items: center; gap: 8px; font-weight: 600; font-size: 18px; }
  .dot { width: 11px; height: 11px; border-radius: 50%; flex-shrink: 0; }
  .dot-green  { background: var(--green); box-shadow: 0 0 8px rgba(34,197,94,0.5); }
  .dot-yellow { background: var(--yellow); box-shadow: 0 0 8px rgba(234,179,8,0.5); }
  .dot-grey   { background: #6b7280; }
  .dot-blue   { background: var(--blue); box-shadow: 0 0 8px rgba(59,130,246,0.5); }
  .dot-red    { background: var(--red); box-shadow: 0 0 8px rgba(239,68,68,0.5); }
  .bar-wrap { background: var(--bar-bg); border-radius: 4px; height: 7px; width: 110px; overflow: hidden; transition: background var(--transition); }
  .bar-fill { height: 100%; border-radius: 4px; transition: width 0.4s ease; }
  .bar-green  { background: var(--green); }
  .bar-yellow { background: var(--yellow); }
  .bar-red    { background: var(--red); }
  .earnings-big { font-size: 36px; font-weight: 700; color: var(--green); font-variant-numeric: tabular-nums; margin-bottom: 4px; }
  .earnings-sub { font-size: 15px; color: var(--text-dim); }
  .node-id { font-size: 14px; color: var(--text-muted); font-family: monospace; margin-top: 6px; }
  .connected { color: var(--green); }
  .disconnected { color: var(--red); }
  #updated { position: fixed; bottom: 16px; right: 24px; font-size: 13px; color: var(--text-muted); }
  .charts-section { margin-top: 28px; padding-bottom: 44px; }
  .tab-bar { display: flex; gap: 0; margin-bottom: 18px; }
  .tab-bar button { background: var(--bg-card); border: 1px solid var(--border); color: var(--text); padding: 10px 20px; font-size: 15px; font-weight: 600; cursor: pointer; transition: all 0.2s; }
  .tab-bar button:first-child { border-radius: 6px 0 0 6px; }
  .tab-bar button:last-child { border-radius: 0 6px 6px 0; }
  .tab-bar button.active { background: var(--bg-card-hover); color: var(--text-heading); border-color: var(--border-active); }
  .chart-grid { display: grid; grid-template-columns: 1fr; gap: 18px; }
  .chart-card { background: var(--bg-card); border: 1px solid var(--border); border-radius: 12px; padding: 20px; transition: background var(--transition), border-color var(--transition); }
  .chart-card .card-title { font-size: 14px; font-weight: 600; text-transform: uppercase; letter-spacing: 0.8px; color: var(--text-dim); margin-bottom: 14px; }
  .wallet-warn { background: var(--wallet-warn-bg); border: 1px solid var(--wallet-warn-border); border-radius: 8px; padding: 16px 20px; margin-bottom: 18px; display: none; transition: background var(--transition), border-color var(--transition); }
  .wallet-warn .warn-title { color: var(--accent); font-weight: 600; font-size: 15px; margin-bottom: 4px; }
  .wallet-warn .warn-body { color: var(--accent-hover); font-size: 14px; line-height: 1.5; }
  .wallet-warn.configured { background: var(--wallet-ok-bg); border-color: var(--wallet-ok-border); }
  .wallet-warn.configured .warn-title { color: #4ade80; }
  .wallet-warn.configured .warn-body { color: #86efac; }
  [data-theme="light"] .wallet-warn.configured .warn-body { color: #16a34a; }
  .wallet-warn code { background: var(--code-bg); padding: 2px 6px; border-radius: 4px; font-size: 13px; color: var(--text); }
  .network-badge { display: inline-block; background: #b45309; color: #fff; font-size: 11px; font-weight: 600; padding: 3px 9px; border-radius: 4px; margin-left: 8px; text-transform: uppercase; vertical-align: middle; }
  .broadcast-empty { color: var(--text-muted); font-size: 15px; font-style: italic; padding: 8px 0; }
  .broadcast-item { display: flex; justify-content: space-between; align-items: flex-start; gap: 14px; padding: 12px 0; border-bottom: 1px solid var(--border); }
  .broadcast-item:last-child { border-bottom: none; }
  .broadcast-msg { color: var(--text); font-size: 16px; flex: 1; }
  .broadcast-time { color: var(--text-dim); font-size: 14px; white-space: nowrap; font-variant-numeric: tabular-nums; }
  .legend-row { display: inline-flex; align-items: center; gap: 6px; margin-right: 18px; font-size: 14px; color: var(--text-dim); }
  /* Theme toggle */
  .theme-toggle { width: 44px; height: 24px; background: var(--border); border: 1px solid var(--border); border-radius: 100px; cursor: pointer; position: relative; transition: background var(--transition), border-color var(--transition); flex-shrink: 0; }
  .theme-toggle:hover { border-color: var(--accent); }
  .theme-toggle::after { content: ''; position: absolute; top: 2px; left: 2px; width: 18px; height: 18px; border-radius: 50%; background: var(--accent); transition: transform var(--transition); box-shadow: 0 1px 4px rgba(0,0,0,0.2); }
  [data-theme="dark"] .theme-toggle::after { transform: translateX(20px); }
  .theme-label { font-size: 12px; color: var(--text-muted); display: flex; align-items: center; gap: 6px; }
  @keyframes spin { to { transform: rotate(360deg); } }
  .spinner { display: inline-block; width: 12px; height: 12px; border: 2px solid var(--border); border-top-color: var(--accent); border-radius: 50%; animation: spin 0.6s linear infinite; vertical-align: middle; }
  .payout-item { display: flex; justify-content: space-between; align-items: center; padding: 6px 0; border-bottom: 1px solid var(--border); font-size: 13px; }
  .payout-item:last-child { border-bottom: none; }
  .payout-amount { color: var(--green); font-weight: 600; font-variant-numeric: tabular-nums; }
  .payout-link { color: var(--accent); text-decoration: none; font-size: 11px; font-family: monospace; }
  .payout-link:hover { text-decoration: underline; }
  .payout-time { color: var(--text-muted); font-size: 11px; }
</style>
</head>
<body>
<div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:22px">
<h1 style="margin-bottom:0">🦉 Owlrun <span id="version"></span><span id="network-badge" class="network-badge" style="display:none"></span></h1>
<div class="theme-label"><span id="theme-icon">☀️</span><div class="theme-toggle" onclick="toggleTheme()"></div><span id="theme-icon2">🌙</span></div>
</div>
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
          <div style="margin-bottom:8px"><span style="color:#f7931a;font-weight:600">2.</span> Find your Lightning address in Minibits (looks like <code style="background:var(--code-bg);padding:2px 6px;border-radius:4px;font-size:13px;color:#f7931a">you@minibits.cash</code>)</div>
          <div><span style="color:#f7931a;font-weight:600">3.</span> Paste it below</div>
        </div>
      </div>
      <div style="margin-top:12px">
        <input id="ln-address-input" type="text" placeholder="yourname@minibits.cash" style="width:100%;padding:12px 14px;background:var(--bg);color:var(--accent);border:1px solid var(--border-active);border-radius:8px;font-size:15px;font-family:monospace;box-sizing:border-box" />
        <button id="btn-save-ln" onclick="saveLightningAddress()" style="width:100%;margin-top:8px;padding:12px 16px;background:#f7931a;color:#fff;border:none;border-radius:8px;cursor:pointer;font-weight:600;font-size:15px">Save &amp; Start Earning</button>
        <div id="ln-save-error" style="display:none;color:#ef4444;font-size:13px;margin-top:6px;text-align:center"></div>
      </div>
      <div style="margin-top:14px;font-size:13px;color:var(--text-muted);text-align:center">
        Works with any Lightning wallet — Minibits, Phoenix, Wallet of Satoshi, etc.
      </div>
    </div>
    <div id="wallet-active" style="display:none">
      <div class="stat">
        <span class="stat-label">Lightning address</span>
        <span class="stat-value" id="ln-address-display" style="color:#f7931a;font-family:monospace;font-size:14px;max-width:200px;text-align:right;word-break:break-all"></span>
      </div>
      <div style="margin-top:10px">
        <button onclick="toggleEditLnAddress()" style="padding:6px 14px;background:var(--bg-card-hover);color:var(--text);border:1px solid var(--border-active);border-radius:6px;cursor:pointer;font-size:13px">Change address</button>
      </div>
      <div id="edit-ln-section" style="display:none;margin-top:10px">
        <input id="ln-address-edit" type="text" style="width:100%;padding:10px 12px;background:var(--bg);color:var(--accent);border:1px solid var(--border-active);border-radius:8px;font-size:14px;font-family:monospace;box-sizing:border-box" />
        <div style="display:flex;gap:6px;margin-top:6px">
          <button onclick="saveLightningAddressEdit()" style="flex:1;padding:8px 12px;background:#f7931a;color:#fff;border:none;border-radius:6px;cursor:pointer;font-weight:600;font-size:13px">Save</button>
          <button onclick="toggleEditLnAddress()" style="padding:8px 12px;background:var(--bg-card-hover);color:var(--text);border:1px solid var(--border-active);border-radius:6px;cursor:pointer;font-size:13px">Cancel</button>
        </div>
        <div id="ln-edit-error" style="display:none;color:#ef4444;font-size:13px;margin-top:6px"></div>
      </div>

      <!-- Payout threshold slider -->
      <div style="margin-top:16px;padding-top:14px;border-top:1px solid var(--border)">
        <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:6px">
          <span style="font-size:14px;color:#d0d0e0">Payout threshold</span>
          <span id="threshold-value" style="font-size:14px;color:#f7931a;font-weight:600">500 sats</span>
        </div>
        <input id="threshold-slider" type="range" min="100" max="1000" step="50" value="500" oninput="updateThresholdDisplay(this.value)" onchange="saveRedeemThreshold(this.value)" style="width:100%;accent-color:#f7931a;cursor:pointer" />
        <div style="display:flex;justify-content:space-between;font-size:12px;color:var(--text-muted);margin-top:2px">
          <span>100</span><span>500</span><span>1000</span>
        </div>
        <div style="font-size:13px;color:#aaaabb;margin-top:6px" id="threshold-hint">Lower = faster payouts, higher fees. Higher = slower payouts, lower fees.</div>
        <div style="font-size:13px;margin-top:4px">
          <span style="color:#aaaabb">Est. Lightning fee: </span>
          <span id="fee-estimate" style="color:#f7931a;font-weight:600">~1%</span>
        </div>
        <div style="margin-top:8px">
          <label style="display:flex;align-items:center;gap:8px;cursor:pointer;font-size:13px;color:var(--text-muted)">
            <input type="checkbox" id="unlock-50" onchange="toggleLowThreshold(this.checked)" style="accent-color:#f7931a" />
            Unlock 50 sat minimum
          </label>
          <div id="low-threshold-warn" style="display:none;font-size:12px;color:#eab308;margin-top:4px;margin-left:24px">&#9888; ~10% eaten by Lightning fees at this level</div>
        </div>
      </div>

      <!-- Job mode selector -->
      <div style="margin-top:16px;padding-top:14px;border-top:1px solid var(--border)">
        <div style="display:flex;justify-content:space-between;align-items:center;margin-bottom:8px">
          <span style="font-size:14px;color:#d0d0e0">Accept jobs</span>
          <span id="job-mode-label" style="font-size:13px;color:var(--text-muted)"></span>
        </div>
        <div id="job-mode-btns" style="display:flex;gap:6px">
          <button onclick="setJobMode('always')" id="jm-always" style="flex:1;padding:8px 0;border-radius:6px;border:1px solid var(--border-active);background:var(--bg-card-hover);color:var(--text);cursor:pointer;font-size:13px;font-weight:600;transition:all .15s">Always</button>
          <button onclick="setJobMode('idle')" id="jm-idle" style="flex:1;padding:8px 0;border-radius:6px;border:1px solid var(--border-active);background:var(--bg-card-hover);color:var(--text);cursor:pointer;font-size:13px;font-weight:600;transition:all .15s">When idle</button>
          <button onclick="setJobMode('never')" id="jm-never" style="flex:1;padding:8px 0;border-radius:6px;border:1px solid var(--border-active);background:var(--bg-card-hover);color:var(--text);cursor:pointer;font-size:13px;font-weight:600;transition:all .15s">Never</button>
        </div>
        <div id="job-mode-hint" style="font-size:12px;color:var(--text-muted);margin-top:6px"></div>
      </div>

      <!-- Earnings stats -->
      <div style="margin-top:16px;padding-top:14px;border-top:1px solid var(--border)">
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
      <!-- Recent payouts -->
      <div id="payout-history" style="display:none;margin-top:12px;padding-top:12px;border-top:1px solid var(--border)">
        <div style="font-size:12px;color:var(--text-dim);font-weight:600;text-transform:uppercase;letter-spacing:0.5px;margin-bottom:6px">Recent Payouts</div>
        <div id="payout-list"></div>
      </div>

      <!-- Fee disclaimer -->
      <div style="margin-top:12px;font-size:12px;color:#888;line-height:1.5;padding:10px 12px;background:var(--bg);border-radius:6px">
        Lightning fees go to the Bitcoin network, not Owlrun. Our fee is always under 10%.
      </div>

      <!-- Advanced: ecash -->
      <div style="margin-top:14px">
        <details>
          <summary style="cursor:pointer;font-size:14px;color:#aaaabb;user-select:none">&#9656; Advanced: Withdraw as ecash (QR)</summary>
          <div style="margin-top:12px;padding:12px;background:var(--bg);border-radius:8px">
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
              <textarea id="claim-token" readonly style="width:100%;height:60px;background:var(--bg-card);color:var(--text);border:1px solid var(--border);border-radius:6px;padding:8px;font-size:12px;font-family:monospace;resize:vertical;box-sizing:border-box"></textarea>
              <div style="display:flex;justify-content:space-between;align-items:center;margin-top:4px">
                <span id="claim-amount" style="font-size:13px;color:#aaaabb"></span>
                <button onclick="copyToken()" style="padding:4px 10px;background:var(--bg-card-hover);color:var(--text);border:1px solid var(--border-active);border-radius:4px;cursor:pointer;font-size:12px">Copy</button>
              </div>
            </div>
            <div id="ecash-token-history" style="margin-top:10px"></div>
          </div>
        </details>
      </div>

      <div style="font-size:13px;color:var(--text-dim);margin-top:10px;padding-top:10px;border-top:1px solid var(--border)">
        Earnings auto-sent to your Lightning wallet. No action needed.
      </div>
    </div>
  </div>

  <!-- ═══ Row 1 right: Status, Earnings, Notifications ═══ -->
  <div class="card">
    <div class="card-title">Status</div>
    <div id="state-badge" class="state-badge">—</div>
    <div class="node-id" id="node-id"></div>
    <div class="node-id" id="provider-key" style="cursor:pointer;user-select:all" title="Click to copy"></div>
    <div style="margin-top:10px;border-top:1px solid var(--border);padding-top:10px">
      <div id="models-section"></div>
    </div>
    <div style="margin-top:8px;padding-top:8px;border-top:1px solid var(--border);display:flex;flex-wrap:wrap;gap:3px 0">
      <span class="legend-row"><span class="dot dot-green"></span>Earning</span>
      <span class="legend-row"><span class="dot dot-yellow"></span>Ready</span>
      <span class="legend-row"><span class="dot dot-blue"></span>No wallet</span>
      <span class="legend-row"><span class="dot dot-red"></span>Error</span>
      <span class="legend-row"><span class="dot dot-grey"></span>Paused</span>
    </div>
  </div>

  <div class="card">
    <div class="card-title">Earnings</div>
    <div class="earnings-big" id="total-sats">0 sats</div>
    <div style="font-size:18px;color:var(--text-dim);font-variant-numeric:tabular-nums;margin-top:2px" id="total-usd">~$0.00</div>
    <div style="margin-top:14px;padding-top:12px;border-top:1px solid var(--border)">
      <div class="stat">
        <span class="stat-label" style="font-size:17px">Today</span>
        <span class="stat-value" id="today-sats" style="color:var(--green);font-size:20px;font-weight:700">0 sats</span>
      </div>
      <div style="text-align:right;margin-top:2px">
        <span id="today-usd" style="font-size:14px;color:var(--text-muted)">~$0.00</span>
      </div>
    </div>
    <div style="margin-top:12px;font-size:12px;color:var(--text-muted);opacity:0.7">USD approximated at live BTC rate</div>
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
// Theme toggle — persist in localStorage
function toggleTheme() {
  var html = document.documentElement;
  var next = html.getAttribute('data-theme') === 'dark' ? 'light' : 'dark';
  html.setAttribute('data-theme', next);
  localStorage.setItem('owlrun-theme', next);
}
(function() {
  var saved = localStorage.getItem('owlrun-theme');
  if (saved) document.documentElement.setAttribute('data-theme', saved);
})();

function escapeHtml(s) { var d = document.createElement('div'); d.textContent = s; return d.innerHTML; }
function fmt2(n) {
  if (n < 0.01) return '$' + n.toFixed(6);
  if (n < 1) return '$' + n.toFixed(4);
  return '$' + n.toFixed(2);
}
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
  var verEl = document.getElementById('version');
  verEl.textContent = 'v' + d.version;
  verEl.style.cssText = 'opacity:0.7;font-weight:500;font-size:14px;background:var(--bg-card-hover);padding:2px 8px;border-radius:4px;border:1px solid var(--border);margin-left:4px';
  document.getElementById('node-id').textContent = 'node ' + d.node_id;

  // Provider key (click to copy)
  var pkEl = document.getElementById('provider-key');
  if (d.provider_key) {
    pkEl.textContent = 'key ' + d.provider_key;
    pkEl.onclick = function() {
      navigator.clipboard.writeText(d.provider_key).then(function() {
        pkEl.textContent = 'copied!';
        setTimeout(function() { pkEl.textContent = 'key ' + d.provider_key; }, 1500);
      });
    };
  }

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
  var badgeHtml = '<span class="dot ' + dotClass + '"></span>' + label;
  if (d.state === 'error' && d.error_detail) {
    badgeHtml += '<div style="margin-top:10px;padding:10px 12px;background:var(--wallet-warn-bg);border:1px solid #ef4444;border-radius:8px;font-size:13px;color:#fca5a5;line-height:1.5;font-weight:400">' + escapeHtml(d.error_detail) + '</div>';
  }
  document.getElementById('state-badge').innerHTML = badgeHtml;

  // Earnings: sats as hero number, USD below
  // Use gateway's earned_sats fields when available (authoritative, 1-sat minimum applied).
  // Fallback: max(usd→sats conversion, jobs count) since every job earns at least 1 sat (Decision #50).
  var btcRate = (d.btc_price && d.btc_price.live_usd) ? d.btc_price.live_usd : 0;
  function usdToSatsRaw(usd) { return btcRate > 0 ? usd / btcRate * 100000000 : 0; }
  var todaySatsRaw = d.gateway.earned_today_sats || Math.max(usdToSatsRaw(d.earnings.today_usd), d.gateway.jobs_today || 0);
  var totalSatsRaw = d.gateway.earned_total_sats || Math.max(usdToSatsRaw(d.earnings.total_usd), todaySatsRaw);
  function fmtSatsEarnings(raw) {
    if (raw === 0) return '0 sats';
    return Math.round(raw).toLocaleString() + ' sats';
  }
  document.getElementById('total-sats').textContent = fmtSatsEarnings(totalSatsRaw);
  document.getElementById('total-usd').textContent = '~' + fmt2(d.earnings.total_usd);
  document.getElementById('today-sats').textContent = fmtSatsEarnings(todaySatsRaw);
  document.getElementById('today-usd').textContent = '~' + fmt2(d.earnings.today_usd);

  const g = d.gpu;
  document.getElementById('gpu-name').textContent  = g.name || 'No GPU detected';
  document.getElementById('util-pct').textContent  = g.util_pct + '%';
  document.getElementById('util-bar').style.width  = g.util_pct + '%';
  document.getElementById('vram-free').textContent = fmtMB(g.vram_free_mb);
  document.getElementById('temp').textContent      = g.temp_c ? g.temp_c + ' °C' : '—';
  document.getElementById('power').textContent     = g.power_w ? g.power_w.toFixed(0) + ' W' : '—';

  const utilBar = document.getElementById('util-bar');
  utilBar.className = 'bar-fill ' + (g.util_pct > 80 ? 'bar-red' : g.util_pct > 50 ? 'bar-yellow' : 'bar-green');

  // Model picker — interactive
  var ms = document.getElementById('models-section');
  var avail = d.available_models || [];
  var pulling = d.pulling || false;
  if (avail.length === 0) {
    ms.innerHTML = '<div class="stat"><span class="stat-label">Model</span><span class="stat-value">—</span></div>';
  } else {
    // Split into fits vs slow, sort: fits first (installed first within each group)
    var fitsModels = avail.filter(function(m) { return m.fits; });
    var slowModels = avail.filter(function(m) { return !m.fits; });
    fitsModels.sort(function(a,b) { return (b.installed?1:0) - (a.installed?1:0) || (b.active?1:0) - (a.active?1:0); });
    slowModels.sort(function(a,b) { return (b.installed?1:0) - (a.installed?1:0); });

    var diskInfo = d.disk ? d.disk.free_gb.toFixed(1) + ' GB free' : '';
    var html = '<div style="font-size:11px;color:var(--text-muted);margin-bottom:6px;display:flex;justify-content:space-between"><span>Models</span><span>' + diskInfo + '</span></div>';
    if (pulling) html += '<div id="pull-progress" style="margin-bottom:8px;padding:8px 10px;border:1px solid #f7931a;border-radius:8px;background:var(--wallet-warn-bg);font-size:12px;color:var(--accent)"><span class="spinner"></span> Downloading…</div>';

    var registeredModels = d.models || [];
    function renderModelCard(m) {
      var pricing = (d.all_model_pricing && d.all_model_pricing[m.tag]) || null;
      var isActive = m.active;
      var isRegistered = registeredModels.indexOf(m.tag) >= 0;
      var isDark = document.documentElement.getAttribute('data-theme') === 'dark';
      var border = isActive ? '#4ade80' : isRegistered ? (isDark?'#2a5a3a':'#b6e8c8') : m.installed ? (isDark?'#2a2a38':'#e0e0ea') : (isDark?'#1a1a24':'#eee');
      var bg = isActive ? (isDark?'#1a2a1a':'#ecfdf5') : isRegistered ? (isDark?'#162218':'#f0fdf4') : (isDark?'#141420':'#fafafa');
      var opacity = m.installed || m.fits ? '1' : '0.5';
      var h = '<div style="display:flex;align-items:center;justify-content:space-between;padding:8px 10px;margin-bottom:4px;border:1px solid ' + border + ';border-radius:8px;background:' + bg + ';opacity:' + opacity + '">';
      h += '<div style="display:flex;align-items:center;gap:8px;flex:1;min-width:0">';
      h += '<div style="width:8px;height:8px;border-radius:50%;flex-shrink:0;background:' + (isActive ? '#4ade80' : isRegistered ? '#22c55e' : m.installed ? '#555' : '#333') + '"></div>';
      h += '<div style="min-width:0">';
      h += '<div style="font-size:13px;color:#e8e8f0;font-weight:' + (isActive ? '600' : '400') + ';white-space:nowrap;overflow:hidden;text-overflow:ellipsis">' + escapeHtml(m.tag) + '</div>';
      var meta = m.vram_gb > 0 ? m.vram_gb + ' GB VRAM' : 'CPU';
      if (pricing) meta += ' &middot; $' + pricing.per_m_output_usd.toFixed(2) + '/M';
      h += '<div style="font-size:10px;color:var(--text-muted)">' + meta + '</div>';
      h += '</div></div>';
      // Action buttons + badges
      if (isActive) {
        h += '<span style="font-size:9px;background:#4ade80;color:#000;padding:2px 6px;border-radius:4px;font-weight:700;flex-shrink:0">ACTIVE</span>';
      } else if (isRegistered && m.installed && !pulling) {
        h += '<div style="display:flex;gap:4px;align-items:center;flex-shrink:0">';
        h += '<span style="font-size:9px;background:#22c55e33;color:#4ade80;padding:2px 6px;border-radius:4px;font-weight:600">AVAILABLE</span>';
        h += '<button onclick="switchModel(\'' + escapeHtml(m.tag) + '\')" style="font-size:10px;background:var(--bg-card-hover);color:var(--text);border:1px solid var(--border-active);border-radius:4px;padding:2px 8px;cursor:pointer">Activate</button>';
        h += '<button onclick="removeModel(\'' + escapeHtml(m.tag) + '\')" style="font-size:10px;background:var(--bg);color:#ef4444;border:1px solid #ef444444;border-radius:4px;padding:2px 6px;cursor:pointer" title="Remove model">✕</button>';
        h += '</div>';
      } else if (m.installed && !pulling) {
        h += '<div style="display:flex;gap:4px;flex-shrink:0">';
        h += '<button onclick="switchModel(\'' + escapeHtml(m.tag) + '\')" style="font-size:10px;background:var(--bg-card-hover);color:var(--text);border:1px solid var(--border-active);border-radius:4px;padding:2px 8px;cursor:pointer">Activate</button>';
        h += '<button onclick="removeModel(\'' + escapeHtml(m.tag) + '\')" style="font-size:10px;background:var(--bg);color:#ef4444;border:1px solid #ef444444;border-radius:4px;padding:2px 6px;cursor:pointer" title="Remove model">✕</button>';
        h += '</div>';
      } else if (!m.installed && !pulling) {
        h += '<button id="dl-' + escapeHtml(m.tag).replace(/[:.]/g,'_') + '" onclick="pullModel(\'' + escapeHtml(m.tag) + '\',' + m.vram_gb + ')" style="font-size:10px;background:var(--bg);color:var(--accent);border:1px solid rgba(245,158,11,0.27);border-radius:4px;padding:2px 8px;cursor:pointer;flex-shrink:0">Download</button>';
      } else {
        h += '<span style="font-size:10px;color:var(--text-muted);flex-shrink:0"><span class="spinner"></span></span>';
      }
      h += '</div>';
      return h;
    }

    fitsModels.forEach(function(m) { html += renderModelCard(m); });

    // Slow models — hidden by default, toggle to show
    if (slowModels.length > 0) {
      var showSlow = document.getElementById('show-slow-check');
      var slowChecked = showSlow ? showSlow.checked : false;
      html += '<label style="display:flex;align-items:center;gap:6px;font-size:11px;color:var(--text-muted);margin:8px 0 4px;cursor:pointer">';
      html += '<input type="checkbox" id="show-slow-check" onchange="poll()" ' + (slowChecked ? 'checked' : '') + ' style="accent-color:var(--accent)">';
      html += 'Show ' + slowModels.length + ' larger models (may be slow on this machine)</label>';
      if (slowChecked) {
        slowModels.forEach(function(m) { html += renderModelCard(m); });
      }
    }

    ms.innerHTML = html;
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
    // Job mode
    if (d.job_mode) { applyJobModeUI(d.job_mode); }

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
    // Payout history (Lightning withdrawals)
    var phEl = document.getElementById('payout-history');
    var plEl = document.getElementById('payout-list');
    if (sw.withdraw_history && sw.withdraw_history.length > 0) {
      phEl.style.display = '';
      plEl.innerHTML = sw.withdraw_history.slice(0, 3).map(function(w) {
        var ts = new Date(w.timestamp);
        var timeStr = isNaN(ts) ? w.timestamp : ts.toLocaleString();
        var hashShort = w.payment_hash ? w.payment_hash.substring(0, 8) + '…' + w.payment_hash.substring(w.payment_hash.length - 6) : '';
        var explorerUrl = w.payment_hash ? 'https://mempool.space/lightning/payment/' + w.payment_hash : '';
        var h = '<div class="payout-item">';
        h += '<div><span class="payout-amount">&#9889; ' + fmtSats(w.amount_sats) + '</span><div class="payout-time">' + timeStr + '</div></div>';
        if (explorerUrl) h += '<a class="payout-link" href="' + explorerUrl + '" target="_blank" rel="noopener">' + hashShort + '</a>';
        h += '</div>';
        return h;
      }).join('');
    } else {
      phEl.style.display = 'none';
    }
    // Ecash advanced section
    document.getElementById('ecash-local-sats').textContent = fmtSats(sw.local_sats);
    document.getElementById('ecash-proof-count').textContent = sw.proof_count || 0;
    // Token history
    var histEl = document.getElementById('ecash-token-history');
    if (sw.token_history && sw.token_history.length > 0) {
      histEl.innerHTML = '<div style="font-size:13px;color:#aaaabb;margin-bottom:6px">Recent tokens:</div>' +
        sw.token_history.slice(0, 5).map(function(t) {
          return '<div style="font-size:12px;color:var(--text-muted);margin-bottom:4px;word-break:break-all">' +
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
      n.style.cssText = 'color:var(--text-muted);font-size:14px;font-style:italic;padding:8px 0;text-align:center';
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
      var title = b.title ? '<strong>' + escapeHtml(b.title) + '</strong> — ' : '';
      return '<div class="broadcast-item"><span class="broadcast-msg">' + title + escapeHtml(b.message) + '</span><span class="broadcast-time">' + ts + '</span></div>';
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

async function setJobMode(mode) {
  try {
    var r = await fetch('/api/set-job-mode', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({mode:mode})});
    if (r.ok) { applyJobModeUI(mode); poll(); }
  } catch(e) {}
}

function applyJobModeUI(mode) {
  var btns = {always: document.getElementById('jm-always'), idle: document.getElementById('jm-idle'), never: document.getElementById('jm-never')};
  var hints = {always: 'Earning whenever connected', idle: 'Earning only when you are away', never: 'Not accepting any jobs'};
  for (var k in btns) {
    if (k === mode) {
      btns[k].style.background = '#f7931a';
      btns[k].style.color = '#fff';
      btns[k].style.borderColor = '#f7931a';
    } else {
      btns[k].style.background = 'var(--bg-card-hover)';
      btns[k].style.color = 'var(--text)';
      btns[k].style.borderColor = 'var(--border-active)';
    }
  }
  document.getElementById('job-mode-hint').textContent = hints[mode] || '';
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

var dimColor = getComputedStyle(document.documentElement).getPropertyValue('--text-dim').trim();
var gridColor = getComputedStyle(document.documentElement).getPropertyValue('--border').trim();

function makeChartOpts(yTickCb) {
  return {
    responsive: true, maintainAspectRatio: false,
    animation: false, resizeDelay: 0,
    interaction: { mode: 'index', intersect: false },
    plugins: { legend: { display: true, labels: { color: dimColor, font: { size: 12 }, boxWidth: 14, padding: 12 } } },
    scales: {
      x: { ticks: { color: dimColor, font: { size: 12 }, maxRotation: 45 }, grid: { color: gridColor } },
      y: { beginAtZero: true, position: 'left', ticks: { color: dimColor, font: { size: 12 }, callback: yTickCb || function(v) { return v; } }, grid: { color: gridColor } },
      y1: { beginAtZero: true, position: 'right', ticks: { color: dimColor, font: { size: 12 }, callback: yTickCb || function(v) { return v; } }, grid: { drawOnChartArea: false } }
    }
  };
}

function smartUsd(v) {
  if (v === 0) return '$0';
  if (Math.abs(v) < 0.001) return '$' + v.toFixed(6);
  if (Math.abs(v) < 0.01) return '$' + v.toFixed(4);
  if (Math.abs(v) < 1) return '$' + v.toFixed(3);
  return '$' + v.toFixed(2);
}

function initCharts() {
  jobsChart = new Chart(document.getElementById('chart-jobs').getContext('2d'), {
    type: 'line',
    data: { labels: [], datasets: [
      { label: 'Per period', data: [], borderColor: '#eab308', backgroundColor: '#eab30833', borderWidth: 2, pointRadius: 2, tension: 0.3, fill: true, yAxisID: 'y' },
      { label: 'Cumulative', data: [], borderColor: '#3b82f6', backgroundColor: 'transparent', borderWidth: 2, pointRadius: 1, borderDash: [4,3], tension: 0.3, yAxisID: 'y1' }
    ] },
    options: makeChartOpts()
  });
  earningsChart = new Chart(document.getElementById('chart-earnings').getContext('2d'), {
    type: 'line',
    data: { labels: [], datasets: [
      { label: 'Per period', data: [], borderColor: '#22c55e', backgroundColor: '#22c55e33', borderWidth: 2, pointRadius: 2, tension: 0.3, fill: true, yAxisID: 'y' },
      { label: 'Cumulative', data: [], borderColor: '#f59e0b', backgroundColor: 'transparent', borderWidth: 2, pointRadius: 1, borderDash: [4,3], tension: 0.3, yAxisID: 'y1' }
    ] },
    options: makeChartOpts(smartUsd)
  });
}

async function fetchHistory() {
  if (!chartReady) return;
  try {
    var r = await fetch('/api/history?period=' + currentPeriod);
    var d = await r.json();
    var labels = d.buckets.map(function(b) { return b.label; });
    var dailyJobs = d.buckets.map(function(b) { return b.jobs; });
    var dailyEarned = d.buckets.map(function(b) { return b.earned; });
    var cumJobs = [], cumEarned = [], sj = 0, se = 0;
    for (var i = 0; i < dailyJobs.length; i++) {
      sj += dailyJobs[i]; cumJobs.push(sj);
      se += dailyEarned[i]; cumEarned.push(se);
    }
    jobsChart.data.labels = labels;
    jobsChart.data.datasets[0].data = dailyJobs;
    jobsChart.data.datasets[1].data = cumJobs;
    jobsChart.update('none');
    earningsChart.data.labels = labels;
    earningsChart.data.datasets[0].data = dailyEarned;
    earningsChart.data.datasets[1].data = cumEarned;
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

async function removeModel(tag) {
  if (!confirm('Remove ' + tag + '?\n\nThis will delete the model from disk and free up space.\nYou can re-download it later.')) return;
  try {
    var resp = await fetch('/api/remove-model', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({model:tag})});
    var data = await resp.json();
    if (!resp.ok) { alert('Error: ' + (data.error || 'unknown')); return; }
    poll();
  } catch(e) { alert('Failed: ' + e.message); }
}

async function switchModel(tag) {
  if (!confirm('Switch active model to ' + tag + '?\n\nThis will reload the model into memory and re-register with the gateway.')) return;
  try {
    var resp = await fetch('/api/switch-model', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({model:tag})});
    var data = await resp.json();
    if (!resp.ok) { alert('Error: ' + (data.error || 'unknown')); return; }
    poll(); // refresh immediately
  } catch(e) { alert('Failed: ' + e.message); }
}

async function pullModel(tag, vramGb) {
  // Show spinner on download button while checking size
  var btnId = 'dl-' + tag.replace(/[:.]/g, '_');
  var dlBtn = document.getElementById(btnId);
  if (dlBtn) { dlBtn.disabled = true; dlBtn.innerHTML = '<span class="spinner"></span> Checking…'; }

  var st = await (await fetch('/api/status')).json();
  var diskFree = st.disk ? st.disk.free_gb : 0;
  var diskTotal = st.disk ? st.disk.total_gb : 0;

  // Step 1: Ask Ollama registry for real size (10s timeout on server)
  var sizeMb = 0;
  var sizeSource = 'estimate';
  try {
    var sizeResp = await fetch('/api/model-size?model=' + encodeURIComponent(tag));
    var sizeData = await sizeResp.json();
    if (sizeData.size_mb > 0) { sizeMb = sizeData.size_mb; sizeSource = 'registry'; }
  } catch(e) {}
  if (dlBtn) { dlBtn.disabled = false; dlBtn.textContent = 'Download'; }

  // Step 2: Fallback estimate if registry failed
  if (sizeMb === 0) {
    sizeMb = Math.max(1024, Math.round((vramGb || 1) * 1.5 * 1024)); // vram * 1.5 GB, min 1 GB
    sizeSource = 'estimate';
  }

  var sizeGb = (sizeMb / 1024).toFixed(1);
  var usedAfter = (diskTotal - diskFree) + (sizeMb / 1024);
  var usagePct = diskTotal > 0 ? (usedAfter / diskTotal * 100) : 100;

  // Step 3: Abort if would exceed 90% disk usage
  if (usagePct > 90) {
    alert('Not enough disk space!\n\n' +
      'Model size: ~' + sizeGb + ' GB' + (sizeSource === 'estimate' ? ' (estimated)' : '') + '\n' +
      'Disk free: ' + diskFree.toFixed(1) + ' GB / ' + diskTotal.toFixed(0) + ' GB total\n' +
      'After download: ' + usagePct.toFixed(0) + '% used\n\n' +
      'Download aborted — disk usage would exceed 90%.\n' +
      'Free up space or remove unused models first.');
    return;
  }

  if (!confirm('Download ' + tag + '?\n\n' +
    'Model size: ~' + sizeGb + ' GB' + (sizeSource === 'estimate' ? ' (estimated)' : '') + '\n' +
    'Disk free: ' + diskFree.toFixed(1) + ' GB / ' + diskTotal.toFixed(0) + ' GB total\n' +
    'After download: ~' + usagePct.toFixed(0) + '% used\n\n' +
    'This may take a few minutes depending on your connection.')) return;

  // Show progress in the models section
  var progDiv = document.getElementById('pull-progress');
  if (!progDiv) {
    progDiv = document.createElement('div');
    progDiv.id = 'pull-progress';
    progDiv.style.cssText = 'margin-bottom:8px;padding:8px 10px;border:1px solid #f7931a;border-radius:8px;background:var(--wallet-warn-bg);font-size:12px;color:var(--accent)';
    var ms = document.getElementById('models-section');
    ms.insertBefore(progDiv, ms.children[1]);
  }
  progDiv.innerHTML = '<span class="spinner"></span> Starting download…';

  try {
    var resp = await fetch('/api/pull-model', {method:'POST', headers:{'Content-Type':'application/json'}, body:JSON.stringify({model:tag})});
    if (!resp.ok) {
      var err = await resp.json();
      alert('Error: ' + (err.error || 'unknown'));
      return;
    }
    var reader = resp.body.getReader();
    var decoder = new TextDecoder();
    var buf = '';
    while (true) {
      var result = await reader.read();
      if (result.done) break;
      buf += decoder.decode(result.value, {stream:true});
      var lines = buf.split('\n\n');
      buf = lines.pop();
      for (var i = 0; i < lines.length; i++) {
        var line = lines[i].replace(/^data: /, '');
        if (!line) continue;
        try {
          var ev = JSON.parse(line);
          if (ev.error) { progDiv.textContent = 'Error: ' + ev.error; progDiv.style.borderColor = '#ef4444'; return; }
          if (ev.status === 'done') { progDiv.textContent = 'Download complete!'; progDiv.style.borderColor = '#4ade80'; progDiv.style.color = '#4ade80';
            setTimeout(function() { poll(); }, 1000); return; }
          if (ev.total > 0) {
            var pct = Math.round(ev.completed / ev.total * 100);
            progDiv.innerHTML = ev.status + ' <b>' + pct + '%</b> (' + (ev.completed/1048576).toFixed(0) + '/' + (ev.total/1048576).toFixed(0) + ' MB)';
          } else {
            progDiv.textContent = ev.status || 'Downloading…';
          }
        } catch(e) {}
      }
    }
    progDiv.textContent = 'Download complete!';
    progDiv.style.borderColor = '#4ade80'; progDiv.style.color = '#4ade80';
    setTimeout(function() { poll(); }, 1000);
  } catch(e) { progDiv.textContent = 'Failed: ' + e.message; progDiv.style.borderColor = '#ef4444'; }
}
</script>
</body>
</html>`
