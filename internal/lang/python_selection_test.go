// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"encoding/json"
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
		`"format": "corral-selection-3"`,
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
		`"format": "corral-selection-3"`,
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

func TestPythonIsLibraryCodeFlatLayout(t *testing.T) {
	hasPath := func(p string) bool { return p == "mypkg/__init__.py" }
	if !(pyPlugin{}).IsLibraryCode("mypkg/foo.py", hasPath) {
		t.Error("mypkg/foo.py with mypkg/__init__.py present must be library code")
	}
	if (pyPlugin{}).IsLibraryCode("scripts/foo.py", hasPath) {
		t.Error("scripts/foo.py with no __init__.py anywhere must NOT be library code")
	}
}

func TestPythonIsLibraryCodeNestedPackage(t *testing.T) {
	// sub/ has no __init__.py of its own, but mypkg/ (an ancestor) does —
	// the WHOLE directory chain must be walked, not just the immediate dir.
	hasPath := func(p string) bool { return p == "mypkg/__init__.py" }
	if !(pyPlugin{}).IsLibraryCode("mypkg/sub/foo.py", hasPath) {
		t.Error("mypkg/sub/foo.py must be library code via the mypkg ancestor's __init__.py")
	}
}

func TestPythonIsLibraryCodeSrcLayout(t *testing.T) {
	// src/<pkg>/... counts even with NO __init__.py anywhere — PEP 420
	// namespace packages ship without one.
	hasPath := func(string) bool { return false }
	if !(pyPlugin{}).IsLibraryCode("src/pkg/foo.py", hasPath) {
		t.Error("src/pkg/foo.py must be library code even with no __init__.py measured")
	}
	if !(pyPlugin{}).IsLibraryCode("src/pkg/sub/foo.py", hasPath) {
		t.Error("src/pkg/sub/foo.py (deeper under src/pkg) must also be library code")
	}
	// A bare file directly under src/, with no package segment, does not
	// qualify on the src/ shortcut alone.
	if (pyPlugin{}).IsLibraryCode("src/foo.py", hasPath) {
		t.Error("src/foo.py (no package dir under src/) must not be library code via the shortcut alone")
	}
}

func TestPythonIsLibraryCodeDocsAndScripts(t *testing.T) {
	hasPath := func(string) bool { return false }
	for _, p := range []string{"docs/conf.py", "setup.py", "noxfile.py", "scripts/release.py"} {
		if (pyPlugin{}).IsLibraryCode(p, hasPath) {
			t.Errorf("%s must not be library code — it sits in no importable package", p)
		}
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
