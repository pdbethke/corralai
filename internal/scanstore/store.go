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
	"errors"
	"fmt"
	"math"
	"strings"
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
	// scan FIRST — the exact inversion of "best-scoring". A caller SHOULD
	// convert math.NaN() to nil before constructing a Scan (see
	// certify_repo_record.go's killRatePtr, which does this at the
	// source, where the intent is visible); Record additionally
	// re-checks IsNaN itself as a backstop (see sanitizeKillRate below),
	// so this field's *float64 type is the load-bearing contract, not
	// caller discipline alone.
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
	// Detail is the underlying error text behind Reason, when the caller
	// has one (today: reposcan.FileResult.Detail, populated for
	// executor-error rejections). "" for every other reason — Reason alone
	// is self-explanatory for those. A rejected row with a bare reason code
	// and no Detail is the ORIGINAL problem this field exists to fix: "why"
	// used to require re-running with a code trace, not a query.
	Detail string
	// TimedOut mirrors advpool.Verdict.TimedOut / reposcan.WeakFile.TimedOut:
	// true for an AUDITED row whose kill rate was banked from a run that hit
	// its wall-clock deadline before the pool converged. A claim carries how
	// it was earned — a later query over this ledger must be able to tell
	// "measured, but the pool did not finish" apart from a clean audited
	// row without re-deriving it from KillRate alone.
	TimedOut bool
	// TestWriterFailed mirrors advpool.Verdict.TestWriterFailed /
	// reposcan.WeakFile.TestWriterFailed: true for an AUDITED row whose pool
	// exhausted its compile-retry budget without authoring a compiling
	// killing test for at least one survivor. HONESTY NOTE: a row with
	// Survivors > 0 and this true is NOT a clean suite — no killing test was
	// PROVEN, not "no gaps were found". A later query over this ledger must
	// be able to tell that apart from an ordinary audited row without
	// re-deriving it, the same way TimedOut already lets it.
	TestWriterFailed bool
	// PoolTestUnsound mirrors advpool.Verdict.PoolTestUnsound /
	// reposcan.WeakFile.PoolTestUnsound: true for an AUDITED row whose pool
	// authored a COMPILING test (TestWriterFailed is false) whose scoring
	// report never genuinely graded (failed on the unmutated compliant code,
	// the canary was never killed, or nothing was scored). A DIFFERENT
	// diagnosis from TestWriterFailed with the same honesty consequence:
	// ProvenMissed reads 0 for a reason that is neither "clean" nor "tried
	// and missed", and a later query must be able to tell that apart too.
	PoolTestUnsound bool
	// ProvenMissed mirrors advpool.Verdict.ProvenMissed /
	// reposcan.WeakFile.ProvenMissed: survivors the pool's authored test
	// then killed BY EXECUTION — corral's strongest claim, a specific
	// demonstrated bug the dev suite misses. HONESTY NOTE: 0 here is
	// ambiguous on its own — combined with Survivors and TestWriterFailed it
	// resolves to one of three cases (no survivors to prove; writer never
	// authored a compiling test; writer's test proved nothing) — see
	// reposcan.WeakFile.ProvenMissed's doc for the full breakdown. A query
	// over this ledger that wants the unambiguous "real, demonstrated gap"
	// signal should filter on ProvenMissed > 0, not just != 0.
	ProvenMissed int
	// ProvenMutantIDs is the EVIDENCE behind ProvenMissed: a comma-separated
	// list of the mutant ids the pool's authored test actually killed, in the
	// scoring report's own order. ProvenMissed is a COUNT, and a count cannot
	// be interrogated — a row reading 0 could not be told apart from a row
	// reading 3 without re-running the audit, which on the repo path means
	// paying for it again (`certify --repo` has no tape flag).
	ProvenMutantIDs string
	// AuthoredTest is the pool's compiling authored test source, retained as
	// evidence for exactly the case that motivated it: a sound, collected,
	// genuinely-grading test that killed NOTHING. On 2026-07-31 a paid
	// pallets/flask audit did precisely that against 10 survivors, and the
	// entire surviving record of the attempt was a single integer, so the
	// question "what did it actually try?" had no answer at any price short of
	// another run. "" when no compiling test was ever produced.
	AuthoredTest string
	// TestSelection, SelectedTests and SuiteTests say WHICH MEASUREMENT this
	// row's kill rate is: the tests coverage evidence showed execute this
	// file ("coverage-context", SelectedTests of SuiteTests), rather than the
	// whole suite. A ledger that stores the number without the question it
	// answers cannot be queried honestly afterwards: a rate earned against 14
	// of 1431 tests and one earned against all 1431 are not comparable, and
	// nothing else in the row can tell them apart.
	TestSelection string
	SelectedTests int
	SuiteTests    int
	// SelectionFallback is the REASON this row was graded by the whole suite
	// (no selector for the language, --whole-suite, an evidence run that
	// failed). Empty when TestSelection is set — never both.
	SelectionFallback string
	// Uncovered: the evidence ran and found NO test executing this file. Its
	// KillRate is written NULL for the same reason a rejected file's is —
	// nothing graded the file, so a stored 0.0 would later read as "your
	// tests caught nothing here" about a measurement that was never made.
	Uncovered bool
	// CacheKey is reposcan's content address for this file's audit — every
	// input that can change the verdict, hashed. It is what makes a later
	// scan able to reuse this row instead of re-running the suite once per
	// mutant. "" for a rejected file: nothing was measured, so there is
	// nothing to reuse.
	CacheKey string
	// VerdictJSON is the marshalled advpool.Verdict, stored whole rather than
	// rebuilt from this row's individual columns. A reconstitution assembled
	// field-by-field silently drops whatever the column list does not cover,
	// and a verdict served back MISSING a field is a different claim from the
	// one that was signed.
	VerdictJSON string
	// ComputedAt is when this verdict was actually earned, carried so a later
	// reuse can disclose its AGE. A scan that reports reused work without
	// saying how old it is presents stale measurement as current — the exact
	// self-flattering record corral exists to prevent.
	ComputedAt time.Time
	// ModelsByRole is advpool.Verdict.ModelsByRole, serialized with
	// reposcan.CanonicalKV so a per-file role assignment is byte-comparable
	// with the scan-wide model_set — the same canonicalization, not a
	// second one that could drift from it.
	ModelsByRole string
	// MutantsTotal is advpool.Verdict.MutantsTotal — the denominator a kill
	// rate is computed over. Kept as its own column, not just inside
	// VerdictJSON, because grading models means GROUP BY / SUM, and a blob
	// cannot be aggregated.
	MutantsTotal int
	// RegionsTotal and RegionsProbed are advpool.Verdict.RegionsTotal /
	// RegionsProbed — the mutant-generator seats the run dispatched, and the
	// seats that actually returned usable mutants.
	RegionsTotal  int
	RegionsProbed int
	// DroppedRegions are mutant-generator seats abandoned after
	// MaxShardRetries — the run's COVERAGE SHORTFALL. A kill rate is over the
	// mutants that were produced, not the ones that should have been, so a
	// row with dropped regions is a weaker claim than one without, and a
	// leaderboard that cannot see the difference is comparing unlike runs.
	DroppedRegions string
	// VacuousFindings is the COUNT of advpool.Verdict.VacuousFindings —
	// test-critic's designed-to-pass/vacuous flags on this file's run.
	VacuousFindings int
	// Status is advpool.Verdict.Status ("certified" | "needs-review").
	Status string
	// AuthoredTestNotCollected mirrors advpool.Verdict.AuthoredTestNotCollected:
	// the run proved a killing test compiled and ran, but the dev suite's own
	// collection never picked it up, so ProvenMissed on this row is earned
	// against a test the target project would never actually execute.
	AuthoredTestNotCollected bool
	// BaselineFailed mirrors advpool.Verdict.BaselineFailed: the dev suite did
	// not pass on the UNMUTATED code, so DevKillRate on this row is
	// meaningless — the audit had nothing sound to measure a mutant against.
	BaselineFailed bool
	// SuiteBaselineMillis mirrors advpool.Verdict.BaselineDuration, in
	// milliseconds: the compliant (unmutated) suite's own wall-clock runtime.
	// It is the single input to the audit cost model — O(mutants x the
	// TARGET's suite runtime), measured at 1.46s for pallets/flask and 77s
	// for psf/requests, a 53x spread — so AVG(suite_baseline_ms) over this
	// ledger is what capacity planning should be computed FROM, not
	// extrapolated from one repo. Milliseconds, not a Go duration: this
	// column is read by SQL and by DuckDB-WASM in the browser, neither of
	// which has a decoder for time.Duration.
	SuiteBaselineMillis int64
	// CacheHit mirrors reposcan.FileResult.CacheHit: true when this row's
	// verdict was served from a prior scan's cache_key match rather than
	// earned by running this scan's own mutants. Exists alongside
	// ReusedFromScanID so an aggregate can exclude reused rows — without it,
	// enabling the cache would make one measurement count once per scan
	// forever, and whatever happened to be cached would dominate every
	// average.
	CacheHit bool
	// ReusedFromScanID is the id of the scan whose row this one reused, or
	// nil when this row was measured fresh. *int64, not int64: "not reused"
	// must read back as NULL, not a scan id of 0, which would be a foreign
	// key to nothing.
	ReusedFromScanID *int64
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
	{"detail", "detail VARCHAR"},
	{"timed_out", "timed_out BOOLEAN"},
	{"test_writer_failed", "test_writer_failed BOOLEAN"},
	{"proven_missed", "proven_missed INTEGER"},
	{"pool_test_unsound", "pool_test_unsound BOOLEAN"},
	{"proven_mutant_ids", "proven_mutant_ids VARCHAR"},
	{"authored_test", "authored_test VARCHAR"},
	{"cache_key", "cache_key VARCHAR"},
	{"verdict_json", "verdict_json VARCHAR"},
	{"computed_at", "computed_at TIMESTAMP"},
	{"models_by_role", "models_by_role VARCHAR"},
	{"mutants_total", "mutants_total INTEGER"},
	{"regions_total", "regions_total INTEGER"},
	{"regions_probed", "regions_probed INTEGER"},
	{"dropped_regions", "dropped_regions VARCHAR"},
	{"vacuous_findings", "vacuous_findings INTEGER"},
	{"status", "status VARCHAR"},
	{"authored_test_not_collected", "authored_test_not_collected BOOLEAN"},
	{"baseline_failed", "baseline_failed BOOLEAN"},
	{"cache_hit", "cache_hit BOOLEAN"},
	{"reused_from_scan_id", "reused_from_scan_id BIGINT"},
	{"suite_baseline_ms", "suite_baseline_ms BIGINT"},
	{"test_selection", "test_selection VARCHAR"},
	{"selected_tests", "selected_tests INTEGER"},
	{"suite_tests", "suite_tests INTEGER"},
	{"selection_fallback", "selection_fallback VARCHAR"},
	{"uncovered", "uncovered BOOLEAN"},
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
	// instrument's report, "proven" is execution, and "" means no evidence
	// claim was ever made (a file the scan never ran anything against — see
	// cmd/corral/certify_repo_record.go's ungradableEvidence/
	// exclusionEvidence). A table that averages proof with guesswork is a
	// leaderboard nobody can defend — the CHECK constraints below exist so
	// a typo'd label (a future caller writing "prooven" or "n/a") fails
	// loud at INSERT time instead of silently entering a table that is
	// meant to be queried by exact string. They apply only to a table this
	// CREATE actually creates: a pre-existing store from before this
	// change keeps whatever it already had (DuckDB has no
	// `ADD CONSTRAINT` this package uses for the additive migrations
	// below), the same best-effort boundary migrateScanFiles already
	// accepts for newly added columns.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS scan_files (
		scan_id BIGINT, path VARCHAR, lang VARCHAR,
		disposition VARCHAR CHECK (disposition IN ('audited', 'rejected')),
		reason VARCHAR,
		kill_rate DOUBLE, survivors INTEGER, gradable BOOLEAN,
		preflight_state VARCHAR CHECK (preflight_state IN ('', 'executed', 'not-executed')),
		evidence VARCHAR CHECK (evidence IN ('', 'paired', 'coverage', 'proven')),
		detail VARCHAR,
		timed_out BOOLEAN,
		test_writer_failed BOOLEAN,
		proven_missed INTEGER,
		pool_test_unsound BOOLEAN,
		proven_mutant_ids VARCHAR,
		authored_test VARCHAR,
		cache_key VARCHAR,
		verdict_json VARCHAR,
		computed_at TIMESTAMP,
		models_by_role VARCHAR,
		mutants_total INTEGER,
		regions_total INTEGER,
		regions_probed INTEGER,
		dropped_regions VARCHAR,
		vacuous_findings INTEGER,
		status VARCHAR,
		authored_test_not_collected BOOLEAN,
		baseline_failed BOOLEAN,
		cache_hit BOOLEAN,
		reused_from_scan_id BIGINT,
		suite_baseline_ms BIGINT,
		test_selection VARCHAR,
		selected_tests INTEGER,
		suite_tests INTEGER,
		selection_fallback VARCHAR,
		uncovered BOOLEAN
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("scanstore: create scan_files table: %w", err)
	}

	if err := migrateScanFiles(db); err != nil {
		db.Close()
		return nil, err
	}

	// scan_mutants is one row per mutant per file per scan — the grain a
	// per-file kill rate averages away. "Which generator produces mutants a
	// suite does not catch" is a question about mutants, and it cannot be
	// asked of a table whose finest row is a file.
	//
	// The mutant's SOURCE is deliberately absent. ParentSHA256 ties the row to
	// the exact bytes it was derived from, which is enough to group, count and
	// compare, without putting a tenant's code at rest in the warehouse.
	// Storing patches is a later, deliberate decision — it can be added, it
	// cannot be un-added.
	//
	// There is NO reuse marker of its own here, and that is worth stating
	// because it surprises: when a file's verdict is served from the verdict
	// cache, its mutants are re-recorded under the new scan_id exactly like
	// freshly-earned ones, so a naive leaderboard counts one measurement once
	// per scan forever. Excluding reused mutants requires joining back to
	// scan_files on (scan_id, path) and filtering on cache_hit — the flag
	// lives there, at the grain the cache actually operates on.
	//
	// The CHECK on outcome exists for the same reason scan_files has one on
	// evidence: this table is queried by exact string, and a typo'd label
	// should fail loud at INSERT rather than quietly enter a leaderboard.
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS scan_mutants (
		scan_id BIGINT, path VARCHAR, mutant_id VARCHAR,
		outcome VARCHAR CHECK (outcome IN ('killed', 'survived')),
		parent_sha256 VARCHAR,
		proven BOOLEAN
	)`); err != nil {
		db.Close()
		return nil, fmt.Errorf("scanstore: create scan_mutants table: %w", err)
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

// fileKillRate is sanitizeKillRate plus the one row-level rule the value
// alone cannot express: an UNCOVERED file was never graded by anything — the
// selection evidence found no test that executes it — so whatever number the
// verdict carries is not a measurement of the suite's strength here. It is
// written NULL, exactly like a rejected file's, rather than a 0.0 that reads
// as "your tests caught nothing" about a question nobody asked. Enforced in
// the store, not left to each caller, because the caller getting it wrong is
// how a false accusation gets persisted.
func fileKillRate(f File) *float64 {
	if f.Uncovered {
		return nil
	}
	return sanitizeKillRate(f.KillRate)
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
			kill_rate, survivors, gradable, preflight_state, evidence, detail, timed_out, test_writer_failed, proven_missed, pool_test_unsound,
			proven_mutant_ids, authored_test, cache_key, verdict_json, computed_at,
			models_by_role, mutants_total, regions_total, regions_probed, dropped_regions, vacuous_findings, status,
			authored_test_not_collected, baseline_failed, cache_hit, reused_from_scan_id, suite_baseline_ms,
			test_selection, selected_tests, suite_tests, selection_fallback, uncovered
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			id, f.Path, f.Lang, f.Disposition, f.Reason,
			fileKillRate(f), f.Survivors, f.Gradable, f.PreflightState, f.Evidence, f.Detail, f.TimedOut, f.TestWriterFailed, f.ProvenMissed, f.PoolTestUnsound,
			f.ProvenMutantIDs, f.AuthoredTest, f.CacheKey, f.VerdictJSON, f.ComputedAt,
			f.ModelsByRole, f.MutantsTotal, f.RegionsTotal, f.RegionsProbed, f.DroppedRegions, f.VacuousFindings, f.Status,
			f.AuthoredTestNotCollected, f.BaselineFailed, f.CacheHit, f.ReusedFromScanID, f.SuiteBaselineMillis,
			f.TestSelection, f.SelectedTests, f.SuiteTests, f.SelectionFallback, f.Uncovered,
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
// insertion order — enforced with `ORDER BY rowid`, not left to whatever
// order DuckDB happens to return. DuckDB's rowid pseudocolumn tracks
// physical insertion order for a table this package only ever INSERTs
// into within one transaction per Record call (never UPDATEs or DELETEs a
// scan_files row), which is exactly this table's access pattern — an
// ORDER BY that relied on rowid surviving updates/deletes would not be
// safe here, but that case never arises. Confirmed against this exact
// driver (github.com/marcboeker/go-duckdb/v2): three sequential inserts
// read back via `SELECT rowid, ...` in insertion order (0, 1, 2).
// ScanRow is one `scans` header row as READ BACK — Scan plus the id the store
// assigned it, which a caller needs to then ask FilesForScan about.
type ScanRow struct {
	ID int64
	TS time.Time
	Scan
}

// Scans returns the most recent scan headers, newest first, capped at limit
// (<= 0 means a default of 20).
//
// This is the read side of a ledger that was, in practice, WRITE-ONLY: the
// store recorded every scan and every per-file disposition, and nothing could
// get them back out without a duckdb CLI (not installed on the production
// host) or a hand-written Go program. That is a cost with no product surface —
// and it bit exactly when it mattered, the day the ledger first held evidence
// worth reading. See `corral scans` (cmd/corral/scans.go) for the operator
// surface over this.
func (s *Store) Scans(ctx context.Context, limit int) ([]ScanRow, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, ts, owner, repo, commit,
		substrate, engine_version, model_set, top, all_candidates, diff_base,
		total_files, candidates, audited, kill_rate, cache_hits,
		preflight_ran, preflight_note, started_at, finished_at
		FROM scans ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("scanstore: list scans: %w", err)
	}
	defer rows.Close()

	var out []ScanRow
	for rows.Next() {
		var r ScanRow
		var ts, started, finished sql.NullTime
		var diffBase, preflightNote, modelSet, engineVersion, substrate sql.NullString
		// kill_rate is deliberately scanned into *float64: a scan that audited
		// nothing stored NULL rather than 0.0 (see Scan.KillRate's doc for the
		// DuckDB NaN-ordering trap that forced this), and it must read back as
		// "no measurement", never as a terrible score.
		if err := rows.Scan(&r.ID, &ts, &r.Owner, &r.Repo, &r.Commit,
			&substrate, &engineVersion, &modelSet, &r.Top, &r.AllCandidates, &diffBase,
			&r.TotalFiles, &r.Candidates, &r.Audited, &r.KillRate, &r.CacheHits,
			&r.PreflightRan, &preflightNote, &started, &finished); err != nil {
			return nil, fmt.Errorf("scanstore: scan scans row: %w", err)
		}
		r.TS, r.StartedAt, r.FinishedAt = ts.Time, started.Time, finished.Time
		r.Substrate, r.EngineVersion, r.ModelSet = substrate.String, engineVersion.String, modelSet.String
		r.DiffBase, r.PreflightNote = diffBase.String, preflightNote.String
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scanstore: iterate scans: %w", err)
	}
	return out, nil
}

func (s *Store) FilesForScan(ctx context.Context, scanID int64) ([]File, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT path, lang, disposition, reason,
		kill_rate, survivors, gradable, preflight_state, evidence, detail, timed_out, test_writer_failed, proven_missed, pool_test_unsound,
		proven_mutant_ids, authored_test,
		models_by_role, mutants_total, regions_total, regions_probed, dropped_regions, vacuous_findings, status,
		authored_test_not_collected, baseline_failed, cache_hit, reused_from_scan_id,
		suite_baseline_ms,
		test_selection, selected_tests, suite_tests, selection_fallback, uncovered
		FROM scan_files WHERE scan_id = ? ORDER BY rowid`, scanID)
	if err != nil {
		return nil, fmt.Errorf("scanstore: files for scan %d: %w", scanID, err)
	}
	defer rows.Close()

	var out []File
	for rows.Next() {
		var f File
		var detail sql.NullString
		var timedOut, testWriterFailed, poolTestUnsound sql.NullBool
		var provenMissed sql.NullInt64
		var provenIDs, authoredTest sql.NullString
		// The eleven verdict columns all read back nullable: a row written
		// before this migration ran will not have them, and a rejected file
		// was never scored, so NULL is the honest value for its counts too.
		var modelsByRole, droppedRegions, status sql.NullString
		var mutantsTotal, regionsTotal, regionsProbed, vacuousFindings sql.NullInt64
		var authoredTestNotCollected, baselineFailed, cacheHit sql.NullBool
		var reusedFromScanID sql.NullInt64
		// suite_baseline_ms reads back nullable for the same reason the
		// columns above do (pre-migration rows, and rejected files that were
		// never scored). It is read back at all because capacity planning is
		// meant to be a QUERY over this ledger — and this reader IS the query
		// surface: DuckDB is single-process on a file, so ad-hoc CLI SQL is
		// not a reliable fallback while a scan holds the handle open.
		var suiteBaselineMS sql.NullInt64
		// The five selection columns read back nullable for the same reason:
		// a row written before this migration ran has none of them, and a
		// rejected file never had a grading mode at all. NULL here means
		// "this ledger does not say" — never "graded by the whole suite",
		// which is a positive claim a pre-change row cannot make.
		var testSelection, selectionFallback sql.NullString
		var selectedTests, suiteTests sql.NullInt64
		var uncovered sql.NullBool
		if err := rows.Scan(&f.Path, &f.Lang, &f.Disposition, &f.Reason,
			&f.KillRate, &f.Survivors, &f.Gradable, &f.PreflightState, &f.Evidence, &detail, &timedOut, &testWriterFailed, &provenMissed, &poolTestUnsound,
			&provenIDs, &authoredTest,
			&modelsByRole, &mutantsTotal, &regionsTotal, &regionsProbed, &droppedRegions, &vacuousFindings, &status,
			&authoredTestNotCollected, &baselineFailed, &cacheHit, &reusedFromScanID,
			&suiteBaselineMS,
			&testSelection, &selectedTests, &suiteTests, &selectionFallback, &uncovered); err != nil {
			return nil, fmt.Errorf("scanstore: scan scan_files row: %w", err)
		}
		f.Detail = detail.String
		f.TimedOut = timedOut.Bool
		f.TestWriterFailed = testWriterFailed.Bool
		f.PoolTestUnsound = poolTestUnsound.Bool
		// NULL (a row written before this column existed, or a rejected file
		// that was never scored) reads back as 0 — the same "nothing to
		// report" value a fresh audited row with no proven gap would have,
		// which is fine: Survivors/TestWriterFailed already carry the
		// distinction a caller needs (see File.ProvenMissed's doc).
		f.ProvenMissed = int(provenMissed.Int64)
		// NULL for a row written before these columns existed, and for every
		// path that never authored a grading test. Empty is the honest value:
		// "no evidence recorded", never a fabricated attempt.
		f.ProvenMutantIDs = provenIDs.String
		f.AuthoredTest = authoredTest.String
		f.ModelsByRole = modelsByRole.String
		f.MutantsTotal = int(mutantsTotal.Int64)
		f.RegionsTotal = int(regionsTotal.Int64)
		f.RegionsProbed = int(regionsProbed.Int64)
		f.DroppedRegions = droppedRegions.String
		f.VacuousFindings = int(vacuousFindings.Int64)
		f.Status = status.String
		f.AuthoredTestNotCollected = authoredTestNotCollected.Bool
		f.BaselineFailed = baselineFailed.Bool
		f.CacheHit = cacheHit.Bool
		f.SuiteBaselineMillis = suiteBaselineMS.Int64
		f.TestSelection = testSelection.String
		f.SelectedTests = int(selectedTests.Int64)
		f.SuiteTests = int(suiteTests.Int64)
		f.SelectionFallback = selectionFallback.String
		f.Uncovered = uncovered.Bool
		// ReusedFromScanID stays *int64: NULL (never reused, or a
		// pre-migration row) must read back as nil, not a scan id of 0 — see
		// the field's own doc.
		if reusedFromScanID.Valid {
			v := reusedFromScanID.Int64
			f.ReusedFromScanID = &v
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// VerdictByCacheKey returns the most recently recorded verdict for (owner,
// cacheKey), if any.
//
// Owner scoping is in the WHERE clause, not applied in Go after fetching:
// this is the query that keeps one tenant's verdict from satisfying another
// tenant's audit, and a filter that runs after the rows are already in memory
// is one refactor away from being dropped.
//
// An empty owner is an ERROR, never a wildcard. The alternative is a shared
// bucket that every tenant with an unset owner reads and writes.
//
// Rows with no verdict JSON are skipped rather than returned as an empty hit:
// a row recorded before this column existed has nothing to reuse, and serving
// "" as a verdict would be worse than missing.
//
// "Most recent" orders by (s.ts, s.id), not s.ts alone. s.ts is a TIMESTAMP
// set from time.Now().UTC() at Record time, so two scans recorded within the
// same tick (reachable on a fast machine, or under batched recording) would
// tie on ts alone — and picking between two rows that could hold different
// verdicts for the same content address on an arbitrary tiebreak is exactly
// the ambiguity the package's fail-closed rule forbids. s.id is allocated
// from the scans_id DuckDB SEQUENCE (see Open's comment on it), so it is
// unique and monotonic with insertion order; ordering by (ts, id) makes
// "most recent" a TOTAL order, so the tie case cannot arise rather than
// being resolved arbitrarily — cheaper than falling back to a miss on tie,
// which would force a needless full re-audit over what is really just clock
// granularity.
func (s *Store) VerdictByCacheKey(ctx context.Context, owner, cacheKey string) (string, time.Time, bool, error) {
	if strings.TrimSpace(owner) == "" {
		return "", time.Time{}, false, fmt.Errorf("scanstore: VerdictByCacheKey: empty owner")
	}
	if strings.TrimSpace(cacheKey) == "" {
		return "", time.Time{}, false, nil
	}
	row := s.db.QueryRowContext(ctx, `
		SELECT f.verdict_json, f.computed_at
		FROM scan_files f JOIN scans s ON s.id = f.scan_id
		WHERE s.owner = ? AND f.cache_key = ?
		  AND f.verdict_json IS NOT NULL AND f.verdict_json <> ''
		ORDER BY s.ts DESC, s.id DESC
		LIMIT 1`, owner, cacheKey)
	var js sql.NullString
	var at sql.NullTime
	switch err := row.Scan(&js, &at); {
	case errors.Is(err, sql.ErrNoRows):
		return "", time.Time{}, false, nil
	case err != nil:
		return "", time.Time{}, false, fmt.Errorf("scanstore: VerdictByCacheKey: %w", err)
	}
	if !js.Valid || js.String == "" {
		return "", time.Time{}, false, nil
	}
	return js.String, at.Time, true, nil
}

// Mutant is one mutant's fate in one scan.
type Mutant struct {
	ScanID       int64
	Path         string
	MutantID     string
	Outcome      string // "killed" | "survived"
	ParentSHA256 string
	// Proven is true when this survivor was subsequently killed by the pool's
	// AUTHORED test — a gap demonstrated by execution, not merely disclosed.
	// Survived-and-proven and survived-and-unadjudicated are different claims
	// and a leaderboard that conflates them is indefensible.
	Proven bool
}

// RecordMutants appends mutant rows. An empty slice is a no-op, not an error:
// a file whose baseline failed produced no mutants, and that is a normal
// outcome the caller should not have to special-case.
func (s *Store) RecordMutants(ctx context.Context, ms []Mutant) error {
	if len(ms) == 0 {
		return nil
	}
	for _, m := range ms {
		if m.Outcome != "killed" && m.Outcome != "survived" {
			return fmt.Errorf("scanstore: RecordMutants: %s/%s: unknown outcome %q", m.Path, m.MutantID, m.Outcome)
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("scanstore: RecordMutants: begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, m := range ms {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO scan_mutants (scan_id, path, mutant_id, outcome, parent_sha256, proven) VALUES (?, ?, ?, ?, ?, ?)`,
			m.ScanID, m.Path, m.MutantID, m.Outcome, m.ParentSHA256, m.Proven,
		); err != nil {
			return fmt.Errorf("scanstore: RecordMutants: insert %s/%s: %w", m.Path, m.MutantID, err)
		}
	}
	return tx.Commit()
}

// MutantsForScan returns every mutant row for a scan, for round-trip tests and
// for the CLI reader.
func (s *Store) MutantsForScan(ctx context.Context, scanID int64) ([]Mutant, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT scan_id, path, mutant_id, outcome, parent_sha256, proven FROM scan_mutants WHERE scan_id = ? ORDER BY path, mutant_id`, scanID)
	if err != nil {
		return nil, fmt.Errorf("scanstore: MutantsForScan: %w", err)
	}
	defer rows.Close()
	var out []Mutant
	for rows.Next() {
		var m Mutant
		var parent sql.NullString
		if err := rows.Scan(&m.ScanID, &m.Path, &m.MutantID, &m.Outcome, &parent, &m.Proven); err != nil {
			return nil, fmt.Errorf("scanstore: MutantsForScan: scan row: %w", err)
		}
		m.ParentSHA256 = parent.String
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scanstore: MutantsForScan: %w", err)
	}
	return out, nil
}
