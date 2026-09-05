// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/reposcan"
)

// selCacheFixture builds a python repo whose one candidate (a.py, paired
// with test_a.py) is changed after a base commit — so --diff-base selects
// it — and is deliberately left OUT of goals.json, so EmitJobs excludes it
// as ungoaled and no baseline ever runs for real. collectSelection is still
// reached (it gates on `selected`, computed before goal resolution), which
// is all this fixture needs: it lets the WHOLE certify_repo pipeline run,
// record and exit cleanly without requiring a real pytest install or paying
// for a single model call.
func selCacheFixture(t *testing.T) (root, base, goals string) {
	t.Helper()
	root = t.TempDir()
	gitRun := gitCmd(t, root)
	mustWrite(t, filepath.Join(root, "a.py"), "def f():\n    return 1\n")
	mustWrite(t, filepath.Join(root, "test_a.py"), "def test_f():\n    assert True\n")
	gitRun("init", "-q")
	gitRun("add", ".")
	gitRun("commit", "-q", "-m", "base", "--no-gpg-sign")
	base = gitRevParseHead(t, root)

	mustWrite(t, filepath.Join(root, "a.py"), "def f():\n    return 2\n")
	gitRun("add", "a.py")
	gitRun("commit", "-q", "-m", "change", "--no-gpg-sign")

	goals = filepath.Join(root, "goals.json")
	mustWrite(t, goals, `{}`)
	return root, base, goals
}

// selCacheArgs points the run at dsn as its CACHE file and at the ledger
// directory beside it as its record — the two files a real run keeps.
func selCacheArgs(root, base, goals, dsn string, extra ...string) []string {
	args := []string{
		"--repo", root, "--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off",
		"--diff-base", base, "--goals", goals,
		"--substrate", substrateWorkspace,
		"--cache-db", dsn, "--ledger", selCacheLedger(dsn),
	}
	return append(args, extra...)
}

func selCacheLedger(dsn string) string { return filepath.Join(filepath.Dir(dsn), "ledger") }

// TestSelectionCacheReusesIdenticalTree is the headline case from the task
// brief: scan 1 runs the instrumented pass and Puts; scan 2 over the SAME
// tree does not run it again, serves byte-identical evidence, says so on
// stdout, and records selection_reused_from = scan 1's id with selection_ms
// NULL (the pass did not happen THIS scan — both are asserted, since either
// one alone would pass an implementation that got the other wrong). A
// one-byte source change busts the cache and runs again.
func TestSelectionCacheReusesIdenticalTree(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root, base, goals := selCacheFixture(t)
	dsn := filepath.Join(t.TempDir(), "scans.duckdb")

	calls := 0
	orig := collectSelectionEvidence
	t.Cleanup(func() { collectSelectionEvidence = orig })
	collectSelectionEvidence = func(ctx context.Context, runner coverageRunner, files map[string]string, p lang.Plugin, testCmd []string, sourcePaths []string) reposcan.SelectionEvidence {
		calls++
		return reposcan.SelectionEvidence{Ran: true, Raw: []byte("evidence-from-call-" + string(rune('0'+calls)))}
	}

	// Scan 1: a MISS. Must run the instrumented pass exactly once and say so.
	var out1, err1 bytes.Buffer
	runCertifyRepo(selCacheArgs(root, base, goals, dsn), &out1, &err1)
	if err1.Len() != 0 {
		t.Fatalf("scan 1: stderr must be empty, got %q", err1.String())
	}
	if calls != 1 {
		t.Fatalf("scan 1: collectSelectionEvidence called %d time(s), want 1", calls)
	}
	if !strings.Contains(out1.String(), "selection: running the suite once with per-test coverage instrumentation…") {
		t.Errorf("scan 1: stdout must announce the run, got:\n%s", out1.String())
	}
	if strings.Contains(out1.String(), "selection: reused") {
		t.Errorf("scan 1: must not claim a reuse on the very first scan:\n%s", out1.String())
	}

	scan1ID, scan1SelectionMS, scan1ReusedFrom := readScanSelectionRow(t, dsn, 1)
	if scan1SelectionMS == nil {
		t.Error("scan 1: selection_ms must be non-NULL — the pass actually ran")
	}
	if scan1ReusedFrom != nil {
		t.Errorf("scan 1: selection_reused_from = %v, want NULL — nothing to reuse yet", *scan1ReusedFrom)
	}

	// Scan 2: the tree is UNCHANGED. Must be a HIT — no second run, the
	// SAME evidence bytes, the reused announce line, selection_reused_from
	// naming scan 1, and selection_ms NULL (the phase did not happen this
	// scan).
	var out2, err2 bytes.Buffer
	runCertifyRepo(selCacheArgs(root, base, goals, dsn), &out2, &err2)
	if err2.Len() != 0 {
		t.Fatalf("scan 2: stderr must be empty, got %q", err2.String())
	}
	if calls != 1 {
		t.Errorf("scan 2: collectSelectionEvidence called again (now %d total) — a byte-identical tree must be a cache HIT, not a re-run", calls)
	}
	wantReused := "selection: reused — tree unchanged since an earlier scan (cached evidence)"
	if !strings.Contains(out2.String(), wantReused) {
		t.Errorf("scan 2: stdout = %q, want it to contain %q", out2.String(), wantReused)
	}

	_, scan2SelectionMS, scan2ReusedFrom := readScanSelectionRow(t, dsn, 2)
	if scan2SelectionMS != nil {
		t.Errorf("scan 2: selection_ms = %v, want NULL — the pass did not run THIS scan, it was reused", *scan2SelectionMS)
	}
	if scan2ReusedFrom == nil {
		t.Errorf("scan 2: selection_reused_from is NULL, want set — the entry must say the evidence was reused, not merely omit the run")
	}
	_ = scan1ID

	// Scan 3: ONE byte of the (already-changed, but now further edited)
	// tracked source changes. A genuinely different question — must run
	// again, not be silently served scan 1/2's stale evidence.
	if err := os.WriteFile(filepath.Join(root, "a.py"), []byte("def f():\n    return 3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out3, err3 bytes.Buffer
	runCertifyRepo(selCacheArgs(root, base, goals, dsn), &out3, &err3)
	if err3.Len() != 0 {
		t.Fatalf("scan 3: stderr must be empty, got %q", err3.String())
	}
	if calls != 2 {
		t.Errorf("scan 3: collectSelectionEvidence called %d time(s) total, want 2 — a changed tracked byte must bust the cache", calls)
	}
	if strings.Contains(out3.String(), "selection: reused") {
		t.Errorf("scan 3: must not claim a reuse over a changed tree:\n%s", out3.String())
	}
}

// TestNoSelectionCacheFlagRunsEveryTime proves --no-selection-cache actually
// disables the wiring, not merely a disclosure line: with it given, a
// second scan over an UNCHANGED tree still pays for a fresh instrumented
// run.
func TestNoSelectionCacheFlagRunsEveryTime(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root, base, goals := selCacheFixture(t)
	dsn := filepath.Join(t.TempDir(), "scans.duckdb")

	calls := 0
	orig := collectSelectionEvidence
	t.Cleanup(func() { collectSelectionEvidence = orig })
	collectSelectionEvidence = func(ctx context.Context, runner coverageRunner, files map[string]string, p lang.Plugin, testCmd []string, sourcePaths []string) reposcan.SelectionEvidence {
		calls++
		return reposcan.SelectionEvidence{Ran: true, Raw: []byte("evidence")}
	}

	args := selCacheArgs(root, base, goals, dsn, "--no-selection-cache")
	var out1, err1 bytes.Buffer
	runCertifyRepo(args, &out1, &err1)
	var out2, err2 bytes.Buffer
	runCertifyRepo(args, &out2, &err2)

	if err1.Len() != 0 || err2.Len() != 0 {
		t.Fatalf("stderr must be empty: %q / %q", err1.String(), err2.String())
	}
	if calls != 2 {
		t.Errorf("--no-selection-cache: collectSelectionEvidence called %d time(s), want 2 (every scan runs its own pass)", calls)
	}
	if strings.Contains(out2.String(), "selection: reused") {
		t.Errorf("--no-selection-cache: must never claim a reuse, got:\n%s", out2.String())
	}
}

// TestSelectionCacheNeverPutsOnAFailedRun is the write-side half of the
// caching defect: an instrumented run that came back Ran:false (a failed
// suite, or one that printed nothing) must NEVER reach the ledger's
// selection_cache table — collectSelection gates the Put on ev.Ran, and
// this proves that gate actually stops a poisoned entry from being
// written, not merely that a passing run's real evidence gets recorded (the
// headline reuse test already covers that). A second scan over the SAME
// unchanged tree must therefore run the instrumented pass AGAIN too — there
// is nothing cached to serve.
func TestSelectionCacheNeverPutsOnAFailedRun(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "test-placeholder-not-a-real-key")
	root, base, goals := selCacheFixture(t)
	dsn := filepath.Join(t.TempDir(), "scans.duckdb")

	calls := 0
	orig := collectSelectionEvidence
	t.Cleanup(func() { collectSelectionEvidence = orig })
	collectSelectionEvidence = func(ctx context.Context, runner coverageRunner, files map[string]string, p lang.Plugin, testCmd []string, sourcePaths []string) reposcan.SelectionEvidence {
		calls++
		// Ran:false — exactly what CollectSelectionEvidence itself now
		// returns for an instrumented run that printed nothing (a failed
		// suite before its own evidence-emitting step, or any other
		// disclosed failure). Never a Raw-carrying, Ran:true evidence.
		return reposcan.SelectionEvidence{Note: "python: selection evidence run exited 4 and printed nothing"}
	}

	var out1, err1 bytes.Buffer
	runCertifyRepo(selCacheArgs(root, base, goals, dsn), &out1, &err1)
	if err1.Len() != 0 {
		t.Fatalf("scan 1: stderr must be empty, got %q", err1.String())
	}
	if calls != 1 {
		t.Fatalf("scan 1: collectSelectionEvidence called %d time(s), want 1", calls)
	}

	db, derr := sql.Open("duckdb", dsn)
	if derr != nil {
		t.Fatalf("open ledger: %v", derr)
	}
	defer db.Close()
	var n int
	if qerr := db.QueryRow(`SELECT count(*) FROM selection_cache`).Scan(&n); qerr != nil {
		t.Fatalf("count selection_cache: %v", qerr)
	}
	if n != 0 {
		t.Fatalf("selection_cache has %d row(s) after a failed run, want 0 — a failed instrumented run must never be Put", n)
	}

	// A second scan over the SAME unchanged tree: with nothing cached, this
	// must run the instrumented pass again, not silently serve a miss as
	// though it were a hit for zero cost.
	var out2, err2 bytes.Buffer
	runCertifyRepo(selCacheArgs(root, base, goals, dsn), &out2, &err2)
	if err2.Len() != 0 {
		t.Fatalf("scan 2: stderr must be empty, got %q", err2.String())
	}
	if calls != 2 {
		t.Errorf("scan 2: collectSelectionEvidence called %d time(s) total, want 2 — a failed run must never be served from cache", calls)
	}
	if strings.Contains(out2.String(), "selection: reused") {
		t.Errorf("scan 2: must never claim a reuse of a failed run's evidence:\n%s", out2.String())
	}
}

// itoa avoids importing strconv twice for one call site's sake — this file
// already imports enough; fmt.Sprint would allocate an interface, this does
// not.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// readScanSelectionRow reads the Nth-oldest (by id, 1-indexed) scan's
// selection_ms and selection_reused_from directly off the ledger — bypassing
// scanstore's own reader so this test does not depend on ScanByID's
// correctness to prove Record's.
// readScanSelectionRow reads the nth entry (1 = oldest) of the ledger
// beside dsn — the record — through the same reader `corral scans` uses.
func readScanSelectionRow(t *testing.T, dsn string, nth int) (id int64, selectionMS, reusedFrom *int64) {
	t.Helper()
	st, err := openLedgerScans(selCacheLedger(dsn))
	if err != nil {
		t.Fatalf("open ledger: %v", err)
	}
	defer st.Close()
	row, ok, err := st.ScanByID(context.Background(), int64(nth))
	if err != nil {
		t.Fatalf("scan %d: %v", nth, err)
	}
	if !ok {
		t.Fatalf("no scan %d recorded (want at least %d entries)", nth, nth)
	}
	return row.ID, row.SelectionMillis, row.SelectionReusedFrom
}

func TestSelectionCacheKeyDigestsTheCommandThatActuallyRuns(t *testing.T) {
	root, _, _ := selCacheFixture(t)
	ex := newLocalExecutor(root, nil, substrateWorkspace, 0, io.Discard)
	defer ex.Close()
	py, ok := lang.ByName("python")
	if !ok {
		t.Fatal("no python plugin")
	}
	testCmd := []string{"pytest", "-q"}
	srcA := []string{"pkg_a/a.py", "pkg_a/test_a.py"}
	srcB := []string{"pkg_b/b.py", "pkg_b/test_b.py"}

	_, dA, okA := ex.selectionCacheKey(py, testCmd, srcA)
	_, dB, okB := ex.selectionCacheKey(py, testCmd, srcB)
	if !okA || !okB {
		t.Fatalf("no key: a=%v b=%v", okA, okB)
	}
	if dA == dB {
		t.Errorf("two scans over different source roots share cmd_digest %s — the second would be served the first's evidence", dA)
	}
	ranA, _ := reposcan.InstrumentedCommand(py, testCmd, srcA)
	if want := selectionCmdDigest(ranA); dA != want {
		t.Errorf("cmd_digest = %s, want %s — the digest of the command that runs (%q)", dA, want, strings.Join(ranA, " "))
	}
	if !strings.Contains(strings.Join(ranA, " "), "pkg_a") {
		t.Fatalf("fixture: the instrumented python command carries no source root: %q", ranA)
	}
}
