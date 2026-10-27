// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"reflect"
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
		{Unit: "test/broken.test.ts", Err: "exit status 1"},
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
	if got := m.Tests["test/broken.test.ts"].Err; got != "exit status 1" {
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
