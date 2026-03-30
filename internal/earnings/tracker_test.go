package earnings

import (
	"os"
	"testing"
	"time"
)

// newTestTracker opens a Tracker backed by a DB in t.TempDir().
func newTestTracker(t *testing.T) *Tracker {
	t.Helper()
	dir := t.TempDir()
	// Temporarily redirect owlrunDir() by setting HOME.
	orig := os.Getenv("HOME")
	os.Setenv("HOME", dir)
	t.Cleanup(func() { os.Setenv("HOME", orig) })

	tr := New()
	if tr.db == nil {
		t.Fatal("Tracker.db is nil — SQLite failed to open")
	}
	t.Cleanup(func() { tr.Close() })
	return tr
}

func TestGet_EmptyDB(t *testing.T) {
	tr := newTestTracker(t)
	snap := tr.Get()
	if snap.Today != 0 {
		t.Errorf("Today = %f, want 0", snap.Today)
	}
	if snap.Total != 0 {
		t.Errorf("Total = %f, want 0", snap.Total)
	}
}

func TestRecord_And_Get(t *testing.T) {
	tr := newTestTracker(t)

	if err := tr.Record("llama3:8b", 500, 0.01); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := tr.Record("llama3:8b", 1000, 0.02); err != nil {
		t.Fatalf("Record: %v", err)
	}

	snap := tr.Get()
	const want = 0.03
	if snap.Today < 0.0299 || snap.Today > 0.0301 {
		t.Errorf("Today = %f, want ~%f", snap.Today, want)
	}
	if snap.Total < 0.0299 || snap.Total > 0.0301 {
		t.Errorf("Total = %f, want ~%f", snap.Total, want)
	}
}

func TestRecord_TodayVsTotal(t *testing.T) {
	tr := newTestTracker(t)

	// Insert a row with yesterday's timestamp directly via SQL.
	yesterday := time.Now().UTC().AddDate(0, 0, -1).Unix()
	tr.mu.Lock()
	tr.db.Exec(
		`INSERT INTO jobs(ts, model, tokens, earned) VALUES (?,?,?,?)`,
		yesterday, "old-model", 100, 0.05,
	)
	tr.mu.Unlock()

	// Today's job.
	if err := tr.Record("llama3:8b", 200, 0.10); err != nil {
		t.Fatalf("Record: %v", err)
	}

	snap := tr.Get()
	if snap.Today < 0.099 || snap.Today > 0.101 {
		t.Errorf("Today = %f, want ~0.10 (yesterday's job excluded)", snap.Today)
	}
	if snap.Total < 0.149 || snap.Total > 0.151 {
		t.Errorf("Total = %f, want ~0.15 (all jobs)", snap.Total)
	}
}

func TestGet_NilDB_Safe(t *testing.T) {
	// Tracker with no DB must not panic.
	tr := &Tracker{}
	snap := tr.Get()
	if snap.Today != 0 || snap.Total != 0 {
		t.Errorf("nil-db Get returned non-zero: %+v", snap)
	}
}

func TestRecord_NilDB_ReturnsError(t *testing.T) {
	tr := &Tracker{}
	err := tr.Record("model", 100, 0.01)
	if err == nil {
		t.Error("Record on nil-db should return error")
	}
}

func TestClose_Idempotent(t *testing.T) {
	tr := newTestTracker(t)
	tr.Close()
	tr.Close() // must not panic
}

func TestRecord_Multiple_Accumulates(t *testing.T) {
	tr := newTestTracker(t)

	for i := 0; i < 10; i++ {
		if err := tr.Record("llama3:8b", 100, 0.001); err != nil {
			t.Fatalf("Record %d: %v", i, err)
		}
	}

	snap := tr.Get()
	if snap.Total < 0.0099 || snap.Total > 0.0101 {
		t.Errorf("Total = %f after 10 records of 0.001, want ~0.01", snap.Total)
	}
}

func TestRecord_ReturnsErrorAfterClose(t *testing.T) {
	tr := newTestTracker(t)
	tr.Close()
	err := tr.Record("model", 100, 0.01)
	if err == nil {
		t.Error("Record after Close should return error")
	}
}
