// gateway.go — Owlrun Gateway connector.
// Nodes connect ONLY here. Buyer routing, margin, and failover are
// gateway-side concerns — the node never talks to buyers directly.
package marketplace

import (
	"encoding/json"

	"github.com/fabgoodvibes/owlrun/internal/gpu"
)

// DefaultGatewayBase is the hardcoded default — the Owlrun Gateway.
// MIT-licensed: users can override in owlrun.conf, but 99% won't.
const DefaultGatewayBase = "https://gateway.owlrun.me"

// registerPayload is the JSON body sent to POST /v1/gateway/register.
type registerPayload struct {
	NodeID       string   `json:"node_id"`
	APIKey       string   `json:"api_key"`
	GPU          string   `json:"gpu"`
	GPUVendor    string   `json:"gpu_vendor"`
	VRAMTotalMB  int      `json:"vram_total_mb"`
	VRAMFreeMB   int      `json:"vram_free_mb"`
	VRAMExact    bool     `json:"vram_exact"`
	Models       []string `json:"models"`
	OllamaURL    string   `json:"ollama_url"`
	Region       string   `json:"region"`
	Wallet       string   `json:"wallet,omitempty"`
	ReferralCode string   `json:"referral_code,omitempty"`
	Version      string   `json:"version"`
}

// BuildRegistration serialises the node registration payload.
func BuildRegistration(nodeID, apiKey, wallet, referralCode, region, version string, info gpu.Info, models []string) ([]byte, error) {
	if region == "" {
		region = "auto"
	}
	p := registerPayload{
		NodeID:      nodeID,
		APIKey:      apiKey,
		GPU:         info.Name,
		GPUVendor:   info.Vendor,
		VRAMTotalMB: info.VRAMTotalMB,
		VRAMFreeMB:  info.VRAMFreeMB,
		VRAMExact:   info.VRAMExact,
		Models:      models,
		OllamaURL:   "http://localhost:11434",
		Region:       region,
		Wallet:       wallet,
		ReferralCode: referralCode,
		Version:      version,
	}
	return json.Marshal(p)
}
