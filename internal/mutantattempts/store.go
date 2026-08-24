// SPDX-License-Identifier: Elastic-2.0

// Package mutantattempts is the append-only, RECORD-anchored DuckDB store
// behind the writer-seat correlation statistic: one row per (audit record ×
// seat × mutant), saying which model, in which role, killed or did not kill
// that mutant. Two rows sharing (record_id, path, mutant_id) and differing in
// model are the paired observation internal/modelcorr needs.
//
// WHY ITS OWN STORE, KEYED BY record_id. The measurement was first built as a
// table in internal/scanstore, keyed by scan_id. That was the wrong home twice
// over: scanstore is opened ONLY by the repo-scan path, which exposes no
// challenger-writer flag and structurally cannot produce this data, and
// `certify --local` — the one command that CAN — never opens a scanstore at
// all. record_id is also the right key on the merits: it is the id of the
// signed audit record the outcomes came from, so it discriminates RUNS. A
// path-keyed table with no run discriminator would merge two audits of one
// file last-write-wins, reintroducing exactly the cross-run pooling the design
// rejects (correlation is computed WITHIN a run, over one fixed mutant set).
//
// Mirrors internal/bugcatch's DuckDB pattern: CREATE IF NOT EXISTS on open,
// parameterized SQL, timestamps supplied by the caller — no time.Now() here.
//
// This is a MEASUREMENT store. Nothing written here reaches a Verdict, an
// aggregate, or a signed record; nothing read from it may gate an audit.
package mutantattempts

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// Store is a DuckDB-backed table of mutant_attempts rows.
type Store struct{ db *sql.DB }

// Attempt is ONE seat's outcome for ONE mutant.
//
// Rows are written as a PAIR or not at all (advpool's recordMutantAttempts
// owns that rule — this store cannot see a run and does not enforce it): an
// unpaired vector cannot contribute to a within-run correlation, and a table
// half-full of unpairable rows would invite the cross-run pooling the design
// rejects.
type Attempt struct {
	TS         time.Time
	RecordID   int64
	RecordHead string
	MissionID  int64
	Repo       string
	Commit     string

	Path     string
	MutantID string
	Model    string
	Role     string // "test-writer" | "test-writer-shadow"
	// Shadow is derivable from Role but kept for query convenience and for
	// symmetry with bugcatch_observations.
	Shadow  bool
	Outcome string // "killed" | "survived"
}

// Open opens (creating if absent) the mutant_attempts store at dsn. dsn is
// kept opaque, matching bugcatch.Open, so both a local `.duckdb` file and a
// MotherDuck `md:` DSN work unchanged.
//
// The store ships with its full column set, so there is no migration ledger
// yet. The first additive column must follow bugcatch's proven
// probe-information_schema-then-ALTER pattern rather than a bespoke one.
func Open(dsn string) (*Store, error) {
	db, err := sql.Open("duckdb", dsn)
	if err != nil {
		return nil, fmt.Errorf("mutantattempts: open %q: %w", dsn, err)
	}
	// The CHECK on outcome exists for the same reason scan_mutants has one:
	// this table is queried by exact string, and a typo'd label should fail
	// loud at INSERT rather than quietly enter a statistic.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS mutant_attempts (
		ts TIMESTAMP, record_id BIGINT, record_head VARCHAR, mission_id BIGINT,
		repo VARCHAR, commit VARCHAR,
		path VARCHAR, mutant_id VARCHAR, model VARCHAR, role VARCHAR, shadow BOOLEAN,
		outcome VARCHAR CHECK (outcome IN ('killed', 'survived'))
	)`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("mutantattempts: create table: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Record writes a batch of seat outcomes in one transaction.
func (s *Store) Record(ctx context.Context, as []Attempt) error {
	if len(as) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("mutantattempts: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, a := range as {
		model := a.Model
		if model == "" {
			model = "(unknown model)"
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO mutant_attempts (
			ts, record_id, record_head, mission_id, repo, commit,
			path, mutant_id, model, role, shadow, outcome
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			a.TS, a.RecordID, a.RecordHead, a.MissionID, a.Repo, a.Commit,
			a.Path, a.MutantID, model, a.Role, a.Shadow, a.Outcome); err != nil {
			return fmt.Errorf("mutantattempts: insert %s/%s/%s: %w", a.Path, a.MutantID, model, err)
		}
	}
	return tx.Commit()
}

// attemptsLimit bounds Attempts to the most recent N rows so it can never scan
// an arbitrarily large ledger into memory — same reasoning as bugcatch's
// observationsLimit.
const attemptsLimit = 100000

// Attempts returns the most recent rows, newest record first. Ordered by
// (record_id, path, mutant_id, model) so a caller reading a pair reads it
// adjacently, and so the ordering is deterministic for tests.
func (s *Store) Attempts(ctx context.Context) ([]Attempt, error) {
	return s.query(ctx, `SELECT ts, record_id, record_head, mission_id, repo, commit,
		path, mutant_id, model, role, shadow, outcome
		FROM mutant_attempts
		ORDER BY record_id DESC, path, mutant_id, model
		LIMIT ?`, attemptsLimit)
}

// AttemptsForRecord returns every seat outcome recorded for ONE audit record —
// the natural unit for the correlation statistic, since a comparison is
// defined within a single run.
func (s *Store) AttemptsForRecord(ctx context.Context, recordID int64) ([]Attempt, error) {
	return s.query(ctx, `SELECT ts, record_id, record_head, mission_id, repo, commit,
		path, mutant_id, model, role, shadow, outcome
		FROM mutant_attempts WHERE record_id = ?
		ORDER BY path, mutant_id, model`, recordID)
}

func (s *Store) query(ctx context.Context, q string, args ...any) ([]Attempt, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("mutantattempts: query: %w", err)
	}
	defer rows.Close()
	var out []Attempt
	for rows.Next() {
		var a Attempt
		if err := rows.Scan(&a.TS, &a.RecordID, &a.RecordHead, &a.MissionID, &a.Repo, &a.Commit,
			&a.Path, &a.MutantID, &a.Model, &a.Role, &a.Shadow, &a.Outcome); err != nil {
			return nil, fmt.Errorf("mutantattempts: scan row: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mutantattempts: rows: %w", err)
	}
	return out, nil
}
