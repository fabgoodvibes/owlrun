// Package marketplace manages the node's connection to the Owlrun Gateway.
//
// Architecture: the node connects to ONE place — the Owlrun Gateway (private
// infrastructure). The gateway handles all buyer routing internally.
// If the gateway is unreachable, the node pauses — it does NOT fall back to
// any buyer directly.
package marketplace

import (
	"log"

	"github.com/fabgoodvibes/owlrun/internal/geo"
	"github.com/fabgoodvibes/owlrun/internal/gpu"
)

// Router owns the gateway Connector and exposes a simple Start/Stop interface
// for the tray's state machine.
type Router struct {
	conn             *Connector
	nodeID           string
	apiKey           string
	wallet           string
	referralCode     string
	lightningAddress string
	redeemThreshold  int
	freeTierPct      int
	region           string
	version          string
	gpuInfo          gpu.Info
	proxyBase        string
}

// New creates a Router and wires up the gateway connector.
// onComplete is called (from a goroutine) when a job_complete message arrives
// from the gateway — use it to call earnings.Tracker.Record().
func New(
	gatewayBase, proxyBase, apiKey, nodeID, wallet, referralCode, lightningAddress string, redeemThreshold, freeTierPct int, region, version string,
	gpuInfo gpu.Info,
	getStats StatsFunc,
	onComplete JobCompleteFunc,
	onConnect func(),
	onBalanceUpdate ...func(balanceSats int64),
) *Router {
	if gatewayBase == "" {
		gatewayBase = DefaultGatewayBase
	}

	// Resolve region once: if the config says "auto" or is empty, detect from IP.
	if region == "" || region == "auto" {
		region = geo.DetectRegion()
	}

	var balanceCb func(int64)
	if len(onBalanceUpdate) > 0 {
		balanceCb = onBalanceUpdate[0]
	}
	c := NewConnector(gatewayBase, proxyBase, apiKey, nodeID, wallet, getStats, onComplete, onConnect, balanceCb)

	r := &Router{
		conn:         c,
		nodeID:       nodeID,
		apiKey:       apiKey,
		wallet:           wallet,
		referralCode:     referralCode,
		lightningAddress: lightningAddress,
		redeemThreshold:  redeemThreshold,
		freeTierPct:      freeTierPct,
		region:           region,
		version:      version,
		gpuInfo:      gpuInfo,
		proxyBase:    proxyBase,
	}

	// Pre-build the registration payload (no model loaded yet; models updated
	// via SetModel before Connect is called).
	payload, err := BuildRegistration(nodeID, apiKey, wallet, referralCode, lightningAddress, redeemThreshold, freeTierPct, region, version, gpuInfo, nil)
	if err != nil {
		log.Printf("owlrun: gateway: build registration payload: %v", err)
	} else {
		c.SetRegistration(payload)
	}

	return r
}

// SetModels updates the connector with all models this node can serve and
// rebuilds the registration payload. The first model is the primary (loaded
// into VRAM); the rest are available for on-demand loading.
func (r *Router) SetModels(models []string) {
	r.conn.SetModels(models)

	payload, err := BuildRegistration(r.nodeID, r.apiKey, r.wallet, r.referralCode, r.lightningAddress, r.redeemThreshold, r.freeTierPct, r.region, r.version, r.gpuInfo, models)
	if err != nil {
		log.Printf("owlrun: gateway: rebuild registration payload: %v", err)
		return
	}
	r.conn.SetRegistration(payload)
}

// SetModel updates the connector with a single model (convenience wrapper).
func (r *Router) SetModel(model string) {
	r.SetModels([]string{model})
}

// SetLightningAddress updates the provider's Lightning address and re-registers
// with the gateway so it knows where to send auto-payouts.
func (r *Router) SetLightningAddress(addr string) {
	r.lightningAddress = addr
	// Rebuild registration with all current models.
	models := r.currentModels()
	payload, err := BuildRegistration(r.nodeID, r.apiKey, r.wallet, r.referralCode, r.lightningAddress, r.redeemThreshold, r.freeTierPct, r.region, r.version, r.gpuInfo, models)
	if err != nil {
		log.Printf("owlrun: gateway: rebuild registration (lightning address): %v", err)
		return
	}
	r.conn.SetRegistration(payload)
	log.Printf("owlrun: gateway: lightning address updated, reconnecting")
	go r.conn.Reconnect()
}

// SetRedeemThreshold updates the payout threshold (sats) and re-registers.
func (r *Router) SetRedeemThreshold(threshold int) {
	r.redeemThreshold = threshold
	models := r.currentModels()
	payload, err := BuildRegistration(r.nodeID, r.apiKey, r.wallet, r.referralCode, r.lightningAddress, r.redeemThreshold, r.freeTierPct, r.region, r.version, r.gpuInfo, models)
	if err != nil {
		log.Printf("owlrun: gateway: rebuild registration (redeem threshold): %v", err)
		return
	}
	r.conn.SetRegistration(payload)
	log.Printf("owlrun: gateway: redeem threshold updated to %d mSats, reconnecting", threshold)
	go r.conn.Reconnect()
}

// SetFreeTierPct updates the free tier donation percentage and re-registers.
func (r *Router) SetFreeTierPct(pct int) {
	r.freeTierPct = pct
	models := r.currentModels()
	payload, err := BuildRegistration(r.nodeID, r.apiKey, r.wallet, r.referralCode, r.lightningAddress, r.redeemThreshold, r.freeTierPct, r.region, r.version, r.gpuInfo, models)
	if err != nil {
		log.Printf("owlrun: gateway: rebuild registration (free tier): %v", err)
		return
	}
	r.conn.SetRegistration(payload)
	log.Printf("owlrun: gateway: free tier pct updated to %d%%, reconnecting", pct)
	go r.conn.Reconnect()
}

// SetOllamaBase overrides the Ollama API base URL on the connector.
func (r *Router) SetOllamaBase(base string) {
	r.conn.SetOllamaBase(base)
}

// SetContextLength updates the Ollama num_ctx on the connector.
func (r *Router) SetContextLength(n int) {
	r.conn.SetContextLength(n)
}

// currentModels returns the list of registered model tags from the connector.
func (r *Router) currentModels() []string {
	r.conn.mu.RLock()
	defer r.conn.mu.RUnlock()
	models := make([]string, 0, len(r.conn.models))
	for tag := range r.conn.models {
		models = append(models, tag)
	}
	return models
}

// Connect starts the gateway WS lifecycle. Non-blocking.
func (r *Router) Connect() {
	r.conn.Connect()
}

// Disconnect tears down the gateway connection cleanly.
func (r *Router) Disconnect() {
	r.conn.Disconnect()
}

// Reconnect drops the WS and reconnects with updated registration.
func (r *Router) Reconnect() {
	r.conn.Reconnect()
}

// Stats returns the latest heartbeat_ack data from the gateway.
func (r *Router) Stats() GatewayStats {
	return r.conn.Stats()
}
