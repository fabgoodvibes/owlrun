// Package earnings persists and retrieves daily and all-time earnings
// using a local SQLite database at ~/.owlrun/earnings.db.
package earnings

import (
	"database/sql"
	"fmt"
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

// HistoryBucket is one time-bucketed aggregation of jobs.
type HistoryBucket struct {
	Label  string  `json:"label"`
	TS     int64   `json:"ts"`
	Jobs   int     `json:"jobs"`
	Earned float64 `json:"earned"`
}

// History returns time-bucketed job/earnings data for the given period.
// Valid periods: "24h", "7d", "30d", "1y". Safe to call when db is nil.
func (t *Tracker) History(period string) []HistoryBucket {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.db == nil {
		return nil
	}

	now := time.Now().UTC()
	var since time.Time
	var bucketSec int64
	var labelFn func(time.Time) string

	switch period {
	case "7d":
		since = now.Add(-7 * 24 * time.Hour)
		bucketSec = 86400
		labelFn = func(t time.Time) string { return t.Format("Jan 2") }
	case "30d":
		since = now.Add(-30 * 24 * time.Hour)
		bucketSec = 86400
		labelFn = func(t time.Time) string { return t.Format("Jan 2") }
	case "1y":
		since = now.Add(-365 * 24 * time.Hour)
		bucketSec = 0 // monthly — handled separately
		labelFn = func(t time.Time) string { return t.Format("Jan '06") }
	default: // "24h"
		since = now.Add(-24 * time.Hour)
		bucketSec = 3600
		labelFn = func(t time.Time) string { return t.Format("15:04") }
	}

	sinceUnix := since.Unix()

	if bucketSec == 0 {
		return t.historyMonthly(sinceUnix, now, labelFn)
	}
	return t.historyBucketed(sinceUnix, now, bucketSec, labelFn)
}

func (t *Tracker) historyBucketed(sinceUnix int64, now time.Time, bucketSec int64, labelFn func(time.Time) string) []HistoryBucket {
	rows, err := t.db.Query(
		`SELECT (ts / ?) * ? AS bucket_ts, COUNT(*) AS jobs, COALESCE(SUM(earned),0) AS earned
		 FROM jobs WHERE ts >= ? GROUP BY bucket_ts ORDER BY bucket_ts ASC`,
		bucketSec, bucketSec, sinceUnix,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	data := map[int64]HistoryBucket{}
	for rows.Next() {
		var b HistoryBucket
		if err := rows.Scan(&b.TS, &b.Jobs, &b.Earned); err != nil {
			continue
		}
		data[b.TS] = b
	}

	// Fill empty buckets.
	startBucket := (sinceUnix / bucketSec) * bucketSec
	endBucket := (now.Unix() / bucketSec) * bucketSec
	var out []HistoryBucket
	for ts := startBucket; ts <= endBucket; ts += bucketSec {
		b, ok := data[ts]
		if !ok {
			b = HistoryBucket{TS: ts}
		}
		b.Label = labelFn(time.Unix(ts, 0).UTC())
		out = append(out, b)
	}
	return out
}

func (t *Tracker) historyMonthly(sinceUnix int64, now time.Time, labelFn func(time.Time) string) []HistoryBucket {
	rows, err := t.db.Query(
		`SELECT strftime('%Y-%m', ts, 'unixepoch') AS month, MIN(ts) AS bucket_ts,
		        COUNT(*) AS jobs, COALESCE(SUM(earned),0) AS earned
		 FROM jobs WHERE ts >= ? GROUP BY month ORDER BY month ASC`,
		sinceUnix,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	data := map[string]HistoryBucket{}
	for rows.Next() {
		var month string
		var b HistoryBucket
		if err := rows.Scan(&month, &b.TS, &b.Jobs, &b.Earned); err != nil {
			continue
		}
		data[month] = b
	}

	// Fill empty months.
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -11, 0)
	var out []HistoryBucket
	for m := start; !m.After(now); m = m.AddDate(0, 1, 0) {
		key := fmt.Sprintf("%04d-%02d", m.Year(), m.Month())
		b, ok := data[key]
		if !ok {
			b = HistoryBucket{TS: m.Unix()}
		}
		b.Label = labelFn(m)
		out = append(out, b)
	}
	return out
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
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		// Fallback: check $HOME explicitly (launchd on macOS may not
		// propagate it to os.UserHomeDir in all contexts).
		home = os.Getenv("HOME")
	}
	if home == "" {
		// Last resort — use temp dir so we don't write to a read-only CWD.
		home = os.TempDir()
	}
	return filepath.Join(home, ".owlrun")
}
