// SPDX-License-Identifier: Elastic-2.0

package testmap

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

// These are Python's selection tests, moved here from internal/lang when
// pyPlugin's Select, ForSpan, WithAuthoredTest and Index were deleted (Task 6):
// the engine is what selects now. Their inputs and expected values are
// unchanged; only the call that produces the answer is, through the helpers
// below, which build the map from the evidence the way a caller does.

func pySelect(t *testing.T, evidence []byte, repoRoot, codePath, testPath string, testCmd []string) (lang.Selection, error) {
	t.Helper()
	return newPyEquiv(t).engineSelect(selectCase{evidence: evidence, root: repoRoot, code: codePath, test: testPath, cmd: testCmd})
}

func pyForSpan(t *testing.T, sel lang.Selection, span lang.LineRange) ([]string, []string, string) {
	t.Helper()
	return ForSpan(sel, newPyEquiv(t).row, span)
}

// pyWithAuthoredTest is the engine's authored command for a coverage-context
// selection; Python's selector always built one, so a refusal fails the test.
func pyWithAuthoredTest(t *testing.T, sel lang.Selection, testCmd []string, path string) []string {
	t.Helper()
	got, ok := WithAuthoredTest(Map{Method: MethodCoverageContext}, sel, newPyEquiv(t).row, testCmd, "", path)
	if !ok {
		t.Fatalf("WithAuthoredTest refused %v + %s", testCmd, path)
	}
	return got
}

// pyIndex is what pyPlugin.Index answered: the map's per-file readout, with
// paths aligned as Index aligned them (no repo root).
func pyIndex(t *testing.T, evidence []byte) (map[string]lang.FileCoverage, error) {
	t.Helper()
	m, err := newPyEquiv(t).pyMap(evidence, "")
	if err != nil {
		return nil, err
	}
	return m.Coverage(), nil
}

func pyEvidencePlugin(t *testing.T) lang.TestEvidence {
	t.Helper()
	return newPyEquiv(t).ev
}

func TestPythonSelectPicksOnlyTestsThatExecutedTheFile(t *testing.T) {
	base := []string{"python3", "-m", "pytest", "-q"}
	sel, err := pySelect(t, recordedPyEvidence(t), "", "pkg/calc.py", "tests/test_calc.py", base)
	if err != nil {
		t.Fatal(err)
	}
	wantTests := []string{"tests/test_calc.py::test_add", "tests/test_other.py::test_sub"}
	if !reflect.DeepEqual(sel.Tests, wantTests) {
		t.Errorf("Tests = %v, want %v (test_trivial never ran calc.py; |run suffix stripped; sorted)", sel.Tests, wantTests)
	}
	wantCmd := append(append([]string{}, base...), wantTests...)
	if !reflect.DeepEqual(sel.Cmd, wantCmd) {
		t.Errorf("Cmd = %v, want operator command + node ids", sel.Cmd)
	}
	if sel.Method != "coverage-context" {
		t.Errorf("Method = %q", sel.Method)
	}
	if sel.Of != 3 {
		t.Errorf("Of = %d, want 3 distinct tests seen by the run", sel.Of)
	}
}

// pkg/untested.py is ABSENT from the recording (never imported). Even
// though its paired test file DID run (sawTest), absence is NEVER
// "uncovered": a file present in the evidence with zero covering tests is
// the only uncovered finding (see TestPythonSelectPresentWithZeroTestsIsUncovered)
// — an absent one can be missing from the report for reasons that have
// nothing to do with whether the suite executed it (an editable/src-layout
// install measures it OUTSIDE the repo root and the reducer drops it; see
// python.go's Select doc). Error → the caller grades whole-suite, disclosed.
func TestPythonSelectAbsentFileWithATestThatRanIsAnErrorNotUncovered(t *testing.T) {
	_, err := pySelect(t, recordedPyEvidence(t), "", "pkg/untested.py", "tests/test_calc.py", []string{"pytest"})
	if err == nil {
		t.Fatal("an absent file must fall back disclosed, not read as uncovered")
	}
}

// pkg/__init__.py IS present in the recording, with zero covering tests —
// this, and only this, is what "uncovered" measures.
func TestPythonSelectPresentWithZeroTestsIsUncovered(t *testing.T) {
	sel, err := pySelect(t, recordedPyEvidence(t), "", "pkg/__init__.py", "tests/test_calc.py", []string{"pytest"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Tests) != 0 || sel.Cmd != nil {
		t.Errorf("want empty selection, got %+v", sel)
	}
	if sel.Method != "coverage-context" {
		t.Errorf("an empty selection is still an evidence-based finding; Method = %q", sel.Method)
	}
}

// Absent file AND absent paired test: the suite may simply never have run
// that test (a -k filter, a collection error), so "uncovered" would be a
// false accusation. Error → the caller grades whole-suite, disclosed.
func TestPythonSelectAbsentTestFileIsAnError(t *testing.T) {
	if _, err := pySelect(t, recordedPyEvidence(t), "", "pkg/not_in_report.py", "tests/test_never_ran.py", []string{"pytest"}); err == nil {
		t.Error("absent file with an absent paired test must be an error, not uncovered")
	}
	if _, err := pySelect(t, recordedPyEvidence(t), "", "pkg/not_in_report.py", "", []string{"pytest"}); err == nil {
		t.Error("absent file with NO paired test path must be an error")
	}
}

// The reducer stamps its shape; anything else — including the full
// coverage-json this used to parse — is refused rather than misread.
func TestPythonSelectRejectsAnUnknownEvidenceFormat(t *testing.T) {
	old := `{"meta":{"show_contexts":true},"totals":{"covered_lines":1},"files":{"pkg/calc.py":{"summary":{"num_statements":1,"covered_lines":1},"contexts":{"1":["tests/test_calc.py::test_add|run"]}}}}`
	if _, err := pySelect(t, []byte(old), "", "pkg/calc.py", "tests/test_calc.py", []string{"pytest"}); err == nil || !strings.Contains(err.Error(), "corral-selection-3") {
		t.Errorf("the old coverage-json shape must be refused by name, got err=%v", err)
	}
}

func TestPythonSelectRejectsNonCoverageJSON(t *testing.T) {
	if _, err := pySelect(t, []byte(`{"hello":1}`), "", "pkg/calc.py", "tests/test_calc.py", []string{"pytest"}); err == nil {
		t.Error("want an error on a document that is not a coverage report")
	}
}

func TestPythonSelectAlignsAbsoluteReportPaths(t *testing.T) {
	// Some coverage configs emit absolute paths; align them to the repo
	// root the same way ParseCoverage does.
	ev := strings.ReplaceAll(string(recordedPyEvidence(t)), `"pkg/calc.py"`, `"/repo/pkg/calc.py"`)
	sel, err := pySelect(t, []byte(ev), "/repo", "pkg/calc.py", "tests/test_calc.py", []string{"pytest"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Tests) != 2 {
		t.Errorf("Tests = %v, want the same 2 after alignment", sel.Tests)
	}
}

func TestPythonSelectFallsBackToFilesWhenArgvWouldBeTooLong(t *testing.T) {
	// Build evidence where one file is executed by 3000 GENUINELY DISTINCT
	// tests in one module — each node id carries its own index so the
	// deduplicated set really is 3000 entries, not a handful cycling
	// through a short suffix, and the sorted argv genuinely exceeds
	// selectionMaxArgv.
	var ctxs []string
	for i := 0; i < 3000; i++ {
		ctxs = append(ctxs, fmt.Sprintf(`"tests/test_big.py::test_%04d_%s"`, i, strings.Repeat("x", 20)))
	}
	ev := `{"format":"corral-selection-3","tests":3000,"files":{` +
		`"pkg/calc.py":{"tests":[` + strings.Join(ctxs, ",") + `],"lines":{"0":[[2,2]],"1":[[6,6]]},"static":[[1,1]]},` +
		`"tests/test_big.py":{"tests":[` + ctxs[0] + `],"lines":{},"static":[]}}}`
	sel, err := pySelect(t, []byte(ev), "", "pkg/calc.py", "tests/test_big.py", []string{"pytest"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sel.Tests, []string{"tests/test_big.py"}) {
		t.Errorf("Tests = %v, want the containing FILE (a superset, still evidence-derived)", sel.Tests)
	}
	// The line evidence is keyed by NODE ID, and the collapse just threw
	// every node id away. Keeping it would leave ForSpan looking up ids that
	// are no longer in Tests, finding nothing, and calling every mutant
	// "unreached" — a positive coverage claim about a file that was not
	// narrowed at all. Evidence in a shape that cannot be narrowed is no
	// line evidence.
	if sel.Lines != nil || sel.Static != nil {
		t.Errorf("a collapsed selection must carry NO line evidence: Lines=%v Static=%v", sel.Lines, sel.Static)
	}
}

func TestPythonWithAuthoredTestAppendsThePath(t *testing.T) {
	base := []string{"python3", "-m", "pytest", "-q"}
	sel := lang.Selection{Cmd: append(append([]string{}, base...), "tests/test_calc.py::test_add"), Tests: []string{"tests/test_calc.py::test_add"}, Method: "coverage-context"}
	got := pyWithAuthoredTest(t, sel, base, "tests/test_corral_calc.py")
	want := []string{"python3", "-m", "pytest", "-q", "tests/test_calc.py::test_add", "tests/test_corral_calc.py"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	// Uncovered: no selected tests, but the authored test MUST still run.
	got = pyWithAuthoredTest(t, lang.Selection{Method: "coverage-context"}, base, "tests/test_corral_x.py")
	want = []string{"python3", "-m", "pytest", "-q", "tests/test_corral_x.py"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("uncovered: got %v, want %v", got, want)
	}
}

// Runs Instrument's real command on the fixture and feeds its stdout to
// Select. This is the guard against the recorded JSON drifting from what
// pytest-cov actually emits. Skips, never fails, when pytest-cov is absent.
func TestPythonSelectionEndToEndOnFixture(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	if out, err := exec.Command("python3", "-c", "import pytest_cov, coverage").CombinedOutput(); err != nil {
		t.Skipf("pytest-cov not importable: %s", out)
	}
	fixture, _ := filepath.Abs(filepath.Join("..", "lang", "testdata", "pycov-fixture"))
	cmd, ok := pyEvidencePlugin(t).Instrument([]string{"python3", "-m", "pytest", "-q"})
	if !ok {
		t.Fatal("Instrument refused the fixture command")
	}
	c := exec.Command(cmd[0], cmd[1:]...)
	c.Dir = fixture
	c.Env = append(os.Environ(), "PYTHONPATH="+fixture, "PYTHONDONTWRITEBYTECODE=1")
	out, err := c.Output()
	if err != nil {
		t.Fatalf("instrumented run: %v\n%s", err, out)
	}
	sel, err := pySelect(t, out, fixture, "pkg/calc.py", "tests/test_calc.py", []string{"python3", "-m", "pytest", "-q"})
	if err != nil {
		t.Fatalf("Select on live evidence: %v\n%s", err, out)
	}
	want := []string{"tests/test_calc.py::test_add", "tests/test_other.py::test_sub"}
	if !reflect.DeepEqual(sel.Tests, want) {
		t.Errorf("live Tests = %v, want %v", sel.Tests, want)
	}
	if len(sel.Lines) == 0 {
		t.Errorf("live evidence must carry per-test line ranges: %+v", sel)
	}
	// And the narrowed command actually runs, collecting exactly those.
	run := exec.Command(sel.Cmd[0], sel.Cmd[1:]...)
	run.Dir = fixture
	run.Env = c.Env
	if out, err := run.CombinedOutput(); err != nil || !strings.Contains(string(out), "2 passed") {
		t.Errorf("narrowed command: err=%v\n%s", err, out)
	}
}

func TestPythonSelectReadsLinesAndStaticFromV2(t *testing.T) {
	sel, err := pySelect(t, recordedPyEvidence(t), "", "pkg/calc.py", "tests/test_calc.py", []string{"pytest"})
	if err != nil {
		t.Fatal(err)
	}
	if sel.Method != "coverage-context" || len(sel.Tests) != 2 || sel.Of != 3 {
		t.Fatalf("per-file facts must be unchanged by v2: %+v", sel)
	}
	// From the recording: test_add executes calc.py's add body (line 2);
	// test_sub its sub body (line 6). def lines 1 and 5 run at import time.
	add := sel.Lines["tests/test_calc.py::test_add"]
	sub := sel.Lines["tests/test_other.py::test_sub"]
	if len(add) == 0 || len(sub) == 0 {
		t.Fatalf("Lines must carry each selected test's ranges: %+v", sel.Lines)
	}
	if !add[0].Overlaps(lang.LineRange{Start: 2, End: 2}) || add[0].Overlaps(lang.LineRange{Start: 6, End: 6}) {
		t.Errorf("test_add should reach add's body (line 2) and not sub's (line 6): %+v", add)
	}
	if len(sel.Static) == 0 {
		t.Errorf("the def lines execute at import and must be reported static: %+v", sel.Static)
	}
}

func TestPythonSelectRefusesV1ByName(t *testing.T) {
	v1 := `{"format":"corral-selection-1","tests":1,"files":{"pkg/calc.py":["tests/test_calc.py::test_add"]}}`
	_, err := pySelect(t, []byte(v1), "", "pkg/calc.py", "tests/test_calc.py", []string{"pytest"})
	if err == nil || !strings.Contains(err.Error(), "corral-selection-3") {
		t.Errorf("v1 must be refused naming the expected format, got %v", err)
	}
}

// C1. pytest UNIONS positional args: `pytest tests/ tests/test_a.py::test_x`
// collects all of tests/, so appending node ids to a command that names a
// collection target narrows NOTHING while the verdict, the ledger and the
// cache key all say "coverage-context". Select therefore strips the
// operator's collection targets and keeps everything else — options, their
// values, and any positional token that is not a path this repo has.
func TestPythonSelectStripsCollectionTargetsFromTheBaseCommand(t *testing.T) {
	fixture, err := filepath.Abs(filepath.Join("..", "lang", "testdata", "pycov-fixture"))
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{"tests/test_calc.py::test_add", "tests/test_other.py::test_sub"}
	for _, tc := range []struct {
		name string
		base []string
		want []string // the expected Base (command with targets stripped)
	}{
		{"a directory target", []string{"pytest", "tests/"}, []string{"pytest"}},
		{
			"marker option keeps its value, the directory goes",
			[]string{"python3", "-m", "pytest", "-q", "-k", "not slow", "tests/"},
			[]string{"python3", "-m", "pytest", "-q", "-k", "not slow"},
		},
		{"a node id target", []string{"pytest", "tests/test_calc.py::test_add"}, []string{"pytest"}},
		{"an option value that is not a path is untouched", []string{"pytest", "--maxfail", "3"}, []string{"pytest", "--maxfail", "3"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sel, err := pySelect(t, recordedPyEvidence(t), fixture, "pkg/calc.py", "tests/test_calc.py", tc.base)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(sel.Base, tc.want) {
				t.Errorf("Base = %v, want %v", sel.Base, tc.want)
			}
			wantCmd := append(append([]string{}, tc.want...), ids...)
			if !reflect.DeepEqual(sel.Cmd, wantCmd) {
				t.Errorf("Cmd = %v, want %v", sel.Cmd, wantCmd)
			}
		})
	}
}

// The uncovered pass inverts the same way: without the strip, the authored
// test's path is appended to a command that still names tests/, so the whole
// suite runs and the "no test executes this file" finding is graded against
// everything. pkg/__init__.py is PRESENT in the recording with zero
// covering tests — a genuinely uncovered file, not merely absent.
func TestPythonWithAuthoredTestUsesTheStrippedBase(t *testing.T) {
	fixture, err := filepath.Abs(filepath.Join("..", "lang", "testdata", "pycov-fixture"))
	if err != nil {
		t.Fatal(err)
	}
	sel, err := pySelect(t, recordedPyEvidence(t), fixture, "pkg/__init__.py", "tests/test_calc.py", []string{"pytest", "tests/"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Tests) != 0 {
		t.Fatalf("fixture setup: want an uncovered selection, got %v", sel.Tests)
	}
	got := pyWithAuthoredTest(t, sel, []string{"pytest", "tests/"}, "tests/test_corral_untested.py")
	want := []string{"pytest", "tests/test_corral_untested.py"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("uncovered authored cmd = %v, want %v (the stripped base + the authored test alone)", got, want)
	}
}

func spanSelection() lang.Selection {
	return lang.Selection{
		Method: "coverage-context", Of: 10,
		Base:  []string{"python3", "-m", "pytest", "-q"},
		Cmd:   []string{"python3", "-m", "pytest", "-q", "tests/test_a.py::t1", "tests/test_a.py::t2", "tests/test_b.py::t3"},
		Tests: []string{"tests/test_a.py::t1", "tests/test_a.py::t2", "tests/test_b.py::t3"},
		Lines: map[string][]lang.LineRange{
			"tests/test_a.py::t1": {{Start: 10, End: 20}},
			"tests/test_a.py::t2": {{Start: 10, End: 12}, {Start: 40, End: 45}},
			"tests/test_b.py::t3": {{Start: 40, End: 60}},
		},
		Static: []lang.LineRange{{Start: 1, End: 5}, {Start: 30, End: 30}},
	}
}

func TestForSpanPicksTheTestsThatReachTheSpan(t *testing.T) {
	cmd, tests, rule := pyForSpan(t, spanSelection(), lang.LineRange{Start: 41, End: 42})
	if rule != lang.SpanRuleLines {
		t.Fatalf("rule = %q", rule)
	}
	want := []string{"tests/test_a.py::t2", "tests/test_b.py::t3"}
	if !reflect.DeepEqual(tests, want) {
		t.Errorf("tests = %v, want %v", tests, want)
	}
	if !reflect.DeepEqual(cmd, []string{"python3", "-m", "pytest", "-q", "tests/test_a.py::t2", "tests/test_b.py::t3"}) {
		t.Errorf("cmd = %v", cmd)
	}
}

func TestForSpanStaticLineRunsTheWholeFileSelection(t *testing.T) {
	sel := spanSelection()
	cmd, tests, rule := pyForSpan(t, sel, lang.LineRange{Start: 29, End: 31}) // touches static line 30
	if rule != lang.SpanRuleStatic || !reflect.DeepEqual(cmd, sel.Cmd) || !reflect.DeepEqual(tests, sel.Tests) {
		t.Errorf("static span must run the file selection: rule=%q cmd=%v", rule, cmd)
	}
}

func TestForSpanUnreachedRunsTheWholeFileSelectionAnyway(t *testing.T) {
	sel := spanSelection()
	cmd, _, rule := pyForSpan(t, sel, lang.LineRange{Start: 70, End: 75})
	if rule != lang.SpanRuleUnreached || !reflect.DeepEqual(cmd, sel.Cmd) {
		t.Errorf("unreached span must still run the file selection: rule=%q cmd=%v", rule, cmd)
	}
}

func TestForSpanNoSpanOrNoLinesIsToday(t *testing.T) {
	sel := spanSelection()
	if cmd, _, rule := pyForSpan(t, sel, lang.LineRange{}); rule != lang.SpanRuleFile || !reflect.DeepEqual(cmd, sel.Cmd) {
		t.Errorf("zero span: rule=%q cmd=%v", rule, cmd)
	}
	sel.Lines = nil
	if cmd, _, rule := pyForSpan(t, sel, lang.LineRange{Start: 41, End: 42}); rule != lang.SpanRuleFile || !reflect.DeepEqual(cmd, sel.Cmd) {
		t.Errorf("no line evidence: rule=%q cmd=%v", rule, cmd)
	}
}

func TestForSpanArgvFallbackStillApplies(t *testing.T) {
	sel := spanSelection()
	var ids []string
	for i := 0; i < 3000; i++ {
		id := fmt.Sprintf("tests/test_big.py::test_%04d_%s", i, strings.Repeat("x", 20))
		ids = append(ids, id)
		sel.Lines[id] = []lang.LineRange{{Start: 41, End: 42}}
	}
	sel.Tests = append(sel.Tests, ids...)
	_, tests, rule := pyForSpan(t, sel, lang.LineRange{Start: 41, End: 42})
	if rule != lang.SpanRuleLines || len(tests) != 3 || tests[len(tests)-1] != "tests/test_big.py" {
		t.Errorf("over the argv cap the subset collapses to files: %v", tests)
	}
}

// A lang.Selection whose Tests were collapsed to FILES still carries Lines keyed
// by the node ids that no longer appear in Tests. ForSpan must read that as
// "no line evidence" — never as "no test reaches this span", which is a
// positive claim about coverage that gets signed into the verdict.
func TestForSpanRefusesUnreachedWhenNoSelectedTestHasLines(t *testing.T) {
	sel := spanSelection()
	sel.Tests = []string{"tests/test_a.py", "tests/test_b.py"} // collapsed to files
	sel.Cmd = append([]string{"python3", "-m", "pytest", "-q"}, sel.Tests...)
	cmd, tests, rule := pyForSpan(t, sel, lang.LineRange{Start: 41, End: 42})
	if rule != lang.SpanRuleFile {
		t.Errorf("rule = %q, want %q — the ids in Lines are not the tests that will run", rule, lang.SpanRuleFile)
	}
	if !reflect.DeepEqual(cmd, sel.Cmd) || !reflect.DeepEqual(tests, sel.Tests) {
		t.Errorf("must run the file selection unchanged: cmd=%v tests=%v", cmd, tests)
	}
}

// Cmd is Base + Tests by construction, and the nil-Base fallback slices Cmd
// on that assumption. A lang.Selection that does not hold it would slice out of
// range (or, worse, silently cut the interpreter off the front), so the
// fallback checks rather than trusts.
func TestForSpanFileFallbackWhenCmdIsShorterThanTests(t *testing.T) {
	sel := spanSelection()
	sel.Base = nil
	sel.Cmd = []string{"pytest"} // shorter than Tests: not Base+Tests
	cmd, _, rule := pyForSpan(t, sel, lang.LineRange{Start: 41, End: 42})
	if rule != lang.SpanRuleFile || !reflect.DeepEqual(cmd, sel.Cmd) {
		t.Errorf("rule=%q cmd=%v, want the file selection unchanged", rule, cmd)
	}
}

// Index parses the SAME evidence document Select reads, but answers about
// every measured file at once: the per-file test->lines-executed readout
// candidacy needs to decide "does anything cover this file" without calling
// Select once per source file.
func TestPythonIndexReadsEveryMeasuredFile(t *testing.T) {
	idx, err := pyIndex(t, recordedPyEvidence(t))
	if err != nil {
		t.Fatal(err)
	}
	calc, ok := idx["pkg/calc.py"]
	if !ok {
		t.Fatal("Index: pkg/calc.py missing from the readout")
	}
	want := map[string]int{
		"tests/test_calc.py::test_add":  1,
		"tests/test_other.py::test_sub": 1,
	}
	if !reflect.DeepEqual(calc.Tests, want) {
		t.Errorf("pkg/calc.py Tests = %v, want %v", calc.Tests, want)
	}
	// A file the suite measured but nothing executed still appears, with an
	// empty Tests map — Index reports zero coverage as a fact about the
	// file, not as the file's absence from the readout.
	if init, ok := idx["pkg/__init__.py"]; !ok || len(init.Tests) != 0 {
		t.Errorf("pkg/__init__.py = %+v, ok=%v, want a present entry with zero covering tests", init, ok)
	}
	// A file the evidence never measured at all is genuinely absent — Index
	// must not invent a zero-coverage entry for it.
	if _, ok := idx["never/measured.py"]; ok {
		t.Error("Index must not fabricate an entry for a file the evidence never measured")
	}
}

// THE FOURTH DEFECT: Index must carry whether a file's coverage includes
// import/module-load-time (static) execution, alongside the per-test
// contexts — this, not coveringTests alone, is what lets a caller tell a
// package __init__.py (executed by every test that imports it, exercised
// by none directly) from a genuinely dead file.
func TestPythonIndexCarriesHasStatic(t *testing.T) {
	idx, err := pyIndex(t, recordedPyEvidence(t))
	if err != nil {
		t.Fatal(err)
	}
	if calc, ok := idx["pkg/calc.py"]; !ok || !calc.HasStatic {
		t.Errorf("pkg/calc.py HasStatic = %v, ok=%v, want true (the fixture records static ranges for it)", calc.HasStatic, ok)
	}
	// The fixture's pkg/__init__.py has BOTH zero tests and zero static —
	// genuinely dead, not import-only. HasStatic must be false here so the
	// two states are not conflated.
	if init, ok := idx["pkg/__init__.py"]; !ok || init.HasStatic {
		t.Errorf("pkg/__init__.py HasStatic = %v, ok=%v, want false (the fixture's static coverage is empty)", init.HasStatic, ok)
	}
}

// The actual import-only shape: zero covering tests, non-empty static.
func TestPythonIndexImportOnlyFileHasZeroTestsAndHasStaticTrue(t *testing.T) {
	raw := `{"format":"corral-selection-3","tests":1,"files":{` +
		`"src/pkg/__init__.py":{"tests":[],"lines":{},"static":[[1,3]]}}}`
	idx, err := pyIndex(t, []byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	f, ok := idx["src/pkg/__init__.py"]
	if !ok {
		t.Fatal("Index: src/pkg/__init__.py missing from the readout")
	}
	if len(f.Tests) != 0 {
		t.Errorf("Tests = %v, want empty (zero covering tests)", f.Tests)
	}
	if !f.HasStatic {
		t.Error("HasStatic = false, want true — the file has non-empty static (import-time) coverage")
	}
}

func TestPythonIndexSharesSelectsParsing(t *testing.T) {
	if _, err := pyIndex(t, []byte(`{"format":"not-corral-selection-3"}`)); err == nil {
		t.Error("Index must refuse a document with the wrong format stamp, exactly like Select")
	}
	if _, err := pyIndex(t, []byte("")); err == nil {
		t.Error("Index must refuse empty evidence, exactly like Select")
	}
}

// THE SALVAGE'S --deselect MUST SURVIVE THE SELECTION. WithAuthoredTest rebuilds
// the command from sel.Cmd whenever a selection exists and used to discard the
// passed testCmd entirely — which is exactly where salvageByDeselect had put
// the `--deselect <failing test>` it computed. Under selection (the default),
// the salvage ran the original command, failed the same way, and reported
// nothing; with an empty lang.Selection the identical inputs salvaged one proof.
func TestWithAuthoredTestCarriesDeselectAcrossASelection(t *testing.T) {
	sel := lang.Selection{Base: []string{"python3", "-m", "pytest", "-q"}, Cmd: []string{"python3", "-m", "pytest", "-q", "tests/test_calc.py::test_add"}, Tests: []string{"tests/test_calc.py::test_add"}}
	passed := []string{"python3", "-m", "pytest", "-q", "--deselect", "tests/test_corral.py::test_wrong"}
	got := pyWithAuthoredTest(t, sel, passed, "tests/test_corral.py")
	joined := strings.Join(got, " ")
	if !strings.Contains(joined, "--deselect tests/test_corral.py::test_wrong") {
		t.Fatalf("the --deselect the salvage computed was discarded by the selection: %q", joined)
	}
	if !strings.Contains(joined, "tests/test_calc.py::test_add") {
		t.Errorf("the selection's own tests must still be there: %q", joined)
	}
}

// Evidence and Index read one document; they must not drift. For every file
// Evidence returns, each test's executed lines summed equal Index's count, and
// the static/statement flags agree. Index is now the map's Coverage readout.
func TestPythonEvidenceAgreesWithIndex(t *testing.T) {
	raw := recordedPyEvidence(t)
	doc, err := pyEvidencePlugin(t).Evidence(raw)
	if err != nil {
		t.Fatal(err)
	}
	idx, err := pyIndex(t, raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Files) == 0 || doc.Of == 0 {
		t.Fatalf("fixture produced no evidence: %+v", doc)
	}
	for path, f := range doc.Files {
		p, ok := lang.AlignEvidencePath(path, "")
		if !ok {
			continue
		}
		cov, ok := idx[p]
		if !ok {
			t.Fatalf("Index lacks %s", p)
		}
		if len(f.Tests) != len(cov.Tests) {
			t.Errorf("%s: %d tests vs Index %d", p, len(f.Tests), len(cov.Tests))
		}
		for id, rngs := range f.Tests {
			n := 0
			for _, r := range rngs {
				n += r.End - r.Start + 1
			}
			if got, ok := cov.Tests[id]; !ok || got != n {
				t.Errorf("%s %s: lines %d, Index %d (present %v)", p, id, n, got, ok)
			}
		}
		if (len(f.Static) > 0) != cov.HasStatic || f.HasStatements != cov.HasStatements {
			t.Errorf("%s: flags disagree: %+v vs %+v", p, f, cov)
		}
	}
}
