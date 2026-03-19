// Package cashu implements Cashu ecash token types and serialization
// for the Owlrun node client. Follows NUT-00 specification.
package cashu

// Proof is a single ecash token (NUT-00).
type Proof struct {
	Amount   uint64 `json:"amount"`
	ID       string `json:"id"`      // keyset ID
	Secret   string `json:"secret"`  // unique random secret
	C        string `json:"C"`       // unblinded signature (hex secp256k1 point)
}

// TokenEntry groups proofs by mint URL (NUT-00 V3 token format).
type TokenEntry struct {
	Mint   string  `json:"mint"`
	Proofs []Proof `json:"proofs"`
}

// Token is the V3 Cashu token envelope.
type Token struct {
	Token []TokenEntry `json:"token"`
	Memo  string       `json:"memo,omitempty"`
}

// TotalSats returns the sum of all proof amounts in the token.
func (t Token) TotalSats() uint64 {
	var total uint64
	for _, e := range t.Token {
		for _, p := range e.Proofs {
			total += p.Amount
		}
	}
	return total
}
