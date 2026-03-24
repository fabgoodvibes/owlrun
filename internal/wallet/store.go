// Package wallet provides a local Cashu ecash wallet for the Owlrun node client.
// Proofs are stored in SQLite at ~/.owlrun/wallet.db.
package wallet

import (
	"database/sql"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/fabgoodvibes/owlrun/internal/cashu"
)

const storeSchema = `
CREATE TABLE IF NOT EXISTS proofs (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	amount     INTEGER NOT NULL,
	keyset_id  TEXT    NOT NULL,
	secret     TEXT    NOT NULL UNIQUE,
	c          TEXT    NOT NULL,
	mint_url   TEXT    NOT NULL,
	spent      INTEGER NOT NULL DEFAULT 0,
	claimed_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_proofs_unspent ON proofs(spent) WHERE spent = 0;
`

// Store manages local ecash proof persistence.
type Store struct {
	mu sync.Mutex
	db *sql.DB
}

// NewStore opens (or creates) the wallet database.
// Logs and returns a zero-value store if the DB cannot be opened —
// the client still works, wallet just won't persist.
func NewStore() *Store {
	s := &Store{}
	if err := s.open(); err != nil {
		log.Printf("owlrun: wallet db: %v — wallet will not persist", err)
	}
	return s
}

func (s *Store) open() error {
	dir := owlrunDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	db, err := sql.Open("sqlite", filepath.Join(dir, "wallet.db"))
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(storeSchema); err != nil {
		db.Close()
		return err
	}
	s.db = db
	return nil
}

// Balance returns the total unspent sats. Safe to call when db is nil.
// Returns 0 and logs on error (safe default for dashboard display).
func (s *Store) Balance() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return 0
	}
	var total int64
	if err := s.db.QueryRow(`SELECT COALESCE(SUM(amount), 0) FROM proofs WHERE spent = 0`).Scan(&total); err != nil {
		log.Printf("owlrun: wallet balance query: %v", err)
		return 0
	}
	return total
}

// ProofCount returns the number of unspent proofs.
// Returns 0 and logs on error (safe default for dashboard display).
func (s *Store) ProofCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return 0
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM proofs WHERE spent = 0`).Scan(&count); err != nil {
		log.Printf("owlrun: wallet proof count query: %v", err)
		return 0
	}
	return count
}

// Save persists claimed proofs. Duplicates (by secret) are silently ignored.
func (s *Store) Save(mintURL string, proofs []cashu.Proof) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	now := time.Now().UTC().Unix()
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(
		`INSERT OR IGNORE INTO proofs(amount, keyset_id, secret, c, mint_url, claimed_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
	)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, p := range proofs {
		if _, err := stmt.Exec(p.Amount, p.ID, p.Secret, p.C, mintURL, now); err != nil {
			tx.Rollback()
			return fmt.Errorf("wallet: save proof (secret=%s): %w", p.Secret, err)
		}
	}
	return tx.Commit()
}

// Proofs returns all unspent proofs grouped by mint URL.
func (s *Store) Proofs() map[string][]cashu.Proof {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	rows, err := s.db.Query(
		`SELECT amount, keyset_id, secret, c, mint_url FROM proofs WHERE spent = 0 ORDER BY mint_url, amount`,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()

	out := map[string][]cashu.Proof{}
	for rows.Next() {
		var p cashu.Proof
		var mint string
		if err := rows.Scan(&p.Amount, &p.ID, &p.Secret, &p.C, &mint); err != nil {
			continue
		}
		out[mint] = append(out[mint], p)
	}
	return out
}

// MarkSpent marks proofs with the given secrets as spent.
func (s *Store) MarkSpent(secrets []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil || len(secrets) == 0 {
		return nil
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	stmt, err := tx.Prepare(`UPDATE proofs SET spent = 1 WHERE secret = ?`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, sec := range secrets {
		if _, err := stmt.Exec(sec); err != nil {
			tx.Rollback()
			return fmt.Errorf("wallet: mark spent (secret=%s): %w", sec, err)
		}
	}
	return tx.Commit()
}

// Close shuts down the database connection.
func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		s.db.Close()
		s.db = nil
	}
}

func owlrunDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		home = os.Getenv("HOME")
	}
	if home == "" {
		home = os.TempDir()
	}
	return filepath.Join(home, ".owlrun")
}
