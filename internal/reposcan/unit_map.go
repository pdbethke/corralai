// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"sort"

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
// not which lines). A zero-executed edge is kept: measured and never run is a
// fact about the file. A source's HasStatements is "some unit found an
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
			m.Edges = append(m.Edges, testmap.Edge{Test: u.Unit, Source: src, Executed: lc.Executed})
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
