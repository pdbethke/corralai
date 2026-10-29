// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/testmap"
)

// MapFromUnits is the unit pass as a test map: the structure the selection
// engine and the candidacy index both read, so the pass's evidence is
// interpreted in one place (IndexFromUnits is a view of this map).
//
// Every LISTED unit is a node, including the ones that failed or timed out:
// their Err is copied, so Unreported names exactly the units with no
// evidence, which a selection must add to every file's tests (a unit that did
// not finish may be the one that executes the file). Dropping them here, as
// the index's tally once did silently, would lose them from the one list that
// guards against under-selecting.
//
// An edge is one (unit, source) pair the unit's own coverage run measured,
// with Executed the lines it ran and no Lines (a unit run reports counts,
// not which lines), Loaded when the run loaded the file, and Uncounted
// (the reporter's reason) for a file it measured and could not count. A
// zero-executed edge is kept: measured and never run is a fact about the
// file. A source's HasStatements is "some unit found an
// executable statement in it" and HasStatic is "some unit loaded it" (ruling
// R24), so an unexecuted file stays import-only rather than uncovered.
//
// Runner is the row RunnerFor names for inv. It is left empty only for a
// language with no row at all, which the selection engine refuses by name
// rather than selecting with a runner nobody chose.
func MapFromUnits(ev UnitEvidence, uf UnitFiles, inv lang.Invocation, plug lang.Plugin) testmap.Map {
	m := unitMap(ev, uf)
	m.Lang = plug.Name()
	m.Command = append([]string{}, inv.Argv...)
	if r, ok := lang.RunnerFor(plug, inv); ok {
		m.Runner = r.ID
	}
	return m
}

// unitMap is the part of MapFromUnits that needs only the evidence, so the
// candidacy index (which has no invocation or plugin to hand) builds the very
// same map.
func unitMap(ev UnitEvidence, uf UnitFiles) testmap.Map {
	m := testmap.Map{
		Schema:  testmap.Schema,
		Method:  testmap.MethodUnitEvidence,
		Of:      len(ev.Units),
		Sources: map[string]testmap.Source{},
		Tests:   make(map[string]testmap.Node, len(ev.Units)),
		Note:    ev.Note,
	}
	for _, u := range ev.Units {
		m.Tests[u.Unit] = testmap.Node{ID: u.Unit, Grain: testmap.GrainUnit, Files: uf.TestFiles(u.Unit), Err: u.Err}
		if u.Err != "" {
			continue
		}
		loaded := make(map[string]bool, len(u.Loaded))
		for _, f := range u.Loaded {
			loaded[f] = true
		}
		for src, lc := range u.Counts {
			s := m.Sources[src]
			s.HasStatements = s.HasStatements || lc.Executable > 0
			s.HasStatic = s.HasStatic || loaded[src]
			m.Sources[src] = s
			m.Edges = append(m.Edges, testmap.Edge{Test: u.Unit, Source: src, Executed: lc.Executed, Loaded: loaded[src]})
		}
		// A file the unit loaded but did not count, and a file it measured
		// and could not count, are edges too: each is a reason the unit may
		// execute the file, and the selection engine selects the unit for it
		// — leaving it out under-selects exactly where the evidence is
		// silent. Neither becomes a Source: no count says anything about the
		// file, so a file only such units saw stays absent from the
		// evidence (whole-suite, never uncovered) unless another unit
		// counted it.
		for _, f := range u.Loaded {
			if _, counted := u.Counts[f]; !counted {
				m.Edges = append(m.Edges, testmap.Edge{Test: u.Unit, Source: f, Loaded: true})
			}
		}
		for _, entry := range u.Uncounted {
			path, why, ok := strings.Cut(entry, ": ")
			if !ok {
				why = "no reason given"
			}
			m.Edges = append(m.Edges, testmap.Edge{Test: u.Unit, Source: path, Uncounted: why})
		}
	}
	sort.Slice(m.Edges, func(i, j int) bool {
		if m.Edges[i].Source != m.Edges[j].Source {
			return m.Edges[i].Source < m.Edges[j].Source
		}
		return m.Edges[i].Test < m.Edges[j].Test
	})
	return m
}

// UnitSelectionFor answers one file of the unit pass's language from the
// pass's map, through the selection engine (testmap.Select): the units
// whose own run executed codePath, plus every unit that reported no
// evidence. It is SelectionEvidence.For's counterpart for a language whose
// evidence is per unit, and like it every non-answer is a Selection with a
// Fallback saying why the file grades whole-suite: a map of another
// language, a map naming no runner row (refused by name, never re-detected
// from the command — no defaults), or anything the engine refuses.
func UnitSelectionFor(m testmap.Map, p lang.Plugin, repoRoot, codePath, testPath string, testCmd []string) lang.Selection {
	if !SameEvidenceRun(m, p) {
		return lang.Selection{Fallback: fmt.Sprintf("the unit pass ran for %s, so it has no evidence about %s file %s", m.Lang, p.Name(), codePath)}
	}
	if why := NoRunnerRow(m); why != "" {
		return lang.Selection{Fallback: why}
	}
	r, err := lang.RunnerByID(p, m.Runner)
	if err != nil {
		return lang.Selection{Fallback: err.Error()}
	}
	sel, err := testmap.Select(m, r, testCmd, repoRoot, codePath, testPath)
	if err != nil {
		return lang.Selection{Fallback: err.Error()}
	}
	return sel
}

// SameEvidenceRun reports whether m's evidence answers for p's files: m was
// built for p's language, or for a language whose instrumented command for
// m's command is p's own, byte for byte — one run collecting both, as one
// vitest command collects JavaScript and TypeScript. It is the criterion the
// scan uses to run such languages once (cmd/corral's pickPreflightLanguage),
// which then names the map after only one of them; refusing the other made
// every TypeScript file of a mixed repo grade whole-suite (REA: 1,784 of
// 1,881 auditable files).
func SameEvidenceRun(m testmap.Map, p lang.Plugin) bool {
	if m.Lang == p.Name() {
		return true
	}
	mp, ok := lang.ByName(m.Lang)
	if !ok {
		return false
	}
	a, aok := mp.(lang.CoverageReporter)
	b, bok := p.(lang.CoverageReporter)
	if !aok || !bok {
		return false
	}
	ca, aok := a.CoverageCmd(m.Command)
	cb, bok := b.CoverageCmd(m.Command)
	return aok && bok && slices.Equal(ca, cb)
}

// WithoutRedUnits answers codePath again with the units of sel whose own run
// FAILED in the unit pass left out, and names them; nil when sel holds none,
// so there is nothing to retry without. It is for a file whose baseline is
// red: such a unit was added to every selection because it reported no
// evidence, and a unit already red on unmutated code can kill no mutant, so
// it can only make the baseline red. A unit the pass cut, never ran alone, or
// could not start (no "run failed" reason) proved nothing about the code and
// stays. The caller must run the retry's baseline before trusting it, and
// disclose the dropped units: the operator's own command may run them green
// where the pass's command did not (lang.EvidenceNote).
func WithoutRedUnits(m testmap.Map, p lang.Plugin, repoRoot, codePath, testPath string, testCmd []string, sel lang.Selection) (lang.Selection, []string) {
	red := testmap.RanRedIn(m, sel, func(n testmap.Node) bool { return strings.HasPrefix(n.Err, unitRunFailed) })
	if len(red) == 0 {
		return lang.Selection{}, nil
	}
	r, err := lang.RunnerByID(p, m.Runner)
	if err != nil {
		return lang.Selection{Fallback: err.Error()}, red
	}
	exclude := make(map[string]bool, len(red))
	for _, id := range red {
		exclude[id] = true
	}
	retry, err := testmap.SelectExcluding(m, r, testCmd, repoRoot, codePath, testPath, exclude)
	if err != nil {
		return lang.Selection{Fallback: err.Error()}, red
	}
	return retry, red
}

// NoRunnerRow is why a unit map can select nothing for want of a runner row
// to run its units with, or "" when it names one. The one wording, so the
// scan's selection line and each file's fallback say the same thing.
func NoRunnerRow(m testmap.Map) string {
	if m.Runner != "" {
		return ""
	}
	return fmt.Sprintf("the %s unit evidence names no runner row for %v, so no unit can be selected to run", m.Lang, m.Command)
}
