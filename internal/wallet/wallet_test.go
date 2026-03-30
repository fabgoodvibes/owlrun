package wallet

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/fabgoodvibes/owlrun/internal/cashu"
)

// newTestWallet creates a Wallet with a real SQLite store in a temp dir.
func newTestWallet(t *testing.T, gatewayURL, apiKey string) *Wallet {
	t.Helper()
	dir := t.TempDir()
	orig := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	t.Cleanup(func() { os.Setenv("HOME", orig) })

	w := New(gatewayURL, apiKey)
	t.Cleanup(func() { w.Close() })
	return w
}

// fakeGateway starts an httptest server that responds to /v1/provider/withdraw-ecash
// with a valid cashuA token containing the given proofs.
func fakeGateway(t *testing.T, mintURL string, proofs []cashu.Proof) *httptest.Server {
	t.Helper()
	tok := cashu.Token{
		Token: []cashu.TokenEntry{{Mint: mintURL, Proofs: proofs}},
		Memo:  "test",
	}
	tokenStr, err := cashu.Serialize(tok)
	if err != nil {
		t.Fatalf("serialize test token: %v", err)
	}

	var totalSats int64
	for _, p := range proofs {
		totalSats += int64(p.Amount)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/provider/withdraw-ecash" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.Header.Get("Authorization") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(ClaimResponse{
			Token:      tokenStr,
			AmountSats: totalSats,
		})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestWallet_Balance_Empty(t *testing.T) {
	w := newTestWallet(t, "https://gw.test", "owlr_prov_test")
	if got := w.Balance(); got != 0 {
		t.Errorf("Balance = %d, want 0", got)
	}
}

func TestWallet_GetStats_ZeroState(t *testing.T) {
	w := newTestWallet(t, "https://gw.test", "owlr_prov_test")

	stats := w.GetStats(100, 87000.0)
	if stats.GatewaySats != 100 {
		t.Errorf("GatewaySats = %d, want 100", stats.GatewaySats)
	}
	if stats.LocalSats != 0 {
		t.Errorf("LocalSats = %d, want 0", stats.LocalSats)
	}
	if stats.TotalSats != 100 {
		t.Errorf("TotalSats = %d, want 100", stats.TotalSats)
	}
	if stats.USDApprox <= 0 {
		t.Errorf("USDApprox = %f, want > 0", stats.USDApprox)
	}
}

func TestWallet_Claim_NoGateway(t *testing.T) {
	w := newTestWallet(t, "", "")

	_, err := w.Claim(0)
	if err == nil {
		t.Error("Claim with empty gateway should error")
	}
}

func TestWallet_Claim_Success(t *testing.T) {
	proofs := []cashu.Proof{
		{Amount: 16, ID: "ks1", Secret: "s1", C: "c1"},
		{Amount: 8, ID: "ks1", Secret: "s2", C: "c2"},
	}
	srv := fakeGateway(t, "https://mint.test", proofs)
	w := newTestWallet(t, srv.URL, "owlr_prov_test123")

	token, err := w.Claim(0)
	if err != nil {
		t.Fatalf("Claim: %v", err)
	}
	if token == "" {
		t.Error("Claim returned empty token")
	}

	// Proofs should be saved locally.
	if got := w.Balance(); got != 24 {
		t.Errorf("Balance after Claim = %d, want 24", got)
	}

	// Token history should have one entry.
	hist := w.TokenHistory()
	if len(hist) != 1 {
		t.Fatalf("TokenHistory len = %d, want 1", len(hist))
	}
	if hist[0].Sats != 24 {
		t.Errorf("TokenHistory[0].Sats = %d, want 24", hist[0].Sats)
	}
}

func TestWallet_Claim_ReturnsTokenEvenOnSaveError(t *testing.T) {
	proofs := []cashu.Proof{
		{Amount: 32, ID: "ks1", Secret: "s1", C: "c1"},
	}
	srv := fakeGateway(t, "https://mint.test", proofs)
	w := newTestWallet(t, srv.URL, "owlr_prov_test123")

	// Drop the proofs table to force Save() to fail on Prepare().
	w.store.mu.Lock()
	w.store.db.Exec(`DROP TABLE proofs`)
	w.store.mu.Unlock()

	// Claim should return the token AND an error.
	token, err := w.Claim(0)
	if err == nil {
		t.Error("Claim with broken store should return error")
	}
	if token == "" {
		t.Error("Claim should return token even when Save fails (for manual recovery)")
	}
}

func TestWallet_Claim_Gateway404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("not found"))
	}))
	defer srv.Close()

	w := newTestWallet(t, srv.URL, "owlr_prov_test123")

	_, err := w.Claim(0)
	if err == nil {
		t.Error("Claim against 404 gateway should error")
	}
}

func TestWallet_Claim_GatewayUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte("bad key"))
	}))
	defer srv.Close()

	w := newTestWallet(t, srv.URL, "owlr_prov_bad_key")

	_, err := w.Claim(0)
	if err == nil {
		t.Error("Claim with bad API key should error")
	}
}

func TestWallet_Export_Empty(t *testing.T) {
	w := newTestWallet(t, "https://gw.test", "owlr_prov_test")

	_, err := w.Export()
	if err == nil {
		t.Error("Export with no proofs should error")
	}
}

func TestWallet_Export_WithProofs(t *testing.T) {
	w := newTestWallet(t, "https://gw.test", "owlr_prov_test")

	proofs := []cashu.Proof{
		{Amount: 16, ID: "ks1", Secret: "s1", C: "c1"},
		{Amount: 8, ID: "ks1", Secret: "s2", C: "c2"},
	}
	if err := w.store.Save("https://mint.test", proofs); err != nil {
		t.Fatalf("store.Save: %v", err)
	}

	token, err := w.Export()
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if token == "" {
		t.Error("Export returned empty token")
	}

	parsed, err := cashu.Deserialize(token)
	if err != nil {
		t.Fatalf("Deserialize exported token: %v", err)
	}
	if parsed.TotalSats() != 24 {
		t.Errorf("exported TotalSats = %d, want 24", parsed.TotalSats())
	}
}

func TestWallet_TokenHistory_Empty(t *testing.T) {
	w := newTestWallet(t, "https://gw.test", "owlr_prov_test")

	if got := w.TokenHistory(); len(got) != 0 {
		t.Errorf("TokenHistory = %d items, want 0", len(got))
	}
}

func TestWallet_TokenHistory_MaxCapped(t *testing.T) {
	proofs := []cashu.Proof{
		{Amount: 1, ID: "ks1", Secret: "s1", C: "c1"},
	}
	srv := fakeGateway(t, "https://mint.test", proofs)
	w := newTestWallet(t, srv.URL, "owlr_prov_test123")

	// Claim more than maxTokenHistory times.
	for i := 0; i < maxTokenHistory+3; i++ {
		w.Claim(0)
	}

	hist := w.TokenHistory()
	if len(hist) > maxTokenHistory {
		t.Errorf("TokenHistory len = %d, want <= %d", len(hist), maxTokenHistory)
	}
}

func TestWallet_AutoClaim_BelowMinimum(t *testing.T) {
	w := newTestWallet(t, "https://gw.test", "owlr_prov_test")

	// Should not attempt claim when below minimum (10_000 msats = 10 sats).
	w.AutoClaim(5_000)
	// No error, no panic — just returns silently.
}

func TestWallet_AutoClaim_Success(t *testing.T) {
	proofs := []cashu.Proof{
		{Amount: 16, ID: "ks1", Secret: "s1", C: "c1"},
	}
	srv := fakeGateway(t, "https://mint.test", proofs)
	w := newTestWallet(t, srv.URL, "owlr_prov_test123")

	w.AutoClaim(100_000) // 100 sats in msats, above minMsats=10_000
	// Should have claimed and saved.
	if got := w.Balance(); got != 16 {
		t.Errorf("Balance after AutoClaim = %d, want 16", got)
	}
}

func TestWallet_AutoClaim_Cooldown(t *testing.T) {
	proofs := []cashu.Proof{
		{Amount: 8, ID: "ks1", Secret: "s1", C: "c1"},
	}
	srv := fakeGateway(t, "https://mint.test", proofs)
	w := newTestWallet(t, srv.URL, "owlr_prov_test123")

	// First claim should work.
	w.AutoClaim(100_000) // 100 sats in msats
	bal1 := w.Balance()

	// Second claim immediately should be skipped (cooldown).
	w.AutoClaim(100_000)
	bal2 := w.Balance()

	if bal2 != bal1 {
		t.Errorf("Balance changed after cooldown AutoClaim: %d → %d (should be same)", bal1, bal2)
	}
}

func TestWallet_SetGateway(t *testing.T) {
	w := newTestWallet(t, "https://old.test", "old_key")
	w.SetGateway("https://new.test", "new_key")

	w.mu.Lock()
	gw := w.gatewayURL
	key := w.apiKey
	w.mu.Unlock()

	if gw != "https://new.test" {
		t.Errorf("gatewayURL = %q, want https://new.test", gw)
	}
	if key != "new_key" {
		t.Errorf("apiKey = %q, want new_key", key)
	}
}
