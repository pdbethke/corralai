// SPDX-License-Identifier: Elastic-2.0

package testmap

import (
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"sort"

	"github.com/pdbethke/corralai/internal/lang"
)

// The selection engine: the one place a map and a runner row become a
// lang.Selection and the commands that run it. It knows no language. Every
// language fact it needs is a row field (CollectionBase, TestsCmd, TestUnit,
// CarryArgs for test-grain maps; UnitsCmd, UnitsCoruns, UnitForFile for
// unit-grain ones), and every selection fact is in the map. Its rules are
// the ones Python's selector applied (pyPlugin's Select, ForSpan and
// WithAuthoredTest, deleted once every caller used this engine), and
// python_equivalence_test.go holds the engine to that selector's recorded
// output on every input its tests used: moving the rules here must not move
// what Python selects.

// MaxArgv bounds the selection a command line carries, measured as argvLen.
// Past it a test-grain selection names the units its tests live in instead —
// a superset, still the evidence's, never the whole suite. 32 KiB sits far
// below any kernel's argument limit; it is the size at which a command stops
// being something a person can read in a record.
const MaxArgv = 32 * 1024

// grainOf is the node grain a map's method produces. A method this build
// does not know is refused, never read as one or the other: the grain
// decides which row command runs the selection.
func grainOf(m Map) (string, error) {
	switch m.Method {
	case MethodCoverageContext:
		return GrainTest, nil
	case MethodUnitEvidence:
		return GrainUnit, nil
	}
	return "", fmt.Errorf("testmap: %s map has method %q, which names no grain; want %q or %q", m.Lang, m.Method, MethodCoverageContext, MethodUnitEvidence)
}

// Select narrows testCmd to the tests (or units) whose recorded execution
// reached codePath.
//
//   - codePath absent from the map is an error, never "uncovered": a file a
//     run did not measure may be missing for reasons unrelated to whether
//     the suite executed it (an editable install measured outside the root).
//     The message says whether its paired test was seen, as Python's did.
//   - codePath measured with no edge is uncovered: a Selection with Method,
//     Of and Base and no Cmd.
//   - At unit grain, a map in which no unit reported evidence is refused
//     before the file is looked up, with the pass's own Note: every unit
//     would be "unreported", and a command of all of them is the whole
//     suite under a name that says it was narrowed (Review Focus 5). Only
//     an edge whose unit executed something selects it — a zero-executed
//     edge records a measurement, not an execution — and every unreported
//     unit joins every selection, so partial evidence never under-selects
//     (Review Focus 1). A measured file no reported unit executes is then
//     graded by the unreported units, and is uncovered only when there are
//     none.
//   - Otherwise the ids are sorted so the command, and the cache key built
//     from it, is stable. A test-grain set over MaxArgv widens to its nodes'
//     units and drops its line evidence, which is keyed by ids that will no
//     longer run. A unit-grain set has nothing coarser to widen to and
//     refuses over the cap; it also refuses when its command would run a
//     listed unit it does not name, so no run collects a set nobody chose.
//
// Base is the row's CollectionBase of testCmd (so appending narrows rather
// than joining a directory testCmd already names), or testCmd for a row
// whose arguments narrow as they are.
func Select(m Map, r lang.Runner, testCmd []string, repoRoot, codePath, testPath string) (lang.Selection, error) {
	grain, err := grainOf(m)
	if err != nil {
		return lang.Selection{}, err
	}
	if err := canRun(r, grain); err != nil {
		return lang.Selection{}, err
	}
	if grain == GrainUnit {
		if err := someUnitReported(m); err != nil {
			return lang.Selection{}, err
		}
	}
	want := filepath.ToSlash(codePath)
	src, measured := m.Sources[want]
	if !measured {
		wantTest := filepath.ToSlash(testPath)
		if _, saw := m.Sources[wantTest]; !saw || wantTest == "" {
			return lang.Selection{}, fmt.Errorf("testmap: %s selection evidence never saw %s or its paired test %q — did the suite run it?", m.Lang, codePath, testPath)
		}
		return lang.Selection{}, fmt.Errorf("testmap: %s selection evidence measured %d file(s) but never %s, though its paired test %q did run — its coverage may not have been recorded under the repo root (e.g. an editable/src-layout install); falling back to the whole suite", m.Lang, len(m.Sources), codePath, testPath)
	}

	base := slices.Clone(testCmd)
	if r.CollectionBase != nil {
		base = r.CollectionBase(testCmd, repoRoot)
	}
	sel := lang.Selection{Method: m.Method, Of: m.Of, Base: base}
	if grain == GrainTest {
		// A measured file carries a (possibly empty) line map: it is what
		// tells ForSpan "measured, and nothing reached this span" apart from
		// "no line evidence at all".
		sel.Lines = map[string][]lang.LineRange{}
		if len(src.Static) > 0 {
			sel.Static = slices.Clone(src.Static)
		}
	}
	seen := map[string]bool{}
	var ids []string
	for _, e := range m.EdgesTo(want) {
		n, ok := m.Tests[e.Test]
		if !ok {
			return lang.Selection{}, fmt.Errorf("testmap: %s map has an edge from %q to %s, but no node %q", m.Lang, e.Test, want, e.Test)
		}
		if n.Grain != grain {
			return lang.Selection{}, fmt.Errorf("testmap: %s map is %s, but node %q has grain %q, not %q", m.Lang, m.Method, e.Test, n.Grain, grain)
		}
		if grain == GrainUnit && e.Executed == 0 {
			continue
		}
		if !seen[e.Test] {
			seen[e.Test] = true
			ids = append(ids, e.Test)
		}
		if grain == GrainTest && len(e.Lines) > 0 {
			sel.Lines[e.Test] = append(sel.Lines[e.Test], e.Lines...)
		}
	}
	if grain == GrainUnit {
		for _, id := range m.Unreported() {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	if len(ids) == 0 {
		return sel, nil
	}
	sort.Strings(ids)

	if grain == GrainUnit {
		cmd, err := unitsCmd(m, r, base, ids)
		if err != nil {
			return lang.Selection{}, err
		}
		sel.Tests, sel.Cmd = ids, cmd
		return sel, nil
	}

	if argvLen(ids) > MaxArgv {
		units, err := widenByNode(m, ids)
		if err != nil {
			return lang.Selection{}, err
		}
		// The line evidence is keyed by test id, and the ids just gave way to
		// their units. Kept, it would leave ForSpan looking up ids that no
		// longer run, matching nothing, and labelling every mutant
		// "unreached" — a positive claim about a file that was not narrowed
		// at all. Evidence that cannot narrow is no line evidence.
		//
		// A widened set still over the cap is RUN, not refused: that is what
		// Python's selector did with it, and its behaviour does not move
		// (ledger ruling, Task 5). Only a unit-grain set, which had no earlier
		// answer, refuses over the cap.
		sel.Lines, sel.Static = nil, nil
		ids = units
	}
	cmd, ok := r.TestsCmd(base, ids)
	if !ok {
		return lang.Selection{}, fmt.Errorf("testmap: the %s runner cannot run the %d selected tests from %v", r.ID, len(ids), base)
	}
	sel.Tests, sel.Cmd = ids, cmd
	return sel, nil
}

// someUnitReported refuses a unit map in which no listed unit contributed
// evidence: the pass timed out before any unit finished, or every unit
// failed. The pass's own Note says why, verbatim, so the whole-suite
// fallback names the real cause rather than a file it never measured.
func someUnitReported(m Map) error {
	if len(m.Unreported()) < len(m.Tests) {
		return nil
	}
	why := fmt.Sprintf("testmap: the %s unit pass reported no unit's evidence (%d listed, none reported), so no unit can be selected", m.Lang, len(m.Tests))
	if m.Note != "" {
		why += ": " + m.Note
	}
	return errors.New(why)
}

// UnreportedIn is how many of sel's units are in it only because they
// reported no evidence (m's Unreported), which Select adds to every
// unit-grain selection. It is counted back out of the selection, rather
// than carried on it, so the count is always of units that will actually
// run: a selection the engine refused carries no Tests and counts zero.
func UnreportedIn(m Map, sel lang.Selection) int {
	n := 0
	for _, id := range sel.Tests {
		if node, ok := m.Tests[id]; ok && node.Grain == GrainUnit && node.Err != "" {
			n++
		}
	}
	return n
}

// canRun refuses a row with no command for the map's grain, before any
// evidence is read: there is nothing a selection could be run with.
func canRun(r lang.Runner, grain string) error {
	if grain == GrainTest && r.TestsCmd == nil {
		return fmt.Errorf("testmap: the %s runner has no command for chosen tests, so a test-grain map cannot select for it", r.ID)
	}
	if grain == GrainUnit && r.UnitsCmd == nil {
		return fmt.Errorf("testmap: the %s runner has no command for chosen units, so a unit-grain map cannot select for it", r.ID)
	}
	return nil
}

// unitsCmd is the command running exactly units: refused over the argv cap,
// when the row would also run a listed unit nobody named, and wherever the
// row's UnitsCmd refuses (a name filter in argv, a launcher with no
// arguments).
func unitsCmd(m Map, r lang.Runner, argv, units []string) ([]string, error) {
	if n := argvLen(units); n > MaxArgv {
		return nil, fmt.Errorf("testmap: %d selected units take %d bytes of command line, over the %d-byte cap, and a unit has nothing coarser to widen to", len(units), n, MaxArgv)
	}
	if other, ok := corunsUnnamed(m, r, units); ok {
		return nil, fmt.Errorf("testmap: running the selected units with the %s runner would also run %q, which no evidence selected", r.ID, other)
	}
	cmd, ok := r.UnitsCmd(argv, units)
	if !ok {
		return nil, fmt.Errorf("testmap: the %s runner cannot run exactly the selected units %v from %v", r.ID, units, argv)
	}
	return cmd, nil
}

// corunsUnnamed is the first listed unit (in id order, so the reason is
// stable) that a run of units would collect without naming it.
func corunsUnnamed(m Map, r lang.Runner, units []string) (string, bool) {
	if r.UnitsCoruns == nil {
		return "", false
	}
	var listed []string
	for id, n := range m.Tests {
		if n.Grain == GrainUnit && !slices.Contains(units, id) {
			listed = append(listed, id)
		}
	}
	sort.Strings(listed)
	for _, other := range listed {
		if r.UnitsCoruns(units, other) {
			return other, true
		}
	}
	return "", false
}

// widenByNode replaces sorted test ids with the sorted, distinct units their
// nodes record.
func widenByNode(m Map, ids []string) ([]string, error) {
	set := map[string]bool{}
	for _, id := range ids {
		u := m.Tests[id].Unit
		if u == "" {
			return nil, fmt.Errorf("testmap: %d selected tests exceed the %d-byte command-line cap, and test %q records no unit to widen to", len(ids), MaxArgv, id)
		}
		set[u] = true
	}
	return sortedKeys(set), nil
}

func sortedKeys(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ForSpan narrows sel to the tests whose recorded lines reach span, ordered
// kill-likeliest first, and says why with a lang.SpanRule. Anything it cannot
// narrow — no span, no line evidence among the tests that will run (a unit
// selection, or one widened past the argv cap), a Selection whose Cmd is not
// Base + Tests, a row that cannot build the narrowed command — runs the
// file's own selection unchanged. A span on an import-time line, or one no
// test reaches, runs it too, labelled so: "unreached" is a claim about
// coverage, made only where the evidence could have said otherwise.
func ForSpan(sel lang.Selection, r lang.Runner, span lang.LineRange) (cmd []string, tests []string, rule string) {
	file := func(rule string) ([]string, []string, string) {
		return append([]string{}, sel.Cmd...), append([]string{}, sel.Tests...), rule
	}
	if span.IsZero() || !sel.NarrowableByLine() {
		return file(lang.SpanRuleFile)
	}
	for _, s := range sel.Static {
		if s.Overlaps(span) {
			return file(lang.SpanRuleStatic)
		}
	}
	var ids []string
	for _, id := range sel.Tests {
		for _, rg := range sel.Lines[id] {
			if rg.Overlaps(span) {
				ids = append(ids, id)
				break
			}
		}
	}
	if len(ids) == 0 {
		return file(lang.SpanRuleUnreached)
	}
	// KILL-LIKELIEST FIRST. A test that executes more of the mutated span is
	// likelier to observe the change; with per-mutant fail-fast the average
	// killed mutant then costs the tests up to its killer, not the whole set.
	// This reorders exactly the ids selected above, so "did any selected test
	// fail" — the verdict — cannot move.
	sortBySpanCoverage(ids, sel, span)
	if argvLen(ids) > MaxArgv {
		if r.TestUnit == nil {
			return file(lang.SpanRuleFile)
		}
		set := map[string]bool{}
		for _, id := range ids {
			set[r.TestUnit(id)] = true
		}
		ids = sortedKeys(set)
	}
	base := sel.Base
	if base == nil {
		// Cmd is Base + Tests by construction. A Selection that does not
		// hold that runs the file selection rather than slicing a command
		// apart on an assumption it just failed.
		if len(sel.Cmd) < len(sel.Tests) {
			return file(lang.SpanRuleFile)
		}
		base = sel.Cmd[:len(sel.Cmd)-len(sel.Tests)]
	}
	if r.TestsCmd == nil {
		return file(lang.SpanRuleFile)
	}
	out, ok := r.TestsCmd(base, ids)
	if !ok {
		return file(lang.SpanRuleFile)
	}
	return out, ids, lang.SpanRuleLines
}

// sortBySpanCoverage orders ids by how many of span's own lines each test
// executed, descending, tie-broken by id ascending — a TOTAL order, so two
// runs of the same evidence build the same command, which a signed record
// requires.
func sortBySpanCoverage(ids []string, sel lang.Selection, span lang.LineRange) {
	reach := make(map[string]int, len(ids))
	for _, id := range ids {
		n := 0
		for _, r := range sel.Lines[id] {
			lo, hi := max(r.Start, span.Start), min(r.End, span.End)
			if hi >= lo {
				n += hi - lo + 1
			}
		}
		reach[id] = n
	}
	sort.SliceStable(ids, func(i, j int) bool {
		if reach[ids[i]] != reach[ids[j]] {
			return reach[ids[i]] > reach[ids[j]]
		}
		return ids[i] < ids[j]
	})
}

// WithAuthoredTest is the command for the pool's pass: the selection plus
// the test the pool wrote, so the authored test runs beside the tests that
// already reach the file. ok=false means no command runs exactly that; the
// caller grades the authored test whole-suite and says why.
//
// Test grain (m's method is coverage-context): the authored path goes after
// sel.Cmd through the row's TestsCmd. An uncovered selection builds on
// sel.Base, the operator's command without its collection targets, so the
// authored test runs ALONE rather than beside every test a directory target
// would collect; sel.Base is nil only for a Selection the engine did not
// build, where testCmd is all there is. The row's CarryArgs of testCmd are
// kept: the salvage pass puts `--deselect <failing test>` on testCmd, and a
// rebuild from sel.Cmd that dropped it re-ran the failing test and salvaged
// nothing.
//
// Unit grain: UnitsCmd over the selected units plus the unit the authored
// file belongs to (UnitForFile, which reads manifests under repoRoot) — the
// unit it was written into, even one the evidence never listed. Refused when
// no unit holds the file, when the run would also collect a listed unit
// nobody named, or wherever UnitsCmd refuses. m supplies the method that
// decides the grain and the listed units the corun check needs; a Selection
// carries neither.
func WithAuthoredTest(m Map, sel lang.Selection, r lang.Runner, testCmd []string, repoRoot, authoredTestPath string) ([]string, bool) {
	cmd, err := AuthoredTestCmd(m, sel, r, testCmd, repoRoot, authoredTestPath)
	return cmd, err == nil
}

// AuthoredTestCmd is WithAuthoredTest with the reason it has no command. A
// caller that runs something else in its place (the whole command) must
// say so where the measurement is recorded, and this is the sentence it
// says.
func AuthoredTestCmd(m Map, sel lang.Selection, r lang.Runner, testCmd []string, repoRoot, authoredTestPath string) ([]string, error) {
	grain, err := grainOf(m)
	if err != nil {
		return nil, err
	}
	if err := canRun(r, grain); err != nil {
		return nil, err
	}
	if grain == GrainUnit {
		if r.UnitForFile == nil {
			return nil, fmt.Errorf("testmap: the %s runner cannot name the unit a test file belongs to, so %s cannot be added to a unit selection", r.ID, authoredTestPath)
		}
		u, ok := r.UnitForFile(repoRoot, authoredTestPath)
		if !ok {
			return nil, fmt.Errorf("testmap: no %s unit holds %s, so it cannot be added to a unit selection", r.ID, authoredTestPath)
		}
		units := slices.Clone(sel.Tests)
		if !slices.Contains(units, u) {
			units = append(units, u)
		}
		sort.Strings(units)
		return unitsCmd(m, r, testCmd, units)
	}

	base := sel.Cmd
	if len(sel.Tests) == 0 {
		base = testCmd
		if sel.Base != nil {
			base = sel.Base
		}
	}
	out, ok := r.TestsCmd(base, []string{authoredTestPath})
	if !ok {
		return nil, fmt.Errorf("testmap: the %s runner cannot add %s to %v", r.ID, authoredTestPath, base)
	}
	if r.CarryArgs != nil {
		out = append(out, r.CarryArgs(testCmd)...)
	}
	return out, nil
}

// argvLen approximates the length of a command line carrying args, as one
// space-joined string — the measure MaxArgv bounds.
func argvLen(args []string) int {
	n := 0
	for _, a := range args {
		n += len(a) + 1
	}
	return n
}
