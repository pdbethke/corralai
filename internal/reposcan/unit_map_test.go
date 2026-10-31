// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/testmap"
)

// unitMapEvidence is a unit pass with every shape the index treats
// differently: a unit that executed a file, one that only measured it, one
// that loaded a file without running it, and one that failed.
func unitMapEvidence() UnitEvidence {
	return UnitEvidence{Ran: true, Note: "one unit failed", Units: []UnitResult{
		{Unit: "test/alpha.test.ts", Counts: map[string]lang.LineCounts{
			"src/x.ts": {Executed: 7, Executable: 10},
			"src/y.ts": {Executed: 0, Executable: 4},
			"src/z.ts": {Executed: 0, Executable: 0},
		}, Loaded: []string{"src/z.ts"}},
		{Unit: "test/beta.test.ts", Counts: map[string]lang.LineCounts{"src/x.ts": {Executed: 2, Executable: 10}}},
		{Unit: "test/broken.test.ts", Err: "run failed: exit 1"},
	}}
}

func unitMapFiles(p lang.Plugin) UnitFiles {
	return fileUnitLanding(p, "test/alpha.test.ts", "test/beta.test.ts", "test/broken.test.ts", "src/x.ts", "src/y.ts", "src/z.ts")
}

func TestMapFromUnitsKeepsEveryListedUnit(t *testing.T) {
	p, _ := lang.Detect("a.ts")
	inv := lang.Cmd([]string{"vitest", "run"})
	ev := unitMapEvidence()
	m := MapFromUnits(ev, unitMapFiles(p), inv, p)

	if m.Method != testmap.MethodUnitEvidence || m.Schema != testmap.Schema || m.Of != 3 || m.Note != ev.Note {
		t.Fatalf("header: %+v", m)
	}
	if m.Lang != p.Name() || m.Runner == "" || !reflect.DeepEqual(m.Command, inv.Argv) {
		t.Errorf("how the evidence was collected is not recorded: lang %q runner %q command %v", m.Lang, m.Runner, m.Command)
	}
	if len(m.Tests) != 3 {
		t.Fatalf("every listed unit is a node, failed ones too: %v", m.Tests)
	}
	for id, n := range m.Tests {
		if n.ID != id || n.Grain != testmap.GrainUnit || n.Unit != "" {
			t.Errorf("node %s: %+v", id, n)
		}
	}
	if got := m.Tests["test/broken.test.ts"].Err; got != "run failed: exit 1" {
		t.Errorf("Err not copied: %q", got)
	}
	if got := m.Tests["test/alpha.test.ts"].Files; !reflect.DeepEqual(got, []string{"test/alpha.test.ts"}) {
		t.Errorf("Files = %v", got)
	}
	if got := m.Unreported(); !reflect.DeepEqual(got, []string{"test/broken.test.ts"}) {
		t.Errorf("Unreported = %v", got)
	}
	if len(m.Edges) != 4 {
		t.Fatalf("one edge per (unit, source): %+v", m.Edges)
	}
	for _, e := range m.Edges {
		if e.Lines != nil {
			t.Errorf("a unit edge has no line ranges: %+v", e)
		}
	}
	if e := m.EdgesTo("src/x.ts"); len(e) != 2 || e[0].Test != "test/alpha.test.ts" || e[0].Executed != 7 || e[1].Executed != 2 {
		t.Errorf("edges to x: %+v", e)
	}
	if e := m.EdgesTo("src/y.ts"); len(e) != 1 || e[0].Executed != 0 {
		t.Errorf("a measured-but-unexecuted file keeps its zero edge: %+v", e)
	}
	if s := m.Sources["src/y.ts"]; !s.HasStatements || s.HasStatic {
		t.Errorf("y: %+v", s)
	}
	if s := m.Sources["src/z.ts"]; s.HasStatements || !s.HasStatic {
		t.Errorf("a loaded file is static (import-only, R24): %+v", s)
	}
}

func TestIndexFromUnitsIsUnchanged(t *testing.T) {
	p, _ := lang.Detect("a.ts")
	uf := unitMapFiles(p)
	cases := map[string]UnitEvidence{
		"partial pass": unitMapEvidence(),
		"not run":      {Note: "no units"},
		"all failed":   {Ran: true, Units: []UnitResult{{Unit: "u", Err: "boom"}}},
		"clean pass": {Ran: true, Units: []UnitResult{
			{Unit: "test/alpha.test.ts", Counts: map[string]lang.LineCounts{"src/x.ts": {Executed: 1, Executable: 2}, "src/y.ts": {Executable: 3}}},
			{Unit: "test/beta.test.ts", Counts: map[string]lang.LineCounts{"src/x.ts": {Executed: 5, Executable: 2}}, Loaded: []string{"src/x.ts"}},
		}},
	}
	for name, ev := range cases {
		wantIdx, wantOK := legacyIndexFromUnits(ev, uf)
		gotIdx, gotOK := IndexFromUnits(ev, uf)
		if gotOK != wantOK || !reflect.DeepEqual(gotIdx, wantIdx) {
			t.Errorf("%s: IndexFromUnits = (%+v, %v), want (%+v, %v)", name, gotIdx, gotOK, wantIdx, wantOK)
		}
	}
}

// legacyIndexFromUnits is IndexFromUnits as it was before it became a view of
// the map, frozen so the equivalence is judged against what the index
// answered and not against itself.
func legacyIndexFromUnits(ev UnitEvidence, uf UnitFiles) (EvidenceIndex, bool) {
	if !ev.Ran {
		return EvidenceIndex{}, false
	}
	type tally struct {
		executed, hasStatements, loaded bool
		units                           int
		perFile                         map[string]int
		compiled                        map[string]bool
	}
	tallies := map[string]*tally{}
	succeeded, partial := 0, false
	for _, u := range ev.Units {
		if u.Err != "" {
			partial = true
			continue
		}
		succeeded++
		loaded := make(map[string]bool, len(u.Loaded))
		for _, f := range u.Loaded {
			loaded[f] = true
		}
		for src, lc := range u.Counts {
			t := tallies[src]
			if t == nil {
				t = &tally{perFile: map[string]int{}, compiled: map[string]bool{}}
				tallies[src] = t
			}
			t.hasStatements = t.hasStatements || lc.Executable > 0
			t.loaded = t.loaded || loaded[src]
			if lc.Executed == 0 {
				continue
			}
			t.executed = true
			landing := uf.Landing(u.Unit, src)
			if landing == "" {
				continue
			}
			t.units++
			t.perFile[landing] += lc.Executed
			t.compiled[landing] = true
			for _, tf := range uf.TestFiles(u.Unit) {
				t.compiled[tf] = true
			}
		}
	}
	if succeeded == 0 {
		return EvidenceIndex{}, false
	}

	files := make(map[string]evidenceFileEntry, len(tallies))
	for src, t := range tallies {
		if t.units == 0 && (t.executed || partial) {
			continue
		}
		best, bestLines := "", -1
		for testFile, lines := range t.perFile {
			if lines > bestLines || (lines == bestLines && moreSpecificTestPath(testFile, best)) {
				best, bestLines = testFile, lines
			}
		}
		files[src] = evidenceFileEntry{
			coveringTests: t.units,
			coveringUnits: t.units,
			mostCovering:  best,
			coveringFiles: t.compiled,
			// A unit that loaded the file ran its module-level code: the
			// unit-pass counterpart of a per-test index's static coverage,
			// which is what makes an unexecuted file import-only.
			hasStatic:     t.loaded,
			hasStatements: t.hasStatements,
		}
	}
	return EvidenceIndex{files: files, unitKind: true}, true
}

// A file of the unit pass's language is answered by the engine from the
// pass's map: the units that executed it, plus every unit that reported
// nothing. A zero-executed edge (alpha measured src/y.ts and ran none of it)
// selects nothing, so y is graded by the failed unit alone.
func TestUnitSelectionForSelectsThroughTheEngine(t *testing.T) {
	p, _ := lang.Detect("a.ts")
	argv := []string{"vitest", "run"}
	m := MapFromUnits(unitMapEvidence(), unitMapFiles(p), lang.Cmd(argv), p)

	sel := UnitSelectionFor(m, p, "", "src/x.ts", "test/alpha.test.ts", argv)
	want := []string{"test/alpha.test.ts", "test/beta.test.ts", "test/broken.test.ts"}
	if sel.Fallback != "" || sel.Method != testmap.MethodUnitEvidence || !reflect.DeepEqual(sel.Tests, want) || sel.Of != 3 {
		t.Fatalf("x: %+v, want the two units that ran it and the one that reported nothing", sel)
	}
	if n := testmap.UnreportedIn(m, sel); n != 1 {
		t.Errorf("x: %d unreported unit(s) in the selection, want 1", n)
	}
	if y := UnitSelectionFor(m, p, "", "src/y.ts", "test/alpha.test.ts", argv); !reflect.DeepEqual(y.Tests, []string{"test/broken.test.ts"}) {
		t.Errorf("y: %+v, want the unreported unit alone", y)
	}
}

// No defaults: a unit map that names no runner row (a language with no row
// for the command) is refused by name, and the file grades whole-suite with
// that reason — never with a row nobody chose.
func TestUnitSelectionForRefusesAMapNamingNoRunner(t *testing.T) {
	p, _ := lang.Detect("a.ts")
	argv := []string{"vitest", "run"}
	m := MapFromUnits(unitMapEvidence(), unitMapFiles(p), lang.Cmd(argv), p)
	m.Runner = ""
	sel := UnitSelectionFor(m, p, "", "src/x.ts", "test/alpha.test.ts", argv)
	if sel.Method != "" || sel.Cmd != nil || !strings.Contains(sel.Fallback, "names no runner row") {
		t.Errorf("selection = %+v, want a whole-suite fallback naming the missing runner row", sel)
	}

	// The map answers for its own language, and for no language whose
	// instrumented command is a different run: a Go file is not answered
	// from a vitest map. (Until 2026-10-29 this said "its own language
	// only", which also refused the language that SHARES the run — see
	// TestUnitSelectionForAnswersEveryLanguageOfTheSameRun.)
	golang, _ := lang.ByName("go")
	m.Runner = "vitest"
	if sel := UnitSelectionFor(m, golang, "", "x.go", "", argv); sel.Method != "" || !strings.Contains(sel.Fallback, "typescript") {
		t.Errorf("a go file read from a typescript map: %+v", sel)
	}
}

// JavaScript and TypeScript share one instrumented run (one vitest command
// collects both), so the scan runs it once and names the map after the
// first of them alphabetically: "javascript". A TypeScript file must be
// answered from that map like a JavaScript one. It was refused, so every
// TypeScript file of a mixed repo graded whole-suite though the evidence
// paired it: on REA, 1,784 of 1,881 auditable files.
func TestUnitSelectionForAnswersEveryLanguageOfTheSameRun(t *testing.T) {
	js, _ := lang.ByName("javascript")
	ts, _ := lang.ByName("typescript")
	argv := []string{"vitest", "run"}
	m := MapFromUnits(unitMapEvidence(), unitMapFiles(ts), lang.Cmd(argv), js)
	if m.Lang != "javascript" {
		t.Fatalf("fixture: map lang %q", m.Lang)
	}
	sel := UnitSelectionFor(m, ts, "", "src/x.ts", "test/alpha.test.ts", argv)
	want := []string{"test/alpha.test.ts", "test/beta.test.ts", "test/broken.test.ts"}
	if sel.Fallback != "" || sel.Method != testmap.MethodUnitEvidence || !reflect.DeepEqual(sel.Tests, want) {
		t.Errorf("a typescript file from the shared javascript map: %+v, want %v", sel, want)
	}
}

// Fix round 1. A unit that measured a file and could not count it
// (UnitResult.Uncounted) has no Err and no count for the file, and was left
// out of its selection: under-selection the evidence does not support. It is
// an edge now, and selects its unit. So does a unit that only LOADED the
// file; a unit that measured it with nothing executed and did not load it
// does not.
func TestUnitSelectionForSelectsUnitsThatLoadedOrCouldNotCount(t *testing.T) {
	p, _ := lang.Detect("a.ts")
	argv := []string{"vitest", "run"}
	ev := UnitEvidence{Ran: true, Units: []UnitResult{
		{Unit: "test/alpha.test.ts", Counts: map[string]lang.LineCounts{"src/x.ts": {Executed: 3, Executable: 5}}},
		{Unit: "test/beta.test.ts", Uncounted: []string{"src/x.ts: no line table"}},
		{Unit: "test/gamma.test.ts", Counts: map[string]lang.LineCounts{"src/x.ts": {Executed: 0, Executable: 5}}, Loaded: []string{"src/x.ts"}},
		{Unit: "test/delta.test.ts", Counts: map[string]lang.LineCounts{"src/x.ts": {Executed: 0, Executable: 5}}},
	}}
	m := MapFromUnits(ev, fileUnitLanding(p, "test/alpha.test.ts", "test/beta.test.ts", "test/gamma.test.ts", "test/delta.test.ts", "src/x.ts"), lang.Cmd(argv), p)
	sel := UnitSelectionFor(m, p, "", "src/x.ts", "test/alpha.test.ts", argv)
	want := []string{"test/alpha.test.ts", "test/beta.test.ts", "test/gamma.test.ts"}
	if sel.Fallback != "" || !reflect.DeepEqual(sel.Tests, want) {
		t.Errorf("selection = %+v, want %v", sel, want)
	}
	// The uncounting unit adds no candidacy: the index still reads only
	// executed counts.
	idx, ok := IndexFromUnits(ev, fileUnitLanding(p, "test/alpha.test.ts", "test/beta.test.ts", "test/gamma.test.ts", "test/delta.test.ts", "src/x.ts"))
	if n, _ := idx.UnitsFor("src/x.ts"); !ok || n != 1 {
		t.Errorf("index covering units for x = %d (ok %v), want 1: an uncounted or loaded-only unit is not a covering unit", n, ok)
	}
}

// A red baseline's retry. WithoutRedUnits answers the same file again with
// the units whose own run FAILED in the pass left out, and names them. Only
// "run failed" is red: a unit the time limit cut or whose jail never started
// proved nothing about the code, so it stays. A selection holding no red
// unit has nothing to retry without, and says so by returning no units.
func TestWithoutRedUnitsDropsOnlyUnitsWhoseRunFailed(t *testing.T) {
	p, _ := lang.Detect("a.ts")
	argv := []string{"vitest", "run"}
	ev := unitMapEvidence()
	ev.Units[2].Err = unitRunFailed + "exit 1"
	ev.Units = append(ev.Units, UnitResult{Unit: "test/late.test.ts", Err: unitNotRunTimeLimit})
	files := fileUnitLanding(p, "test/alpha.test.ts", "test/beta.test.ts", "test/broken.test.ts", "test/late.test.ts", "src/x.ts", "src/y.ts", "src/z.ts")
	m := MapFromUnits(ev, files, lang.Cmd(argv), p)

	sel := UnitSelectionFor(m, p, "", "src/x.ts", "test/alpha.test.ts", argv)
	retry, dropped := WithoutRedUnits(m, p, "", "src/x.ts", "test/alpha.test.ts", argv, sel)
	if !reflect.DeepEqual(dropped, []string{"test/broken.test.ts"}) {
		t.Fatalf("dropped = %v, want the one unit whose run failed", dropped)
	}
	want := []string{"test/alpha.test.ts", "test/beta.test.ts", "test/late.test.ts"}
	if retry.Fallback != "" || retry.Method != testmap.MethodUnitEvidence || !reflect.DeepEqual(retry.Tests, want) {
		t.Errorf("retry = %+v, want %v through the engine", retry, want)
	}

	// Negative control: no red unit, nothing to retry.
	ev = unitMapEvidence()
	ev.Units[2].Err = unitNotRunTimeLimit
	clean := MapFromUnits(ev, unitMapFiles(p), lang.Cmd(argv), p)
	s := UnitSelectionFor(clean, p, "", "src/x.ts", "test/alpha.test.ts", argv)
	if _, dropped := WithoutRedUnits(clean, p, "", "src/x.ts", "test/alpha.test.ts", argv, s); dropped != nil {
		t.Errorf("no red unit: dropped = %v, want nil", dropped)
	}
}

// A coverage-context document that names failed tests builds a map in which
// each is a node whose own run FAILED (the reason WithoutRedUnits keys on)
// with no edge — a failing test's coverage is not evidence — so it joins
// every selection, and a red baseline may be retried without it.
func TestBuildSelectionMapMarksFailedTestsRedAndDropsTheirCoverage(t *testing.T) {
	py, _ := lang.ByName("python")
	argv := []string{"python3", "-m", "pytest", "-q"}
	raw := []byte(`{"format":"corral-selection-3","tests":2,"failed":["tests/test_calc.py::test_red"],"files":{"calc.py":{` +
		`"tests":["tests/test_calc.py::test_green","tests/test_calc.py::test_red"],` +
		`"lines":{"0":[[1,2]],"1":[[4,5]]},"static":[],"statements":4}}}`)
	m, err := BuildSelectionMap(py, argv, raw)
	if err != nil {
		t.Fatal(err)
	}
	red := m.Tests["tests/test_calc.py::test_red"]
	if !strings.HasPrefix(red.Err, unitRunFailed) || red.Grain != testmap.GrainTest {
		t.Errorf("failed test node = %+v, want a red test-grain node", red)
	}
	for _, e := range m.Edges {
		if e.Test == "tests/test_calc.py::test_red" {
			t.Errorf("the failed test kept an edge: %+v", e)
		}
	}
	sel := (SelectionEvidence{Ran: true, Map: m}).For(py, "", "calc.py", "tests/test_calc.py", argv)
	if !reflect.DeepEqual(sel.Tests, []string{"tests/test_calc.py::test_green", "tests/test_calc.py::test_red"}) || !reflect.DeepEqual(sel.Always, []string{"tests/test_calc.py::test_red"}) {
		t.Fatalf("selection %+v, want the green test plus the red one, always run", sel)
	}
	retry, dropped := WithoutRedUnits(m, py, "", "calc.py", "tests/test_calc.py", argv, sel)
	if !reflect.DeepEqual(dropped, []string{"tests/test_calc.py::test_red"}) || !reflect.DeepEqual(retry.Tests, []string{"tests/test_calc.py::test_green"}) || retry.Always != nil {
		t.Errorf("retry %+v dropped %v, want the green test alone", retry, dropped)
	}
}
