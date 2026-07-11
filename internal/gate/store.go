// SPDX-License-Identifier: Elastic-2.0

// Package gate is corralai's merge-gate dedupe/index store: a thin DuckDB
// table (`gate_runs`) mapping (repo, head_sha) -> {pr, passed, record_id,
// ran_at}. The full SIGNED gate record lives in the existing buildstore;
// this table just lets the poller (Task 5) skip SHAs that already have a
// gate run and lets a read endpoint look one up cheaply.
package gate

import (
	"database/sql"
	"fmt"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// Store is a DuckDB-backed table of gate_runs dedupe/index rows.
type Store struct{ db *sql.DB }

// OpenStore opens (creating if absent) the gate_runs store at dsn. dsn is
// kept opaque — never parsed/validated as a filesystem path — so a local
// `.duckdb` file and a MotherDuck `md:` DSN both work unchanged.
func OpenStore(dsn string) (*Store, error) {
	db, err := sql.Open("duckdb", dsn)
	if err != nil {
		return nil, fmt.Errorf("gate: open: %w", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS gate_runs (
		repo VARCHAR NOT NULL,
		head_sha VARCHAR NOT NULL,
		pr INTEGER NOT NULL,
		passed BOOLEAN NOT NULL,
		record_id BIGINT NOT NULL,
		ran_at TIMESTAMP NOT NULL,
		PRIMARY KEY (repo, head_sha)
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("gate: creating gate_runs table: %w", err)
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// Save upserts a gate run row keyed on (Repo, HeadSHA). RanAt is persisted
// exactly as given — the store never calls time.Now() itself; the caller
// (the runner, Task 4) is responsible for stamping it, which keeps this
// store deterministic under test.
func (s *Store) Save(r Run) error {
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO gate_runs (repo, head_sha, pr, passed, record_id, ran_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		r.Repo, r.HeadSHA, r.PR, r.Passed, r.RecordID, r.RanAt)
	if err != nil {
		return fmt.Errorf("gate: save: %w", err)
	}
	return nil
}

// GetBySHA looks up the gate run for (repo, sha), returning (Run{}, false,
// nil) when no such row exists.
func (s *Store) GetBySHA(repo, sha string) (Run, bool, error) {
	var r Run
	r.Repo = repo
	r.HeadSHA = sha
	err := s.db.QueryRow(
		`SELECT pr, passed, record_id, ran_at FROM gate_runs WHERE repo = ? AND head_sha = ?`,
		repo, sha).Scan(&r.PR, &r.Passed, &r.RecordID, &r.RanAt)
	if err == sql.ErrNoRows {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, fmt.Errorf("gate: get by sha: %w", err)
	}
	return r, true, nil
}
