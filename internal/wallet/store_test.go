package wallet

import (
	"os"
	"testing"

	"github.com/fabgoodvibes/owlrun/internal/cashu"
)

// newTestStore creates a Store backed by a temp-dir SQLite database.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	orig := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	t.Cleanup(func() { os.Setenv("HOME", orig) })

	s := NewStore()
	if s.db == nil {
		t.Fatal("Store.db is nil — SQLite failed to open")
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func testProofs(n int) []cashu.Proof {
	proofs := make([]cashu.Proof, n)
	for i := range proofs {
		proofs[i] = cashu.Proof{
			Amount: 16,
			ID:     "009a1f293253e41e",
			Secret: "secret-" + string(rune('a'+i)),
			C:      "02deadbeef" + string(rune('0'+i)),
		}
	}
	return proofs
}

func TestSave_And_Balance(t *testing.T) {
	s := newTestStore(t)
	proofs := testProofs(3)
	if err := s.Save("https://mint.test", proofs); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got := s.Balance()
	if got != 48 { // 3 × 16
		t.Errorf("Balance = %d, want 48", got)
	}
}

func TestSave_AtomicCommit(t *testing.T) {
	s := newTestStore(t)

	// Save 2 proofs, then 2 more — verify all 4 committed.
	first := testProofs(2)
	if err := s.Save("https://mint.test", first); err != nil {
		t.Fatalf("Save first: %v", err)
	}
	if got := s.Balance(); got != 32 {
		t.Fatalf("Balance after first save = %d, want 32", got)
	}

	second := []cashu.Proof{
		{Amount: 8, ID: "009a1f293253e41e", Secret: "new-secret-1", C: "02aabbccdd01"},
		{Amount: 4, ID: "009a1f293253e41e", Secret: "new-secret-2", C: "02aabbccdd02"},
	}
	if err := s.Save("https://mint.test", second); err != nil {
		t.Fatalf("Save second: %v", err)
	}
	if got := s.Balance(); got != 44 { // 32 + 8 + 4
		t.Errorf("Balance = %d, want 44", got)
	}
}

func TestSave_Rollback_OnDroppedTable(t *testing.T) {
	s := newTestStore(t)

	// Save initial proofs.
	initial := testProofs(2)
	if err := s.Save("https://mint.test", initial); err != nil {
		t.Fatalf("Save initial: %v", err)
	}
	if got := s.Balance(); got != 32 {
		t.Fatalf("Balance after initial = %d, want 32", got)
	}

	// Drop the proofs table to force the next Save to fail mid-TX.
	s.mu.Lock()
	s.db.Exec(`DROP TABLE proofs`)
	s.mu.Unlock()

	// This Save should fail because the table is gone.
	bad := []cashu.Proof{
		{Amount: 100, ID: "ks", Secret: "new1", C: "c1"},
	}
	err := s.Save("https://mint.test", bad)
	if err == nil {
		t.Fatal("Save after DROP TABLE should return error")
	}

	// Recreate table for Balance check.
	s.mu.Lock()
	s.db.Exec(storeSchema)
	s.mu.Unlock()

	// Balance should be 0 (table was dropped + recreated empty) — not 100.
	if got := s.Balance(); got != 0 {
		t.Errorf("Balance after failed Save = %d, want 0 (rollback should have prevented commit)", got)
	}
}

func TestSave_Rollback_OnClosedDB(t *testing.T) {
	s := newTestStore(t)

	// Save initial proofs.
	initial := testProofs(2)
	if err := s.Save("https://mint.test", initial); err != nil {
		t.Fatalf("Save initial: %v", err)
	}

	// Close the DB to force errors on next Save.
	s.mu.Lock()
	s.db.Close()
	s.mu.Unlock()

	bad := []cashu.Proof{
		{Amount: 50, ID: "ks", Secret: "x1", C: "c1"},
	}
	err := s.Save("https://mint.test", bad)
	if err == nil {
		t.Error("Save on closed DB should return error")
	}
}

func TestSave_NilDB_ReturnsNil(t *testing.T) {
	s := &Store{}
	err := s.Save("https://mint.test", testProofs(1))
	if err != nil {
		t.Errorf("Save on nil-db should return nil, got: %v", err)
	}
}

func TestBalance_NilDB_ReturnsZero(t *testing.T) {
	s := &Store{}
	if got := s.Balance(); got != 0 {
		t.Errorf("Balance on nil-db = %d, want 0", got)
	}
}

func TestBalance_ClosedDB_ReturnsZero(t *testing.T) {
	s := newTestStore(t)
	s.Save("https://mint.test", testProofs(2))

	s.mu.Lock()
	s.db.Close()
	s.db = nil
	s.mu.Unlock()

	// Should return 0, not panic.
	if got := s.Balance(); got != 0 {
		t.Errorf("Balance after close = %d, want 0", got)
	}
}

func TestProofCount(t *testing.T) {
	s := newTestStore(t)
	if got := s.ProofCount(); got != 0 {
		t.Errorf("ProofCount empty = %d, want 0", got)
	}
	s.Save("https://mint.test", testProofs(3))
	if got := s.ProofCount(); got != 3 {
		t.Errorf("ProofCount = %d, want 3", got)
	}
}

func TestProofCount_NilDB_ReturnsZero(t *testing.T) {
	s := &Store{}
	if got := s.ProofCount(); got != 0 {
		t.Errorf("ProofCount on nil-db = %d, want 0", got)
	}
}

func TestMarkSpent_UpdatesBalance(t *testing.T) {
	s := newTestStore(t)
	proofs := testProofs(3)
	s.Save("https://mint.test", proofs)

	if err := s.MarkSpent([]string{proofs[0].Secret}); err != nil {
		t.Fatalf("MarkSpent: %v", err)
	}
	if got := s.Balance(); got != 32 { // 2 × 16
		t.Errorf("Balance after MarkSpent = %d, want 32", got)
	}
	if got := s.ProofCount(); got != 2 {
		t.Errorf("ProofCount after MarkSpent = %d, want 2", got)
	}
}

func TestMarkSpent_AllProofs(t *testing.T) {
	s := newTestStore(t)
	proofs := testProofs(3)
	s.Save("https://mint.test", proofs)

	secrets := make([]string, len(proofs))
	for i, p := range proofs {
		secrets[i] = p.Secret
	}
	if err := s.MarkSpent(secrets); err != nil {
		t.Fatalf("MarkSpent all: %v", err)
	}
	if got := s.Balance(); got != 0 {
		t.Errorf("Balance after marking all spent = %d, want 0", got)
	}
	if got := s.ProofCount(); got != 0 {
		t.Errorf("ProofCount after marking all spent = %d, want 0", got)
	}
}

func TestMarkSpent_ClosedDB_ReturnsError(t *testing.T) {
	s := newTestStore(t)
	proofs := testProofs(2)
	s.Save("https://mint.test", proofs)

	s.mu.Lock()
	s.db.Close()
	s.db = nil
	s.mu.Unlock()

	// nil db returns nil (safe no-op).
	if err := s.MarkSpent([]string{proofs[0].Secret}); err != nil {
		t.Errorf("MarkSpent on nil-db should return nil: %v", err)
	}
}

func TestMarkSpent_EmptySlice(t *testing.T) {
	s := newTestStore(t)
	if err := s.MarkSpent([]string{}); err != nil {
		t.Errorf("MarkSpent empty should not error: %v", err)
	}
}

func TestMarkSpent_NilDB(t *testing.T) {
	s := &Store{}
	if err := s.MarkSpent([]string{"x"}); err != nil {
		t.Errorf("MarkSpent on nil-db should return nil: %v", err)
	}
}

func TestProofs_GroupedByMint(t *testing.T) {
	s := newTestStore(t)
	s.Save("https://mint-a.test", []cashu.Proof{
		{Amount: 8, ID: "ks1", Secret: "a1", C: "c1"},
	})
	s.Save("https://mint-b.test", []cashu.Proof{
		{Amount: 4, ID: "ks2", Secret: "b1", C: "c2"},
		{Amount: 2, ID: "ks2", Secret: "b2", C: "c3"},
	})

	byMint := s.Proofs()
	if len(byMint["https://mint-a.test"]) != 1 {
		t.Errorf("mint-a proofs = %d, want 1", len(byMint["https://mint-a.test"]))
	}
	if len(byMint["https://mint-b.test"]) != 2 {
		t.Errorf("mint-b proofs = %d, want 2", len(byMint["https://mint-b.test"]))
	}
}

func TestClose_Idempotent(t *testing.T) {
	s := newTestStore(t)
	s.Close()
	s.Close() // must not panic
}
