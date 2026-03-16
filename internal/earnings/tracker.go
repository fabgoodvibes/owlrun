// Package earnings persists and retrieves daily and all-time earnings
// using a local SQLite database at ~/.owlrun/earnings.db.
package earnings

import (
	"database/sql"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS jobs (
	id        INTEGER PRIMARY KEY AUTOINCREMENT,
	ts        INTEGER NOT NULL,  -- Unix timestamp (UTC)
	model     TEXT    NOT NULL,
	tokens    INTEGER NOT NULL,
	earned    REAL    NOT NULL   -- USD
);
CREATE INDEX IF NOT EXISTS idx_jobs_ts ON jobs(ts);
`

// Snapshot holds a point-in-time view of earnings.
type Snapshot struct {
	Today float64
	Total float64
}

// Tracker manages earnings persistence.
type Tracker struct {
	mu sync.Mutex
	db *sql.DB
}

// New opens (or creates) the earnings database and returns a Tracker.
// Logs and returns a zero-value tracker if the DB cannot be opened —
// the tray still works, earnings just won't persist.
func New() *Tracker {
	t := &Tracker{}
	if err := t.open(); err != nil {
		log.Printf("owlrun: earnings db: %v — earnings will not persist", err)
	}
	return t
}

func (t *Tracker) open() error {
	dir := owlrunDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "earnings.db"))
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1) // SQLite: single writer
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return err
	}
	t.db = db
	return nil
}

// Get returns current earnings totals. Safe to call when db is nil.
func (t *Tracker) Get() Snapshot {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.db == nil {
		return Snapshot{}
	}

	// Midnight UTC today.
	now := time.Now().UTC()
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).Unix()

	var snap Snapshot
	t.db.QueryRow(
		`SELECT COALESCE(SUM(earned),0) FROM jobs WHERE ts >= ?`, midnight,
	).Scan(&snap.Today)
	t.db.QueryRow(
		`SELECT COALESCE(SUM(earned),0) FROM jobs`,
	).Scan(&snap.Total)
	return snap
}

// Record persists a completed job. Safe to call when db is nil.
func (t *Tracker) Record(model string, tokens int, earned float64) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.db == nil {
		return
	}
	if _, err := t.db.Exec(
		`INSERT INTO jobs(ts, model, tokens, earned) VALUES (?,?,?,?)`,
		time.Now().UTC().Unix(), model, tokens, earned,
	); err != nil {
		log.Printf("owlrun: earnings record: %v", err)
	}
}

// Close shuts down the database connection cleanly.
func (t *Tracker) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.db != nil {
		t.db.Close()
		t.db = nil
	}
}

func owlrunDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".owlrun")
}
