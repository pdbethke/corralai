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
	"strings"

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
	// Before anything else touches gate_runs: an older binary's key
	// migration could be interrupted between its statements and leave a
	// state the code below would either fail on or silently orphan. Repair
	// it first. (Review 8be2189163b0, R5.)
	if err := recoverInterruptedMigration(db); err != nil {
		_ = db.Close()
		return nil, err
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
	// A store created before 2026-09-12 has neither column and is keyed on
	// (repo, head_sha) alone. Add what is missing rather than refusing to
	// open: an operator's existing gate must keep working.
	//
	// THE COMMENT THAT USED TO BE HERE WAS FALSE. It said the legacy PRIMARY
	// KEY "cannot be widened in place, which is fine — the old key is strictly
	// narrower, so those rows keep deduping". It is not fine. Save uses INSERT
	// OR REPLACE, so under the narrow key two policies on the SAME head
	// OVERWRITE each other: the second context's row replaces the first's,
	// GetByHead then misses the row it just wrote for the other context, and
	// the poller re-runs both policies on every tick, forever. That is a worse
	// failure than the one the context column was added to fix, and it was
	// shipped on the strength of a sentence I wrote asserting it was safe.
	// (Cold review round three, 2026-09-12, R3 — reproduced, high.)
	//
	// So the key is genuinely widened: DuckDB cannot ALTER a PRIMARY KEY, and
	// migrateKey below rebuilds the table when duckdb_constraints() shows the
	// old shape. It is conditional and idempotent — a store already on the
	// wide key is untouched.
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
	if err := migrateKey(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// migrateKey widens gate_runs' PRIMARY KEY to (repo, head_sha, context) when
// it is still the legacy (repo, head_sha).
//
// DuckDB cannot ALTER a PRIMARY KEY, so the table is rebuilt: create the right
// shape, copy every row, swap. It reads the CURRENT key out of
// duckdb_constraints() rather than guessing from a version marker, so it is
// idempotent and a store already on the wide key costs one cheap query.
//
// Rows that collided under the narrow key are already lost — one overwrote the
// other before this ran — and no migration can invent them back. They are
// re-gated when their heads next appear, which is the correct outcome.
//
// The four statements run in ONE transaction. They used to be four separately
// committed statements, so a crash between them left either a stale
// gate_runs_wide (every later open failed on "already exists", disabling the
// gate) or every row stranded in gate_runs_wide beside a fresh, empty table
// (the dedupe history orphaned, every open head re-gated and re-certified).
// recoverInterruptedMigration repairs either state an older binary may
// already have left. (Review 8be2189163b0, R5.)
func migrateKey(db *sql.DB) error {
	wide, ok, err := gateRunsKeyIsWide(db)
	if err != nil {
		return err
	}
	if !ok || wide {
		return nil // no primary key at all, or already wide: nothing to widen
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("gate: widening gate_runs primary key: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after Commit
	stmts := []string{
		`CREATE TABLE gate_runs_wide (
			repo VARCHAR NOT NULL,
			head_sha VARCHAR NOT NULL,
			context VARCHAR NOT NULL DEFAULT 'corral/gate',
			pr INTEGER NOT NULL,
			passed BOOLEAN NOT NULL,
			status_posted BOOLEAN NOT NULL DEFAULT FALSE,
			record_id BIGINT NOT NULL,
			ran_at TIMESTAMP NOT NULL,
			PRIMARY KEY (repo, head_sha, context)
		)`,
		`INSERT INTO gate_runs_wide
		 SELECT repo, head_sha, coalesce(context, 'corral/gate'), pr, passed,
		        coalesce(status_posted, TRUE), record_id, ran_at
		 FROM gate_runs`,
		`DROP TABLE gate_runs`,
		`ALTER TABLE gate_runs_wide RENAME TO gate_runs`,
	}
	for _, st := range stmts {
		if _, err := tx.Exec(st); err != nil {
			return fmt.Errorf("gate: widening gate_runs primary key: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("gate: widening gate_runs primary key: %w", err)
	}
	return nil
}

// gateRunsKeyIsWide reports whether gate_runs' primary key includes context.
// ok is false when the table has no primary key at all.
func gateRunsKeyIsWide(db *sql.DB) (wide, ok bool, err error) {
	var cols string
	err = db.QueryRow(`SELECT list_aggregate(constraint_column_names, 'string_agg', ',')
		FROM duckdb_constraints()
		WHERE table_name = 'gate_runs' AND constraint_type = 'PRIMARY KEY'`).Scan(&cols)
	if err == sql.ErrNoRows {
		return false, false, nil
	}
	if err != nil {
		return false, false, fmt.Errorf("gate: reading gate_runs primary key: %w", err)
	}
	return strings.Contains(cols, "context"), true, nil
}

// recoverInterruptedMigration repairs what an interrupted key migration from
// an older, non-transactional binary can leave on disk. gate_runs_wide exists
// only mid-migration, so its presence at open means one was cut short, and
// WHERE it was cut decides the repair:
//
//   - gate_runs is gone: the crash came after DROP and before RENAME. The rows
//     live only in gate_runs_wide — finish the swap.
//   - gate_runs still has the NARROW key: the crash came after CREATE (and
//     perhaps INSERT), before DROP. The narrow table still holds every row, so
//     the partial copy is discarded and migrateKey runs the whole thing again.
//   - gate_runs already has the WIDE key: the crash came after DROP, and the
//     next open created a fresh table that has been in use since. The orphan
//     is merged back with INSERT OR IGNORE — a row the live table wrote since
//     is newer and wins — and only then dropped. Dropping it unmerged would
//     throw away the very history this repair exists to keep.
//
// It runs in one transaction, so the repair cannot itself be interrupted into
// a new state.
func recoverInterruptedMigration(db *sql.DB) error {
	has := func(name string) (bool, error) {
		var n int
		err := db.QueryRow(`SELECT count(*) FROM duckdb_tables() WHERE table_name = ?`, name).Scan(&n)
		return n > 0, err
	}
	leftover, err := has("gate_runs_wide")
	if err != nil {
		return fmt.Errorf("gate: checking for an interrupted migration: %w", err)
	}
	if !leftover {
		return nil
	}
	live, err := has("gate_runs")
	if err != nil {
		return fmt.Errorf("gate: checking for an interrupted migration: %w", err)
	}
	var stmts []string
	switch {
	case !live:
		stmts = []string{`ALTER TABLE gate_runs_wide RENAME TO gate_runs`}
	default:
		wide, _, err := gateRunsKeyIsWide(db)
		if err != nil {
			return err
		}
		if wide {
			stmts = append(stmts, `INSERT OR IGNORE INTO gate_runs
				(repo, head_sha, context, pr, passed, status_posted, record_id, ran_at)
				SELECT repo, head_sha, coalesce(context, 'corral/gate'), pr, passed,
				       coalesce(status_posted, TRUE), record_id, ran_at
				FROM gate_runs_wide`)
		}
		stmts = append(stmts, `DROP TABLE gate_runs_wide`)
	}
	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("gate: repairing an interrupted migration: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // a no-op after Commit
	for _, st := range stmts {
		if _, err := tx.Exec(st); err != nil {
			return fmt.Errorf("gate: repairing an interrupted migration: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("gate: repairing an interrupted migration: %w", err)
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// Save upserts a gate run row keyed on (Repo, HeadSHA). RanAt is persisted
// exactly as given — the store never calls time.Now() itself; the caller
// (the runner, Task 4) is responsible for stamping it, which keeps this
// store deterministic under test.
func (s *Store) Save(r Run) error {
	ctxName := normalizeContext(r.Context)
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
	statusCtx = normalizeContext(statusCtx)
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
	statusCtx = normalizeContext(statusCtx)
	if _, err := s.db.Exec(
		`UPDATE gate_runs SET status_posted = TRUE
		 WHERE repo = ? AND head_sha = ? AND coalesce(context, 'corral/gate') = ?`,
		repo, sha, statusCtx); err != nil {
		return fmt.Errorf("gate: mark posted: %w", err)
	}
	return nil
}
