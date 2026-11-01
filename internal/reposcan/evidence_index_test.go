// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"errors"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/testmap"
)

// fakeSelector is a minimal lang.TestEvidence plugin the reposcan tests
// drive directly, rather than going through a real corral-selection-3
// document — this package's contract is the test map's per-file readout
// (testmap.Map.Coverage), not python's own evidence format. index is that
// readout; evidence() is Ran evidence carrying the map that yields it.
type fakeSelector struct {
	lang.Plugin
	index    map[string]lang.FileCoverage
	indexErr error
}

func (f fakeSelector) Instrument(cmd []string) ([]string, bool) { return nil, false }
func (f fakeSelector) Evidence(raw []byte) (lang.EvidenceDoc, error) {
	return lang.EvidenceDoc{}, f.indexErr
}
func (f fakeSelector) Name() string { return "fake" }

// evidence is what CollectSelectionEvidence would hold after a run that
// measured exactly f.index: a map with one source per file and one edge per
// (test, file), Executed being the readout's line count.
func (f fakeSelector) evidence() SelectionEvidence {
	m := testmap.Map{Method: testmap.MethodCoverageContext, Sources: map[string]testmap.Source{}, Tests: map[string]testmap.Node{}}
	for path, fc := range f.index {
		m.Sources[path] = testmap.Source{HasStatic: fc.HasStatic, HasStatements: fc.HasStatements}
		for id, n := range fc.Tests {
			m.Tests[id] = testmap.Node{ID: id, Grain: testmap.GrainTest}
			m.Edges = append(m.Edges, testmap.Edge{Test: id, Source: path, Executed: n})
		}
	}
	return SelectionEvidence{Ran: true, Raw: []byte("x"), Map: m}
}

func TestParseEvidenceIndexFalseWhenEvidenceDidNotRun(t *testing.T) {
	_, ok := ParseEvidenceIndex(SelectionEvidence{Ran: false}, fakeSelector{})
	if ok {
		t.Fatal("ParseEvidenceIndex must return false when the evidence never ran — pairing-only fallback")
	}
}

func TestParseEvidenceIndexFalseForASelectorLessPlugin(t *testing.T) {
	// A plugin that does not implement lang.TestEvidence at all.
	_, ok := ParseEvidenceIndex(SelectionEvidence{Ran: true, Raw: []byte("x")}, plainPlugin{})
	if ok {
		t.Fatal("ParseEvidenceIndex must return false for a plugin with no selector")
	}
}

type plainPlugin struct{ lang.Plugin }

func (plainPlugin) Name() string { return "plain" }

// Evidence whose document did not parse never gets a map (CollectSelectionEvidence
// records the parse error as its Note); evidence carrying no map has no index.
func TestParseEvidenceIndexFalseWhenNoMapWasBuilt(t *testing.T) {
	_, ok := ParseEvidenceIndex(SelectionEvidence{Ran: true, Raw: []byte("x")}, fakeSelector{indexErr: errBoom})
	if ok {
		t.Fatal("ParseEvidenceIndex must return false when no map was built from the evidence")
	}
}

var errBoom = errors.New("boom")

func TestParseEvidenceIndexCoverageForAndMostCovering(t *testing.T) {
	sel := fakeSelector{index: map[string]lang.FileCoverage{
		"pkg/utils.py": {Tests: map[string]int{
			"tests/test_api.py::test_a":   3,
			"tests/test_api.py::test_b":   5,
			"tests/test_other.py::test_c": 1,
		}},
		"pkg/dead.py": {Tests: map[string]int{}},
	}}
	idx, ok := ParseEvidenceIndex(sel.evidence(), sel)
	if !ok {
		t.Fatal("ParseEvidenceIndex: ok=false, want true")
	}

	n, mostCovering, hasStatic, hasStatements, measured := idx.CoverageFor("pkg/utils.py")
	if !measured || n != 3 {
		t.Fatalf("CoverageFor(pkg/utils.py) = %d, %q, %v, %v, %v; want 3 covering tests, measured=true", n, mostCovering, hasStatic, hasStatements, measured)
	}
	if mostCovering != "tests/test_api.py" {
		t.Errorf("mostCovering = %q, want the FILE of the single test with the most executed lines (test_b, 5 lines) = tests/test_api.py", mostCovering)
	}

	n, mostCovering, hasStatic, hasStatements, measured = idx.CoverageFor("pkg/dead.py")
	if !measured || n != 0 || mostCovering != "" || hasStatic {
		t.Errorf("CoverageFor(pkg/dead.py) = %d, %q, %v, %v, %v; want 0 covering tests, no most-covering, hasStatic=false, measured=true (a POSITIVE zero finding)", n, mostCovering, hasStatic, hasStatements, measured)
	}

	n, mostCovering, hasStatic, hasStatements, measured = idx.CoverageFor("pkg/never-measured.py")
	if measured {
		t.Errorf("CoverageFor(pkg/never-measured.py): measured=true, want false — absence of evidence is not evidence of absence")
	}
	_ = n
	_ = mostCovering
	_ = hasStatic
	_ = hasStatements
}

// A file with zero covering TESTS but HasStatic true (import/module-load
// time coverage only) must read back hasStatic=true from CoverageFor —
// this, not coveringTests alone, is what distinguishes ReasonImportOnly
// from ReasonUncovered in WidenCandidacyByEvidence.
func TestParseEvidenceIndexCarriesHasStatic(t *testing.T) {
	sel := fakeSelector{index: map[string]lang.FileCoverage{
		"pkg/__init__.py": {Tests: map[string]int{}, HasStatic: true},
	}}
	idx, ok := ParseEvidenceIndex(sel.evidence(), sel)
	if !ok {
		t.Fatal("ParseEvidenceIndex: ok=false")
	}
	n, _, hasStatic, _, measured := idx.CoverageFor("pkg/__init__.py")
	if !measured || n != 0 || !hasStatic {
		t.Errorf("CoverageFor(pkg/__init__.py) = %d, hasStatic=%v, measured=%v; want 0 covering tests, hasStatic=true, measured=true", n, hasStatic, measured)
	}
}

// hasStatements distinguishes a genuinely dead file (real code, zero
// coverage — hasStatements true) from an empty one (no code at all —
// hasStatements false); the same coveringTests==0 shape reads either way,
// and only this field tells them apart.
func TestParseEvidenceIndexCarriesHasStatements(t *testing.T) {
	sel := fakeSelector{index: map[string]lang.FileCoverage{
		"pkg/dead.py":  {Tests: map[string]int{}, HasStatements: true},
		"pkg/empty.py": {Tests: map[string]int{}, HasStatements: false},
	}}
	idx, ok := ParseEvidenceIndex(sel.evidence(), sel)
	if !ok {
		t.Fatal("ParseEvidenceIndex: ok=false")
	}
	if _, _, _, hasStatements, measured := idx.CoverageFor("pkg/dead.py"); !measured || !hasStatements {
		t.Errorf("pkg/dead.py: hasStatements=%v measured=%v, want true/true", hasStatements, measured)
	}
	if _, _, _, hasStatements, measured := idx.CoverageFor("pkg/empty.py"); !measured || hasStatements {
		t.Errorf("pkg/empty.py: hasStatements=%v measured=%v, want false/true", hasStatements, measured)
	}
}

func TestMoreSpecificTestPathTieBreak(t *testing.T) {
	sel := fakeSelector{index: map[string]lang.FileCoverage{
		"pkg/utils.py": {Tests: map[string]int{
			"tests/test_api.py::test_a":        2,
			"tests/unit/test_utils.py::test_b": 2,
		}},
	}}
	idx, ok := ParseEvidenceIndex(sel.evidence(), sel)
	if !ok {
		t.Fatal("ParseEvidenceIndex: ok=false")
	}
	_, mostCovering, _, _, _ := idx.CoverageFor("pkg/utils.py")
	if mostCovering != "tests/unit/test_utils.py" {
		t.Errorf("mostCovering = %q, want the more specific (deeper) tied path tests/unit/test_utils.py", mostCovering)
	}
}

// A --diff-base SCAN ASKS "WHICH SOURCES DOES THIS CHANGED TEST DEFEND?", and
// the answer is every test that executes the source, not the single
// most-covering one. Keeping only mostCovering meant a pull request that
// weakened the second-most-covering test changed nothing in scope.
func TestEvidenceIndexCoveredByAnyUsesEveryCoveringTest(t *testing.T) {
	idx := EvidenceIndex{files: map[string]evidenceFileEntry{
		"pkg/calc.py": {
			coveringTests: 2,
			mostCovering:  "tests/test_calc.py",
			coveringFiles: map[string]bool{"tests/test_calc.py": true, "tests/test_behaviour.py": true},
		},
	}}
	if !idx.CoveredByAny("pkg/calc.py", map[string]bool{"tests/test_behaviour.py": true}) {
		t.Error("a change to the SECOND covering test must put the source in scope")
	}
	if idx.CoveredByAny("pkg/calc.py", map[string]bool{"tests/test_other.py": true}) {
		t.Error("a test that never executed the source must not put it in scope")
	}
	if idx.CoveredByAny("pkg/unknown.py", map[string]bool{"tests/test_calc.py": true}) {
		t.Error("an unmeasured source must not be in scope by evidence")
	}
}
