package cashu

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

const tokenPrefix = "cashuA"

// Serialize encodes a Token to the cashuA... string format (V3).
func Serialize(t Token) (string, error) {
	b, err := json.Marshal(t)
	if err != nil {
		return "", fmt.Errorf("cashu: marshal token: %w", err)
	}
	return tokenPrefix + base64.URLEncoding.EncodeToString(b), nil
}

// Deserialize decodes a cashuA... string into a Token.
func Deserialize(s string) (Token, error) {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, tokenPrefix) {
		return Token{}, fmt.Errorf("cashu: invalid token prefix (expected %s)", tokenPrefix)
	}
	raw := s[len(tokenPrefix):]

	// Try URL-safe base64 first, then standard.
	b, err := base64.URLEncoding.DecodeString(raw)
	if err != nil {
		b, err = base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return Token{}, fmt.Errorf("cashu: decode base64: %w", err)
		}
	}

	var t Token
	if err := json.Unmarshal(b, &t); err != nil {
		return Token{}, fmt.Errorf("cashu: unmarshal token: %w", err)
	}
	return t, nil
}

// SplitAmount decomposes an amount into powers of 2.
// e.g. 13 → [1, 4, 8]
func SplitAmount(amount uint64) []uint64 {
	var parts []uint64
	for i := uint(0); amount > 0; i++ {
		if amount&1 == 1 {
			parts = append(parts, 1<<i)
		}
		amount >>= 1
	}
	return parts
}
