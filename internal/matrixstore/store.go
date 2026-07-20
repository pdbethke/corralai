// SPDX-License-Identifier: Elastic-2.0

// Package matrixstore is the append-only, per-test DuckDB store behind the
// tests×mutants adequacy matrix: one row per test per converged run,
// recording how many of the mutants planted for that run a given test
// actually killed. Unlike internal/criticscore (mutable, one row per key,
// upserted), this mirrors internal/bugcatch's append-per-run shape — every
// converged run adds new rows, nothing is ever updated in place, and
// "current" adequacy is derived by taking the latest row per
// (repo, commit, test_selector) key. That derivation is what powers
// DeleteCandidates: a test whose most recent run killed zero of the mutants
// offered to it is a candidate for removal (dead weight, not coverage).
package matrixstore

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// matrixTestAdequacyMigrationCols is the additive set of columns this
// package has ever needed beyond the original CREATE TABLE, in the order
// they must be added — a ledger created before a given column existed gets
// it added on open; a ledger created after already has it and it is not
// re-added. Empty today; kept so future columns follow bugcatch's proven
// additive-migration pattern instead of a destructive one.
var matrixTestAdequacyMigrationCols = []struct{ name, ddl string }{}

type Store struct{ db *sql.DB }

func Open(dsn string) (*Store, error) {
	db, err := sql.Open("duckdb", dsn)
	if err != nil {
		return nil, fmt.Errorf("matrixstore: open %q: %w", dsn, err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS matrix_test_adequacy (
		ts DOUBLE, record_id BIGINT, record_head VARCHAR, repo VARCHAR, commit VARCHAR,
		mission_id BIGINT, lang VARCHAR, test_selector VARCHAR, test_file VARCHAR,
		kills INTEGER, mutants_total INTEGER, delete_candidate BOOLEAN
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("matrixstore: create table: %w", err)
	}
	if err := migrateMatrixTestAdequacy(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// migrateMatrixTestAdequacy additively brings a ledger created before a
// migration column existed up to the current column set. DuckDB has no
// `ADD COLUMN IF NOT EXISTS`, and this is a ledger — silently discarding
// every ALTER error would make a genuinely broken migration indistinguishable
// from an already-applied one. Instead: probe information_schema.columns for
// what already exists, add only what's missing, and surface any other ALTER
// failure as a real error. Idempotent across repeated opens: a table that
// already has every column runs zero ALTERs.
func migrateMatrixTestAdequacy(db *sql.DB) error {
	rows, err := db.Query(`SELECT column_name FROM information_schema.columns WHERE table_name = ?`, "matrix_test_adequacy")
	if err != nil {
		return fmt.Errorf("matrixstore: probe existing columns: %w", err)
	}
	existing := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return fmt.Errorf("matrixstore: scan existing column: %w", err)
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("matrixstore: probe existing columns: %w", err)
	}
	rows.Close()

	for _, col := range matrixTestAdequacyMigrationCols {
		if existing[col.name] {
			continue
		}
		if _, err := db.Exec("ALTER TABLE matrix_test_adequacy ADD COLUMN " + col.ddl); err != nil {
			return fmt.Errorf("matrixstore: migrate: add column %s: %w", col.name, err)
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

// Record appends rows for a single converged run, stamping each with the
// insert-time ts (this is an append log, not a caller-supplied-timestamp
// ledger — the moment a row lands is the moment it's true as of). No-op on
// an empty slice so callers can pass a possibly-empty matrix without a
// guard.
func (s *Store) Record(ctx context.Context, rows []Row) error {
	if len(rows) == 0 {
		return nil
	}
	ts := float64(time.Now().UnixNano()) / 1e9
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("matrixstore: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	for _, r := range rows {
		if _, err := tx.ExecContext(ctx, `INSERT INTO matrix_test_adequacy (
			ts, record_id, record_head, repo, commit, mission_id, lang,
			test_selector, test_file, kills, mutants_total, delete_candidate
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
			ts, r.RecordID, r.RecordHead, r.Repo, r.Commit, r.MissionID, r.Lang,
			r.TestSelector, r.TestFile, r.Kills, r.MutantsTotal, r.DeleteCandidate); err != nil {
			return fmt.Errorf("matrixstore: insert: %w", err)
		}
	}
	return tx.Commit()
}

// matrixListLimit bounds List to the most recent N rows so it can never scan
// an arbitrarily large production ledger into memory — mirrors bugcatch's
// observationsLimit.
const matrixListLimit = 10000

var matrixSelectCols = `ts, record_id, record_head, repo, commit, mission_id, lang, test_selector, test_file, kills, mutants_total, delete_candidate`

func scanRow(scanner interface{ Scan(...any) error }) (Row, error) {
	var r Row
	err := scanner.Scan(&r.TS, &r.RecordID, &r.RecordHead, &r.Repo, &r.Commit, &r.MissionID, &r.Lang,
		&r.TestSelector, &r.TestFile, &r.Kills, &r.MutantsTotal, &r.DeleteCandidate)
	return r, err
}

// DeleteCandidates returns the latest row per (repo, commit, test_selector)
// key where that latest row's delete_candidate is true — a test that, as of
// its most recent converged run, killed none of the mutants offered to it.
// An older delete_candidate=true row superseded by a newer non-candidate row
// for the same key is correctly excluded: the subquery pins ts to the max
// per key across ALL rows (not just candidate rows), so "latest" always
// means the newest run, and the outer WHERE then filters to the ones that
// are still candidates as of that latest run.
func (s *Store) DeleteCandidates(ctx context.Context) ([]Row, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+matrixSelectCols+`
		FROM matrix_test_adequacy m
		WHERE delete_candidate = TRUE
		AND ts = (
			SELECT MAX(ts) FROM matrix_test_adequacy m2
			WHERE m2.repo = m.repo AND m2.commit = m.commit AND m2.test_selector = m.test_selector
		)
		ORDER BY repo, commit, test_selector`)
	if err != nil {
		return nil, fmt.Errorf("matrixstore: delete candidates: %w", err)
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, fmt.Errorf("matrixstore: scan delete candidate: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// List returns the most recent rows (newest first, capped at
// matrixListLimit), unaggregated — every row Record wrote, for ad hoc
// debugging and round-trip tests.
func (s *Store) List(ctx context.Context) ([]Row, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+matrixSelectCols+`
		FROM matrix_test_adequacy
		ORDER BY ts DESC
		LIMIT ?`, matrixListLimit)
	if err != nil {
		return nil, fmt.Errorf("matrixstore: list: %w", err)
	}
	defer rows.Close()
	var out []Row
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, fmt.Errorf("matrixstore: scan row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
