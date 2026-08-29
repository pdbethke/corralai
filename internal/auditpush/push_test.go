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
