package wallet

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/fabgoodvibes/owlrun/internal/cashu"
)

// Stats is the dashboard-facing wallet snapshot.
type Stats struct {
	GatewaySats int64   `json:"gateway_sats"` // unclaimed balance on gateway
	LocalSats   int64   `json:"local_sats"`   // claimed proofs stored locally
	TotalSats   int64   `json:"total_sats"`   // gateway + local
	USDApprox   float64 `json:"usd_approx"`   // approximate USD value
	ProofCount  int     `json:"proof_count"`   // number of local proofs
	LastClaim   string  `json:"last_claim"`    // ISO timestamp of last claim
}

// Wallet manages the provider's local Cashu ecash.
type Wallet struct {
	mu         sync.Mutex
	store      *Store
	gatewayURL string
	apiKey     string
	lastClaim  time.Time
}

// New creates a wallet backed by local SQLite storage.
func New(gatewayURL, apiKey string) *Wallet {
	return &Wallet{
		store:      NewStore(),
		gatewayURL: gatewayURL,
		apiKey:     apiKey,
	}
}

// SetGateway updates the gateway URL and API key (e.g. after reconnect).
func (w *Wallet) SetGateway(gatewayURL, apiKey string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.gatewayURL = gatewayURL
	w.apiKey = apiKey
}

// Claim requests ecash from the gateway and stores proofs locally.
// Returns the serialized cashuA... token string for external wallet import.
// If amountSats is 0, claims the full gateway balance.
func (w *Wallet) Claim(amountSats int64) (string, error) {
	w.mu.Lock()
	gw := w.gatewayURL
	key := w.apiKey
	w.mu.Unlock()

	if gw == "" || key == "" {
		return "", fmt.Errorf("wallet: not connected to gateway")
	}

	cr, err := ClaimEcash(gw, key, amountSats)
	if err != nil {
		return "", err
	}

	// Parse and store proofs locally.
	mintURL, proofs, err := ParseClaimResponse(cr)
	if err != nil {
		// Return the raw token even if we can't parse it — user can still paste it.
		log.Printf("owlrun: wallet: could not parse claimed token: %v", err)
		return cr.Token, nil
	}

	if err := w.store.Save(mintURL, proofs); err != nil {
		log.Printf("owlrun: wallet: failed to save proofs locally: %v", err)
	}

	w.mu.Lock()
	w.lastClaim = time.Now().UTC()
	w.mu.Unlock()

	return cr.Token, nil
}

// Balance returns the local wallet balance in sats.
func (w *Wallet) Balance() int64 {
	return w.store.Balance()
}

// Export serializes all local proofs as a cashuA... token string
// for importing into an external Cashu wallet.
func (w *Wallet) Export() (string, error) {
	byMint := w.store.Proofs()
	if len(byMint) == 0 {
		return "", fmt.Errorf("wallet: no proofs to export")
	}

	var entries []cashu.TokenEntry
	for mint, proofs := range byMint {
		entries = append(entries, cashu.TokenEntry{
			Mint:   mint,
			Proofs: proofs,
		})
	}

	tok := cashu.Token{
		Token: entries,
		Memo:  "Owlrun provider earnings",
	}
	return cashu.Serialize(tok)
}

// GetStats returns the current wallet state for the dashboard.
// gatewaySats is the unclaimed balance reported by the gateway in heartbeat_ack.
// btcUSD is the current BTC/USD rate for USD approximation.
func (w *Wallet) GetStats(gatewaySats int64, btcUSD float64) Stats {
	localSats := w.store.Balance()
	totalSats := gatewaySats + localSats

	var usd float64
	if btcUSD > 0 {
		usd = float64(totalSats) / 100_000_000.0 * btcUSD
	}

	w.mu.Lock()
	var lastClaim string
	if !w.lastClaim.IsZero() {
		lastClaim = w.lastClaim.Format(time.RFC3339)
	}
	w.mu.Unlock()

	return Stats{
		GatewaySats: gatewaySats,
		LocalSats:   localSats,
		TotalSats:   totalSats,
		USDApprox:   usd,
		ProofCount:  w.store.ProofCount(),
		LastClaim:   lastClaim,
	}
}

// Close shuts down the wallet store.
func (w *Wallet) Close() {
	w.store.Close()
}
