package marketplace

import (
	"encoding/json"
	"testing"

	"github.com/fabgoodvibes/owlrun/internal/gpu"
)

func TestBuildRegistration_Fields(t *testing.T) {
	info := gpu.Info{
		Vendor:      "nvidia",
		Name:        "NVIDIA GeForce RTX 4090",
		VRAMTotalMB: 24576,
		VRAMFreeMB:  20000,
		VRAMExact:   true,
	}
	models := []string{"llama3:8b", "mistral:7b"}

	raw, err := BuildRegistration("node-123", "sk-key", "SolanaWallet", "owlr_ref_abc", "us-east", "v0.1.0", info, models)
	if err != nil {
		t.Fatalf("BuildRegistration error: %v", err)
	}

	var p registerPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("JSON unmarshal error: %v", err)
	}

	if p.NodeID != "node-123" {
		t.Errorf("NodeID = %q, want node-123", p.NodeID)
	}
	if p.APIKey != "sk-key" {
		t.Errorf("APIKey = %q, want sk-key", p.APIKey)
	}
	if p.Wallet != "SolanaWallet" {
		t.Errorf("Wallet = %q, want SolanaWallet", p.Wallet)
	}
	if p.Version != "v0.1.0" {
		t.Errorf("Version = %q, want v0.1.0", p.Version)
	}
	if p.GPU != "NVIDIA GeForce RTX 4090" {
		t.Errorf("GPU = %q", p.GPU)
	}
	if p.GPUVendor != "nvidia" {
		t.Errorf("GPUVendor = %q", p.GPUVendor)
	}
	if p.VRAMTotalMB != 24576 {
		t.Errorf("VRAMTotalMB = %d, want 24576", p.VRAMTotalMB)
	}
	if p.VRAMFreeMB != 20000 {
		t.Errorf("VRAMFreeMB = %d, want 20000", p.VRAMFreeMB)
	}
	if !p.VRAMExact {
		t.Error("VRAMExact should be true")
	}
	if len(p.Models) != 2 || p.Models[0] != "llama3:8b" || p.Models[1] != "mistral:7b" {
		t.Errorf("Models = %v", p.Models)
	}
	if p.OllamaURL != "http://localhost:11434" {
		t.Errorf("OllamaURL = %q", p.OllamaURL)
	}
}

func TestBuildRegistration_NoModels(t *testing.T) {
	info := gpu.Info{Vendor: "amd", Name: "AMD RX 7900 XTX", VRAMExact: false}

	raw, err := BuildRegistration("n", "k", "", "", "", "dev", info, nil)
	if err != nil {
		t.Fatalf("BuildRegistration error: %v", err)
	}

	var p registerPayload
	json.Unmarshal(raw, &p)

	if p.Models != nil && len(p.Models) != 0 {
		t.Errorf("expected nil/empty models, got %v", p.Models)
	}
	if p.Wallet != "" {
		t.Errorf("Wallet should be empty string, got %q", p.Wallet)
	}
	if p.VRAMExact {
		t.Error("VRAMExact should be false for AMD")
	}
}

func TestBuildRegistration_JSONRoundtrip(t *testing.T) {
	info := gpu.Info{Vendor: "nvidia", Name: "RTX 3080", VRAMTotalMB: 10240}
	raw, err := BuildRegistration("id", "key", "wallet", "", "eu-west", "v1", info, []string{"phi3:mini"})
	if err != nil {
		t.Fatal(err)
	}

	// Must be valid JSON.
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}

	// Required fields must be present.
	for _, field := range []string{"node_id", "api_key", "gpu", "gpu_vendor", "vram_total_mb", "ollama_url", "version"} {
		if _, ok := m[field]; !ok {
			t.Errorf("missing JSON field %q", field)
		}
	}
}

func TestDefaultGatewayBase(t *testing.T) {
	if DefaultGatewayBase != "https://node.owlrun.me" {
		t.Errorf("DefaultGatewayBase = %q, want https://node.owlrun.me", DefaultGatewayBase)
	}
}
