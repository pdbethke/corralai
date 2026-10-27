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

// This file proves the engine selects exactly as Python's own selector did,
// on every input that selector's tests already use: the recorded evidence
// and fixture under internal/lang/testdata, and the argv, selections and
// spans those tests state. Nothing here is a new expectation. Each case runs
// both sides and compares them field by field, so the day pyPlugin's
// methods are deleted (Task 6) the inputs they were judged on have already
// been judged on the engine.

// pyEquiv is Python's side of the comparison: the plugin's old selector, its
// evidence reader, and the pytest row the engine builds commands from.
type pyEquiv struct {
	sel lang.TestSelector
	ev  lang.TestEvidence
	row lang.Runner
}

func newPyEquiv(t *testing.T) pyEquiv {
	t.Helper()
	p, ok := lang.ByName("python")
	if !ok {
		t.Fatal("no python plugin")
	}
	row, err := lang.RunnerByID(p, "pytest")
	if err != nil {
		t.Fatal(err)
	}
	return pyEquiv{sel: p.(lang.TestSelector), ev: p.(lang.TestEvidence), row: row}
}

// pyMap is the map a caller builds for Select: the evidence read once, every
// path aligned under repoRoot as the old selector aligned it, every node
// named its unit by the row.
func (e pyEquiv) pyMap(evidence []byte, repoRoot string) (Map, error) {
	doc, err := e.ev.Evidence(evidence)
	if err != nil {
		return Map{}, err
	}
	m := FromEvidence(doc, func(p string) (string, bool) { return lang.AlignEvidencePath(p, repoRoot) }, e.row.TestUnit)
	m.Lang = "python"
	return m, nil
}

func pyTestdata(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(filepath.Join("..", "lang", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func recordedPyEvidence(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile(pyTestdata(t, "pycov-contexts.json"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// bigPyEvidence is TestPythonSelectFallsBackToFilesWhenArgvWouldBeTooLong's
// document: 3000 distinct ids, far over the argv cap.
func bigPyEvidence() []byte {
	var ctxs []string
	for i := 0; i < 3000; i++ {
		ctxs = append(ctxs, fmt.Sprintf(`"tests/test_big.py::test_%04d_%s"`, i, strings.Repeat("x", 20)))
	}
	return []byte(`{"format":"corral-selection-3","tests":3000,"files":{` +
		`"pkg/calc.py":{"tests":[` + strings.Join(ctxs, ",") + `],"lines":{"0":[[2,2]],"1":[[6,6]]},"static":[[1,1]]},` +
		`"tests/test_big.py":{"tests":[` + ctxs[0] + `],"lines":{},"static":[]}}}`)
}

// errText strips the package prefix from an error: the engine speaks as
// testmap, the plugin spoke as lang, and the sentence after it is the claim.
func errText(err error) string {
	if err == nil {
		return ""
	}
	s := err.Error()
	for _, p := range []string{"lang: ", "testmap: "} {
		s = strings.TrimPrefix(s, p)
	}
	return s
}

type selectCase struct {
	name               string
	evidence           []byte
	root, code, test   string
	cmd                []string
	wantErr, wantEmpty bool // sanity on the inputs, so a case cannot pass by both sides breaking
}

func pySelectCases(t *testing.T) []selectCase {
	fixture := pyTestdata(t, "pycov-fixture")
	rec := recordedPyEvidence(t)
	abs := []byte(strings.ReplaceAll(string(rec), `"pkg/calc.py"`, `"/repo/pkg/calc.py"`))
	cases := []selectCase{
		{name: "picks only tests that executed the file", evidence: rec, code: "pkg/calc.py", test: "tests/test_calc.py", cmd: []string{"python3", "-m", "pytest", "-q"}},
		{name: "reads lines and static", evidence: rec, code: "pkg/calc.py", test: "tests/test_calc.py", cmd: []string{"pytest"}},
		{name: "absent file whose test ran is an error", evidence: rec, code: "pkg/untested.py", test: "tests/test_calc.py", cmd: []string{"pytest"}, wantErr: true},
		{name: "present with zero tests is uncovered", evidence: rec, code: "pkg/__init__.py", test: "tests/test_calc.py", cmd: []string{"pytest"}, wantEmpty: true},
		{name: "absent file and absent test", evidence: rec, code: "pkg/not_in_report.py", test: "tests/test_never_ran.py", cmd: []string{"pytest"}, wantErr: true},
		{name: "absent file and no test path", evidence: rec, code: "pkg/not_in_report.py", cmd: []string{"pytest"}, wantErr: true},
		{name: "aligns absolute report paths", evidence: abs, root: "/repo", code: "pkg/calc.py", test: "tests/test_calc.py", cmd: []string{"pytest"}},
		{name: "collapses to files over the argv cap", evidence: bigPyEvidence(), code: "pkg/calc.py", test: "tests/test_big.py", cmd: []string{"pytest"}},
		{name: "uncovered with a directory target", evidence: rec, root: fixture, code: "pkg/__init__.py", test: "tests/test_calc.py", cmd: []string{"pytest", "tests/"}, wantEmpty: true},
		{name: "old coverage-json refused", evidence: []byte(`{"meta":{"show_contexts":true},"totals":{"covered_lines":1},"files":{"pkg/calc.py":{"summary":{"num_statements":1,"covered_lines":1},"contexts":{"1":["tests/test_calc.py::test_add|run"]}}}}`), code: "pkg/calc.py", test: "tests/test_calc.py", cmd: []string{"pytest"}, wantErr: true},
		{name: "non-coverage json refused", evidence: []byte(`{"hello":1}`), code: "pkg/calc.py", test: "tests/test_calc.py", cmd: []string{"pytest"}, wantErr: true},
		{name: "v1 refused", evidence: []byte(`{"format":"corral-selection-1","tests":1,"files":{"pkg/calc.py":["tests/test_calc.py::test_add"]}}`), code: "pkg/calc.py", test: "tests/test_calc.py", cmd: []string{"pytest"}, wantErr: true},
	}
	// TestPythonSelectStripsCollectionTargetsFromTheBaseCommand's argv table.
	for _, argv := range [][]string{
		{"pytest", "tests/"},
		{"python3", "-m", "pytest", "-q", "-k", "not slow", "tests/"},
		{"pytest", "tests/test_calc.py::test_add"},
		{"pytest", "--maxfail", "3"},
	} {
		cases = append(cases, selectCase{name: "strips targets " + strings.Join(argv, " "), evidence: rec, root: fixture, code: "pkg/calc.py", test: "tests/test_calc.py", cmd: argv})
	}
	return cases
}

// engineSelect is the engine's side: build the map (a parse refusal is the
// engine's refusal too, since the map is the only way evidence reaches it),
// then Select.
func (e pyEquiv) engineSelect(c selectCase) (lang.Selection, error) {
	m, err := e.pyMap(c.evidence, c.root)
	if err != nil {
		return lang.Selection{}, err
	}
	return Select(m, e.row, c.cmd, c.root, c.code, c.test)
}

// The spans every ForSpan test asks about, plus one inside each static range
// and one past the end, so the engine's own selections are narrowed the way
// the per-mutant pass narrows them.
var equivSpans = []lang.LineRange{{}, {Start: 1, End: 1}, {Start: 2, End: 2}, {Start: 6, End: 6}, {Start: 2, End: 6}, {Start: 41, End: 42}, {Start: 29, End: 31}, {Start: 70, End: 75}, {Start: 10, End: 19}, {Start: 5, End: 6}}

func TestEngineMatchesPythonSelector(t *testing.T) {
	e := newPyEquiv(t)
	authored := []string{"tests/test_corral_calc.py", "tests/test_corral_x.py", "tests/test_corral_untested.py"}
	carried := [][]string{nil, {"--deselect", "tests/test_corral.py::test_wrong"}, {"--deselect=tests/test_calc.py::test_add"}}

	for _, c := range pySelectCases(t) {
		t.Run("Select/"+c.name, func(t *testing.T) {
			want, wantErr := e.sel.Select(c.evidence, c.root, c.code, c.test, c.cmd)
			got, gotErr := e.engineSelect(c)
			if (wantErr != nil) != c.wantErr {
				t.Fatalf("fixture drift: python err = %v, case says wantErr=%v", wantErr, c.wantErr)
			}
			if errText(gotErr) != errText(wantErr) {
				t.Fatalf("error:\n engine %v\n python %v", gotErr, wantErr)
			}
			if wantErr != nil {
				return
			}
			if c.wantEmpty != (len(want.Tests) == 0) {
				t.Fatalf("fixture drift: python Tests = %v, case says empty=%v", want.Tests, c.wantEmpty)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("Selection:\n engine %#v\n python %#v", got, want)
			}
			// The selection the engine built must also narrow and take the
			// authored test exactly as Python's would.
			for _, span := range equivSpans {
				compareForSpan(t, e, want, span)
			}
			for _, a := range authored {
				for _, extra := range carried {
					compareWithAuthored(t, e, want, append(append([]string{}, c.cmd...), extra...), c.root, a)
				}
			}
		})
	}

	// The hand-built selections the ForSpan and WithAuthoredTest tests use.
	for name, sel := range pyHandSelections() {
		t.Run("Hand/"+name, func(t *testing.T) {
			for _, span := range equivSpans {
				compareForSpan(t, e, sel, span)
			}
			base := []string{"python3", "-m", "pytest", "-q"}
			for _, a := range authored {
				for _, extra := range carried {
					compareWithAuthored(t, e, sel, append(append([]string{}, base...), extra...), "", a)
				}
			}
		})
	}
}

// pyHandSelections are the Selections python_selection_test.go,
// forspan_order_test.go and deselect_test.go construct by hand.
func pyHandSelections() map[string]lang.Selection {
	span := func() lang.Selection {
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
	out := map[string]lang.Selection{"span": span()}

	noLines := span()
	noLines.Lines = nil
	out["span without lines"] = noLines

	big := span()
	for i := 0; i < 3000; i++ {
		id := fmt.Sprintf("tests/test_big.py::test_%04d_%s", i, strings.Repeat("x", 20))
		big.Tests = append(big.Tests, id)
		big.Lines[id] = []lang.LineRange{{Start: 41, End: 42}}
	}
	out["span over the argv cap"] = big

	files := span()
	files.Tests = []string{"tests/test_a.py", "tests/test_b.py"}
	files.Cmd = append([]string{"python3", "-m", "pytest", "-q"}, files.Tests...)
	out["span collapsed to files"] = files

	short := span()
	short.Base = nil
	short.Cmd = []string{"pytest"}
	out["span whose Cmd is shorter than Tests"] = short

	noBase := span()
	noBase.Base = nil
	out["span with no Base"] = noBase

	order := lang.Selection{
		Base:  []string{"pytest"},
		Tests: []string{"t/a.py::one", "t/b.py::two", "t/c.py::three", "t/d.py::four"},
		Lines: map[string][]lang.LineRange{
			"t/a.py::one":   {{Start: 1, End: 10}},
			"t/b.py::two":   {{Start: 10, End: 19}},
			"t/c.py::three": {{Start: 17, End: 25}},
			"t/d.py::four":  {{Start: 40, End: 50}},
		},
		Method: "coverage-context",
	}
	order.Cmd = append(append([]string{}, order.Base...), order.Tests...)
	out["kill-likeliest order"] = order

	tie := lang.Selection{
		Base:   []string{"pytest"},
		Tests:  []string{"t/z.py::z", "t/a.py::a"},
		Lines:  map[string][]lang.LineRange{"t/z.py::z": {{Start: 5, End: 6}}, "t/a.py::a": {{Start: 5, End: 6}}},
		Method: "coverage-context",
	}
	tie.Cmd = append(append([]string{}, tie.Base...), tie.Tests...)
	out["tie broken by id"] = tie

	base := []string{"python3", "-m", "pytest", "-q"}
	out["authored onto a selection"] = lang.Selection{Cmd: append(append([]string{}, base...), "tests/test_calc.py::test_add"), Tests: []string{"tests/test_calc.py::test_add"}, Method: "coverage-context"}
	out["authored uncovered"] = lang.Selection{Method: "coverage-context"}
	out["deselect across a selection"] = lang.Selection{Base: base, Cmd: append(append([]string{}, base...), "tests/test_calc.py::test_add"), Tests: []string{"tests/test_calc.py::test_add"}}
	out["zero"] = lang.Selection{}
	return out
}

func compareForSpan(t *testing.T, e pyEquiv, sel lang.Selection, span lang.LineRange) {
	t.Helper()
	wantCmd, wantTests, wantRule := e.sel.ForSpan(sel, span)
	gotCmd, gotTests, gotRule := ForSpan(sel, e.row, span)
	if gotRule != wantRule || !reflect.DeepEqual(gotCmd, wantCmd) || !reflect.DeepEqual(gotTests, wantTests) {
		t.Errorf("ForSpan %v:\n engine %q %v %v\n python %q %v %v", span, gotRule, gotCmd, gotTests, wantRule, wantCmd, wantTests)
	}
}

func compareWithAuthored(t *testing.T, e pyEquiv, sel lang.Selection, testCmd []string, repoRoot, path string) {
	t.Helper()
	want := e.sel.WithAuthoredTest(sel, testCmd, path)
	got, ok := WithAuthoredTest(Map{Method: MethodCoverageContext}, sel, e.row, testCmd, repoRoot, path)
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("WithAuthoredTest %v + %s:\n engine %v ok=%v\n python %v", testCmd, path, got, ok, want)
	}
}

// The live fixture run (TestPythonSelectionEndToEndOnFixture's evidence):
// what pytest-cov emits today must select the same on both sides. Skips,
// never fails, when pytest-cov is absent, as that test does.
func TestEngineMatchesPythonSelectorOnLiveEvidence(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH")
	}
	if out, err := exec.Command("python3", "-c", "import pytest_cov, coverage").CombinedOutput(); err != nil {
		t.Skipf("pytest-cov not importable: %s", out)
	}
	e := newPyEquiv(t)
	fixture := pyTestdata(t, "pycov-fixture")
	argv := []string{"python3", "-m", "pytest", "-q"}
	cmd, ok := e.ev.Instrument(argv)
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
	sc := selectCase{evidence: out, root: fixture, code: "pkg/calc.py", test: "tests/test_calc.py", cmd: argv}
	want, wantErr := e.sel.Select(sc.evidence, sc.root, sc.code, sc.test, sc.cmd)
	got, gotErr := e.engineSelect(sc)
	if wantErr != nil || gotErr != nil {
		t.Fatalf("engine err %v, python err %v", gotErr, wantErr)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Selection:\n engine %#v\n python %#v", got, want)
	}
}
