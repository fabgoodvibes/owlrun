package wallet

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/fabgoodvibes/owlrun/internal/cashu"
)

// ClaimRequest is sent to the gateway to claim ecash tokens.
type ClaimRequest struct {
	AmountSats int64 `json:"amount_sats,omitempty"` // 0 = claim all
}

// ClaimResponse is returned by the gateway with the ecash token.
type ClaimResponse struct {
	Token      string `json:"token"`       // cashuA... serialized token
	AmountSats int64  `json:"amount_sats"` // actual amount claimed
}

// ClaimEcash calls the gateway's provider withdraw-ecash endpoint.
// If amountSats is 0, claims the full balance.
func ClaimEcash(gatewayURL, apiKey string, amountSats int64) (*ClaimResponse, error) {
	body, err := json.Marshal(ClaimRequest{AmountSats: amountSats})
	if err != nil {
		return nil, fmt.Errorf("wallet: marshal claim: %w", err)
	}

	url := gatewayURL + "/v1/provider/withdraw-ecash"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("wallet: create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("wallet: claim request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("wallet: claim failed (HTTP %d): %s", resp.StatusCode, string(b))
	}

	var cr ClaimResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return nil, fmt.Errorf("wallet: decode claim response: %w", err)
	}
	return &cr, nil
}

// ParseClaimResponse deserializes the token string from a ClaimResponse
// and returns the proofs and mint URL.
func ParseClaimResponse(cr *ClaimResponse) (mintURL string, proofs []cashu.Proof, err error) {
	tok, err := cashu.Deserialize(cr.Token)
	if err != nil {
		return "", nil, fmt.Errorf("wallet: parse token: %w", err)
	}
	if len(tok.Token) == 0 {
		return "", nil, fmt.Errorf("wallet: empty token")
	}
	entry := tok.Token[0]
	return entry.Mint, entry.Proofs, nil
}
