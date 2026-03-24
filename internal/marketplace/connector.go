// Package marketplace manages the node's connection to the Owlrun Gateway.
// See gateway.go for the GatewayConnector and router.go for the Router.
package marketplace

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"sync"
	"time"

	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

const (
	heartbeatInterval   = 30 * time.Second
	jobAcceptTimeout    = 2 * time.Second
	reconnectDelayInit  = 1 * time.Second
	reconnectDelayMax   = 30 * time.Second
	wsPath              = "/v1/gateway/ws"
	jobFetchPath = "/v1/gateway/jobs/%s/proxy/request"
)

// StatsFunc is called by the connector to get live GPU stats for heartbeats.
type StatsFunc func() (utilPct int, vramFreeMB int, tempC int, powerW float64)

// JobCompleteFunc is called when the gateway confirms a job has been billed.
type JobCompleteFunc func(model string, tokens int, earnedUSD float64)

// ModelPricing holds per-model pricing from the gateway's /v1/models endpoint.
type ModelPricing struct {
	PerMInputUSD  float64 `json:"per_m_input_usd"`
	PerMOutputUSD float64 `json:"per_m_output_usd"`
}

// GatewayStats is the last heartbeat_ack received from the gateway.
// Safe to read from any goroutine via Connector.GatewayStats().
type GatewayStats struct {
	Connected        bool
	Status           string
	JobsToday        int
	TokensToday      int
	EarnedTodayUSD   float64
	EarnedTodaySats  int64
	EarnedTotalSats  int64
	QueueDepthGlobal int
	BtcPrice         BtcPrice
	Broadcasts       []Broadcast
	BalanceSats      int64
	Models           []string                    // all registered model tags
	ModelPricing     *ModelPricing               // primary model (backward compat)
	AllModelPricing  map[string]*ModelPricing     // pricing per model
	WithdrawHistory  []WithdrawRecord            // last N Lightning payouts from gateway
}

// wsMsg is the generic WebSocket message envelope used for all control traffic.
type wsMsg struct {
	Type   string `json:"type"`
	NodeID string `json:"node_id,omitempty"`

	// Job assignment (gateway → node)
	JobID          string `json:"job_id,omitempty"`
	Model          string `json:"model,omitempty"`
	VRAMRequiredMB int    `json:"vram_required_mb,omitempty"`
	BuyerRegion    string `json:"buyer_region,omitempty"`

	// Heartbeat (node → gateway)
	GPUUtilPct    int     `json:"gpu_util_pct,omitempty"`
	VRAMFreeMB    int     `json:"vram_free_mb,omitempty"`
	TempC         int     `json:"temp_c,omitempty"`
	PowerW        float64 `json:"power_w,omitempty"`
	QueueDepth    int     `json:"queue_depth,omitempty"`
	EarningState  string  `json:"earning_state,omitempty"`

	// Heartbeat ACK (gateway → node)
	Status           string  `json:"status,omitempty"`
	JobsToday        int     `json:"jobs_today,omitempty"`
	TokensToday      int     `json:"tokens_today,omitempty"`
	EarnedTodayUSD   float64 `json:"earned_today_usd,omitempty"`
	EarnedTodaySats  int64   `json:"earned_today_sats,omitempty"`
	EarnedTotalSats  int64   `json:"earned_total_sats,omitempty"`
	QueueDepthGlobal int     `json:"queue_depth_global,omitempty"`
	BtcLiveUsd       float64 `json:"btc_live_usd,omitempty"`
	BtcYesterdayFix  float64 `json:"btc_yesterday_fix,omitempty"`
	BtcDailyAvg      float64 `json:"btc_daily_avg,omitempty"`
	BtcWeeklyAvg     float64 `json:"btc_weekly_avg,omitempty"`
	BtcPriceStatus   string  `json:"btc_price_status,omitempty"`
	BalanceSats      int64   `json:"balance_sats,omitempty"`

	// Job complete (gateway → node)
	Tokens    int     `json:"tokens,omitempty"`
	EarnedUSD float64 `json:"earned_usd,omitempty"`

	// Reject (node → gateway)
	Reason string `json:"reason,omitempty"`

	// Proxy streaming (node → gateway, WS proxy)
	Data string `json:"data,omitempty"` // Ollama response chunk (UTF-8)

	// Broadcasts (gateway → node, in heartbeat_ack)
	Broadcasts []Broadcast `json:"broadcasts,omitempty"`

	// Withdraw history (gateway → node, in heartbeat_ack)
	WithdrawHistory []WithdrawRecord `json:"withdraw_history,omitempty"`
}

// Broadcast is a gateway notification message.
type Broadcast struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Message   string `json:"message"`
	Severity  string `json:"severity"`
	Timestamp string `json:"created_at"`
}

// WithdrawRecord is a Lightning payout sent by the gateway auto-redeemer.
type WithdrawRecord struct {
	AmountSats  int64  `json:"amount_sats"`
	PaymentHash string `json:"payment_hash"`
	Timestamp   string `json:"timestamp"`
}


// BtcPrice holds the gateway's BTC/USD pricing snapshot.
type BtcPrice struct {
	LiveUsd    float64 `json:"live_usd"`
	YesterdayFix float64 `json:"yesterday_fix"`
	DailyAvg   float64 `json:"daily_avg"`
	WeeklyAvg  float64 `json:"weekly_avg"`
	Status     string  `json:"status"`
}
// Connector manages the persistent WebSocket connection to the Owlrun Gateway.
// It handles: registration, heartbeat, job assignment, proxy initiation, and
// relaying gateway stats back to the dashboard.
type Connector struct {
	proxyBase   string // e.g. "https://node.owlrun.me" — bypasses Cloudflare for HTTP/2 full-duplex proxy; falls back to gatewayBase if empty
	gatewayBase string // e.g. "https://node.owlrun.me"
	apiKey      string
	nodeID      string
	wallet      string
	regPayload  []byte // cached JSON from the last Register call

	getStats    StatsFunc
	onComplete  JobCompleteFunc
	onConnect       func()              // called when WS connects to gateway
	onBalanceUpdate func(balanceSats int64) // called on heartbeat_ack with balance_sats

	// gatewayClient is used for the gateway proxy POST (requires HTTP/2).
	// If nil, http.DefaultClient is used (works in production behind Caddy+TLS).
	// Override in tests to inject an HTTP/2-capable TLS test client.
	gatewayClient *http.Client

	// ollamaBase is the Ollama API base URL. Defaults to http://localhost:11434.
	// Override in tests to point at a fake Ollama server.
	ollamaBase string

	mu              sync.RWMutex
	models          map[string]bool            // all registered models (set for O(1) lookup)
	conn            *websocket.Conn
	gatewayStats    GatewayStats
	modelPricing    *ModelPricing              // primary model pricing (backward compat)
	allModelPricing map[string]*ModelPricing   // pricing for all registered models
	queueDepth      int

	cancelFn context.CancelFunc
	running  bool
}

// gwClient returns the HTTP client for gateway proxy requests.
func (c *Connector) gwClient() *http.Client {
	if c.gatewayClient != nil {
		return c.gatewayClient
	}
	return http.DefaultClient
}

// proxyBaseURL returns the base URL to use for proxy job connections.
// Uses proxyBase if set (direct-to-VPS, bypasses Cloudflare); falls back to gatewayBase.
func (c *Connector) proxyBaseURL() string {
	if c.proxyBase != "" {
		return c.proxyBase
	}
	return c.gatewayBase
}

// ollamaURL returns the Ollama base URL.
func (c *Connector) ollamaURL() string {
	if c.ollamaBase != "" {
		return c.ollamaBase
	}
	return "http://localhost:11434"
}

// NewConnector creates a Connector. Call Connect() to start the WS lifecycle.
func NewConnector(
	gatewayBase, proxyBase, apiKey, nodeID, wallet string,
	getStats StatsFunc,
	onComplete JobCompleteFunc,
	onConnect func(),
	onBalanceUpdate func(balanceSats int64),
) *Connector {
	return &Connector{
		gatewayBase:     gatewayBase,
		proxyBase:       proxyBase,
		apiKey:          apiKey,
		nodeID:          nodeID,
		wallet:          wallet,
		getStats:        getStats,
		onComplete:      onComplete,
		onConnect:       onConnect,
		onBalanceUpdate: onBalanceUpdate,
	}
}

// SetRegistration stores the node registration payload so the connector can
// POST /register before opening the WS. Must be called before Connect().
func (c *Connector) SetRegistration(payload []byte) {
	c.mu.Lock()
	c.regPayload = payload
	c.mu.Unlock()
}

// SetModels updates the set of models this node can serve.
func (c *Connector) SetModels(models []string) {
	m := make(map[string]bool, len(models))
	for _, tag := range models {
		m[tag] = true
	}
	c.mu.Lock()
	c.models = m
	c.mu.Unlock()
}

// SetModel updates a single model (convenience wrapper for SetModels).
func (c *Connector) SetModel(model string) {
	c.SetModels([]string{model})
}

// GatewayStats returns the latest heartbeat_ack snapshot from the gateway.
func (c *Connector) Stats() GatewayStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	s := c.gatewayStats
	s.ModelPricing = c.modelPricing
	s.AllModelPricing = c.allModelPricing
	// Build models list from the set.
	s.Models = make([]string, 0, len(c.models))
	for tag := range c.models {
		s.Models = append(s.Models, tag)
	}
	return s
}

// Connect starts the registration + WS lifecycle in a background goroutine.
// Safe to call multiple times — a second call is a no-op if already running.
func (c *Connector) Connect() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.running {
		return
	}
	c.running = true
	ctx, cancel := context.WithCancel(context.Background())
	c.cancelFn = cancel
	go c.runLoop(ctx)
}

// Disconnect tears down the WS connection and stops all background goroutines.
func (c *Connector) Disconnect() {
	c.mu.Lock()
	if !c.running {
		c.mu.Unlock()
		return
	}
	c.running = false
	cancel := c.cancelFn
	conn := c.conn
	c.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if conn != nil {
		conn.Close(websocket.StatusGoingAway, "node disconnecting")
	}
	log.Printf("owlrun: gateway: disconnected")
}

// Reconnect tears down the current WS session and immediately reconnects
// with the latest registration payload. Used when config changes (e.g.
// redeem threshold, lightning address) need to take effect without a
// full process restart.
func (c *Connector) Reconnect() {
	c.mu.RLock()
	wasRunning := c.running
	c.mu.RUnlock()
	if !wasRunning {
		return
	}
	log.Printf("owlrun: gateway: reconnecting to apply config changes")
	c.Disconnect()
	// Small delay to let the old WS close cleanly on the server side.
	time.Sleep(500 * time.Millisecond)
	c.Connect()
}

// runLoop keeps the WS connection alive, reconnecting on failure.
// Uses exponential backoff: 1s → 2s → 4s → … capped at 30s.
// Resets to 1s after a successful WS session so reconnects are fast after
// transient drops (the common case under load).
func (c *Connector) runLoop(ctx context.Context) {
	delay := reconnectDelayInit
	for {
		regErr := c.register(ctx)
		if regErr != nil {
			log.Printf("owlrun: gateway: register: %v — retry in %s", regErr, delay)
		} else {
			err := c.runSession(ctx)
			if ctx.Err() != nil {
				return // clean shutdown
			}
			if err != nil {
				log.Printf("owlrun: gateway: session ended: %v — reconnecting in %s", err, reconnectDelayInit)
			}
			// Session was established (WS connected) then dropped —
			// reset backoff so we reconnect in 1s, not 30s.
			delay = reconnectDelayInit
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}

		if regErr != nil {
			// Only escalate backoff on repeated registration failures.
			delay *= 2
			if delay > reconnectDelayMax {
				delay = reconnectDelayMax
			}
		}
	}
}

// register POSTs the node payload to /gateway/register.
func (c *Connector) register(ctx context.Context) error {
	c.mu.RLock()
	payload := c.regPayload
	c.mu.RUnlock()

	if len(payload) == 0 {
		return nil // no payload set yet; skip
	}

	url := c.gatewayBase + "/v1/gateway/register"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("POST register: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("register HTTP %d: %s", resp.StatusCode, b)
	}
	log.Printf("owlrun: gateway: registered node %s", c.nodeID)
	return nil
}

// fetchModelPricing queries the gateway's public /v1/models endpoint to get
// pricing for the currently loaded model. Non-critical — failures are logged
// and silently ignored.
func (c *Connector) fetchModelPricing(ctx context.Context) {
	c.mu.RLock()
	// Grab all registered model tags for pricing lookup.
	var modelTags []string
	for tag := range c.models {
		modelTags = append(modelTags, tag)
	}
	c.mu.RUnlock()
	if len(modelTags) == 0 {
		return
	}

	url := DefaultAPIBase + "/v1/models"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("owlrun: gateway: fetch model pricing: %v", err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}

	var result struct {
		Data []struct {
			ID      string        `json:"id"`
			Pricing *ModelPricing `json:"pricing,omitempty"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return
	}

	// Collect pricing for all registered models.
	registered := make(map[string]bool, len(modelTags))
	for _, t := range modelTags {
		registered[t] = true
	}
	allPricing := make(map[string]*ModelPricing)
	var primary *ModelPricing
	for _, m := range result.Data {
		if registered[m.ID] && m.Pricing != nil {
			p := *m.Pricing // copy
			allPricing[m.ID] = &p
			if primary == nil {
				primary = &p
			}
			log.Printf("owlrun: gateway: model %s pricing: $%.3f/$%.3f per M tokens (in/out)", m.ID, m.Pricing.PerMInputUSD, m.Pricing.PerMOutputUSD)
		}
	}
	if len(allPricing) > 0 {
		c.mu.Lock()
		c.modelPricing = primary
		c.allModelPricing = allPricing
		c.mu.Unlock()
	}
}

// runSession opens the WS and runs until it closes or ctx is cancelled.
func (c *Connector) runSession(ctx context.Context) error {
	wsURL := "wss" + c.gatewayBase[len("https"):] + wsPath + "?api_key=" + c.apiKey
	if c.gatewayBase[:5] == "http:" {
		wsURL = "ws" + c.gatewayBase[len("http"):] + wsPath + "?api_key=" + c.apiKey
	}
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("ws dial: %w", err)
	}
	c.mu.Lock()
	c.conn = conn
	c.gatewayStats.Connected = true
	c.mu.Unlock()

	defer func() {
		conn.Close(websocket.StatusNormalClosure, "")
		c.mu.Lock()
		c.conn = nil
		c.gatewayStats.Connected = false
		c.mu.Unlock()
	}()

	log.Printf("owlrun: gateway: WS connected")

	// Best-effort fetch of model pricing after WS connects.
	go c.fetchModelPricing(ctx)

	if c.onConnect != nil {
		c.onConnect()
	}

	// Send first heartbeat immediately (don't wait 30s for ticker).
	c.sendHeartbeat(ctx, conn)

	// Heartbeat ticker.
	hbCtx, hbCancel := context.WithCancel(ctx)
	defer hbCancel()
	go c.heartbeatLoop(hbCtx, conn)

	// Read loop (blocks until WS closes).
	return c.readLoop(ctx, conn)
}

// heartbeatLoop sends a heartbeat message every 30s.
func (c *Connector) heartbeatLoop(ctx context.Context, conn *websocket.Conn) {
	ticker := time.NewTicker(heartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.sendHeartbeat(ctx, conn)
		}
	}
}

func (c *Connector) sendHeartbeat(ctx context.Context, conn *websocket.Conn) {
	utilPct, vramFree, tempC, powerW := c.getStats()
	c.mu.RLock()
	qd := c.queueDepth
	c.mu.RUnlock()

	msg := wsMsg{
		Type:         "heartbeat",
		NodeID:       c.nodeID,
		GPUUtilPct:   utilPct,
		VRAMFreeMB:   vramFree,
		TempC:        tempC,
		PowerW:       powerW,
		QueueDepth:   qd,
		EarningState: "earning",
	}
	if err := wsjson.Write(ctx, conn, msg); err != nil {
		log.Printf("owlrun: gateway: heartbeat write: %v", err)
	}
}

// readLoop processes incoming gateway messages until the WS closes.
func (c *Connector) readLoop(ctx context.Context, conn *websocket.Conn) error {
	for {
		var msg wsMsg
		if err := wsjson.Read(ctx, conn, &msg); err != nil {
			return err
		}
		switch msg.Type {
		case "ping":
			_ = wsjson.Write(ctx, conn, wsMsg{Type: "pong"})
		case "heartbeat_ack":
			c.mu.Lock()
			c.gatewayStats = GatewayStats{
				Connected:        true,
				Status:           msg.Status,
				JobsToday:        msg.JobsToday,
				TokensToday:      msg.TokensToday,
				EarnedTodayUSD:   msg.EarnedTodayUSD,
				EarnedTodaySats:  msg.EarnedTodaySats,
				EarnedTotalSats:  msg.EarnedTotalSats,
				QueueDepthGlobal: msg.QueueDepthGlobal,
				BtcPrice: BtcPrice{
					LiveUsd:    msg.BtcLiveUsd,
					YesterdayFix: msg.BtcYesterdayFix,
					DailyAvg:   msg.BtcDailyAvg,
					WeeklyAvg:  msg.BtcWeeklyAvg,
					Status:     msg.BtcPriceStatus,
				},
				Broadcasts:       msg.Broadcasts,
				BalanceSats:      msg.BalanceSats,
				WithdrawHistory:  msg.WithdrawHistory,
			}
			c.mu.Unlock()
			if c.onBalanceUpdate != nil && msg.BalanceSats > 0 {
				go c.onBalanceUpdate(msg.BalanceSats)
			}
		case "job":
			go c.handleJob(ctx, conn, msg)
		case "job_complete":
			if c.onComplete != nil {
				model := msg.Model
				if model == "" {
					c.mu.RLock()
					for m := range c.models {
						model = m
						break
					}
					c.mu.RUnlock()
				}
				c.onComplete(model, msg.Tokens, msg.EarnedUSD)
			}
		case "drain":
			log.Printf("owlrun: gateway: drain signal received")
			return nil
		}
	}
}

// handleJob evaluates a job assignment and accepts or rejects it.
func (c *Connector) handleJob(ctx context.Context, conn *websocket.Conn, job wsMsg) {
	c.mu.RLock()
	hasModel := c.models[job.Model]
	_, vramFree, _, _ := c.getStats()
	c.mu.RUnlock()

	// Reject if the required model isn't registered or there isn't enough VRAM.
	vramInsufficient := vramFree > 0 && job.VRAMRequiredMB > 0 && vramFree < job.VRAMRequiredMB
	if !hasModel || vramInsufficient {
		reason := "model_not_loaded"
		if hasModel {
			reason = "no_vram"
		}
		_ = wsjson.Write(ctx, conn, wsMsg{
			Type:   "reject",
			JobID:  job.JobID,
			Reason: reason,
		})
		return
	}

	// Accept.
	acceptCtx, cancel := context.WithTimeout(ctx, jobAcceptTimeout)
	defer cancel()
	if err := wsjson.Write(acceptCtx, conn, wsMsg{Type: "accept", JobID: job.JobID}); err != nil {
		log.Printf("owlrun: gateway: job %s accept write: %v", job.JobID, err)
		return
	}

	log.Printf("owlrun: gateway: job %s accepted — model %s", job.JobID, job.Model)

	c.mu.Lock()
	c.queueDepth++
	c.mu.Unlock()

	go func() {
		defer func() {
			c.mu.Lock()
			c.queueDepth--
			c.mu.Unlock()
		}()
		if err := c.proxyJob(ctx, conn, job.JobID); err != nil {
			log.Printf("owlrun: gateway: proxy job %s: %v", job.JobID, err)
		}
	}()
}

// proxyJob claims the job from the gateway and streams Ollama's response:
//
//  1. GET /v1/gateway/jobs/{job_id}/proxy/request  → receive buyer's request body.
//     Gateway handler returns immediately → HTTP/2 END_STREAM → clean EOF.
//  2. Forward buyer's request to local Ollama.
//  3. Stream Ollama's response as WS proxy_chunk messages → gateway → buyer.
//     Send proxy_done when stream ends. CF tunnel handles WS natively — no buffering.
func (c *Connector) proxyJob(ctx context.Context, conn *websocket.Conn, jobID string) error {
	base := c.proxyBaseURL()

	// Step 1: GET buyer's request from gateway.
	fetchURL := fmt.Sprintf(base+jobFetchPath, jobID)
	getReq, err := http.NewRequestWithContext(ctx, http.MethodGet, fetchURL, nil)
	if err != nil {
		return err
	}
	getReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	getResp, err := c.gwClient().Do(getReq)
	if err != nil {
		return fmt.Errorf("proxy fetch: %w", err)
	}
	defer getResp.Body.Close()
	log.Printf("owlrun: gateway: job %s proxy fetch: status=%d", jobID, getResp.StatusCode)

	if getResp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(getResp.Body, 512))
		return fmt.Errorf("proxy fetch: HTTP %d: %s", getResp.StatusCode, snippet)
	}

	buyerBody, err := io.ReadAll(getResp.Body)
	if err != nil {
		return fmt.Errorf("proxy fetch read: %w", err)
	}
	log.Printf("owlrun: gateway: job %s buyer request received (%d bytes)", jobID, len(buyerBody))

	// Step 2: Forward buyer's request to local Ollama.
	ollamaReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.ollamaURL()+"/api/chat", bytes.NewReader(buyerBody))
	if err != nil {
		return err
	}
	ollamaReq.Header.Set("Content-Type", "application/json")

	log.Printf("owlrun: gateway: job %s forwarding to ollama", jobID)
	ollamaResp, err := http.DefaultClient.Do(ollamaReq)
	log.Printf("owlrun: gateway: job %s ollama responded: err=%v", jobID, err)
	if err != nil {
		return fmt.Errorf("ollama request: %w", err)
	}
	defer ollamaResp.Body.Close()

	if ollamaResp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(ollamaResp.Body, 512))
		return fmt.Errorf("ollama: HTTP %d: %s", ollamaResp.StatusCode, snippet)
	}

	// Step 3: Stream Ollama's response as WS proxy_chunk messages.
	// Each chunk is a WS text message with job_id + data. Gateway writes each
	// chunk to the buyer's ResponseWriter + Flush(). CF tunnel handles WS
	// natively — zero buffering, real-time token streaming.
	log.Printf("owlrun: gateway: job %s streaming response via WS", jobID)
	buf := make([]byte, 4096)
	for {
		n, readErr := ollamaResp.Body.Read(buf)
		if n > 0 {
			if writeErr := wsjson.Write(ctx, conn, wsMsg{
				Type:  "proxy_chunk",
				JobID: jobID,
				Data:  string(buf[:n]),
			}); writeErr != nil {
				return fmt.Errorf("proxy chunk write: %w", writeErr)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return fmt.Errorf("ollama read: %w", readErr)
		}
	}

	// Signal stream complete.
	if err := wsjson.Write(ctx, conn, wsMsg{
		Type:  "proxy_done",
		JobID: jobID,
	}); err != nil {
		return fmt.Errorf("proxy done write: %w", err)
	}

	log.Printf("owlrun: gateway: job %s complete (WS proxy)", jobID)
	return nil
}
