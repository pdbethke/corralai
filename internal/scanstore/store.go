// SPDX-License-Identifier: Elastic-2.0

// Package scanstore is the DuckDB ledger behind `corral certify --repo`: one
// row per invocation in `scans` (the header — provenance for the whole run)
// and one row per walked file in `scan_files` (the disposition — audited
// with a kill rate, or rejected with a reason). `certify --repo` already
// computes a complete disposition for every file it walks; today it prints
// that to stdout and discards it. This store is what keeps it, so a later
// question ("why did file X get skipped on scan N") has an answer.
// Mirrors internal/bugcatch's DuckDB pattern (CREATE IF NOT EXISTS on open,
// an additive migration list applied by probing information_schema.columns,
// parameterized SQL) and internal/buildstore's id-sequence pattern.
package scanstore

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// Store is a DuckDB-backed ledger of `certify --repo` scans and the
// per-file dispositions each one produced.
type Store struct{ db *sql.DB }

// Scan is one `certify --repo` invocation — the header row a scan's files
// are keyed to. It exists to carry PROVENANCE: a disposition without its
// substrate, engine version and model set is a claim without a warrant, and
// this is what a later analysis has to defend itself with.
type Scan struct {
	Owner, Repo, Commit string
	Substrate           string
	EngineVersion       string
	ModelSet            string
	Top                 int
	AllCandidates       bool
	DiffBase            string
	TotalFiles          int
	Candidates          int
	Audited             int
	// KillRate is *float64, not float64, for the same reason File.KillRate
	// is: reposcan.RepoReport.KillRate is math.NaN() when Audited == 0 (a
	// deliberate choice — see internal/reposcan/report.go — so a stored 0.0
	// never misrepresents "no measurement was made" as "terrible tests").
	// But DuckDB sorts NaN as larger than any other DOUBLE value: measured
	// against this exact driver, MAX(kill_rate) over a table containing NaN
	// returns NaN (displacing a real 0.9), `kill_rate > 0.5` MATCHES the NaN
	// row, and `ORDER BY kill_rate DESC LIMIT 1` surfaces the never-measured
	// scan FIRST — the exact inversion of "best-scoring". A caller must
	// convert math.NaN() to nil before constructing a Scan (see
	// certify_repo_record.go's killRatePtr); Record does not re-check
	// IsNaN itself, so this field is the load-bearing contract.
	KillRate      *float64
	CacheHits     int
	PreflightRan  bool
	PreflightNote string
	StartedAt     time.Time
	FinishedAt    time.Time
}

// File is one row per file per scan: what corral decided about it, and, for
// rejected files, why. Evidence is first-class and NOT a detail: "paired"
// is a filename guess, "coverage" is an instrument's report from one
// instrumented suite run, "proven" is execution. A table that averages
// proof with guesswork is a leaderboard nobody can defend.
//
// KillRate is *float64, not float64: a rejected file was never scored, and
// it must read back as NULL, not 0.0. A stored 0.0 would later read as
// "your tests caught nothing here" about a file corral never graded — a
// false accusation, persisted.
type File struct {
	Path           string
	Lang           string
	Disposition    string // "audited" | "rejected"
	Reason         string // populated when Disposition == "rejected"
	KillRate       *float64
	Survivors      int
	Gradable       bool
	PreflightState string
	Evidence       string // "paired" | "coverage" | "proven"
}

// scanFilesMigrationCols is the additive set of columns this package has
// ever needed on scan_files beyond the original bare-bones shape
// (scan_id/path/lang/disposition/reason/kill_rate/survivors/gradable), in
// the order they must be added — a ledger created before preflight_state
// and evidence existed gets them added on open; a ledger created after
// already has them and neither is re-added. Both are also present in the
// fresh CREATE TABLE IF NOT EXISTS above, so a brand-new store never runs
// these ALTERs; this list exists for a store created by an earlier version
// of this package (or, in the round-trip tests, one built by hand to prove
// the migration path actually runs).
var scanFilesMigrationCols = []struct{ name, ddl string }{
	{"preflight_state", "preflight_state VARCHAR"},
	{"evidence", "evidence VARCHAR"},
}

// Open opens (creating if absent) the scans/scan_files store at dsn.
func Open(dsn string) (*Store, error) {
	db, err := sql.Open("duckdb", dsn)
	if err != nil {
		return nil, fmt.Errorf("scanstore: open %q: %w", dsn, err)
	}

	// scans is one row per `certify --repo` invocation. It exists to carry
	// PROVENANCE: a disposition without its substrate, engine version and
	// model set is a claim without a warrant, and this table is what a
	// later analysis has to defend itself with.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS scans (
		id BIGINT PRIMARY KEY, ts TIMESTAMP,
		owner VARCHAR, repo VARCHAR, commit VARCHAR,
		substrate VARCHAR, engine_version VARCHAR, model_set VARCHAR,
		top INTEGER, all_candidates BOOLEAN, diff_base VARCHAR,
		total_files INTEGER, candidates INTEGER, audited INTEGER,
		kill_rate DOUBLE, cache_hits INTEGER,
		preflight_ran BOOLEAN, preflight_note VARCHAR,
		started_at TIMESTAMP, finished_at TIMESTAMP
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("scanstore: create scans table: %w", err)
	}

	// scan_files is one row per file per scan. evidence is first-class and
	// NOT a detail: "paired" is a filename guess, "coverage" is an
	// instrument's report, "proven" is execution. A table that averages
	// proof with guesswork is a leaderboard nobody can defend.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS scan_files (
		scan_id BIGINT, path VARCHAR, lang VARCHAR,
		disposition VARCHAR, reason VARCHAR,
		kill_rate DOUBLE, survivors INTEGER, gradable BOOLEAN,
		preflight_state VARCHAR, evidence VARCHAR
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("scanstore: create scan_files table: %w", err)
	}

	if err := migrateScanFiles(db); err != nil {
		db.Close()
		return nil, err
	}

	// scans.id allocation: a CREATE SEQUENCE + nextval(), the same approach
	// internal/buildstore, internal/telemetry, internal/reference and
	// internal/repoindex already use for their own BIGINT PRIMARY KEYs on
	// this driver. Chosen over `SELECT COALESCE(MAX(id),0)+1` because that
	// form races under concurrent Record calls (two callers can read the
	// same MAX before either inserts); a DuckDB SEQUENCE hands out each
	// value atomically. Verified against this exact driver version
	// (github.com/marcboeker/go-duckdb/v2 v2.4.3, already a dependency) by
	// TestRecordRoundTripsEveryDisposition, which asserts the returned id
	// is nonzero and then reads the row back by that id.
	if _, err := db.Exec(`CREATE SEQUENCE IF NOT EXISTS scans_id START 1`); err != nil {
		db.Close()
		return nil, fmt.Errorf("scanstore: create scans_id sequence: %w", err)
	}

	return &Store{db: db}, nil
}

// migrateScanFiles additively brings a scan_files table created before a
// later column existed up to the current column set. DuckDB has no
// `ADD COLUMN IF NOT EXISTS`, and this is a ledger — silently discarding
// every ALTER error would make a genuinely broken migration indistinguishable
// from an already-applied one. Instead: probe information_schema.columns for
// what already exists, add only what's missing, and surface any other ALTER
// failure as a real error. Idempotent across repeated opens: a table that
// already has every column runs zero ALTERs.
func migrateScanFiles(db *sql.DB) error {
	rows, err := db.Query(`SELECT column_name FROM information_schema.columns WHERE table_name = ?`, "scan_files")
	if err != nil {
		return fmt.Errorf("scanstore: probe existing columns: %w", err)
	}
	existing := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return fmt.Errorf("scanstore: scan existing column: %w", err)
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("scanstore: probe existing columns: %w", err)
	}
	rows.Close()

	for _, col := range scanFilesMigrationCols {
		if existing[col.name] {
			continue
		}
		if _, err := db.Exec("ALTER TABLE scan_files ADD COLUMN " + col.ddl); err != nil {
			return fmt.Errorf("scanstore: migrate: add column %s: %w", col.name, err)
		}
	}
	return nil
}

// Close closes the underlying database.
func (s *Store) Close() error { return s.db.Close() }

// sanitizeKillRate converts a NaN-valued *float64 to nil before it ever
// reaches DuckDB. DuckDB sorts NaN as larger than any other DOUBLE value —
// measured against this exact driver, MAX(kill_rate) over a table
// containing NaN returns NaN (displacing a real 0.9), `kill_rate > 0.5`
// MATCHES the NaN row, and `ORDER BY kill_rate DESC LIMIT 1` surfaces the
// never-measured row FIRST. A caller that stores math.NaN() directly (e.g.
// reposcan.RepoReport.KillRate, which is deliberately NaN when Audited ==
// 0) would make the never-measured scan look like the ledger's
// BEST-scoring one — the exact inversion "no measurement was made" was
// supposed to prevent. This is the last line of defense: it runs
// regardless of whether the caller already converted NaN to nil itself.
// math.IsNaN is the check, not some proxy (e.g. Audited == 0) that could
// drift from the actual value stored.
func sanitizeKillRate(v *float64) *float64 {
	if v == nil || math.IsNaN(*v) {
		return nil
	}
	return v
}

// Record writes scan's header row and every file's disposition in one
// transaction — a half-written scan is worse than none, because a later
// report would present it as complete — and returns the assigned scan id.
func (s *Store) Record(ctx context.Context, scan Scan, files []File) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("scanstore: begin: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck

	var id int64
	err = tx.QueryRowContext(ctx, `INSERT INTO scans (
		id, ts, owner, repo, commit, substrate, engine_version, model_set,
		top, all_candidates, diff_base, total_files, candidates, audited,
		kill_rate, cache_hits, preflight_ran, preflight_note, started_at, finished_at
	) VALUES (nextval('scans_id'), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	RETURNING id`,
		time.Now().UTC(), scan.Owner, scan.Repo, scan.Commit, scan.Substrate, scan.EngineVersion, scan.ModelSet,
		scan.Top, scan.AllCandidates, scan.DiffBase, scan.TotalFiles, scan.Candidates, scan.Audited,
		sanitizeKillRate(scan.KillRate), scan.CacheHits, scan.PreflightRan, scan.PreflightNote, scan.StartedAt, scan.FinishedAt,
	).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("scanstore: insert scan header: %w", err)
	}

	for _, f := range files {
		// KillRate stays *float64 all the way to the placeholder: passing a
		// nil *float64 as a driver arg binds SQL NULL, never 0.0. This is
		// the property TestRecordRoundTripsEveryDisposition pins for
		// rejected files.
		if _, err := tx.ExecContext(ctx, `INSERT INTO scan_files (
			scan_id, path, lang, disposition, reason,
			kill_rate, survivors, gradable, preflight_state, evidence
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, f.Path, f.Lang, f.Disposition, f.Reason,
			sanitizeKillRate(f.KillRate), f.Survivors, f.Gradable, f.PreflightState, f.Evidence,
		); err != nil {
			return 0, fmt.Errorf("scanstore: insert scan_files row for %q: %w", f.Path, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("scanstore: commit: %w", err)
	}
	return id, nil
}

// FilesForScan returns every scan_files row recorded for scanID, in
// insertion order.
func (s *Store) FilesForScan(ctx context.Context, scanID int64) ([]File, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path, lang, disposition, reason,
		kill_rate, survivors, gradable, preflight_state, evidence
		FROM scan_files WHERE scan_id = ?`, scanID)
	if err != nil {
		return nil, fmt.Errorf("scanstore: files for scan %d: %w", scanID, err)
	}
	defer rows.Close()

	var out []File
	for rows.Next() {
		var f File
		if err := rows.Scan(&f.Path, &f.Lang, &f.Disposition, &f.Reason,
			&f.KillRate, &f.Survivors, &f.Gradable, &f.PreflightState, &f.Evidence); err != nil {
			return nil, fmt.Errorf("scanstore: scan scan_files row: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}
