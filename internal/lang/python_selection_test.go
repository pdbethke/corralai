// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func recordedEvidence(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "pycov-contexts.json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// One instrumented run, derived from the OPERATOR's command so their flags
// survive, writing per-test contexts and emitting the JSON on stdout.
func TestPythonInstrumentDerivesFromOperatorCommand(t *testing.T) {
	cmd, ok := pyPlugin{}.Instrument([]string{"python3", "-m", "pytest", "-q", "-k", "not slow"})
	if !ok {
		t.Fatal("Instrument: ok=false for a plain pytest command")
	}
	if len(cmd) != 3 || cmd[0] != "sh" || cmd[1] != "-c" {
		t.Fatalf("want sh -c <script>, got %v", cmd)
	}
	script := cmd[2]
	for _, want := range []string{
		"python3 -m pytest -q -k 'not slow' --cov --cov-context=test --cov-report= -p no:cacheprovider",
		// The C tracer is pinned: coverage's sysmon core (the default on
		// Python 3.12+) does not support dynamic contexts, warns "context
		// data may be incomplete", and under a project's filterwarnings=error
		// that warning fails every test at setup (flask: 985 errors, every
		// file "uncovered").
		"COVERAGE_CORE=ctrace COVERAGE_FILE=",
		// The suite must PASS: exit 1 (tests failed/errored) is refused, so
		// selection cannot grade a suite the whole-suite baseline would
		// refuse (#164).
		`[ "$rc" -eq 0 ] || exit "$rc"`,
		// The evidence is reduced INSIDE the run to {file: [node ids]} — the
		// full coverage-json with contexts was 411 MB on flask (#165).
		`"format": "corral-selection-2"`,
		"contexts_by_lineno",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "coverage json") || strings.Contains(script, "0|1)") {
		t.Errorf("script must neither emit the full coverage json nor accept a failing suite:\n%s", script)
	}
}

func TestPythonInstrumentRefusesANonPytestCommand(t *testing.T) {
	if _, ok := (pyPlugin{}).Instrument([]string{"make", "test"}); ok {
		t.Error("Instrument must refuse a command it cannot splice coverage into")
	}
}

// THE SECOND DEFECT: coverage.py's default (bare --cov) scope is the whole
// cwd, which is exactly what makes "uncovered" unreachable for a
// src-layout/editable install (see InstrumentSourceRoots's own doc) —
// coverage measures a file by where python actually imported it from, an
// editable install imports from OUTSIDE the checked-out tree, and the
// reducer's own repo-root filter drops it entirely. One --cov=<root> per
// derived source root instead scopes coverage to what was actually asked
// to be audited.
func TestPythonInstrumentSourceRootsEmitsPerRootCovFlags(t *testing.T) {
	cmd, ok := pyPlugin{}.InstrumentSourceRoots([]string{"pytest", "-q"}, []string{"src"})
	if !ok {
		t.Fatal("InstrumentSourceRoots: ok=false for a plain pytest command")
	}
	script := cmd[2]
	if !strings.Contains(script, "--cov=src --cov-context=test") {
		t.Errorf("script must scope coverage to the derived root, got:\n%s", script)
	}
	if strings.Contains(script, " --cov --cov-context") {
		t.Errorf("script must NOT fall back to bare --cov when a root was given:\n%s", script)
	}
	// Every other clause survives untouched: the rc guard, the JSON tail,
	// the pinned C tracer — this only ever changes the --cov flag(s).
	for _, want := range []string{
		"COVERAGE_CORE=ctrace COVERAGE_FILE=",
		`[ "$rc" -eq 0 ] || exit "$rc"`,
		`"format": "corral-selection-2"`,
		"--cov-context=test --cov-report= -p no:cacheprovider",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %q:\n%s", want, script)
		}
	}
}

// More than one source root emits one --cov= per root, in the order given.
func TestPythonInstrumentSourceRootsEmitsOneFlagPerRoot(t *testing.T) {
	cmd, ok := pyPlugin{}.InstrumentSourceRoots([]string{"pytest"}, []string{"src", "otherpkg"})
	if !ok {
		t.Fatal("ok=false")
	}
	script := cmd[2]
	if !strings.Contains(script, "--cov=src --cov=otherpkg --cov-context=test") {
		t.Errorf("script must carry one --cov= per root, got:\n%s", script)
	}
}

// A root of "." (sources at the repo root) is a legitimate root, not a
// special case that gets dropped.
func TestPythonInstrumentSourceRootsAcceptsDotForRootLevelSources(t *testing.T) {
	cmd, ok := pyPlugin{}.InstrumentSourceRoots([]string{"pytest"}, []string{"."})
	if !ok {
		t.Fatal("ok=false")
	}
	if !strings.Contains(cmd[2], "--cov=. --cov-context=test") {
		t.Errorf("script must carry --cov=., got:\n%s", cmd[2])
	}
}

// No roots derived (nil/empty) is not an error — it is byte-identical to
// Instrument's own bare --cov fallback.
func TestPythonInstrumentSourceRootsFallsBackToBareCovWhenEmpty(t *testing.T) {
	withRoots, ok := pyPlugin{}.InstrumentSourceRoots([]string{"pytest", "-q"}, nil)
	if !ok {
		t.Fatal("ok=false")
	}
	plain, ok := pyPlugin{}.Instrument([]string{"pytest", "-q"})
	if !ok {
		t.Fatal("ok=false")
	}
	if !reflect.DeepEqual(withRoots, plain) {
		t.Errorf("InstrumentSourceRoots(nil) = %v, want byte-identical to Instrument = %v", withRoots, plain)
	}
}

// The command-shape refusal (anything that is not a recognized pytest
// invocation) applies identically to the source-roots entry point.
func TestPythonInstrumentSourceRootsRefusesANonPytestCommand(t *testing.T) {
	if _, ok := (pyPlugin{}).InstrumentSourceRoots([]string{"make", "test"}, []string{"src"}); ok {
		t.Error("InstrumentSourceRoots must refuse a command it cannot splice coverage into")
	}
}

func TestPythonSelectPicksOnlyTestsThatExecutedTheFile(t *testing.T) {
	base := []string{"python3", "-m", "pytest", "-q"}
	sel, err := pyPlugin{}.Select(recordedEvidence(t), "", "pkg/calc.py", "tests/test_calc.py", base)
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
	_, err := pyPlugin{}.Select(recordedEvidence(t), "", "pkg/untested.py", "tests/test_calc.py", []string{"pytest"})
	if err == nil {
		t.Fatal("an absent file must fall back disclosed, not read as uncovered")
	}
}

// pkg/__init__.py IS present in the recording, with zero covering tests —
// this, and only this, is what "uncovered" measures.
func TestPythonSelectPresentWithZeroTestsIsUncovered(t *testing.T) {
	sel, err := pyPlugin{}.Select(recordedEvidence(t), "", "pkg/__init__.py", "tests/test_calc.py", []string{"pytest"})
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
	if _, err := (pyPlugin{}).Select(recordedEvidence(t), "", "pkg/not_in_report.py", "tests/test_never_ran.py", []string{"pytest"}); err == nil {
		t.Error("absent file with an absent paired test must be an error, not uncovered")
	}
	if _, err := (pyPlugin{}).Select(recordedEvidence(t), "", "pkg/not_in_report.py", "", []string{"pytest"}); err == nil {
		t.Error("absent file with NO paired test path must be an error")
	}
}

// The reducer stamps its shape; anything else — including the full
// coverage-json this used to parse — is refused rather than misread.
func TestPythonSelectRejectsAnUnknownEvidenceFormat(t *testing.T) {
	old := `{"meta":{"show_contexts":true},"totals":{"covered_lines":1},"files":{"pkg/calc.py":{"summary":{"num_statements":1,"covered_lines":1},"contexts":{"1":["tests/test_calc.py::test_add|run"]}}}}`
	if _, err := (pyPlugin{}).Select([]byte(old), "", "pkg/calc.py", "tests/test_calc.py", []string{"pytest"}); err == nil || !strings.Contains(err.Error(), "corral-selection-2") {
		t.Errorf("the old coverage-json shape must be refused by name, got err=%v", err)
	}
}

func TestPythonSelectRejectsNonCoverageJSON(t *testing.T) {
	if _, err := (pyPlugin{}).Select([]byte(`{"hello":1}`), "", "pkg/calc.py", "tests/test_calc.py", []string{"pytest"}); err == nil {
		t.Error("want an error on a document that is not a coverage report")
	}
}

func TestPythonSelectAlignsAbsoluteReportPaths(t *testing.T) {
	// Some coverage configs emit absolute paths; align them to the repo
	// root the same way ParseCoverage does.
	ev := strings.ReplaceAll(string(recordedEvidence(t)), `"pkg/calc.py"`, `"/repo/pkg/calc.py"`)
	sel, err := pyPlugin{}.Select([]byte(ev), "/repo", "pkg/calc.py", "tests/test_calc.py", []string{"pytest"})
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
	ev := `{"format":"corral-selection-2","tests":3000,"files":{` +
		`"pkg/calc.py":{"tests":[` + strings.Join(ctxs, ",") + `],"lines":{"0":[[2,2]],"1":[[6,6]]},"static":[[1,1]]},` +
		`"tests/test_big.py":{"tests":[` + ctxs[0] + `],"lines":{},"static":[]}}}`
	sel, err := pyPlugin{}.Select([]byte(ev), "", "pkg/calc.py", "tests/test_big.py", []string{"pytest"})
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
	sel := Selection{Cmd: append(append([]string{}, base...), "tests/test_calc.py::test_add"), Tests: []string{"tests/test_calc.py::test_add"}, Method: "coverage-context"}
	got := pyPlugin{}.WithAuthoredTest(sel, base, "tests/test_corral_calc.py")
	want := []string{"python3", "-m", "pytest", "-q", "tests/test_calc.py::test_add", "tests/test_corral_calc.py"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	// Uncovered: no selected tests, but the authored test MUST still run.
	got = pyPlugin{}.WithAuthoredTest(Selection{Method: "coverage-context"}, base, "tests/test_corral_x.py")
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
	fixture, _ := filepath.Abs(filepath.Join("testdata", "pycov-fixture"))
	cmd, ok := pyPlugin{}.Instrument([]string{"python3", "-m", "pytest", "-q"})
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
	sel, err := pyPlugin{}.Select(out, fixture, "pkg/calc.py", "tests/test_calc.py", []string{"python3", "-m", "pytest", "-q"})
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
	sel, err := pyPlugin{}.Select(recordedEvidence(t), "", "pkg/calc.py", "tests/test_calc.py", []string{"pytest"})
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
	if !add[0].Overlaps(LineRange{2, 2}) || add[0].Overlaps(LineRange{6, 6}) {
		t.Errorf("test_add should reach add's body (line 2) and not sub's (line 6): %+v", add)
	}
	if len(sel.Static) == 0 {
		t.Errorf("the def lines execute at import and must be reported static: %+v", sel.Static)
	}
}

// A module with nothing executable in it (an empty __init__.py) is reported
// by coverage at line 0, and the reducer used to emit [[0,0]] as a static
// range. Line 0 does not exist: no mutant span can overlap it, and every
// reader downstream treats a static range as evidence about real source.
func TestRecordedEvidenceCarriesNoLineZero(t *testing.T) {
	var doc struct {
		Files map[string]pySelectionFile `json:"files"`
	}
	if err := json.Unmarshal(recordedEvidence(t), &doc); err != nil {
		t.Fatal(err)
	}
	for path, f := range doc.Files {
		for _, r := range f.Static {
			if r[0] <= 0 || r[1] <= 0 {
				t.Errorf("%s: static range %v is not a source line", path, r)
			}
		}
		for idx, rngs := range f.Lines {
			for _, r := range rngs {
				if r[0] <= 0 || r[1] <= 0 {
					t.Errorf("%s test %s: line range %v is not a source line", path, idx, r)
				}
			}
		}
	}
}

func TestPythonSelectRefusesV1ByName(t *testing.T) {
	v1 := `{"format":"corral-selection-1","tests":1,"files":{"pkg/calc.py":["tests/test_calc.py::test_add"]}}`
	_, err := pyPlugin{}.Select([]byte(v1), "", "pkg/calc.py", "tests/test_calc.py", []string{"pytest"})
	if err == nil || !strings.Contains(err.Error(), "corral-selection-2") {
		t.Errorf("v1 must be refused naming the expected format, got %v", err)
	}
}

func TestLineRangeOverlaps(t *testing.T) {
	cases := []struct {
		a, b LineRange
		want bool
	}{
		{LineRange{1, 5}, LineRange{5, 9}, true},
		{LineRange{1, 5}, LineRange{6, 9}, false},
		{LineRange{3, 3}, LineRange{1, 9}, true},
		{LineRange{}, LineRange{1, 9}, false},
	}
	for _, c := range cases {
		if got := c.a.Overlaps(c.b); got != c.want {
			t.Errorf("%v overlaps %v = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// The plugin must satisfy the extension, and nothing may still satisfy the
// retired one.
var _ TestSelector = pyPlugin{}

// C1. pytest UNIONS positional args: `pytest tests/ tests/test_a.py::test_x`
// collects all of tests/, so appending node ids to a command that names a
// collection target narrows NOTHING while the verdict, the ledger and the
// cache key all say "coverage-context". Select therefore strips the
// operator's collection targets and keeps everything else — options, their
// values, and any positional token that is not a path this repo has.
func TestPythonSelectStripsCollectionTargetsFromTheBaseCommand(t *testing.T) {
	fixture, err := filepath.Abs(filepath.Join("testdata", "pycov-fixture"))
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
			sel, err := pyPlugin{}.Select(recordedEvidence(t), fixture, "pkg/calc.py", "tests/test_calc.py", tc.base)
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
	fixture, err := filepath.Abs(filepath.Join("testdata", "pycov-fixture"))
	if err != nil {
		t.Fatal(err)
	}
	sel, err := pyPlugin{}.Select(recordedEvidence(t), fixture, "pkg/__init__.py", "tests/test_calc.py", []string{"pytest", "tests/"})
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Tests) != 0 {
		t.Fatalf("fixture setup: want an uncovered selection, got %v", sel.Tests)
	}
	got := pyPlugin{}.WithAuthoredTest(sel, []string{"pytest", "tests/"}, "tests/test_corral_untested.py")
	want := []string{"pytest", "tests/test_corral_untested.py"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("uncovered authored cmd = %v, want %v (the stripped base + the authored test alone)", got, want)
	}
}

func spanSelection() Selection {
	return Selection{
		Method: "coverage-context", Of: 10,
		Base:  []string{"python3", "-m", "pytest", "-q"},
		Cmd:   []string{"python3", "-m", "pytest", "-q", "tests/test_a.py::t1", "tests/test_a.py::t2", "tests/test_b.py::t3"},
		Tests: []string{"tests/test_a.py::t1", "tests/test_a.py::t2", "tests/test_b.py::t3"},
		Lines: map[string][]LineRange{
			"tests/test_a.py::t1": {{10, 20}},
			"tests/test_a.py::t2": {{10, 12}, {40, 45}},
			"tests/test_b.py::t3": {{40, 60}},
		},
		Static: []LineRange{{1, 5}, {30, 30}},
	}
}

func TestForSpanPicksTheTestsThatReachTheSpan(t *testing.T) {
	cmd, tests, rule := pyPlugin{}.ForSpan(spanSelection(), LineRange{41, 42})
	if rule != SpanRuleLines {
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
	cmd, tests, rule := pyPlugin{}.ForSpan(sel, LineRange{29, 31}) // touches static line 30
	if rule != SpanRuleStatic || !reflect.DeepEqual(cmd, sel.Cmd) || !reflect.DeepEqual(tests, sel.Tests) {
		t.Errorf("static span must run the file selection: rule=%q cmd=%v", rule, cmd)
	}
}

func TestForSpanUnreachedRunsTheWholeFileSelectionAnyway(t *testing.T) {
	sel := spanSelection()
	cmd, _, rule := pyPlugin{}.ForSpan(sel, LineRange{70, 75})
	if rule != SpanRuleUnreached || !reflect.DeepEqual(cmd, sel.Cmd) {
		t.Errorf("unreached span must still run the file selection: rule=%q cmd=%v", rule, cmd)
	}
}

func TestForSpanNoSpanOrNoLinesIsToday(t *testing.T) {
	sel := spanSelection()
	if cmd, _, rule := (pyPlugin{}).ForSpan(sel, LineRange{}); rule != SpanRuleFile || !reflect.DeepEqual(cmd, sel.Cmd) {
		t.Errorf("zero span: rule=%q cmd=%v", rule, cmd)
	}
	sel.Lines = nil
	if cmd, _, rule := (pyPlugin{}).ForSpan(sel, LineRange{41, 42}); rule != SpanRuleFile || !reflect.DeepEqual(cmd, sel.Cmd) {
		t.Errorf("no line evidence: rule=%q cmd=%v", rule, cmd)
	}
}

func TestForSpanArgvFallbackStillApplies(t *testing.T) {
	sel := spanSelection()
	var ids []string
	for i := 0; i < 3000; i++ {
		id := fmt.Sprintf("tests/test_big.py::test_%04d_%s", i, strings.Repeat("x", 20))
		ids = append(ids, id)
		sel.Lines[id] = []LineRange{{41, 42}}
	}
	sel.Tests = append(sel.Tests, ids...)
	_, tests, rule := pyPlugin{}.ForSpan(sel, LineRange{41, 42})
	if rule != SpanRuleLines || len(tests) != 3 || tests[len(tests)-1] != "tests/test_big.py" {
		t.Errorf("over the argv cap the subset collapses to files: %v", tests)
	}
}

// A Selection whose Tests were collapsed to FILES still carries Lines keyed
// by the node ids that no longer appear in Tests. ForSpan must read that as
// "no line evidence" — never as "no test reaches this span", which is a
// positive claim about coverage that gets signed into the verdict.
func TestForSpanRefusesUnreachedWhenNoSelectedTestHasLines(t *testing.T) {
	sel := spanSelection()
	sel.Tests = []string{"tests/test_a.py", "tests/test_b.py"} // collapsed to files
	sel.Cmd = append([]string{"python3", "-m", "pytest", "-q"}, sel.Tests...)
	cmd, tests, rule := pyPlugin{}.ForSpan(sel, LineRange{41, 42})
	if rule != SpanRuleFile {
		t.Errorf("rule = %q, want %q — the ids in Lines are not the tests that will run", rule, SpanRuleFile)
	}
	if !reflect.DeepEqual(cmd, sel.Cmd) || !reflect.DeepEqual(tests, sel.Tests) {
		t.Errorf("must run the file selection unchanged: cmd=%v tests=%v", cmd, tests)
	}
}

// Cmd is Base + Tests by construction, and the nil-Base fallback slices Cmd
// on that assumption. A Selection that does not hold it would slice out of
// range (or, worse, silently cut the interpreter off the front), so the
// fallback checks rather than trusts.
func TestForSpanFileFallbackWhenCmdIsShorterThanTests(t *testing.T) {
	sel := spanSelection()
	sel.Base = nil
	sel.Cmd = []string{"pytest"} // shorter than Tests: not Base+Tests
	cmd, _, rule := pyPlugin{}.ForSpan(sel, LineRange{41, 42})
	if rule != SpanRuleFile || !reflect.DeepEqual(cmd, sel.Cmd) {
		t.Errorf("rule=%q cmd=%v, want the file selection unchanged", rule, cmd)
	}
}

// Index parses the SAME evidence document Select reads, but answers about
// every measured file at once: the per-file test->lines-executed readout
// candidacy needs to decide "does anything cover this file" without calling
// Select once per source file.
func TestPythonIndexReadsEveryMeasuredFile(t *testing.T) {
	idx, err := pyPlugin{}.Index(recordedEvidence(t))
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

func TestPythonIndexSharesSelectsParsing(t *testing.T) {
	if _, err := (pyPlugin{}).Index([]byte(`{"format":"not-corral-selection-2"}`)); err == nil {
		t.Error("Index must refuse a document with the wrong format stamp, exactly like Select")
	}
	if _, err := (pyPlugin{}).Index([]byte("")); err == nil {
		t.Error("Index must refuse empty evidence, exactly like Select")
	}
}

func TestPythonDiagnoseSelectionFailureNamesMissingPytestCov(t *testing.T) {
	text := "usage: pytest [options]\npytest: error: unrecognized arguments: --cov --cov-context=test --cov-report=\n"
	hint := (pyPlugin{}).DiagnoseSelectionFailure(text)
	if !strings.Contains(hint, "pytest-cov") || !strings.Contains(hint, "pip install pytest-cov") {
		t.Errorf("hint = %q, want it to name pytest-cov and how to install it", hint)
	}
}

func TestPythonDiagnoseSelectionFailureRecognizesNothingElse(t *testing.T) {
	if hint := (pyPlugin{}).DiagnoseSelectionFailure("some unrelated failure text"); hint != "" {
		t.Errorf("hint = %q, want empty for text it does not recognize", hint)
	}
}
