package cashu

import (
	"testing"
)

func TestSerializeDeserialize(t *testing.T) {
	tok := Token{
		Token: []TokenEntry{{
			Mint: "http://localhost:8085",
			Proofs: []Proof{
				{Amount: 8, ID: "009a1f293253e41e", Secret: "abc123", C: "02deadbeef"},
				{Amount: 4, ID: "009a1f293253e41e", Secret: "def456", C: "03cafebabe"},
			},
		}},
		Memo: "test payment",
	}

	s, err := Serialize(tok)
	if err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	if s[:6] != "cashuA" {
		t.Fatalf("expected cashuA prefix, got %s", s[:6])
	}

	got, err := Deserialize(s)
	if err != nil {
		t.Fatalf("Deserialize: %v", err)
	}
	if got.TotalSats() != 12 {
		t.Fatalf("expected 12 sats, got %d", got.TotalSats())
	}
	if got.Memo != "test payment" {
		t.Fatalf("expected memo 'test payment', got %q", got.Memo)
	}
	if len(got.Token) != 1 || len(got.Token[0].Proofs) != 2 {
		t.Fatalf("expected 1 entry with 2 proofs, got %d entries", len(got.Token))
	}
}

func TestDeserializeInvalid(t *testing.T) {
	_, err := Deserialize("not-a-token")
	if err == nil {
		t.Fatal("expected error for invalid prefix")
	}
	_, err = Deserialize("cashuA!!!invalid-base64!!!")
	if err == nil {
		t.Fatal("expected error for bad base64")
	}
}

func TestSplitAmount(t *testing.T) {
	tests := []struct {
		in   uint64
		want []uint64
	}{
		{0, nil},
		{1, []uint64{1}},
		{13, []uint64{1, 4, 8}},
		{64, []uint64{64}},
		{255, []uint64{1, 2, 4, 8, 16, 32, 64, 128}},
	}
	for _, tt := range tests {
		got := SplitAmount(tt.in)
		if len(got) != len(tt.want) {
			t.Errorf("SplitAmount(%d) = %v, want %v", tt.in, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("SplitAmount(%d)[%d] = %d, want %d", tt.in, i, got[i], tt.want[i])
			}
		}
	}
}

func TestTotalSats(t *testing.T) {
	tok := Token{Token: []TokenEntry{
		{Proofs: []Proof{{Amount: 1}, {Amount: 4}, {Amount: 8}}},
		{Proofs: []Proof{{Amount: 16}, {Amount: 32}}},
	}}
	if got := tok.TotalSats(); got != 61 {
		t.Fatalf("TotalSats() = %d, want 61", got)
	}
}
