// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"database/sql"
	"path/filepath"
	"testing"

	_ "github.com/marcboeker/go-duckdb/v2"
)

func rate(f float64) *float64 { return &f }
func gaps(n int) *int         { return &n }

func openTarget(t *testing.T, path string) *sql.DB {
	t.Helper()
	db, err := sql.Open("duckdb", path)
	if err != nil {
		t.Fatalf("open target: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func TestPushCreatesTheTableAndAppends(t *testing.T) {
	target := filepath.Join(t.TempDir(), "w.duckdb")

	n, err := Push(target, []Row{{
		Repo: "o/r", Commit: "abc", Path: "a.py", Lang: "python",
		KillRate: rate(0.9), Survivors: 2, ProvenMissed: 1,
		Audited: 1, Candidates: 11, MutantsPlanted: 20,
		ModelsByRole: `{"mutant-generator":"gemini-3.6-flash"}`,
		MinKillRate:  rate(0.7), MaxProvenMissed: gaps(0),
		Passed: false, StatementSHA256: "deadbeef",
	}})
	if err != nil || n != 1 {
		t.Fatalf("Push = %d, %v", n, err)
	}

	db := openTarget(t, target)
	var repo, path, models, sha string
	var provenMissed, candidates int
	var passed bool
	if err := db.QueryRow(`SELECT repo, path, models_by_role, statement_sha256,
	    proven_missed, candidates, passed FROM corral_audits`).
		Scan(&repo, &path, &models, &sha, &provenMissed, &candidates, &passed); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if repo != "o/r" || path != "a.py" || provenMissed != 1 || !contains(models, "gemini") {
		t.Errorf("row = %s %s %d %s", repo, path, provenMissed, models)
	}
	// The denominator rides on the row: "clean" reads differently out of 11.
	if candidates != 11 {
		t.Errorf("candidates = %d, want 11 — a reader of one row must see how much was looked at", candidates)
	}
	// And the receipt it came from, so a row traces to something verifiable.
	if sha != "deadbeef" {
		t.Errorf("statement_sha256 = %q — without it the table is self-report", sha)
	}
	if passed {
		t.Error("passed should record the real verdict, including a failure")
	}
}

// Append-only is the property that makes this evidence rather than a dashboard.
// A second push must ADD history, never replace it — that is what lets a reader
// see a file drift over time instead of only its latest number.
func TestPushIsAppendOnly(t *testing.T) {
	target := filepath.Join(t.TempDir(), "w.duckdb")
	row := Row{Repo: "o/r", Commit: "c1", Path: "a.py", KillRate: rate(0.9)}

	if _, err := Push(target, []Row{row}); err != nil {
		t.Fatal(err)
	}
	row.Commit, row.KillRate = "c2", rate(0.6)
	if _, err := Push(target, []Row{row}); err != nil {
		t.Fatal(err)
	}

	db := openTarget(t, target)
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM corral_audits WHERE path = 'a.py'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("got %d rows, want 2 — a second run must not overwrite the first, or the trend is lost", n)
	}
}

// The qualifiers have to survive the trip. Aggregation is exactly where a
// proven_missed of 0 that means "nothing could be proven" gets read as "clean".
func TestQualifiersTravelWithTheNumbers(t *testing.T) {
	target := filepath.Join(t.TempDir(), "w.duckdb")
	if _, err := Push(target, []Row{{
		Repo: "o/r", Commit: "c", Path: "a.py",
		Survivors: 4, ProvenMissed: 0, TestWriterFailed: true,
	}}); err != nil {
		t.Fatal(err)
	}

	db := openTarget(t, target)
	var writerFailed bool
	var survivors, provenMissed int
	if err := db.QueryRow(`SELECT test_writer_failed, survivors, proven_missed
	    FROM corral_audits`).Scan(&writerFailed, &survivors, &provenMissed); err != nil {
		t.Fatal(err)
	}
	if !writerFailed || survivors != 4 || provenMissed != 0 {
		t.Errorf("qualifier lost: writerFailed=%v survivors=%d provenMissed=%d", writerFailed, survivors, provenMissed)
	}
}

func TestPushRefusesAnEmptyTargetAndIgnoresEmptyRows(t *testing.T) {
	if _, err := Push("", []Row{{Repo: "o/r"}}); err == nil {
		t.Error("an empty target must refuse rather than silently drop the rows")
	}
	n, err := Push(filepath.Join(t.TempDir(), "w.duckdb"), nil)
	if err != nil || n != 0 {
		t.Errorf("no rows should be a clean no-op, got %d, %v", n, err)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

// The warehouse is a reader too. An uncovered file's rate is withheld
// everywhere else — report, ledger, attestation — and a 0.0 stored here would
// be the one copy that comes back as a measured fact in a query.
func TestUncoveredRowStoresANullRateAndItsSelection(t *testing.T) {
	target := filepath.Join(t.TempDir(), "w.duckdb")
	if _, err := Push(target, []Row{
		{Repo: "o/r", Commit: "c", Path: "pkg/u.py", Survivors: 2,
			TestSelection: "coverage-context", SuiteTests: 1431, Uncovered: true},
		{Repo: "o/r", Commit: "c", Path: "pkg/a.py", KillRate: rate(0.65),
			TestSelection: "coverage-context", SelectedTests: 14, SuiteTests: 1431},
	}); err != nil {
		t.Fatal(err)
	}

	db := openTarget(t, target)
	var killRate *float64
	var method string
	var selected, suite int
	var uncovered bool
	if err := db.QueryRow(`SELECT kill_rate, test_selection, selected_tests, suite_tests, uncovered
	    FROM corral_audits WHERE path = 'pkg/u.py'`).
		Scan(&killRate, &method, &selected, &suite, &uncovered); err != nil {
		t.Fatalf("read back uncovered row: %v", err)
	}
	if killRate != nil {
		t.Errorf("uncovered row kill_rate = %v, want NULL: nothing graded that file", *killRate)
	}
	if !uncovered || method != "coverage-context" || suite != 1431 {
		t.Errorf("uncovered row lost its selection facts: %v %q %d", uncovered, method, suite)
	}

	if err := db.QueryRow(`SELECT kill_rate, selected_tests FROM corral_audits WHERE path = 'pkg/a.py'`).
		Scan(&killRate, &selected); err != nil {
		t.Fatalf("read back graded row: %v", err)
	}
	if killRate == nil || *killRate != 0.65 || selected != 14 {
		t.Errorf("a measured row must keep its rate and its denominator: %v %d", killRate, selected)
	}
}

// TestUncoveredCandidacyRowIsFindableByColumn is F4's regression: a file the
// evidence-as-candidacy design excludes as "uncovered — no test executes
// this file" (reposcan.ReasonUncovered) is a REJECTED row, not a graded one
// — it never reaches a job at all — and Uncovered must still be TRUE on it,
// so `WHERE uncovered` finds the design's own headline finding by COLUMN,
// the same way a graded-but-zero-coverage row already is above, rather than
// only by matching the reason string.
func TestUncoveredCandidacyRowIsFindableByColumn(t *testing.T) {
	target := filepath.Join(t.TempDir(), "w.duckdb")
	const uncoveredReason = "uncovered — no test executes this file" // reposcan.ReasonUncovered, verbatim
	if _, err := Push(target, []Row{
		{Repo: "o/r", Commit: "c", Path: "pkg/orphan.py",
			Disposition: "rejected", Reason: uncoveredReason, Uncovered: true},
		{Repo: "o/r", Commit: "c", Path: "pkg/unrelated.py",
			Disposition: "rejected", Reason: "no-paired-test", Uncovered: false},
	}); err != nil {
		t.Fatal(err)
	}

	db := openTarget(t, target)
	rows, err := db.Query(`SELECT path FROM corral_audits WHERE uncovered ORDER BY path`)
	if err != nil {
		t.Fatalf("query by column: %v", err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			t.Fatal(err)
		}
		got = append(got, p)
	}
	if len(got) != 1 || got[0] != "pkg/orphan.py" {
		t.Fatalf("WHERE uncovered = %v, want exactly [pkg/orphan.py] — a candidacy-level uncovered exclusion must be findable by the boolean column, not only by matching the reason text", got)
	}

	var disposition, reason string
	var killRate *float64
	if err := db.QueryRow(`SELECT disposition, reason, kill_rate FROM corral_audits WHERE path = 'pkg/orphan.py'`).
		Scan(&disposition, &reason, &killRate); err != nil {
		t.Fatal(err)
	}
	if disposition != "rejected" || reason != uncoveredReason {
		t.Errorf("disposition/reason = %q/%q, want rejected/%q", disposition, reason, uncoveredReason)
	}
	if killRate != nil {
		t.Errorf("an uncovered candidacy row was never graded — kill_rate = %v, want NULL", *killRate)
	}
}

// TestPushMigratesAPreExistingWarehouse pins the upgrade path. An operator's
// corral_audits was created by an earlier corral, so `CREATE TABLE IF NOT
// EXISTS` is a no-op and the table keeps its old 20 columns — while the
// INSERT now names 25. Without an additive migration a push that worked
// yesterday fails outright on the one table this tool asks operators to trust
// as a durable record.
func TestPushMigratesAPreExistingWarehouse(t *testing.T) {
	target := filepath.Join(t.TempDir(), "legacy.duckdb")

	// The PRE-selection column list, verbatim, built by hand.
	legacy := openTarget(t, target)
	if _, err := legacy.Exec(`CREATE TABLE corral_audits (
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
  run_url            VARCHAR
)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy target: %v", err)
	}

	n, err := Push(target, []Row{
		{Repo: "o/r", Commit: "c", Path: "pkg/u.py", Lang: "python", Survivors: 2,
			TestSelection: "coverage-context", SuiteTests: 1431, Uncovered: true},
		{Repo: "o/r", Commit: "c", Path: "pkg/a.py", Lang: "python", KillRate: rate(0.65), Survivors: 4,
			TestSelection: "coverage-context", SelectedTests: 14, SuiteTests: 1431},
	})
	if err != nil {
		t.Fatalf("Push onto a pre-existing warehouse must migrate it, not fail: %v", err)
	}
	if n != 2 {
		t.Fatalf("Push wrote %d rows, want 2", n)
	}

	db := openTarget(t, target)
	var killRate *float64
	var method, fallback *string
	var selected, suite *int
	var uncovered bool
	if err := db.QueryRow(`SELECT kill_rate, test_selection, selected_tests, suite_tests, selection_fallback, uncovered
	    FROM corral_audits WHERE path = 'pkg/u.py'`).
		Scan(&killRate, &method, &selected, &suite, &fallback, &uncovered); err != nil {
		t.Fatalf("read back the uncovered row: %v", err)
	}
	if killRate != nil {
		t.Errorf("uncovered row kill_rate = %v, want NULL: nothing graded that file", *killRate)
	}
	if !uncovered || method == nil || *method != "coverage-context" || suite == nil || *suite != 1431 {
		t.Errorf("migrated columns did not carry the uncovered row's facts: %v %v %v", uncovered, method, suite)
	}
	// Nothing to say is NULL, not a fabricated reason.
	if fallback != nil && *fallback != "" {
		t.Errorf("selection_fallback = %q, want empty: this row did not fall back", *fallback)
	}

	if err := db.QueryRow(`SELECT kill_rate, selected_tests, uncovered FROM corral_audits WHERE path = 'pkg/a.py'`).
		Scan(&killRate, &selected, &uncovered); err != nil {
		t.Fatalf("read back the graded row: %v", err)
	}
	if killRate == nil || *killRate != 0.65 || selected == nil || *selected != 14 || uncovered {
		t.Errorf("graded row = %v %v %v; the rate and its denominator must survive the migration", killRate, selected, uncovered)
	}

	// Idempotent: a second push over the now-current table runs no ALTER and
	// appends normally.
	if _, err := Push(target, []Row{{Repo: "o/r", Commit: "c2", Path: "pkg/a.py", KillRate: rate(0.7)}}); err != nil {
		t.Fatalf("second push over a migrated warehouse: %v", err)
	}
}

// TestPerMutantRowCarriesTheSpread pins the warehouse half of "the qualifiers
// travel with the numbers" at the grain the grading now happens. A kill rate
// measured over mutants that each faced 3 tests and one measured over the
// whole 620 are different measurements, and a query that averages kill_rate
// across both without these columns cannot tell them apart.
func TestPerMutantRowCarriesTheSpread(t *testing.T) {
	target := filepath.Join(t.TempDir(), "w.duckdb")
	if _, err := Push(target, []Row{
		{Repo: "o/r", Commit: "c", Path: "src/flask/cli.py", KillRate: rate(0.65), Survivors: 4,
			TestSelection: "coverage-lines", SelectedTests: 234, SuiteTests: 620,
			PerMutant: true, TestsPerMutant: &TestsPerMutantSpread{Min: 3, Median: 9, Max: 41}},
		{Repo: "o/r", Commit: "c", Path: "pkg/a.py", KillRate: rate(0.9), Survivors: 1,
			TestSelection: "coverage-context", SelectedTests: 14, SuiteTests: 1431},
		// Per-mutant, but the compile gate left no mutant graded: the run
		// DID measure per mutant and measured no spread. A stored 0-0 would
		// be a number nothing measured.
		{Repo: "o/r", Commit: "c", Path: "pkg/none.py", KillRate: rate(0), Survivors: 0,
			TestSelection: "coverage-lines", PerMutant: true},
	}); err != nil {
		t.Fatal(err)
	}

	db := openTarget(t, target)
	var perMutant bool
	var min, median, max *int
	if err := db.QueryRow(`SELECT per_mutant, tests_per_mutant_min, tests_per_mutant_median, tests_per_mutant_max
	    FROM corral_audits WHERE path = 'src/flask/cli.py'`).Scan(&perMutant, &min, &median, &max); err != nil {
		t.Fatalf("read back the per-mutant row: %v", err)
	}
	if !perMutant || min == nil || *min != 3 || median == nil || *median != 9 || max == nil || *max != 41 {
		t.Errorf("per-mutant row = %v %v %v %v; the spread must travel with the rate", perMutant, min, median, max)
	}

	if err := db.QueryRow(`SELECT per_mutant, tests_per_mutant_min, tests_per_mutant_max
	    FROM corral_audits WHERE path = 'pkg/a.py'`).Scan(&perMutant, &min, &max); err != nil {
		t.Fatalf("read back the shared-command row: %v", err)
	}
	if perMutant {
		t.Errorf("a file graded by one shared command must not read as per-mutant")
	}
	if min != nil || max != nil {
		t.Errorf("a shared-command row must store NULL, not 0: %v %v", min, max)
	}

	if err := db.QueryRow(`SELECT per_mutant, tests_per_mutant_min, tests_per_mutant_max
	    FROM corral_audits WHERE path = 'pkg/none.py'`).Scan(&perMutant, &min, &max); err != nil {
		t.Fatalf("read back the unmeasured per-mutant row: %v", err)
	}
	if !perMutant {
		t.Errorf("a run that graded per mutant must say so even when no mutant survived the compile gate")
	}
	if min != nil || max != nil {
		t.Errorf("an unmeasured spread must be NULL, not 0-0: %v %v", min, max)
	}
}

// TestPushMigratesAPreExistingWarehouseOntoThePerMutantColumns is the same
// upgrade path one column set later: a warehouse whose newest columns are the
// FILE-grain selection five must gain the per-mutant four rather than failing
// every push.
func TestPushMigratesAPreExistingWarehouseOntoThePerMutantColumns(t *testing.T) {
	target := filepath.Join(t.TempDir(), "legacy-permutant.duckdb")

	// The pre-per-mutant column list, verbatim.
	legacy := openTarget(t, target)
	if _, err := legacy.Exec(`CREATE TABLE corral_audits (
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
  uncovered          BOOLEAN
)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy target: %v", err)
	}

	n, err := Push(target, []Row{{
		Repo: "o/r", Commit: "c", Path: "src/flask/cli.py", Lang: "python",
		KillRate: rate(0.65), Survivors: 4,
		TestSelection: "coverage-lines", SelectedTests: 234, SuiteTests: 620,
		PerMutant: true, TestsPerMutant: &TestsPerMutantSpread{Min: 3, Median: 9, Max: 41},
	}})
	if err != nil {
		t.Fatalf("Push onto a pre-per-mutant warehouse must migrate it, not fail: %v", err)
	}
	if n != 1 {
		t.Fatalf("Push wrote %d rows, want 1", n)
	}

	db := openTarget(t, target)
	var perMutant bool
	var min, median, max *int
	if err := db.QueryRow(`SELECT per_mutant, tests_per_mutant_min, tests_per_mutant_median, tests_per_mutant_max
	    FROM corral_audits WHERE path = 'src/flask/cli.py'`).Scan(&perMutant, &min, &median, &max); err != nil {
		t.Fatalf("read back the migrated row: %v", err)
	}
	if !perMutant || min == nil || *min != 3 || median == nil || *median != 9 || max == nil || *max != 41 {
		t.Errorf("migrated per-mutant columns did not round-trip: %v %v %v %v", perMutant, min, median, max)
	}
}

// TestConcurrencyColumnsRoundTrip pins the warehouse's half of "every reader
// says how many trees scored the file, or why one". A rate earned with 6
// trees scoring mutants at once and one earned with a single tree after a
// concurrency downgrade are different runs, and a cross-repo query that
// cannot see the tree count cannot tell them apart.
func TestConcurrencyColumnsRoundTrip(t *testing.T) {
	target := filepath.Join(t.TempDir(), "w.duckdb")
	if _, err := Push(target, []Row{
		{Repo: "o/r", Commit: "c", Path: "src/flask/cli.py", KillRate: rate(0.65), Survivors: 4, Trees: 6, SharedDirs: ".venv"},
		{Repo: "o/r", Commit: "c", Path: "pkg/a.py", KillRate: rate(0.9), Survivors: 1, Trees: 1,
			ConcurrencyNote: "suite is not concurrency-safe: baseline failed under 3"},
	}); err != nil {
		t.Fatal(err)
	}

	db := openTarget(t, target)
	var trees int
	var note, shared sql.NullString
	if err := db.QueryRow(`SELECT trees, concurrency_note, shared_dirs FROM corral_audits WHERE path = 'src/flask/cli.py'`).
		Scan(&trees, &note, &shared); err != nil {
		t.Fatalf("read back the many-tree row: %v", err)
	}
	if trees != 6 || note.Valid {
		t.Errorf("got trees=%d note=%v, want 6 and no note", trees, note)
	}
	// The dep dirs the trees shared are the channel between them; a
	// cross-repo query has to be able to see it.
	if !shared.Valid || shared.String != ".venv" {
		t.Errorf("got shared_dirs=%v, want .venv", shared)
	}

	if err := db.QueryRow(`SELECT trees, concurrency_note, shared_dirs FROM corral_audits WHERE path = 'pkg/a.py'`).
		Scan(&trees, &note, &shared); err != nil {
		t.Fatalf("read back the downgraded row: %v", err)
	}
	if shared.Valid {
		t.Errorf("a row that shared nothing must store SQL NULL, got %v", shared)
	}
	if trees != 1 || !note.Valid || note.String != "suite is not concurrency-safe: baseline failed under 3" {
		t.Errorf("got trees=%d note=%v, want the downgrade note preserved", trees, note)
	}
}

// TestUnrecordedConcurrencyIsPushedNull pins the same rule the ledger keeps:
// a row whose concurrency was never measured (the jail substrate builds no
// trees; a cached pre-concurrency verdict carries none) writes SQL NULL, not
// 0. A 0 in an INTEGER column is a value a cross-repo query will average and
// rank; NULL is the only encoding of "this warehouse does not say".
func TestUnrecordedConcurrencyIsPushedNull(t *testing.T) {
	target := filepath.Join(t.TempDir(), "w.duckdb")
	if _, err := Push(target, []Row{
		{Repo: "o/r", Commit: "c", Path: "pkg/jail.py", KillRate: rate(0.65), Survivors: 4},
	}); err != nil {
		t.Fatal(err)
	}
	db := openTarget(t, target)
	var trees sql.NullInt64
	if err := db.QueryRow(`SELECT trees FROM corral_audits WHERE path = 'pkg/jail.py'`).Scan(&trees); err != nil {
		t.Fatalf("read back the unrecorded row: %v", err)
	}
	if trees.Valid {
		t.Errorf("an unrecorded concurrency must be stored SQL NULL, got %d", trees.Int64)
	}
}

// TestPushMigratesAPreExistingWarehouseOntoTheConcurrencyColumns is the same
// upgrade path one column set later: a warehouse whose newest columns are
// the per-mutant four must gain the two concurrency columns rather than
// failing every push.
func TestPushMigratesAPreExistingWarehouseOntoTheConcurrencyColumns(t *testing.T) {
	target := filepath.Join(t.TempDir(), "legacy-concurrency.duckdb")

	legacy := openTarget(t, target)
	if _, err := legacy.Exec(`CREATE TABLE corral_audits (
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
  tests_per_mutant_max    INTEGER
)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy target: %v", err)
	}

	n, err := Push(target, []Row{{
		Repo: "o/r", Commit: "c", Path: "src/flask/cli.py", Lang: "python",
		KillRate: rate(0.65), Survivors: 4, Trees: 6, SharedDirs: ".venv",
	}})
	if err != nil {
		t.Fatalf("Push onto a pre-concurrency warehouse must migrate it, not fail: %v", err)
	}
	if n != 1 {
		t.Fatalf("Push wrote %d rows, want 1", n)
	}

	db := openTarget(t, target)
	var trees int
	var shared sql.NullString
	if err := db.QueryRow(`SELECT trees, shared_dirs FROM corral_audits WHERE path = 'src/flask/cli.py'`).Scan(&trees, &shared); err != nil {
		t.Fatalf("read back the migrated row: %v", err)
	}
	if trees != 6 || !shared.Valid || shared.String != ".venv" {
		t.Errorf("migrated concurrency columns did not round-trip: trees=%d shared=%v", trees, shared)
	}
}

// TestPushRefusesARowWithoutStatementSHA is the guard the certify --repo path
// leans on: when a Link says a statement is required (--attest was given),
// a row with no statement_sha256 must never reach the warehouse looking
// traceable when it is not. The test-only/legacy path above passes rows with
// no Link at all, which must keep working unchanged.
func TestPushRefusesARowWithoutStatementSHA(t *testing.T) {
	target := filepath.Join(t.TempDir(), "w.duckdb")

	_, err := Push(target, []Row{{
		Repo: "o/r", Commit: "c", Path: "a.py",
	}}, Link{Require: true})
	if err == nil {
		t.Fatal("Push must refuse a row with no statement_sha256 when Link.Require is true")
	}
	if !contains(err.Error(), "a.py") {
		t.Errorf("error %q must name the offending row", err.Error())
	}

	// A statement_sha256 present satisfies the requirement.
	if _, err := Push(target, []Row{{
		Repo: "o/r", Commit: "c", Path: "b.py", StatementSHA256: "deadbeef",
	}}, Link{Require: true}); err != nil {
		t.Fatalf("Push with a statement_sha256 present must not be refused: %v", err)
	}
}

// TestPushScanIDRoundTrips confirms the additive scan_id column lands and
// reads back — the ledger-row join key every warehouse consumer relies on.
func TestPushScanIDRoundTrips(t *testing.T) {
	target := filepath.Join(t.TempDir(), "w.duckdb")

	if _, err := Push(target, []Row{{
		Repo: "o/r", Commit: "c", Path: "a.py", ScanID: 42,
	}}); err != nil {
		t.Fatal(err)
	}

	db := openTarget(t, target)
	var scanID int64
	if err := db.QueryRow(`SELECT scan_id FROM corral_audits WHERE path = 'a.py'`).Scan(&scanID); err != nil {
		t.Fatalf("read back scan_id: %v", err)
	}
	if scanID != 42 {
		t.Errorf("scan_id = %d, want 42", scanID)
	}
}

// TestPushCarriesCachedInputTokens: the per-survivor writer's whole cost claim
// is that one file's N calls share one cached prefix. The warehouse column is
// where that claim is checkable, and NULL-vs-0 is the difference between "the
// provider said nothing" and "the provider reused nothing".
func TestPushCarriesCachedInputTokens(t *testing.T) {
	target := filepath.Join(t.TempDir(), "w.duckdb")
	cached := int64(1200)
	if _, err := PushBundle(target, Bundle{Calls: []ModelCallRow{
		{Repo: "o/r", ScanID: 1, Path: "a.py", Role: "test-writer", Model: "w",
			Calls: 24, InputTokens: 50000, OutputTokens: 4000, CachedInputTokens: &cached},
		{Repo: "o/r", ScanID: 1, Path: "a.py", Role: "mutant-generator", Model: "g",
			Calls: 8, InputTokens: 9000, OutputTokens: 3000},
	}}); err != nil {
		t.Fatalf("PushBundle: %v", err)
	}

	db := openTarget(t, target)
	rows, err := db.Query(`SELECT role, cached_input_tokens FROM corral_model_calls ORDER BY role`)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	defer rows.Close()
	got := map[string]*int64{}
	for rows.Next() {
		var role string
		var v sql.NullInt64
		if err := rows.Scan(&role, &v); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if v.Valid {
			n := v.Int64
			got[role] = &n
		} else {
			got[role] = nil
		}
	}
	if got["test-writer"] == nil || *got["test-writer"] != 1200 {
		t.Errorf("test-writer cached_input_tokens = %v, want 1200", got["test-writer"])
	}
	if got["mutant-generator"] != nil {
		t.Errorf("mutant-generator cached_input_tokens = %d, want NULL — nothing reported one", *got["mutant-generator"])
	}
}
