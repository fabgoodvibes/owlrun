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
	region           string
	version          string
	gpuInfo          gpu.Info
	proxyBase        string
}

// New creates a Router and wires up the gateway connector.
// onComplete is called (from a goroutine) when a job_complete message arrives
// from the gateway — use it to call earnings.Tracker.Record().
func New(
	gatewayBase, proxyBase, apiKey, nodeID, wallet, referralCode, lightningAddress, region, version string,
	gpuInfo gpu.Info,
	getStats StatsFunc,
	onComplete JobCompleteFunc,
	onConnect func(),
) *Router {
	if gatewayBase == "" {
		gatewayBase = DefaultGatewayBase
	}

	// Resolve region once: if the config says "auto" or is empty, detect from IP.
	if region == "" || region == "auto" {
		region = geo.DetectRegion()
	}

	c := NewConnector(gatewayBase, proxyBase, apiKey, nodeID, wallet, getStats, onComplete, onConnect)

	r := &Router{
		conn:         c,
		nodeID:       nodeID,
		apiKey:       apiKey,
		wallet:           wallet,
		referralCode:     referralCode,
		lightningAddress: lightningAddress,
		region:           region,
		version:      version,
		gpuInfo:      gpuInfo,
		proxyBase:    proxyBase,
	}

	// Pre-build the registration payload (no model loaded yet; models updated
	// via SetModel before Connect is called).
	payload, err := BuildRegistration(nodeID, apiKey, wallet, referralCode, lightningAddress, region, version, gpuInfo, nil)
	if err != nil {
		log.Printf("owlrun: gateway: build registration payload: %v", err)
	} else {
		c.SetRegistration(payload)
	}

	return r
}

// SetModel updates the connector with the currently loaded Ollama model tag
// and rebuilds the registration payload so the gateway sees the correct model list.
func (r *Router) SetModel(model string) {
	r.conn.SetModel(model)

	payload, err := BuildRegistration(r.nodeID, r.apiKey, r.wallet, r.referralCode, r.lightningAddress, r.region, r.version, r.gpuInfo, []string{model})
	if err != nil {
		log.Printf("owlrun: gateway: rebuild registration payload: %v", err)
		return
	}
	r.conn.SetRegistration(payload)
}

// Connect starts the gateway WS lifecycle. Non-blocking.
func (r *Router) Connect() {
	r.conn.Connect()
}

// Disconnect tears down the gateway connection cleanly.
func (r *Router) Disconnect() {
	r.conn.Disconnect()
}

// Stats returns the latest heartbeat_ack data from the gateway.
func (r *Router) Stats() GatewayStats {
	return r.conn.Stats()
}
