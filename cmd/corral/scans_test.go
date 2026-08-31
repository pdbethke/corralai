// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/scanstore"
)

type fakeScansReader struct {
	scans      []scanstore.ScanRow
	scan       scanstore.ScanRow // what ScanByID answers with; zero ID = not found
	files      []scanstore.File
	mutants    []scanstore.Mutant
	modelCalls []scanstore.ModelCall
	limit      int // records what the command asked for
}

func (f *fakeScansReader) Scans(_ context.Context, limit int) ([]scanstore.ScanRow, error) {
	f.limit = limit
	return f.scans, nil
}
func (f *fakeScansReader) FilesForScan(context.Context, int64) ([]scanstore.File, error) {
	return f.files, nil
}
func (f *fakeScansReader) MutantsForScan(context.Context, int64) ([]scanstore.Mutant, error) {
	return f.mutants, nil
}
func (f *fakeScansReader) ModelCallsForScan(context.Context, int64) ([]scanstore.ModelCall, error) {
	return f.modelCalls, nil
}

// ScanByID answers with the fixture's scan header. A zero ID stands for "no
// such scan", the same ANSWER (not error) the real store returns — every
// pre-existing fixture leaves it unset, so the header is simply absent.
func (f *fakeScansReader) ScanByID(context.Context, int64) (scanstore.ScanRow, bool, error) {
	if f.scan.ID == 0 {
		return scanstore.ScanRow{}, false, nil
	}
	return f.scan, true, nil
}
func (f *fakeScansReader) Close() error { return nil }

func openFake(r *fakeScansReader) func(string) (scansReader, error) {
	return func(string) (scansReader, error) { return r, nil }
}

func ptrF(v float64) *float64 { return &v }

// TestScansShow_DistinguishesTheThreeWaysProvenCanBeZero is the reason this
// command exists at all. `proven_missed = 0` is ambiguous: it can mean the
// pool never authored a compiling test, that the test it authored never
// genuinely graded, or that a perfectly sound test ran and proved nothing.
// The ledger already stored flags telling those apart — but nothing could read
// them, so the distinction was written and then lost to whoever needed it.
func TestScansShow_DistinguishesTheThreeWaysProvenCanBeZero(t *testing.T) {
	r := &fakeScansReader{files: []scanstore.File{
		{Path: "writer.py", Disposition: "audited", Survivors: 4, ProvenMissed: 0, TestWriterFailed: true},
		{Path: "unsound.py", Disposition: "audited", Survivors: 5, ProvenMissed: 0, PoolTestUnsound: true},
		{Path: "missed.py", Disposition: "audited", Survivors: 10, ProvenMissed: 0},
		{Path: "proven.py", Disposition: "audited", Survivors: 3, ProvenMissed: 2, ProvenMutantIDs: "m1,m3"},
		// A FOURTH way, and the note read it as the third: an uncovered file
		// has survivors and (usually) proven_missed 0 because NO TEST
		// EXECUTES IT, not because a writer tried and missed. Saying "the
		// authored test graded and proved nothing" about a file nothing
		// grades is a false diagnosis of the writer.
		{Path: "uncovered.py", Disposition: "audited", Survivors: 7, ProvenMissed: 0, Uncovered: true},
		{Path: "uncovered_proven.py", Disposition: "audited", Survivors: 7, ProvenMissed: 2, Uncovered: true},
	}}
	var out, errOut bytes.Buffer
	if code := runScans([]string{"show", "7"}, openFake(r), &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	got := out.String()
	for _, want := range []string{
		"WRITER FAILED",
		"TEST UNSOUND",
		"tried and missed",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output must distinguish %q; it does not:\n%s", want, got)
		}
	}
	for _, line := range strings.Split(got, "\n") {
		if !strings.HasPrefix(line, "uncovered") {
			continue
		}
		if strings.Contains(line, "tried and missed") {
			t.Errorf("an uncovered row must not accuse the writer: %q", line)
		}
		if !strings.Contains(line, "uncovered — no test executes it; rate withheld") {
			t.Errorf("an uncovered row must say what it is: %q", line)
		}
	}
	// The proven count still prints when an uncovered file's writer DID
	// prove something — the file being uncovered does not erase that.
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "uncovered_proven.py") && !strings.Contains(line, "2") {
			t.Errorf("a proven gap on an uncovered file must still be reported: %q", line)
		}
	}

	// A genuinely proven row must NOT be labelled with any of the caveats.
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "proven.py") && strings.Contains(line, "missed") {
			t.Errorf("a row with proven gaps must not carry a caveat: %q", line)
		}
	}
}

// TestScansShow_EvidencePrintsTheAuthoredTestEvenWhenItProvedNothing pins the
// case the evidence columns were added for: the tried-and-missed. If --evidence
// only printed tests that succeeded, the ledger would still be useless for the
// one question that actually needed it.
func TestScansShow_EvidencePrintsTheAuthoredTestEvenWhenItProvedNothing(t *testing.T) {
	const src = "def test_corral_authored():\n    assert thing() == 1\n"
	r := &fakeScansReader{files: []scanstore.File{
		{Path: "missed.py", Disposition: "audited", Survivors: 10, ProvenMissed: 0, AuthoredTest: src},
	}}
	var out, errOut bytes.Buffer
	if code := runScans([]string{"show", "7", "--evidence"}, openFake(r), &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, src) {
		t.Fatalf("--evidence must print the authored test even when it proved nothing:\n%s", got)
	}
	if !strings.Contains(got, "killed: nothing") {
		t.Errorf("a tried-and-missed must say so explicitly, not print a bare empty list:\n%s", got)
	}
}

// TestScansList_NullKillRateIsNotZero pins the NaN/NULL discipline all the way
// out to the terminal: a scan that audited nothing must not render "0.00",
// which reads as "your tests caught nothing" about something never graded.
func TestScansList_NullKillRateIsNotZero(t *testing.T) {
	r := &fakeScansReader{scans: []scanstore.ScanRow{
		{ID: 2, Scan: scanstore.Scan{Repo: "never-graded", Audited: 0, KillRate: nil}},
		{ID: 1, Scan: scanstore.Scan{Repo: "graded", Audited: 1, KillRate: ptrF(0.48)}},
	}}
	var out, errOut bytes.Buffer
	if code := runScans([]string{"list"}, openFake(r), &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	got := out.String()
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "2") && strings.Contains(line, "0.00") {
			t.Errorf("a never-graded scan must not render 0.00: %q", line)
		}
	}
	if !strings.Contains(got, "0.48") {
		t.Errorf("a real kill rate must still render: %s", got)
	}
}

func TestScansUsageErrors(t *testing.T) {
	r := &fakeScansReader{}
	for _, c := range []struct {
		name string
		args []string
	}{
		{"no subcommand", nil},
		{"unknown subcommand", []string{"delete"}},
		{"show without an id", []string{"show"}},
		{"show with a non-numeric id", []string{"show", "abc"}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var out, errOut bytes.Buffer
			if code := runScans(c.args, openFake(r), &out, &errOut); code != 2 {
				t.Fatalf("exit = %d, want 2 (usage); stderr=%s", code, errOut.String())
			}
		})
	}
}

// TestScansList_LimitReachesTheStore guards the flag actually being plumbed,
// not merely accepted — a --limit the store never sees is the same
// silently-discarded-input shape this codebase keeps producing.
func TestScansList_LimitReachesTheStore(t *testing.T) {
	r := &fakeScansReader{}
	var out, errOut bytes.Buffer
	if code := runScans([]string{"list", "--limit", "3"}, openFake(r), &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	if r.limit != 3 {
		t.Fatalf("store saw limit=%d, want 3", r.limit)
	}
}

// A scan can be 24 of 25 files reused from three weeks ago, and before this
// the reader showed only a kill rate: cache_hits and cache_hit were both read
// back from SQL and neither was ever printed. That is precisely the
// self-flattering record this tool exists to prevent — the number looks like a
// fresh measurement of today's code.
func TestScansListDisclosesCacheHits(t *testing.T) {
	r := &fakeScansReader{scans: []scanstore.ScanRow{
		{ID: 9, Scan: scanstore.Scan{Repo: "flask", Substrate: "workspace", Audited: 25, Candidates: 25, KillRate: ptrF(0.5), CacheHits: 24}},
	}}
	var out, errOut bytes.Buffer
	if code := runScans([]string{"list"}, openFake(r), &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "REUSED") {
		t.Errorf("list view has no reuse column:\n%s", got)
	}
	if !strings.Contains(got, "24") {
		t.Errorf("list view never printed the 24 reused verdicts:\n%s", got)
	}
}

func TestScansShowMarksAReusedFile(t *testing.T) {
	r := &fakeScansReader{files: []scanstore.File{
		{Path: "fresh.py", Disposition: "audited", Survivors: 0, KillRate: ptrF(1)},
		{Path: "reused.py", Disposition: "audited", Survivors: 0, KillRate: ptrF(1), CacheHit: true},
	}}
	var out, errOut bytes.Buffer
	if code := runScans([]string{"show", "9"}, openFake(r), &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	got := out.String()
	for _, line := range strings.Split(got, "\n") {
		if strings.HasPrefix(line, "reused.py") && !strings.Contains(line, "REUSED") {
			t.Errorf("a reused row does not say so:\n%s", got)
		}
		if strings.HasPrefix(line, "fresh.py") && strings.Contains(line, "REUSED") {
			t.Errorf("a freshly earned row was marked reused:\n%s", got)
		}
	}
	if !strings.Contains(got, "REUSED") {
		t.Errorf("no reuse marker at all:\n%s", got)
	}
}

// TestScanFileSelectionShowsThePerMutantSpread pins the ledger reader's
// column at the grain the grading happened. Two rows are only comparable if
// they answered the same question, and "234/620" hides that one file's
// mutants each faced between 3 and 41 of those tests.
func TestScanFileSelectionShowsThePerMutantSpread(t *testing.T) {
	f := scanstore.File{Path: "src/flask/cli.py", TestSelection: "coverage-lines", SelectedTests: 234, SuiteTests: 620}
	got := scanFileSelectionWith(f, mutantSpread{min: 3, max: 41, ok: true})
	if got != "coverage-lines 234/620, 3–41/mutant" {
		t.Errorf("scanFileSelectionWith = %q", got)
	}
	// A scan whose mutants carry no per-mutant grading prints exactly what
	// it always did — never a fabricated 0–0.
	if got := scanFileSelectionWith(f, mutantSpread{}); got != "coverage-lines 234/620" {
		t.Errorf("scanFileSelection = %q, want the unchanged column", got)
	}

	// And end to end: `scans show` must derive the spread from the scan's
	// own mutant rows, not require a second copy of the fact at the file
	// grain.
	r := &fakeScansReader{
		files: []scanstore.File{{Path: "src/flask/cli.py", Disposition: "audited",
			TestSelection: "coverage-lines", SelectedTests: 234, SuiteTests: 620}},
		mutants: []scanstore.Mutant{
			{Path: "src/flask/cli.py", MutantID: "m1", Outcome: "killed", TestsRun: 3, SelectionRule: "lines"},
			{Path: "src/flask/cli.py", MutantID: "m2", Outcome: "survived", TestsRun: 41, SelectionRule: "unreached"},
			// No rule: graded by the file's shared command, so it must not
			// drag the minimum down to a 0 nothing measured.
			{Path: "src/flask/cli.py", MutantID: "m3", Outcome: "killed"},
		},
	}
	var out, errOut bytes.Buffer
	if code := runScans([]string{"show", "7"}, openFake(r), &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "coverage-lines 234/620, 3–41/mutant") {
		t.Errorf("scans show did not disclose the per-mutant spread:\n%s", out.String())
	}
}

// TestScansShow_DisclosesConcurrency pins the ledger reader's half of "every
// reader says how many trees scored the file, or why one" — `corral scans
// show` must print the same fact the live progress and the report line do,
// through the same shared wording.
func TestScansShow_DisclosesConcurrency(t *testing.T) {
	r := &fakeScansReader{
		files: []scanstore.File{
			{Path: "src/flask/cli.py", Disposition: "audited", Trees: 6, SharedDirs: ".venv"},
			{Path: "downgraded.py", Disposition: "audited", Trees: 1,
				ConcurrencyNote: "suite is not concurrency-safe: baseline failed under 3"},
			// Trees 0 is the ledger's "not recorded": a row written before
			// the column existed, a rejected file that was never scored, or
			// a jail-substrate run that builds no trees at all. Printing a
			// "1" for it would invent a measurement the ledger never holds.
			{Path: "unrecorded.py", Disposition: "audited"},
		},
	}
	var out, errOut bytes.Buffer
	if code := runScans([]string{"show", "7"}, openFake(r), &out, &errOut); code != 0 {
		t.Fatalf("exit = %d, stderr=%s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "6 trees (baseline passed under 6; shared: .venv)") {
		t.Errorf("scans show did not disclose the tree count and the shared dep dirs:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "1 (suite is not concurrency-safe: baseline failed under 3)") {
		t.Errorf("scans show did not disclose the downgrade note:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "not recorded") {
		t.Errorf("an unrecorded concurrency must read as such, never as 1 tree:\n%s", out.String())
	}
}

// The ledger records WHICH TEST killed each mutant when the runner said so.
// A reader that stored it and never showed it would have answered "which
// test was awake" for nobody.
func TestScansShow_NamesTheTestThatKilledEachMutant(t *testing.T) {
	r := &fakeScansReader{
		files: []scanstore.File{{Path: "recipes.py", Disposition: "audited", KillRate: ptrF(0.5), Survivors: 1}},
		mutants: []scanstore.Mutant{
			{Path: "recipes.py", MutantID: "m1", Outcome: "killed", KilledBy: "tests/test_recipes.py::test_scale"},
			// Killed, but the runner's output named nobody (a timeout-kill, or
			// a language corral does not parse). No line, no invented id.
			{Path: "recipes.py", MutantID: "m2", Outcome: "killed"},
			// A survivor has no killer by construction.
			{Path: "recipes.py", MutantID: "m3", Outcome: "survived"},
		},
	}
	var out, errOut bytes.Buffer
	if code := runScansShow([]string{"7"}, openFake(r), &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	got := out.String()
	if !strings.Contains(got, "killed by:") {
		t.Fatalf("no killed-by block in:\n%s", got)
	}
	if !strings.Contains(got, "tests/test_recipes.py::test_scale") {
		t.Errorf("the killer is not named:\n%s", got)
	}
	if strings.Contains(got, "m2") || strings.Contains(got, "m3") {
		t.Errorf("a mutant with no recorded killer was listed anyway:\n%s", got)
	}
}

// ...and stays silent when nothing was recorded: a header with no rows under
// it announces a measurement that was never made.
func TestScansShow_SaysNothingAboutKillersWhenNoneWereRecorded(t *testing.T) {
	r := &fakeScansReader{
		files:   []scanstore.File{{Path: "recipes.py", Disposition: "audited", KillRate: ptrF(0.5)}},
		mutants: []scanstore.Mutant{{Path: "recipes.py", MutantID: "m1", Outcome: "killed"}},
	}
	var out, errOut bytes.Buffer
	if code := runScansShow([]string{"7"}, openFake(r), &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if strings.Contains(out.String(), "killed by:") {
		t.Errorf("a killed-by block was printed with nothing to put in it:\n%s", out.String())
	}
}

// TestScansShow_PrintsTheRekorReceiptWhenPresent: a scan --transparency
// uploaded must show its receipt without needing --json or --timing — the
// same discoverability every other scan-grain fact in this command gets.
func TestScansShow_PrintsTheRekorReceiptWhenPresent(t *testing.T) {
	idx := int64(55)
	r := &fakeScansReader{
		files: []scanstore.File{{Path: "a.py", Disposition: "audited", KillRate: ptrF(0.5)}},
		scan:  scanstore.ScanRow{ID: 7, Scan: scanstore.Scan{RekorLogIndex: &idx, RekorUUID: "uuid-xyz"}},
	}
	var out, errOut bytes.Buffer
	if code := runScansShow([]string{"7"}, openFake(r), &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "rekor: index 55") {
		t.Errorf("scans show did not print the rekor receipt:\n%s", out.String())
	}
}

// ...and stays silent when the scan was never uploaded — an em dash here
// would announce a receipt that does not exist.
func TestScansShow_SaysNothingAboutRekorWhenAbsent(t *testing.T) {
	r := &fakeScansReader{
		files: []scanstore.File{{Path: "a.py", Disposition: "audited", KillRate: ptrF(0.5)}},
		scan:  scanstore.ScanRow{ID: 7},
	}
	var out, errOut bytes.Buffer
	if code := runScansShow([]string{"7"}, openFake(r), &out, &errOut); code != 0 {
		t.Fatalf("exit %d: %s", code, errOut.String())
	}
	if strings.Contains(out.String(), "rekor:") {
		t.Errorf("a rekor line was printed for a scan with no receipt:\n%s", out.String())
	}
}
