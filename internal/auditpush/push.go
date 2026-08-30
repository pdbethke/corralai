// SPDX-License-Identifier: Elastic-2.0

// Package auditpush appends a scan's verdict to a DuckDB the OPERATOR owns —
// a local file, or their MotherDuck database.
//
// It is deliberately not a service. corral has no hosted tier and collects no
// telemetry: the key is theirs, the runner is theirs, and the warehouse is
// theirs too. That is also why the target is any DuckDB rather than MotherDuck
// specifically — `md:<db>` is one destination, a path on disk is another, and
// neither is a lock-in.
//
// What it exists for is the question a single pull request cannot answer.
// One kill rate is a sample; we have watched the same unchanged diff score 0.85
// and then 0.90. Forty of them are a distribution, and "this file has drifted
// from 0.9 to 0.6 over two months" is a claim no individual run can support.
//
// Two rules the schema enforces rather than documents:
//
//   - APPEND ONLY. A verified receipt that can be UPDATEd is not a receipt.
//     Rows are inserted, never modified, and — whenever --attest ran too —
//     each carries the sha256 of the signed statement it came from, so a row
//     traces back to something a third party can verify. Without --attest
//     there is no statement to point to, and a row's statement_sha256 is
//     honestly empty rather than fabricated; scan_id is always the ledger's
//     row id, or 0 when --record was not given.
//   - THE QUALIFIERS TRAVEL WITH THE NUMBERS. proven_missed = 0 means "nothing
//     was proven" rather than "the suite is clean" whenever the writer failed
//     or its test never graded. Aggregation is exactly where that distinction
//     gets dropped and a zero silently becomes good news, so the columns sit
//     beside the number and every documented query carries them.
package auditpush

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// Row is one audited file, as it lands in the warehouse.
type Row struct {
	Repo   string
	Commit string
	Path   string
	// KillRate is a POINTER so an UNCOVERED file writes SQL NULL rather than
	// a 0.0 the report itself refuses to print. A warehouse that stores the
	// fabricated zero is where the withheld number comes back as fact.
	KillRate         *float64
	Survivors        int
	ProvenMissed     int
	TimedOut         bool
	TestWriterFailed bool
	PoolTestUnsound  bool
	// Scope travels on every row, denormalized on purpose: a query that reads
	// one row must be able to see how much of the repo was looked at. "3 files
	// clean" reads very differently out of 4 than out of 400, and a join to
	// find that out is a join people skip.
	Audited    int
	Candidates int
	// Comparability. Without these a cross-project row is unusable: a kill
	// rate on a dense 200-line function and one on a 12-line accessor are not
	// the same measurement, and a reader cannot tell a hard file from a weak
	// suite.
	Lang            string
	MutantsPlanted  int
	ModelsByRole    string // JSON, so a new role does not need a migration
	MinKillRate     *float64
	MaxProvenMissed *int
	Passed          bool
	// StatementSHA256 ties this row to the signed in-toto statement the run
	// published. It is what makes the table evidence rather than self-report:
	// any row can be traced to an attestation a third party verifies.
	StatementSHA256 string
	RunURL          string
	// Which measurement KillRate IS — the same five facts the scan ledger
	// records. Without them a cross-repo query averages selection rates and
	// whole-suite rates together, which is two questions in one number.
	TestSelection     string
	SelectedTests     int
	SuiteTests        int
	SelectionFallback string
	Uncovered         bool
	// And at which GRAIN it was measured. PerMutant says each mutant was
	// graded by the tests that reach its own lines, which makes SelectedTests
	// the file's UNION and no mutant's denominator — so the spread travels
	// with it, or a cross-repo average of kill_rate silently mixes a rate
	// earned over 3 tests per mutant with one earned over 620.
	//
	// The three columns are written as SQL NULL, not 0, when no spread was
	// measured — an ordinary shared-command run, or a per-mutant run whose
	// every mutant was rejected by the compile gate before anything could be
	// graded. A stored 0-to-0 range is a measurement nobody made, and the
	// whole point of these columns is that a number in this table was
	// measured. nil is that absence, carried by the type rather than by a
	// caller remembering to leave three ints alone.
	PerMutant      bool
	TestsPerMutant *TestsPerMutantSpread
	// Trees and ConcurrencyNote disclose how many private trees scored this
	// file at once, or — when it only got one — why. The same fact the
	// report line and the signed attestation carry, denormalized onto this
	// row so a cross-repo query does not need to reconstruct it. Trees < 1
	// means nothing measured it and is written SQL NULL, never 0.
	Trees           int
	ConcurrencyNote string
	// SharedDirs is the comma-joined list of dependency directories that were
	// symlinked into every tree rather than copied — the one thing the trees
	// did NOT hold privately. SQL NULL, not "", when nothing was shared.
	SharedDirs string
	// ScanID is the local scan ledger's row id this row was pushed alongside
	// (see scanstore.Store.Record), or 0 when the ledger was not written.
	// It is the join key back to scan_files/scan_mutants, and — together
	// with StatementSHA256 — the other half of the link a signed statement
	// makes to this row: the statement names ScanID and the hash of the
	// rows it was written with, and this row names the statement's own
	// hash. Two pointers, not one, so either can be checked against the
	// other.
	ScanID int64
}

// Link identifies the ledger scan and signed statement a pushed row belongs
// to — the fields cmd/corral's pushAuditRows stamps onto every row from a
// single source so a row and the statement it names can never disagree
// about which run produced them.
type Link struct {
	// ScanID is written onto Row.ScanID.
	ScanID int64
	// StatementSHA256 is written onto Row.StatementSHA256.
	StatementSHA256 string
	// Require, when true, refuses to push any row whose StatementSHA256 is
	// empty. The certify --repo path sets this whenever --attest produced a
	// statement, so a row that would otherwise claim traceability actually
	// has it; without --attest there is no statement to point to, Require
	// is false, and a row with statement_sha256 = '' pushes honestly.
	Require bool
}

// TestsPerMutantSpread is how many tests each graded mutant ran: the
// smallest, the middle and the largest. This package's own copy of the
// pool's spread — auditpush is a leaf writer and imports no engine package
// — reached only through a pointer so an unmeasured spread is absent rather
// than three zeros.
type TestsPerMutantSpread struct{ Min, Median, Max int }

const schema = `
CREATE TABLE IF NOT EXISTS corral_audits (
  ts                 TIMESTAMPTZ NOT NULL,
  repo               VARCHAR     NOT NULL,
  commit_sha         VARCHAR     NOT NULL,
  path               VARCHAR     NOT NULL,
  lang               VARCHAR,
  kill_rate          DOUBLE,
  survivors          INTEGER,
  proven_missed      INTEGER,
  timed_out          BOOLEAN,
  test_writer_failed BOOLEAN,
  pool_test_unsound  BOOLEAN,
  audited            INTEGER,
  candidates         INTEGER,
  mutants_planted    INTEGER,
  models_by_role     VARCHAR,
  min_kill_rate      DOUBLE,
  max_proven_missed  INTEGER,
  passed             BOOLEAN,
  statement_sha256   VARCHAR,
  run_url            VARCHAR,
  test_selection     VARCHAR,
  selected_tests     INTEGER,
  suite_tests        INTEGER,
  selection_fallback VARCHAR,
  uncovered          BOOLEAN,
  per_mutant              BOOLEAN,
  tests_per_mutant_min    INTEGER,
  tests_per_mutant_median INTEGER,
  tests_per_mutant_max    INTEGER,
  trees                   INTEGER,
  concurrency_note        VARCHAR,
  shared_dirs             VARCHAR,
  scan_id                 BIGINT
);`

// corralAuditsMigrationCols is the additive set of columns this package has
// ever needed on corral_audits beyond the original shape (ts … run_url), in
// the order they must be added.
//
// It exists because `CREATE TABLE IF NOT EXISTS` is a NO-OP on a warehouse an
// earlier corral already created: that table keeps its old column set forever,
// and an INSERT naming a column it does not have fails the whole push — a
// working `--push` breaking on upgrade, against the one table this tool asks
// operators to trust as a durable record. Both this list and the fresh
// CREATE TABLE above carry the columns, so a brand-new warehouse never runs
// an ALTER; this path is only for a table that predates them.
//
// DuckDB has no `ADD COLUMN IF NOT EXISTS`, and silently swallowing every
// ALTER error would make a genuinely broken migration indistinguishable from
// an already-applied one — so the existing columns are probed first and any
// other failure is surfaced. Same rule scanstore's scanFilesMigrationCols
// follows, for the same reason.
var corralAuditsMigrationCols = []struct{ name, ddl string }{
	{"test_selection", "test_selection VARCHAR"},
	{"selected_tests", "selected_tests INTEGER"},
	{"suite_tests", "suite_tests INTEGER"},
	{"selection_fallback", "selection_fallback VARCHAR"},
	{"uncovered", "uncovered BOOLEAN"},
	{"per_mutant", "per_mutant BOOLEAN"},
	{"tests_per_mutant_min", "tests_per_mutant_min INTEGER"},
	{"tests_per_mutant_median", "tests_per_mutant_median INTEGER"},
	{"tests_per_mutant_max", "tests_per_mutant_max INTEGER"},
	{"trees", "trees INTEGER"},
	{"concurrency_note", "concurrency_note VARCHAR"},
	{"shared_dirs", "shared_dirs VARCHAR"},
	{"scan_id", "scan_id BIGINT"},
}

// migrateCorralAudits additively brings a corral_audits table created before
// a later column existed up to the current column set. Idempotent: a table
// that already has every column runs zero ALTERs.
//
// The columns are probed through duckdb_columns() rather than
// information_schema.columns because the target is an ATTACHed catalog
// (`warehouse`), and information_schema is scoped to the current one — it
// would report the attached table as having no columns at all, and every
// ALTER would then run and fail on a table that was already current.
func migrateCorralAudits(db *sql.DB) error {
	rows, err := db.Query(`SELECT column_name FROM duckdb_columns()
	    WHERE database_name = 'warehouse' AND table_name = 'corral_audits'`)
	if err != nil {
		return fmt.Errorf("auditpush: probe existing columns: %w", err)
	}
	existing := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			rows.Close()
			return fmt.Errorf("auditpush: scan existing column: %w", err)
		}
		existing[name] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("auditpush: probe existing columns: %w", err)
	}
	rows.Close()

	for _, col := range corralAuditsMigrationCols {
		if existing[col.name] {
			continue
		}
		if _, err := db.Exec("ALTER TABLE warehouse.corral_audits ADD COLUMN " + col.ddl); err != nil {
			return fmt.Errorf("auditpush: migrate: add column %s: %w", col.name, err)
		}
	}
	return nil
}

// Push appends rows to target, creating the table if it is not there.
//
// target is a DuckDB path or `md:<db>`. For MotherDuck the caller must have set
// motherduck_token in the environment — the same contract fleet sync uses, and
// the reason this takes no credential of its own: corral never holds one.
//
// link is variadic so Push(target, rows) keeps working for every caller that
// has no statement to link rows to — the test-only/legacy path, and any
// future caller pushing rows without --attest. Passing a Link enforces
// Link.Require: refuse the whole push, naming the offending row, rather than
// writing a row that looks traceable when it is not.
func Push(target string, rows []Row, link ...Link) (int, error) {
	if strings.TrimSpace(target) == "" {
		return 0, fmt.Errorf("auditpush: no target")
	}
	if len(rows) == 0 {
		return 0, nil
	}
	var lk Link
	if len(link) > 0 {
		lk = link[0]
	}
	if lk.Require {
		for _, r := range rows {
			if strings.TrimSpace(r.StatementSHA256) == "" {
				return 0, fmt.Errorf("auditpush: row %s has no statement_sha256, but a signed statement is required", r.Path)
			}
		}
	}

	db, err := sql.Open("duckdb", "")
	if err != nil {
		return 0, err
	}
	defer db.Close()

	if strings.HasPrefix(target, "md:") {
		if _, err := db.Exec("INSTALL motherduck; LOAD motherduck;"); err != nil {
			return 0, fmt.Errorf("auditpush: load motherduck extension: %w", err)
		}
	}
	if _, err := db.Exec(fmt.Sprintf("ATTACH '%s' AS warehouse", strings.ReplaceAll(target, "'", "''"))); err != nil {
		return 0, fmt.Errorf("auditpush: attach %q: %w", target, err)
	}
	if _, err := db.Exec(strings.Replace(schema, "corral_audits", "warehouse.corral_audits", 1)); err != nil {
		return 0, fmt.Errorf("auditpush: create table: %w", err)
	}
	// A warehouse an earlier corral created already exists, so the CREATE
	// above did nothing and its column set is whatever that version wrote.
	// The INSERT below names every current column, so without this an
	// upgrade turns a working push into a hard failure.
	if err := migrateCorralAudits(db); err != nil {
		return 0, err
	}

	// Columns named explicitly rather than positionally: the list has grown
	// (test selection), and a warehouse table created by an older corral is
	// then a clear "column not found" instead of a silent column-order
	// mismatch that would file kill rates under the wrong heading.
	stmt, err := db.Prepare(`INSERT INTO warehouse.corral_audits (
	    ts, repo, commit_sha, path, lang,
	    kill_rate, survivors, proven_missed,
	    timed_out, test_writer_failed, pool_test_unsound,
	    audited, candidates, mutants_planted, models_by_role,
	    min_kill_rate, max_proven_missed, passed, statement_sha256, run_url,
	    test_selection, selected_tests, suite_tests, selection_fallback, uncovered,
	    per_mutant, tests_per_mutant_min, tests_per_mutant_median, tests_per_mutant_max,
	    trees, concurrency_note, shared_dirs, scan_id
	  ) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
	if err != nil {
		return 0, err
	}
	defer stmt.Close()

	now := time.Now().UTC()
	n := 0
	for _, r := range rows {
		var minKill any
		if r.MinKillRate != nil {
			minKill = *r.MinKillRate
		}
		var maxGaps any
		if r.MaxProvenMissed != nil {
			maxGaps = *r.MaxProvenMissed
		}
		// NULL, never 0.0, for a file nothing graded — a nil *float64 binds
		// SQL NULL, which is the only honest value for a rate that was never
		// measured. Belt and braces on Uncovered: the caller sets the rate
		// nil, and an uncovered row cannot carry one even if it did not.
		var killRate any
		if r.KillRate != nil && !r.Uncovered {
			killRate = *r.KillRate
		}
		// NULL, never 0, for a spread that was never measured — a
		// per-mutant run can end with no graded mutant at all, and a stored
		// 0-to-0 range would read as "every mutant ran no tests" instead of
		// "no mutant was graded".
		var pmMin, pmMedian, pmMax any
		if s := r.TestsPerMutant; r.PerMutant && s != nil {
			pmMin, pmMedian, pmMax = s.Min, s.Median, s.Max
		}
		// concurrencyNote is SQL NULL, not "", for a row with no note: an
		// empty string would be indistinguishable from "the substrate had
		// nothing to say" and "this column predates the row".
		var concurrencyNote any
		if r.ConcurrencyNote != "" {
			concurrencyNote = r.ConcurrencyNote
		}
		// And trees is SQL NULL, not 0, when nothing measured it: the jail
		// substrate builds no trees and a cached pre-concurrency verdict
		// carries none. A 0 in an INTEGER column is a value a cross-repo
		// query will average and rank on; NULL is the only encoding of
		// "this warehouse does not say".
		var trees any
		if r.Trees >= 1 {
			trees = r.Trees
		}
		// Same rule for the shared dep dirs: "" is "nothing was shared",
		// which is a positive claim a row that never measured concurrency
		// cannot make.
		var sharedDirs any
		if r.SharedDirs != "" {
			sharedDirs = r.SharedDirs
		}
		if _, err := stmt.Exec(now, r.Repo, r.Commit, r.Path, r.Lang,
			killRate, r.Survivors, r.ProvenMissed,
			r.TimedOut, r.TestWriterFailed, r.PoolTestUnsound,
			r.Audited, r.Candidates, r.MutantsPlanted, r.ModelsByRole,
			minKill, maxGaps, r.Passed, r.StatementSHA256, r.RunURL,
			r.TestSelection, r.SelectedTests, r.SuiteTests, r.SelectionFallback, r.Uncovered,
			r.PerMutant, pmMin, pmMedian, pmMax,
			trees, concurrencyNote, sharedDirs, r.ScanID); err != nil {
			return n, fmt.Errorf("auditpush: insert %s: %w", r.Path, err)
		}
		n++
	}
	return n, nil
}
