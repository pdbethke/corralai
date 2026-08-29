// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
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
		"COVERAGE_FILE=",
		"-m coverage json --show-contexts -o -",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %q:\n%s", want, script)
		}
	}
}

func TestPythonInstrumentRefusesANonPytestCommand(t *testing.T) {
	if _, ok := (pyPlugin{}).Instrument([]string{"make", "test"}); ok {
		t.Error("Instrument must refuse a command it cannot splice coverage into")
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

// pkg/untested.py is ABSENT from the recording (never imported); its paired
// test file ran, so the absence is evidence: no test executes it.
func TestPythonSelectEmptyForAFileNoTestExecutes(t *testing.T) {
	sel, err := pyPlugin{}.Select(recordedEvidence(t), "", "pkg/untested.py", "tests/test_calc.py", []string{"pytest"})
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
	// Build evidence where one file is executed by 3000 tests in one module.
	var ctxs []string
	for i := 0; i < 3000; i++ {
		ctxs = append(ctxs, `"tests/test_big.py::test_`+strings.Repeat("x", 20)+string(rune('a'+i%26))+`|run"`)
	}
	ev := `{"meta":{"show_contexts":true},"totals":{"covered_lines":1},"files":{"pkg/calc.py":{"summary":{"num_statements":1,"covered_lines":1},"contexts":{"1":[` + strings.Join(ctxs, ",") + `]}},"tests/test_big.py":{"summary":{"num_statements":1,"covered_lines":1},"contexts":{"1":[` + ctxs[0] + `]}}}}`
	sel, err := pyPlugin{}.Select([]byte(ev), "", "pkg/calc.py", "tests/test_big.py", []string{"pytest"})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sel.Tests, []string{"tests/test_big.py"}) {
		t.Errorf("Tests = %v, want the containing FILE (a superset, still evidence-derived)", sel.Tests)
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

// The plugin must satisfy the extension, and nothing may still satisfy the
// retired one.
var _ TestSelector = pyPlugin{}
