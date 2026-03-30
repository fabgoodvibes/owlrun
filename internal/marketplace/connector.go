// Package marketplace manages the node's connection to the Owlrun Gateway.
// See gateway.go for the GatewayConnector and router.go for the Router.
package marketplace

import (
	"bytes"
	"context"
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
	jobFetchPath  = "/v1/gateway/jobs/%s/proxy/request"
	jobSubmitPath = "/v1/gateway/jobs/%s/proxy/response"
)

// StatsFunc is called by the connector to get live GPU stats for heartbeats.
type StatsFunc func() (utilPct int, vramFreeMB int, tempC int, powerW float64)

// JobCompleteFunc is called when the gateway confirms a job has been billed.
type JobCompleteFunc func(model string, tokens int, earnedUSD float64)

// GatewayStats is the last heartbeat_ack received from the gateway.
// Safe to read from any goroutine via Connector.GatewayStats().
type GatewayStats struct {
	Status           string
	JobsToday        int
	TokensToday      int
	EarnedTodayUSD   float64
	QueueDepthGlobal int
	NextPayoutEpoch  string
}

// wsMsg is the generic WebSocket message envelope used for all control traffic.
type wsMsg struct {
	Type string `json:"type"`

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
	QueueDepthGlobal int     `json:"queue_depth_global,omitempty"`
	NextPayoutEpoch  string  `json:"next_payout_epoch,omitempty"`

	// Job complete (gateway → node)
	Tokens    int     `json:"tokens,omitempty"`
	EarnedUSD float64 `json:"earned_usd,omitempty"`

	// Reject (node → gateway)
	Reason string `json:"reason,omitempty"`
}

// Connector manages the persistent WebSocket connection to the Owlrun Gateway.
// It handles: registration, heartbeat, job assignment, proxy initiation, and
// relaying gateway stats back to the dashboard.
type Connector struct {
	proxyBase   string // optional alternate base URL for proxy connections; falls back to gatewayBase if empty
	gatewayBase string // e.g. "https://gateway.owlrun.me"
	apiKey      string
	nodeID      string
	wallet      string
	regPayload  []byte // cached JSON from the last Register call

	getStats    StatsFunc
	onComplete  JobCompleteFunc

	// gatewayClient is used for the gateway proxy POST (requires HTTP/2).
	// If nil, http.DefaultClient is used.
	// Override in tests to inject an HTTP/2-capable TLS test client.
	gatewayClient *http.Client

	// ollamaBase is the Ollama API base URL. Defaults to http://localhost:11434.
	// Override in tests to point at a fake Ollama server.
	ollamaBase string

	mu           sync.RWMutex
	model        string       // currently loaded Ollama model
	conn         *websocket.Conn
	gatewayStats GatewayStats
	queueDepth   int

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
// Uses proxyBase if set; falls back to gatewayBase.
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
) *Connector {
	return &Connector{
		gatewayBase: gatewayBase,
		proxyBase:   proxyBase,
		apiKey:      apiKey,
		nodeID:      nodeID,
		wallet:      wallet,
		getStats:    getStats,
		onComplete:  onComplete,
	}
}

// SetRegistration stores the node registration payload so the connector can
// POST /register before opening the WS. Must be called before Connect().
func (c *Connector) SetRegistration(payload []byte) {
	c.mu.Lock()
	c.regPayload = payload
	c.mu.Unlock()
}

// SetModel updates the currently loaded Ollama model tag.
func (c *Connector) SetModel(model string) {
	c.mu.Lock()
	c.model = model
	c.mu.Unlock()
}

// GatewayStats returns the latest heartbeat_ack snapshot from the gateway.
func (c *Connector) Stats() GatewayStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.gatewayStats
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
	c.mu.Unlock()

	defer func() {
		conn.Close(websocket.StatusNormalClosure, "")
		c.mu.Lock()
		c.conn = nil
		c.mu.Unlock()
	}()

	log.Printf("owlrun: gateway: WS connected")

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
				Status:           msg.Status,
				JobsToday:        msg.JobsToday,
				TokensToday:      msg.TokensToday,
				EarnedTodayUSD:   msg.EarnedTodayUSD,
				QueueDepthGlobal: msg.QueueDepthGlobal,
				NextPayoutEpoch:  msg.NextPayoutEpoch,
			}
			c.mu.Unlock()
		case "job":
			go c.handleJob(ctx, conn, msg)
		case "job_complete":
			if c.onComplete != nil {
				c.mu.RLock()
				model := c.model
				c.mu.RUnlock()
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
	model := c.model
	_, vramFree, _, _ := c.getStats()
	c.mu.RUnlock()

	// Reject if the required model isn't loaded or there isn't enough VRAM.
	vramInsufficient := vramFree > 0 && job.VRAMRequiredMB > 0 && vramFree < job.VRAMRequiredMB
	if model != job.Model || vramInsufficient {
		reason := "model_not_loaded"
		if model == job.Model {
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
		if err := c.proxyJob(ctx, job.JobID); err != nil {
			log.Printf("owlrun: gateway: proxy job %s: %v", job.JobID, err)
		}
	}()
}

// proxyJob claims the job from the gateway using Option A (two-request protocol):
//
//  1. GET /v1/gateway/jobs/{job_id}/proxy/request  → receive buyer's request body.
//     Gateway handler returns immediately → HTTP/2 END_STREAM → clean EOF.
//  2. Forward buyer's request to local Ollama.
//  3. POST /v1/gateway/jobs/{job_id}/proxy/response with Ollama's streaming response.
//     Gateway pipes the POST body to the waiting buyer.
//
// This avoids the HTTP/2 full-duplex deadlock where Do() blocked forever on
// writeLoopDone because the request body (the response body of step 1) could
// only reach EOF when the gateway handler returned — which required the node to
// finish writing — circular dependency.
func (c *Connector) proxyJob(ctx context.Context, jobID string) error {
	base := c.proxyBaseURL()

	// Step 1: GET buyer's request from gateway.
	// Gateway handler returns immediately → END_STREAM → clean EOF on getResp.Body.
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

	// Read buyer's request (clean EOF since GET handler returned).
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

	// Step 3: POST Ollama's response to gateway, which pipes it to the buyer.
	// pr/pw pipe: Ollama's response flows ollamaResp.Body → pw → pr → gateway POST body.
	// The POST request body (pr) is a local Go pipe — writeLoopDone closes when Ollama
	// finishes and pw.Close() is called, so Do() returns cleanly. No deadlock.
	pr, pw := io.Pipe()
	defer pr.Close()

	go func() {
		_, copyErr := io.Copy(pw, ollamaResp.Body)
		pw.CloseWithError(copyErr)
	}()

	submitURL := fmt.Sprintf(base+jobSubmitPath, jobID)
	postReq, err := http.NewRequestWithContext(ctx, http.MethodPost, submitURL, pr)
	if err != nil {
		return err
	}
	postReq.Header.Set("Authorization", "Bearer "+c.apiKey)
	postReq.Header.Set("Content-Type", "application/json")

	log.Printf("owlrun: gateway: job %s streaming response", jobID)
	postResp, err := c.gwClient().Do(postReq)
	log.Printf("owlrun: gateway: job %s proxy submit: err=%v", jobID, err)
	if err != nil {
		return fmt.Errorf("proxy submit: %w", err)
	}
	defer postResp.Body.Close()

	if postResp.StatusCode != http.StatusOK {
		snippet, _ := io.ReadAll(io.LimitReader(postResp.Body, 512))
		return fmt.Errorf("proxy submit: HTTP %d: %s", postResp.StatusCode, snippet)
	}
	log.Printf("owlrun: gateway: job %s complete", jobID)
	return nil
}
