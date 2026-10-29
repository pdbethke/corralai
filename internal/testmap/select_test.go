// SPDX-License-Identifier: Elastic-2.0

package testmap

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

func jestRow(t *testing.T) lang.Runner {
	t.Helper()
	p, ok := lang.ByName("javascript")
	if !ok {
		t.Fatal("no javascript plugin")
	}
	r, err := lang.RunnerByID(p, "jest")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func pytestRow(t *testing.T) lang.Runner {
	t.Helper()
	p, _ := lang.ByName("python")
	r, err := lang.RunnerByID(p, "pytest")
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// unitMap is a unit-grain map: each listed unit is a node, and src/a.js is
// executed by the units named in covering.
func unitMap(listed []string, covering ...string) Map {
	m := Map{Schema: Schema, Lang: "javascript", Runner: "jest", Method: MethodUnitEvidence, Of: len(listed),
		Sources: map[string]Source{"src/a.js": {HasStatements: true}, "src/b.js": {HasStatements: true}},
		Tests:   map[string]Node{}}
	for _, u := range listed {
		m.Tests[u] = Node{ID: u, Grain: GrainUnit, Files: []string{u}}
	}
	for _, u := range covering {
		m.Edges = append(m.Edges, Edge{Test: u, Source: "src/a.js", Executed: 3})
	}
	return m
}

var jestArgv = []string{"npx", "jest"}

func TestSelectUnitGrainUsesUnitsCmd(t *testing.T) {
	r := jestRow(t)
	m := unitMap([]string{"test/a.test.js", "test/b.test.js", "test/c.test.js"}, "test/c.test.js", "test/a.test.js")
	sel, err := Select(m, r, jestArgv, "", "src/a.js", "")
	if err != nil {
		t.Fatal(err)
	}
	units := []string{"test/a.test.js", "test/c.test.js"}
	want, ok := r.UnitsCmd(jestArgv, units)
	if !ok {
		t.Fatal("fixture: jest refused the units")
	}
	if !reflect.DeepEqual(sel.Cmd, want) || !reflect.DeepEqual(sel.Tests, units) {
		t.Errorf("Cmd = %v Tests = %v, want %v over %v", sel.Cmd, sel.Tests, want, units)
	}
	if sel.Method != MethodUnitEvidence || sel.Of != 3 || !reflect.DeepEqual(sel.Base, jestArgv) {
		t.Errorf("header: %+v", sel)
	}
	// A unit run has no per-line evidence: the per-mutant pass must not try
	// to narrow it, and must not call any span "unreached".
	if sel.Lines != nil || sel.Static != nil || sel.NarrowableByLine() {
		t.Errorf("a unit selection carries no line evidence: Lines=%v Static=%v", sel.Lines, sel.Static)
	}
	cmd, tests, rule := ForSpan(sel, r, lang.LineRange{Start: 1, End: 2})
	if rule != lang.SpanRuleFile || !reflect.DeepEqual(cmd, sel.Cmd) || !reflect.DeepEqual(tests, sel.Tests) {
		t.Errorf("ForSpan on a unit selection: %q %v %v", rule, cmd, tests)
	}

	// Measured with no unit executing it: uncovered, no command.
	unc, err := Select(m, r, jestArgv, "", "src/b.js", "")
	if err != nil || unc.Cmd != nil || len(unc.Tests) != 0 || unc.Method != MethodUnitEvidence {
		t.Errorf("uncovered unit file: %+v err=%v", unc, err)
	}
}

// Review Focus 4. Over the argv cap a test-grain selection widens to the
// units its nodes belong to (Python: node ids to files, as it always did) and
// drops line evidence keyed by ids that no longer run. Where Python already
// had an answer for a widened set that is STILL over the cap, it returned
// that set, and the engine keeps it (ledger ruling: Python's behaviour does
// not move). Only a unit-grain selection, which has nothing coarser to
// widen to and no earlier answer, refuses, naming the cap.
func TestSelectOverTheCapWidensThenRefuses(t *testing.T) {
	py := pytestRow(t)
	m := Map{Schema: Schema, Lang: "python", Method: MethodCoverageContext, Of: 3000,
		Sources: map[string]Source{"pkg/calc.py": {HasStatements: true, HasStatic: true, Static: []lang.LineRange{{Start: 1, End: 1}}}},
		Tests:   map[string]Node{}}
	for i := 0; i < 3000; i++ {
		id := fmt.Sprintf("tests/test_big.py::test_%04d_%s", i, strings.Repeat("x", 20))
		m.Tests[id] = Node{ID: id, Grain: GrainTest, Unit: py.TestUnit(id)}
		m.Edges = append(m.Edges, Edge{Test: id, Source: "pkg/calc.py", Lines: []lang.LineRange{{Start: 2, End: 2}}, Executed: 1})
	}
	sel, err := Select(m, py, []string{"pytest"}, "", "pkg/calc.py", "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sel.Tests, []string{"tests/test_big.py"}) || !reflect.DeepEqual(sel.Cmd, []string{"pytest", "tests/test_big.py"}) {
		t.Errorf("widened: Tests=%v Cmd=%v", sel.Tests, sel.Cmd)
	}
	if sel.Lines != nil || sel.Static != nil {
		t.Errorf("a widened selection carries no line evidence: %v %v", sel.Lines, sel.Static)
	}

	// Still over the cap after widening: 2000 distinct files. Python returned
	// the files; so does the engine.
	wide := Map{Schema: Schema, Lang: "python", Method: MethodCoverageContext, Of: 2000,
		Sources: map[string]Source{"pkg/calc.py": {HasStatements: true}}, Tests: map[string]Node{}}
	for i := 0; i < 2000; i++ {
		id := fmt.Sprintf("tests/test_%04d_%s.py::t", i, strings.Repeat("y", 20))
		wide.Tests[id] = Node{ID: id, Grain: GrainTest, Unit: py.TestUnit(id)}
		wide.Edges = append(wide.Edges, Edge{Test: id, Source: "pkg/calc.py"})
	}
	ws, err := Select(wide, py, []string{"pytest"}, "", "pkg/calc.py", "")
	if err != nil {
		t.Fatalf("a widened test-grain set over the cap keeps Python's answer, got %v", err)
	}
	if len(ws.Tests) != 2000 || !strings.HasSuffix(ws.Tests[0], ".py") || argvLen(ws.Tests) <= MaxArgv {
		t.Errorf("want the 2000 files, over the cap: %d %v", len(ws.Tests), ws.Tests[:1])
	}

	// Unit grain over the cap: nothing to widen to, so it refuses.
	var listed []string
	for i := 0; i < 2000; i++ {
		listed = append(listed, fmt.Sprintf("test/u%04d_%s.test.js", i, strings.Repeat("z", 20)))
	}
	um := unitMap(listed, listed...)
	_, err = Select(um, jestRow(t), jestArgv, "", "src/a.js", "")
	if err == nil || !strings.Contains(err.Error(), fmt.Sprint(MaxArgv)) {
		t.Errorf("unit grain over the cap must refuse naming the cap, got %v", err)
	}
}

// Never run a set nobody chose: a unit selection whose command would also
// collect a listed unit it does not name is refused, naming that unit.
func TestSelectUnitGrainRefusesACorunningSelection(t *testing.T) {
	m := unitMap([]string{"a.test.js", "sub/a.test.js", "b.test.js"}, "a.test.js")
	_, err := Select(m, jestRow(t), jestArgv, "", "src/a.js", "")
	if err == nil || !strings.Contains(err.Error(), "sub/a.test.js") {
		t.Errorf("a selection that coruns sub/a.test.js must refuse naming it, got %v", err)
	}
}

func TestSelectUnitGrainRefusesWhatTheRowRefuses(t *testing.T) {
	m := unitMap([]string{"a.test.js", "b.test.js"}, "a.test.js")
	if _, err := Select(m, jestRow(t), []string{"npx", "jest", "-t", "foo"}, "", "src/a.js", ""); err == nil {
		t.Error("an operator name filter makes UnitsCmd refuse; Select must refuse with it")
	}
}

// No defaults: a map whose method names no grain, or a row with no command
// for the map's grain, is refused rather than guessed at.
func TestSelectRefusesAnUnknownMethodAndAMissingRowCommand(t *testing.T) {
	m := unitMap([]string{"a.test.js"}, "a.test.js")
	m.Method = ""
	if _, err := Select(m, jestRow(t), jestArgv, "", "src/a.js", ""); err == nil {
		t.Error("a map with no method must be refused")
	}
	m.Method = MethodUnitEvidence
	if _, err := Select(m, pytestRow(t), []string{"pytest"}, "", "src/a.js", ""); err == nil {
		t.Error("a row with no UnitsCmd cannot run a unit selection")
	}
	tm := Map{Method: MethodCoverageContext, Lang: "javascript", Sources: map[string]Source{"src/a.js": {}},
		Tests: map[string]Node{"x": {ID: "x", Grain: GrainTest, Unit: "x"}}, Edges: []Edge{{Test: "x", Source: "src/a.js"}}}
	if _, err := Select(tm, jestRow(t), jestArgv, "", "src/a.js", ""); err == nil {
		t.Error("a row with no TestsCmd cannot run a test selection")
	}
	tm.Tests["x"] = Node{ID: "x", Grain: GrainUnit}
	if _, err := Select(tm, pytestRow(t), []string{"pytest"}, "", "src/a.js", ""); err == nil {
		t.Error("a node whose grain disagrees with the map's method must be refused")
	}
}

// Review Focus 2: the authored test runs in the unit it was written into,
// even when the evidence never listed that unit.
func TestWithAuthoredTestAddsTheAuthoredUnit(t *testing.T) {
	r := jestRow(t)
	m := unitMap([]string{"test/a.test.js", "test/b.test.js"}, "test/a.test.js")
	sel, err := Select(m, r, jestArgv, "", "src/a.js", "")
	if err != nil {
		t.Fatal(err)
	}
	got, ok := WithAuthoredTest(m, sel, r, jestArgv, "/repo", "test/corral_a.test.js")
	want, _ := r.UnitsCmd(jestArgv, []string{"test/a.test.js", "test/corral_a.test.js"})
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("got %v ok=%v, want %v", got, ok, want)
	}

	// Uncovered: the authored unit alone.
	unc, _ := Select(m, r, jestArgv, "", "src/b.js", "")
	got, ok = WithAuthoredTest(m, unc, r, jestArgv, "/repo", "test/corral_b.test.js")
	want, _ = r.UnitsCmd(jestArgv, []string{"test/corral_b.test.js"})
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("uncovered: got %v ok=%v, want %v", got, ok, want)
	}

	// Already selected: named once (UnitsCmd refuses a list that is not a set).
	got, ok = WithAuthoredTest(m, sel, r, jestArgv, "/repo", "test/a.test.js")
	want, _ = r.UnitsCmd(jestArgv, []string{"test/a.test.js"})
	if !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("authored into a selected unit: got %v ok=%v, want %v", got, ok, want)
	}
}

func TestWithAuthoredTestRefusesWhatItCannotRunExactly(t *testing.T) {
	r := jestRow(t)
	// The authored unit's own pattern would also collect a listed unit
	// nobody named.
	m := unitMap([]string{"a.test.js", "sub/corral.test.js"}, "a.test.js")
	sel, err := Select(m, r, jestArgv, "", "src/a.js", "")
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := WithAuthoredTest(m, sel, r, jestArgv, "/repo", "corral.test.js"); ok {
		t.Errorf("the authored unit coruns sub/corral.test.js; got %v", got)
	}
	// No unit holds the file.
	if got, ok := WithAuthoredTest(m, sel, r, jestArgv, "/repo", "/abs/elsewhere.test.js"); ok {
		t.Errorf("an authored file no unit holds must refuse; got %v", got)
	}
	// The row refuses the operator's command.
	if got, ok := WithAuthoredTest(m, sel, r, []string{"npx", "jest", "-t", "x"}, "/repo", "corral2.test.js"); ok {
		t.Errorf("a name filter in argv must refuse; got %v", got)
	}
	// A row with no unit commands cannot take an authored unit.
	if got, ok := WithAuthoredTest(m, sel, pytestRow(t), []string{"pytest"}, "/repo", "corral2.test.js"); ok {
		t.Errorf("pytest has no UnitsCmd; got %v", got)
	}
}

// A file with no executable statements, and one only loaded (static)
// coverage reached, are neither uncovered nor graded-by-unreported: the first
// has nothing to execute, the second was executed.
func TestUncoveredSkipsStatementlessAndStaticFiles(t *testing.T) {
	m := Map{
		Method: MethodUnitEvidence,
		Of:     1,
		Sources: map[string]Source{
			"real.ts":  {HasStatements: true},
			"types.ts": {HasStatements: false},
			"load.ts":  {HasStatements: true, HasStatic: true},
		},
		Tests: map[string]Node{"u": {ID: "u", Grain: GrainUnit}},
	}
	unc, graded := Uncovered(m)
	if len(unc) != 1 || unc[0] != "real.ts" || len(graded) != 0 {
		t.Errorf("uncovered %v, graded-by-unreported %v; want only real.ts uncovered", unc, graded)
	}
	m.Tests["bad"] = Node{ID: "bad", Grain: GrainUnit, Err: "failed"}
	unc, graded = Uncovered(m)
	if len(unc) != 0 || len(graded) != 1 || graded[0] != "real.ts" {
		t.Errorf("with an unreported unit: uncovered %v, graded %v; want only real.ts graded", unc, graded)
	}
}

// A test that FAILED in the coverage-context run (a node with an Err) is a
// test-grain unreported node: its coverage is not evidence, so it has no
// edge, and like an unreported unit it joins every file's selection (#164:
// a failing test must never silently leave a narrowed baseline). Every
// mutant's per-span command runs it too (Selection.Always), since no line
// evidence says which spans it reaches; a file no other test executes is
// then graded by it alone, not "uncovered".
func TestSelectTestGrainAddsFailedTestsToEverySelectionAndEverySpan(t *testing.T) {
	r := pytestRow(t)
	m := Map{Schema: Schema, Lang: "python", Runner: "pytest", Method: MethodCoverageContext, Of: 3,
		Sources: map[string]Source{"a.py": {HasStatements: true}, "b.py": {HasStatements: true}},
		Tests: map[string]Node{
			"tests/test_a.py::test_one": {ID: "tests/test_a.py::test_one", Grain: GrainTest, Unit: "tests/test_a.py"},
			"tests/test_a.py::test_two": {ID: "tests/test_a.py::test_two", Grain: GrainTest, Unit: "tests/test_a.py"},
			"tests/test_x.py::test_red": {ID: "tests/test_x.py::test_red", Grain: GrainTest, Unit: "tests/test_x.py", Err: "run failed: failed in the instrumented evidence run"},
		},
		Edges: []Edge{
			{Test: "tests/test_a.py::test_one", Source: "a.py", Lines: []lang.LineRange{{Start: 1, End: 2}}, Executed: 2},
			{Test: "tests/test_a.py::test_two", Source: "a.py", Lines: []lang.LineRange{{Start: 5, End: 6}}, Executed: 2},
		}}
	argv := []string{"pytest"}

	sel, err := Select(m, r, argv, "", "a.py", "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"tests/test_a.py::test_one", "tests/test_a.py::test_two", "tests/test_x.py::test_red"}
	if !reflect.DeepEqual(sel.Tests, want) || !reflect.DeepEqual(sel.Always, []string{"tests/test_x.py::test_red"}) || UnreportedIn(m, sel) != 1 {
		t.Fatalf("Tests=%v Always=%v Unreported=%d; want %v with the failed test always run", sel.Tests, sel.Always, UnreportedIn(m, sel), want)
	}
	_, tests, rule := ForSpan(sel, r, lang.LineRange{Start: 1, End: 1})
	if rule != lang.SpanRuleLines || !reflect.DeepEqual(tests, []string{"tests/test_a.py::test_one", "tests/test_x.py::test_red"}) {
		t.Errorf("span 1: rule=%s tests=%v, want the test reaching it plus the failed one", rule, tests)
	}

	// b.py: no passing test executes it, so the failed test grades it.
	only, err := Select(m, r, argv, "", "b.py", "")
	if err != nil || !reflect.DeepEqual(only.Tests, []string{"tests/test_x.py::test_red"}) {
		t.Errorf("b.py: %+v err=%v, want the failed test alone", only, err)
	}
	unc, _ := Uncovered(m)
	if slices.Contains(unc, "b.py") {
		t.Errorf("b.py is graded by the failed test, so it is not uncovered: %v", unc)
	}

	// Negative control: no failed test, Always is nil and nothing changes.
	clean := m
	clean.Tests = map[string]Node{"tests/test_a.py::test_one": m.Tests["tests/test_a.py::test_one"], "tests/test_a.py::test_two": m.Tests["tests/test_a.py::test_two"]}
	s, err := Select(clean, r, argv, "", "a.py", "")
	if err != nil || s.Always != nil || len(s.Tests) != 2 {
		t.Errorf("all green: %+v err=%v", s, err)
	}
}
