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
		context VARCHAR NOT NULL DEFAULT 'corral/gate',
		pr INTEGER NOT NULL,
		passed BOOLEAN NOT NULL,
		status_posted BOOLEAN NOT NULL DEFAULT FALSE,
		record_id BIGINT NOT NULL,
		ran_at TIMESTAMP NOT NULL,
		PRIMARY KEY (repo, head_sha, context)
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("gate: creating gate_runs table: %w", err)
	}
	// A store created before the cold review of 2026-09-12 has neither column
	// and is keyed on (repo, head_sha) alone. Add what is missing rather than
	// refusing to open: an operator's existing gate must keep working, and the
	// old rows are still valid dedupe entries. The PRIMARY KEY cannot be
	// widened in place, which is fine — the old key is strictly narrower, so
	// those rows keep deduping, just without per-context granularity until
	// they age out with their heads.
	//
	// The ALTERs carry NO constraints on purpose: DuckDB refuses "Adding
	// columns with constraints not yet supported", and it refuses at PARSE
	// time, so an `IF NOT EXISTS` that would have been a no-op still fails.
	// The columns are therefore added nullable and backfilled, and every read
	// COALESCEs — so a pre-migration row and a fresh one behave identically.
	for _, mig := range []string{
		`ALTER TABLE gate_runs ADD COLUMN IF NOT EXISTS context VARCHAR`,
		`ALTER TABLE gate_runs ADD COLUMN IF NOT EXISTS status_posted BOOLEAN`,
		`UPDATE gate_runs SET context = 'corral/gate' WHERE context IS NULL`,
		// A row written before this migration was, by definition, only ever
		// stored AFTER its status post was attempted by the old code path, and
		// the old code returned that post's error to the poller. Treating it
		// as delivered keeps existing gates from all re-running at once on
		// upgrade; a genuinely undelivered old row is re-gated when its head
		// next appears, which is the same outcome as before.
		`UPDATE gate_runs SET status_posted = TRUE WHERE status_posted IS NULL`,
	} {
		if _, err := db.Exec(mig); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("gate: migrating gate_runs: %w", err)
		}
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
	ctxName := r.Context
	if ctxName == "" {
		ctxName = "corral/gate"
	}
	_, err := s.db.Exec(
		`INSERT OR REPLACE INTO gate_runs (repo, head_sha, context, pr, passed, status_posted, record_id, ran_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		r.Repo, r.HeadSHA, ctxName, r.PR, r.Passed, r.StatusPosted, r.RecordID, r.RanAt)
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
		`SELECT pr, passed, coalesce(context, 'corral/gate'), coalesce(status_posted, TRUE),
		        record_id, ran_at FROM gate_runs
		 WHERE repo = ? AND head_sha = ? ORDER BY ran_at DESC LIMIT 1`,
		repo, sha).Scan(&r.PR, &r.Passed, &r.Context, &r.StatusPosted, &r.RecordID, &r.RanAt)
	if err == sql.ErrNoRows {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, fmt.Errorf("gate: get by sha: %w", err)
	}
	return r, true, nil
}

// GetByHead looks up the gate run for (repo, sha, statusCtx) — the full key.
// GetBySHA is kept for the read endpoint, which asks "was this head gated at
// all"; the POLLER must use this one, because two policies on one repo report
// under different contexts and each owes the forge its own status.
// (Cold review 2026-09-12, R3.)
func (s *Store) GetByHead(repo, sha, statusCtx string) (Run, bool, error) {
	if statusCtx == "" {
		statusCtx = "corral/gate"
	}
	r := Run{Repo: repo, HeadSHA: sha, Context: statusCtx}
	err := s.db.QueryRow(
		`SELECT pr, passed, coalesce(status_posted, TRUE), record_id, ran_at FROM gate_runs
		 WHERE repo = ? AND head_sha = ? AND coalesce(context, 'corral/gate') = ?`,
		repo, sha, statusCtx).Scan(&r.PR, &r.Passed, &r.StatusPosted, &r.RecordID, &r.RanAt)
	if err == sql.ErrNoRows {
		return Run{}, false, nil
	}
	if err != nil {
		return Run{}, false, fmt.Errorf("gate: get by head: %w", err)
	}
	return r, true, nil
}

// MarkPosted records that the verdict for (repo, sha, statusCtx) reached the
// forge. Until it does, the poller treats the head as work still outstanding,
// so a status post lost to a forge outage is retried on the next tick instead
// of leaving the pull request pending forever. (Cold review 2026-09-12, R4.)
func (s *Store) MarkPosted(repo, sha, statusCtx string) error {
	if statusCtx == "" {
		statusCtx = "corral/gate"
	}
	if _, err := s.db.Exec(
		`UPDATE gate_runs SET status_posted = TRUE
		 WHERE repo = ? AND head_sha = ? AND coalesce(context, 'corral/gate') = ?`,
		repo, sha, statusCtx); err != nil {
		return fmt.Errorf("gate: mark posted: %w", err)
	}
	return nil
}
