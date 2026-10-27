// SPDX-License-Identifier: Elastic-2.0

package testmap

import (
	"reflect"
	"strings"
	"testing"
)

// failUnit marks a listed unit of m as having contributed no evidence, the
// way MapFromUnits copies a unit pass's Err.
func failUnit(m Map, unit, why string) Map {
	n := m.Tests[unit]
	n.Err = why
	m.Tests[unit] = n
	return m
}

// Review Focus 1. A unit whose own run failed for a reason that is not the
// code's (`could not run:`, `coverage unavailable:`) may be the very unit
// that executes the file: its silence is not evidence of absence. So it is
// added to EVERY file's selection, including a file no reported unit
// executes, which is then not uncovered: the unreported unit is what it is
// graded by. UnreportedIn counts them back out of the selection, for the
// verdict to disclose.
func TestSelectUnitGrainAddsEveryUnreportedUnit(t *testing.T) {
	r := jestRow(t)
	m := failUnit(unitMap([]string{"test/a.test.js", "test/b.test.js", "test/c.test.js"}, "test/a.test.js"),
		"test/c.test.js", "could not run: exit status 1")

	sel, err := Select(m, r, jestArgv, "", "src/a.js", "")
	if err != nil {
		t.Fatal(err)
	}
	units := []string{"test/a.test.js", "test/c.test.js"}
	want, _ := r.UnitsCmd(jestArgv, units)
	if !reflect.DeepEqual(sel.Tests, units) || !reflect.DeepEqual(sel.Cmd, want) || UnreportedIn(m, sel) != 1 {
		t.Errorf("covered file: Tests=%v Cmd=%v Unreported=%d; want %v, %v, 1", sel.Tests, sel.Cmd, UnreportedIn(m, sel), units, want)
	}

	unc, err := Select(m, r, jestArgv, "", "src/b.js", "")
	if err != nil {
		t.Fatal(err)
	}
	only := []string{"test/c.test.js"}
	want, _ = r.UnitsCmd(jestArgv, only)
	if !reflect.DeepEqual(unc.Tests, only) || !reflect.DeepEqual(unc.Cmd, want) || UnreportedIn(m, unc) != 1 {
		t.Errorf("file no reported unit executes: Tests=%v Cmd=%v Unreported=%d; want the unreported unit %v run by %v", unc.Tests, unc.Cmd, UnreportedIn(m, unc), only, want)
	}

	// Negative control: with every unit reported nothing is added, and the
	// count says none was.
	clean := unitMap([]string{"test/a.test.js", "test/b.test.js"}, "test/a.test.js")
	s, err := Select(clean, r, jestArgv, "", "src/a.js", "")
	if err != nil || !reflect.DeepEqual(s.Tests, []string{"test/a.test.js"}) || UnreportedIn(clean, s) != 0 {
		t.Errorf("all reported: %+v err=%v", s, err)
	}
}

// Review Focus 5. A unit pass that reported no unit at all (it timed out
// before any finished) has no evidence to select by. Adding every unreported
// unit would build a command of every listed unit — the whole suite under a
// name that says it was narrowed — so the engine refuses, carrying the
// pass's own note, and the file grades whole-suite with it. The refusal
// comes before the file's own lookup, whose "never saw" would misname why.
func TestSelectUnitGrainWithNoReportedUnitRefusesWithThePassNote(t *testing.T) {
	m := unitMap([]string{"test/a.test.js", "test/b.test.js"})
	m.Sources = map[string]Source{}
	m = failUnit(m, "test/a.test.js", "not run (time limit)")
	m = failUnit(m, "test/b.test.js", "not run (time limit)")
	m.Note = "the unit pass stopped at its 30m0s time limit"

	_, err := Select(m, jestRow(t), jestArgv, "", "src/a.js", "")
	if err == nil {
		t.Fatal("a map with no reported unit must refuse to select")
	}
	for _, want := range []string{"no unit", "the unit pass stopped at its 30m0s time limit"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal %q does not say %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "never saw") {
		t.Errorf("refusal %q blames the file, not the pass", err)
	}
}

// A unit-evidence edge with nothing executed records that the unit's run
// MEASURED the file (a Go run with -coverpkg measures every package it
// loads) — not that it executed it. Such an edge selects no unit: were it
// to, every file a broad coverage run measured would be graded by every
// unit, the whole suite under a narrowed name. The candidacy index skips the
// same edges for the same reason.
func TestSelectUnitGrainIgnoresAnEdgeThatExecutedNothing(t *testing.T) {
	r := jestRow(t)
	m := unitMap([]string{"test/a.test.js", "test/b.test.js"}, "test/a.test.js")
	m.Edges = append(m.Edges, Edge{Test: "test/b.test.js", Source: "src/a.js"}, Edge{Test: "test/b.test.js", Source: "src/b.js"})

	sel, err := Select(m, r, jestArgv, "", "src/a.js", "")
	if err != nil || !reflect.DeepEqual(sel.Tests, []string{"test/a.test.js"}) {
		t.Errorf("covered: Tests=%v err=%v, want only the unit that executed it", sel.Tests, err)
	}
	unc, err := Select(m, r, jestArgv, "", "src/b.js", "")
	if err != nil || len(unc.Tests) != 0 || unc.Cmd != nil || unc.Method != MethodUnitEvidence {
		t.Errorf("measured, executed by no unit: %+v err=%v, want uncovered", unc, err)
	}
}

// The authored pass's refusal is a disclosure, so it carries its reason:
// AuthoredTestCmd says what WithAuthoredTest only reports as false.
func TestAuthoredTestCmdSaysWhyItRefuses(t *testing.T) {
	r := jestRow(t)
	m := unitMap([]string{"a.test.js", "sub/corral.test.js"}, "a.test.js")
	sel, err := Select(m, r, jestArgv, "", "src/a.js", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := AuthoredTestCmd(m, sel, r, jestArgv, "/repo", "corral.test.js"); err == nil || !strings.Contains(err.Error(), "sub/corral.test.js") {
		t.Errorf("a corunning authored unit must refuse naming the unit it would also run, got %v", err)
	}
	if _, err := AuthoredTestCmd(m, sel, r, jestArgv, "/repo", "/abs/elsewhere.test.js"); err == nil || !strings.Contains(err.Error(), "/abs/elsewhere.test.js") {
		t.Errorf("an authored file no unit holds must refuse naming it, got %v", err)
	}
	got, err := AuthoredTestCmd(m, sel, r, jestArgv, "/repo", "test/new.test.js")
	want, _ := r.UnitsCmd(jestArgv, []string{"a.test.js", "test/new.test.js"})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("got %v err=%v, want %v", got, err, want)
	}
}
