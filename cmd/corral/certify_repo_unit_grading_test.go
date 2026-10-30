// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"io"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/testmap"
)

// The operator's command every test here grades under: vitest, whose unit
// is a test file and whose multi-unit command names each one.
var unitGradingArgv = []string{"vitest", "run"}

// unitGradingFixture is a TypeScript tree with three sources and five test
// units, none of whose names is a substring of another's (vitest runs every
// file a filter is a substring of, so such names would corun).
func unitGradingFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	for _, src := range []string{"src/a.ts", "src/b.ts", "src/c.ts"} {
		mustWrite(t, filepath.Join(root, src), "export const x = 1;\n")
	}
	for _, u := range []string{"one", "two", "three", "four", "five"} {
		mustWrite(t, filepath.Join(root, "tests", u+".test.ts"), "test('"+u+"', () => {});\n")
	}
	return root
}

// unitGradedExecutor runs the scan's unit pass over root — the real
// collectUnits, with the pass itself (collectUnitEvidence) answering ev —
// and returns the executor the scan grades files with.
func unitGradedExecutor(t *testing.T, root string, ev reposcan.UnitEvidence) *localExecutor {
	t.Helper()
	(&fakeUnitPass{ev: ev}).install(t)
	ex := newLocalExecutor(root, unitGradingArgv, substrateWorkspace, 0, nil)
	ex.unitWorkers = 1
	cands, excl, err := reposcan.Enumerate(root)
	if err != nil {
		t.Fatal(err)
	}
	ex.unitInventory = checkoutInventory(cands, excl)
	ex.units = ex.collectUnits(context.Background(), enumeratedSourcePaths(cands, excl))
	return ex
}

func tsJob(path, testPath string) reposcan.Job {
	return reposcan.Job{Path: path, TestPath: testPath, Lang: "typescript"}
}

func vitestUnitsCmd(t *testing.T, units ...string) []string {
	t.Helper()
	ts, _ := lang.ByName("typescript")
	r, err := lang.RunnerByID(ts, "vitest")
	if err != nil {
		t.Fatal(err)
	}
	cmd, ok := r.UnitsCmd(unitGradingArgv, units)
	if !ok {
		t.Fatalf("fixture: vitest refused %v", units)
	}
	return cmd
}

// THE CHANGE. A TypeScript file covered by two of five units is graded by
// those two: the dev command (the executor's baseline and the dev pass
// alike, one function) runs exactly them, the selection says it was made of
// five, and the scan's method — what every verdict's cache key carries —
// says unit-evidence. Before this, every non-Python file graded against the
// whole suite whatever its evidence said.
func TestUnitGradedFileRunsOnlyItsUnits(t *testing.T) {
	root := unitGradingFixture(t)
	ex := unitGradedExecutor(t, root, reposcan.UnitEvidence{Ran: true, Units: []reposcan.UnitResult{
		unitRun("tests/one.test.ts", map[string]lang.LineCounts{"src/a.ts": {Executed: 3, Executable: 4}}),
		unitRun("tests/two.test.ts", map[string]lang.LineCounts{"src/a.ts": {Executed: 1, Executable: 4}}),
		// Measured src/a.ts and executed none of it: not a covering unit.
		unitRun("tests/three.test.ts", map[string]lang.LineCounts{"src/a.ts": {Executed: 0, Executable: 4}, "src/b.ts": {Executed: 2, Executable: 2}}),
		unitRun("tests/four.test.ts", map[string]lang.LineCounts{"src/b.ts": {Executed: 2, Executable: 2}}),
		unitRun("tests/five.test.ts", map[string]lang.LineCounts{"src/b.ts": {Executed: 1, Executable: 2}}),
	}})
	job := tsJob("src/a.ts", "tests/one.test.ts")

	sel := ex.selectionFor(job)
	units := []string{"tests/one.test.ts", "tests/two.test.ts"}
	if sel.Method != testmap.MethodUnitEvidence || sel.Of != 5 || !reflect.DeepEqual(sel.Tests, units) || sel.Fallback != "" {
		t.Fatalf("selection = %+v, want the two units that executed src/a.ts, of 5, by unit-evidence", sel)
	}
	if got, want := ex.testCmd(job, sel), vitestUnitsCmd(t, units...); !reflect.DeepEqual(got, want) {
		t.Errorf("dev command = %v, want %v", got, want)
	}
	if got := ex.selectionMethod(); got != testmap.MethodUnitEvidence {
		t.Errorf("selectionMethod = %q, want %q", got, testmap.MethodUnitEvidence)
	}
	in := ex.auditInputFor(job)
	if !reflect.DeepEqual(in.selectionMap, ex.unitMap) || in.selectionMap.Method != testmap.MethodUnitEvidence {
		t.Errorf("the audit input carries map %+v, want the unit pass's", in.selectionMap)
	}
	rs := newAuditRunSpec(in, auditRoles{}, runSubject{lang: "typescript", codePath: job.Path, devTestPath: job.TestPath})
	if got, want := advpool.DevCommand(rs), adequacy.ShellJoin(vitestUnitsCmd(t, units...)); got != want {
		t.Errorf("the pool's dev command = %q, want %q", got, want)
	}
}

// Review Focus 1. A unit whose one-unit run could not run contributed no
// evidence — and may be the unit that executes any file. It is in EVERY
// file's selection, and the count reaches the run spec the verdict is made
// from.
func TestUnreportedUnitsAreAlwaysSelected(t *testing.T) {
	root := unitGradingFixture(t)
	ex := unitGradedExecutor(t, root, reposcan.UnitEvidence{Ran: true, Units: []reposcan.UnitResult{
		unitRun("tests/one.test.ts", map[string]lang.LineCounts{"src/a.ts": {Executed: 3, Executable: 4}}),
		unitRun("tests/two.test.ts", map[string]lang.LineCounts{"src/b.ts": {Executed: 1, Executable: 4}}),
		{Unit: "tests/five.test.ts", Err: "could not run: exit status 127"},
	}})
	for path, want := range map[string][]string{
		"src/a.ts": {"tests/five.test.ts", "tests/one.test.ts"},
		"src/b.ts": {"tests/five.test.ts", "tests/two.test.ts"},
	} {
		job := tsJob(path, "tests/one.test.ts")
		sel := ex.selectionFor(job)
		if !reflect.DeepEqual(sel.Tests, want) || sel.Fallback != "" {
			t.Errorf("%s: selection = %+v, want %v", path, sel, want)
			continue
		}
		if got := ex.testCmd(job, sel); !reflect.DeepEqual(got, vitestUnitsCmd(t, want...)) {
			t.Errorf("%s: dev command = %v, want the unreported unit run too", path, got)
		}
		in := ex.auditInputFor(job)
		if n := testmap.UnreportedIn(in.selectionMap, in.selection); n != 1 {
			t.Errorf("%s: %d unreported unit(s) reach the run spec, want 1", path, n)
		}
	}
}

// Review Focus 5. A pass that reported no unit at all — every unit timed
// out, or the pass never ran — has no evidence to select by. The file
// grades whole-suite with the pass's own note, never with a command of every
// listed unit dressed as a selection, and the scan's method is not
// unit-evidence.
func TestNoReportedUnitGradesWholeSuite(t *testing.T) {
	root := unitGradingFixture(t)
	job := tsJob("src/a.ts", "tests/one.test.ts")

	ex := unitGradedExecutor(t, root, reposcan.UnitEvidence{Ran: true, Note: "the unit pass stopped at its time limit", Units: []reposcan.UnitResult{
		{Unit: "tests/one.test.ts", Err: "not run (time limit)"},
		{Unit: "tests/two.test.ts", Err: "not run (time limit)"},
	}})
	sel := ex.selectionFor(job)
	if sel.Method != "" || sel.Cmd != nil || !strings.Contains(sel.Fallback, "the unit pass stopped at its time limit") {
		t.Errorf("timed-out pass: selection = %+v, want whole-suite with the pass's note", sel)
	}
	if got := ex.testCmd(job, sel); !reflect.DeepEqual(got, unitGradingArgv) {
		t.Errorf("timed-out pass: dev command = %v, want the operator's whole command", got)
	}
	if got := ex.selectionMethod(); got != "" {
		t.Errorf("timed-out pass: selectionMethod = %q, want \"\"", got)
	}

	never := unitGradedExecutor(t, root, reposcan.UnitEvidence{Note: "typescript: no unit listing"})
	if sel := never.selectionFor(job); sel.Method != "" || sel.Fallback != "typescript: no unit listing" {
		t.Errorf("pass never ran: selection = %+v, want whole-suite with the pass's note", sel)
	}
	if got := never.selectionMethod(); got != "" {
		t.Errorf("pass never ran: selectionMethod = %q, want \"\"", got)
	}
}

// Never run a set nobody chose. vitest runs every file a filter is a
// substring of, so a selection naming tests/a.test.ts would also run
// tests/xa.test.ts: the file grades whole-suite, and the reason names it.
func TestCorunningSelectionGradesWholeSuite(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "src", "a.ts"), "export const a = 1;\n")
	mustWrite(t, filepath.Join(root, "tests", "a.test.ts"), "test('a', () => {});\n")
	mustWrite(t, filepath.Join(root, "tests", "xa.test.ts"), "test('xa', () => {});\n")
	ex := unitGradedExecutor(t, root, reposcan.UnitEvidence{Ran: true, Units: []reposcan.UnitResult{
		unitRun("tests/a.test.ts", map[string]lang.LineCounts{"src/a.ts": {Executed: 1, Executable: 1}}),
		unitRun("tests/xa.test.ts", map[string]lang.LineCounts{}),
	}})
	job := tsJob("src/a.ts", "tests/a.test.ts")
	sel := ex.selectionFor(job)
	if sel.Method != "" || !strings.Contains(sel.Fallback, "tests/xa.test.ts") {
		t.Fatalf("selection = %+v, want whole-suite naming the unit it would also run", sel)
	}
	if got := ex.testCmd(job, sel); !reflect.DeepEqual(got, unitGradingArgv) {
		t.Errorf("dev command = %v, want the whole command", got)
	}
}

// Review Focus 2. The authored test lands in a unit the evidence never
// listed (for vitest, a new file is a new unit): the authored pass runs the
// selection AND that unit, all the way down the real path to the scorer.
func TestAuthoredPassAddsTheAuthoredUnit(t *testing.T) {
	root := unitGradingFixture(t)
	ex := unitGradedExecutor(t, root, reposcan.UnitEvidence{Ran: true, Units: []reposcan.UnitResult{
		unitRun("tests/one.test.ts", map[string]lang.LineCounts{"src/a.ts": {Executed: 3, Executable: 4}}),
		unitRun("tests/two.test.ts", map[string]lang.LineCounts{"src/a.ts": {Executed: 1, Executable: 4}}),
		unitRun("tests/three.test.ts", map[string]lang.LineCounts{"src/b.ts": {Executed: 1, Executable: 4}}),
	}})
	job := tsJob("src/a.ts", "tests/one.test.ts")
	in := ex.auditInputFor(job)
	ts, _ := lang.ByName("typescript")
	prep, err := prepareAuditJail(context.Background(), in, ts, time.Minute, io.Discard)
	if err != nil {
		t.Fatalf("prepareAuditJail: %v", err)
	}
	defer prep.cleanup()
	scorer := prep.wiring.scorer
	if !reflect.DeepEqual(scorer.SelectionMap, ex.unitMap) {
		t.Fatalf("the scorer carries map %+v, want the unit pass's", scorer.SelectionMap)
	}

	got := scorer.AuthoredCommand(job.Path, unitGradingArgv)
	selected := []string{"tests/one.test.ts", "tests/two.test.ts"}
	var authored []string
	for _, a := range got[len(unitGradingArgv):] {
		if !slices.Contains(selected, a) {
			authored = append(authored, a)
		}
	}
	if len(authored) != 1 || !strings.HasSuffix(authored[0], ".test.ts") || slices.Contains([]string{"tests/three.test.ts", "tests/four.test.ts", "tests/five.test.ts"}, authored[0]) {
		t.Fatalf("authored command = %v, want the selection plus exactly one new unit, the authored test's", got)
	}
	units := append(slices.Clone(selected), authored[0])
	slices.Sort(units)
	if want := vitestUnitsCmd(t, units...); !reflect.DeepEqual(got, want) {
		t.Errorf("authored command = %v, want %v", got, want)
	}
	// The verdict judges the file the scorer writes (fix round 1).
	rs := newAuditRunSpec(in, auditRoles{}, runSubject{lang: "typescript", codePath: job.Path, devTestPath: job.TestPath,
		authoredTestPath: scorer.AuthoredTestPath(job.Path)})
	if rs.AuthoredTestPath != authored[0] || !advpool.AuthoredAlone(rs) {
		t.Errorf("RunSpec.AuthoredTestPath = %q (AuthoredAlone %v), want the scorer's %q", rs.AuthoredTestPath, advpool.AuthoredAlone(rs), authored[0])
	}
}

// A file the evidence measured and no reporting unit executed is UNCOVERED,
// its rate withheld — unless a unit reported nothing, in which case that
// unit may be what executes it: it is selected, and the file is not called
// uncovered.
func TestUncoveredUnitFileIsUncovered(t *testing.T) {
	root := unitGradingFixture(t)
	job := tsJob("src/c.ts", "tests/one.test.ts")
	covered := []reposcan.UnitResult{
		unitRun("tests/one.test.ts", map[string]lang.LineCounts{"src/a.ts": {Executed: 3, Executable: 4}, "src/c.ts": {Executed: 0, Executable: 3}}),
		unitRun("tests/two.test.ts", map[string]lang.LineCounts{"src/b.ts": {Executed: 1, Executable: 4}}),
	}

	ex := unitGradedExecutor(t, root, reposcan.UnitEvidence{Ran: true, Units: covered})
	sel := ex.selectionFor(job)
	if sel.Method != testmap.MethodUnitEvidence || len(sel.Tests) != 0 || sel.Fallback != "" {
		t.Fatalf("all reported: selection = %+v, want uncovered (method set, nothing selected)", sel)
	}
	if got, want := ex.testCmd(job, sel), vitestUnitsCmd(t, "tests/one.test.ts"); !reflect.DeepEqual(got, want) {
		t.Errorf("uncovered dev command = %v, want the paired test's unit alone %v", got, want)
	}

	withFailed := append(slices.Clone(covered), reposcan.UnitResult{Unit: "tests/five.test.ts", Err: "coverage unavailable: no instrument"})
	ex = unitGradedExecutor(t, root, reposcan.UnitEvidence{Ran: true, Units: withFailed})
	sel = ex.selectionFor(job)
	if !reflect.DeepEqual(sel.Tests, []string{"tests/five.test.ts"}) || sel.Fallback != "" {
		t.Errorf("one unit unreported: selection = %+v, want that unit, not uncovered", sel)
	}
}

// The scan's audit config — and so every verdict's cache key — names how
// the scan GRADES. A unit-graded scan grades differently from a whole-suite
// one, so its key differs; under --whole-suite the unit pass still runs (it
// decides candidacy) but grades nothing, and the key is the whole suite's.
func TestUnitEvidenceNamesItsMethodInTheAuditConfigKey(t *testing.T) {
	root := unitGradingFixture(t)
	ev := reposcan.UnitEvidence{Ran: true, Units: []reposcan.UnitResult{
		unitRun("tests/one.test.ts", map[string]lang.LineCounts{"src/a.ts": {Executed: 1, Executable: 1}}),
	}}
	with := unitGradedExecutor(t, root, ev)
	without := &localExecutor{}
	if got := with.selectionMethod(); got != testmap.MethodUnitEvidence {
		t.Fatalf("selectionMethod with unit evidence = %q, want %q", got, testmap.MethodUnitEvidence)
	}
	a := auditConfigKey(false, without.selectionMethod(), unitGradingArgv, "", "")
	b := auditConfigKey(false, with.selectionMethod(), unitGradingArgv, "", "")
	if a == b || !strings.Contains(b, testmap.MethodUnitEvidence) {
		t.Errorf("auditConfigKey must name unit-evidence:\n without %s\n    with %s", a, b)
	}

	whole := unitGradedExecutor(t, root, ev)
	whole.wholeSuite = true
	if got := whole.selectionMethod(); got != "" {
		t.Errorf("--whole-suite: selectionMethod = %q, want \"\"", got)
	}
	if sel := whole.selectionFor(tsJob("src/a.ts", "tests/one.test.ts")); sel.Method != "" || sel.Fallback != "--whole-suite" {
		t.Errorf("--whole-suite: selection = %+v", sel)
	}
	sel := &localExecutor{selection: reposcan.SelectionEvidence{Ran: true}}
	if got := sel.selectionMethod(); got != "coverage-context" {
		t.Errorf("negative control: selection evidence must still set the method, got %q", got)
	}
}

// The scan line says how the scan grades, once the unit pass has decided it:
// by units, counting the ones added for want of evidence — never the
// selection pass's "running the suite once with per-test coverage" (it runs
// nothing for a unit language) nor its "WHOLE suite — no selector" (which
// is now false). A pass that reported no unit says whole suite, and why.
func TestUnitGradedScanSaysHowItGrades(t *testing.T) {
	root := unitEvidenceTSFixture(t)
	mustWrite(t, filepath.Join(root, "tests", "broken.test.ts"), "test('broken', () => {});\n")
	fake := &fakeUnitPass{ev: reposcan.UnitEvidence{Ran: true, Units: []reposcan.UnitResult{
		unitRun("tests/composition/service.test.ts", map[string]lang.LineCounts{"src/app/Service.ts": {Executed: 1, Executable: 1}}),
		{Unit: "tests/broken.test.ts", Err: "could not run: exit status 1"},
	}}}
	fake.install(t)
	s := runUnitEvidenceScan(t, root, "--dry-run", "--evidence", "--", "vitest", "run")
	const want = "selection: grading each typescript file by the test units its evidence says execute it (1 of 2 unit(s) reported; the 1 that did not are added to every file's selection)"
	if !strings.Contains(s, want) {
		t.Errorf("missing %q:\n%s", want, s)
	}
	for _, stale := range []string{"per-test coverage instrumentation", "WHOLE suite"} {
		if strings.Contains(s, stale) {
			t.Errorf("a unit-graded scan must not say %q:\n%s", stale, s)
		}
	}

	none := &fakeUnitPass{ev: reposcan.UnitEvidence{Ran: true, Note: "the unit pass stopped at its time limit", Units: []reposcan.UnitResult{
		{Unit: "tests/composition/service.test.ts", Err: "not run (time limit)"},
	}}}
	none.install(t)
	s = runUnitEvidenceScan(t, root, "--dry-run", "--evidence", "--", "vitest", "run")
	if !strings.Contains(s, "selection: grading by the WHOLE suite — no unit of 1 reported evidence: the unit pass stopped at its time limit") {
		t.Errorf("a pass with no reported unit must say the scan grades whole-suite, and why:\n%s", s)
	}
}

// The selection line never claims unit grading the files will not get: a
// unit map naming no runner row grades every file whole-suite (each says
// so), and the scan line says so too; and a pass that never ran with no
// note of its own still names a reason rather than ending in a dash.
func TestUnitGradingLineNeverClaimsWhatFilesDoNotGet(t *testing.T) {
	root := unitGradingFixture(t)
	ex := unitGradedExecutor(t, root, reposcan.UnitEvidence{Ran: true, Units: []reposcan.UnitResult{
		unitRun("tests/one.test.ts", map[string]lang.LineCounts{"src/a.ts": {Executed: 1, Executable: 1}}),
	}})
	ex.unitMap.Runner = ""
	var b strings.Builder
	printUnitGrading(&b, ex, "typescript")
	if !strings.Contains(b.String(), "selection: grading by the WHOLE suite — ") || !strings.Contains(b.String(), "names no runner row") {
		t.Errorf("no runner row: line = %q, want whole suite naming the missing row", b.String())
	}
	if sel := ex.selectionFor(tsJob("src/a.ts", "tests/one.test.ts")); sel.Method != "" || !strings.Contains(sel.Fallback, "names no runner row") {
		t.Errorf("no runner row: the file's own selection = %+v", sel)
	}
	if got := ex.selectionMethod(); got != "" {
		t.Errorf("no runner row: selectionMethod = %q, want \"\" — no file is graded by units", got)
	}

	never := &localExecutor{}
	b.Reset()
	printUnitGrading(&b, never, "typescript")
	if got := b.String(); strings.HasSuffix(strings.TrimSpace(got), "—") || !strings.Contains(got, "no unit evidence was collected for typescript") {
		t.Errorf("pass with no note: line = %q, want the reason selectionFor gives", got)
	}
}

// argvBaseline is a baseline that is red exactly when its command names one
// of the red units, and records every command it was built with.
type argvBaseline struct{ green bool }

func (b *argvBaseline) RunBaseline() (bool, error) { return b.green, nil }

// redUnitExecutor is a unit-graded executor over the fixture where src/a.ts
// is executed by tests/one, tests/four's own run FAILED and tests/five was
// cut by the time limit — so both are in src/a.ts's selection for caution —
// and whose baseline is red whenever its command runs any of red.
func redUnitExecutor(t *testing.T, red []string, cmds *[][]string, audited *localAuditInput, out io.Writer) *localExecutor {
	t.Helper()
	root := unitGradingFixture(t)
	ex := unitGradedExecutor(t, root, reposcan.UnitEvidence{Ran: true, Units: []reposcan.UnitResult{
		unitRun("tests/one.test.ts", map[string]lang.LineCounts{"src/a.ts": {Executed: 3, Executable: 4}}),
		unitRun("tests/two.test.ts", map[string]lang.LineCounts{"src/b.ts": {Executed: 1, Executable: 2}}),
		unitRun("tests/three.test.ts", map[string]lang.LineCounts{"src/c.ts": {Executed: 1, Executable: 2}}),
		{Unit: "tests/four.test.ts", Err: "run failed: exit 1 (needs dist/)"},
		{Unit: "tests/five.test.ts", Err: "not run (time limit)"},
	}})
	ex.progress = out
	ex.newBaseline = func(_ context.Context, in localAuditInput) (reposcan.BaselineRunner, func(), error) {
		*cmds = append(*cmds, slices.Clone(in.checkArgv))
		green := true
		for _, u := range red {
			if slices.Contains(in.checkArgv, u) {
				green = false
			}
		}
		return &argvBaseline{green: green}, func() {}, nil
	}
	ex.audit = func(_ context.Context, in localAuditInput) (advpool.Verdict, error) {
		*audited = in
		return advpool.Verdict{DevScored: true, DevKillRate: 1, TestSelection: advpool.TestSelection{Excluded: len(in.selection.Excluded)}}, nil
	}
	return ex
}

// THE RETRY. A file's baseline is red because a unit whose own run already
// failed unmutated was added to its selection for caution. The file is
// retried once without that unit — only that one: the unit the time limit
// cut proved nothing and stays — and, the retry's baseline green, graded by
// it. The audit gets the retry's selection (so every later command is built
// from it) with the left-out unit named, and the run says so.
func TestARedBaselineIsRetriedWithoutUnitsThatFailedUnmutated(t *testing.T) {
	var cmds [][]string
	var in localAuditInput
	var out strings.Builder
	ex := redUnitExecutor(t, []string{"tests/four.test.ts"}, &cmds, &in, &out)

	res, err := ex.Execute(context.Background(), tsJob("src/a.ts", "tests/one.test.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Gradable {
		t.Fatalf("the retry's baseline was green, yet the file was not graded: %+v\n%s", res, out.String())
	}
	retry := vitestUnitsCmd(t, "tests/five.test.ts", "tests/one.test.ts")
	if len(cmds) != 2 || !slices.Contains(cmds[0], "tests/four.test.ts") || !reflect.DeepEqual(cmds[1], retry) {
		t.Fatalf("baseline commands = %v, want the evidence's selection then %v", cmds, retry)
	}
	if !reflect.DeepEqual(in.checkArgv, retry) || !reflect.DeepEqual(in.selection.Excluded, []string{"tests/four.test.ts"}) ||
		slices.Contains(in.selection.Tests, "tests/four.test.ts") {
		t.Errorf("audit input: checkArgv=%v selection=%+v, want the retry's command and selection naming the left-out unit", in.checkArgv, in.selection)
	}
	for _, want := range []string{"src/a.ts: baseline failed with 1 unit that failed unmutated in the evidence pass", "tests/four.test.ts", "retried without it: green"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("run output lacks %q:\n%s", want, out.String())
		}
	}
}

// A retry whose baseline is still red is a red baseline: not graded, and the
// run says the retry was tried, so the operator is not left thinking the red
// units were the cause.
func TestARetryThatIsStillRedIsNotGraded(t *testing.T) {
	var cmds [][]string
	var in localAuditInput
	var out strings.Builder
	ex := redUnitExecutor(t, []string{"tests/four.test.ts", "tests/one.test.ts"}, &cmds, &in, &out)

	res, err := ex.Execute(context.Background(), tsJob("src/a.ts", "tests/one.test.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Gradable || res.Reason != reposcan.ReasonBaselineFailed || in.codePath != "" {
		t.Errorf("result %+v (audited %q), want baseline-failed and no audit", res, in.codePath)
	}
	if len(cmds) != 2 || !strings.Contains(out.String(), "retried without it: still red") {
		t.Errorf("commands %v; output:\n%s", cmds, out.String())
	}
}

// Negative control: a red baseline with no unit that failed unmutated in its
// selection has nothing to retry without, and is not retried.
func TestARedBaselineWithNoRedUnitIsNotRetried(t *testing.T) {
	var cmds [][]string
	var in localAuditInput
	var out strings.Builder
	ex := redUnitExecutor(t, []string{"tests/two.test.ts"}, &cmds, &in, &out)

	res, err := ex.Execute(context.Background(), tsJob("src/b.ts", "tests/two.test.ts"))
	if err != nil {
		t.Fatal(err)
	}
	// src/b.ts's selection is tests/two plus the two unreported units; only
	// tests/four is red, and the baseline is red for tests/two's sake, so the
	// retry (without four) is still red.
	if res.Gradable || len(cmds) != 2 {
		t.Fatalf("result %+v after %d baseline(s)", res, len(cmds))
	}
	cmds = nil
	ex2 := redUnitExecutor(t, []string{"tests/two.test.ts"}, &cmds, &in, &out)
	ex2.unitMap.Tests["tests/four.test.ts"] = testmap.Node{ID: "tests/four.test.ts", Grain: testmap.GrainUnit, Err: "could not run: bwrap failed"}
	res, err = ex2.Execute(context.Background(), tsJob("src/b.ts", "tests/two.test.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Gradable || len(cmds) != 1 || strings.Count(out.String(), "retried without") > 1 {
		t.Errorf("no red unit: %d baseline(s), result %+v; output:\n%s", len(cmds), res, out.String())
	}
}

// A red baseline that is not retried says why, rather than leaving the
// operator to wonder whether the retry ran: REA's sixth run (2026-10-30) was
// red with units that had failed unmutated in its evidence pass, and printed
// nothing about a retry either way.
func TestARedBaselineThatIsNotRetriedSaysWhy(t *testing.T) {
	var cmds [][]string
	var in localAuditInput
	var out strings.Builder
	ex := redUnitExecutor(t, []string{"tests/two.test.ts"}, &cmds, &in, &out)
	ex.unitMap.Tests["tests/four.test.ts"] = testmap.Node{ID: "tests/four.test.ts", Grain: testmap.GrainUnit, Err: "could not run: bwrap failed"}
	if _, err := ex.Execute(context.Background(), tsJob("src/b.ts", "tests/two.test.ts")); err != nil {
		t.Fatal(err)
	}
	if want := "src/b.ts: baseline failed; not retried: no unit in its selection failed unmutated in the evidence pass"; !strings.Contains(out.String(), want) {
		t.Errorf("output lacks %q:\n%s", want, out.String())
	}

	_, red, why := ex.redUnitRetry(tsJob("src/b.ts", "tests/two.test.ts"), lang.Selection{Fallback: "the evidence never saw it"})
	if red != nil || why != "it was graded by the whole suite (the evidence never saw it), not by a selection to retry without" {
		t.Errorf("whole-suite file: red %v, why %q", red, why)
	}
}
