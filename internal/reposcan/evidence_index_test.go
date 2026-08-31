// SPDX-License-Identifier: Elastic-2.0

package reposcan

import (
	"errors"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

// fakeSelector is a minimal lang.TestSelector the reposcan tests drive
// directly, rather than going through a real corral-selection-2 document —
// this package's contract with lang.TestSelector.Index is the interface,
// not python's own evidence format.
type fakeSelector struct {
	lang.Plugin
	index    map[string]lang.FileCoverage
	indexErr error
}

func (f fakeSelector) Instrument(cmd []string) ([]string, bool) { return nil, false }
func (f fakeSelector) Select(evidence []byte, repoRoot, codePath, testPath string, testCmd []string) (lang.Selection, error) {
	return lang.Selection{}, nil
}
func (f fakeSelector) WithAuthoredTest(sel lang.Selection, testCmd []string, authoredTestPath string) []string {
	return nil
}
func (f fakeSelector) ForSpan(sel lang.Selection, span lang.LineRange) ([]string, []string, string) {
	return nil, nil, ""
}
func (f fakeSelector) Index(evidence []byte) (map[string]lang.FileCoverage, error) {
	return f.index, f.indexErr
}
func (f fakeSelector) Name() string { return "fake" }

func TestParseEvidenceIndexFalseWhenEvidenceDidNotRun(t *testing.T) {
	_, ok := ParseEvidenceIndex(SelectionEvidence{Ran: false}, fakeSelector{})
	if ok {
		t.Fatal("ParseEvidenceIndex must return false when the evidence never ran — pairing-only fallback")
	}
}

func TestParseEvidenceIndexFalseForASelectorLessPlugin(t *testing.T) {
	// A plugin that does not implement lang.TestSelector at all.
	_, ok := ParseEvidenceIndex(SelectionEvidence{Ran: true, Raw: []byte("x")}, plainPlugin{})
	if ok {
		t.Fatal("ParseEvidenceIndex must return false for a plugin with no selector")
	}
}

type plainPlugin struct{ lang.Plugin }

func (plainPlugin) Name() string { return "plain" }

func TestParseEvidenceIndexFalseWhenIndexErrors(t *testing.T) {
	_, ok := ParseEvidenceIndex(SelectionEvidence{Ran: true, Raw: []byte("x")}, fakeSelector{indexErr: errBoom})
	if ok {
		t.Fatal("ParseEvidenceIndex must return false when the plugin's Index call fails to parse")
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
	idx, ok := ParseEvidenceIndex(SelectionEvidence{Ran: true, Raw: []byte("x")}, sel)
	if !ok {
		t.Fatal("ParseEvidenceIndex: ok=false, want true")
	}

	n, mostCovering, hasStatic, measured := idx.CoverageFor("pkg/utils.py")
	if !measured || n != 3 {
		t.Fatalf("CoverageFor(pkg/utils.py) = %d, %q, %v, %v; want 3 covering tests, measured=true", n, mostCovering, hasStatic, measured)
	}
	if mostCovering != "tests/test_api.py" {
		t.Errorf("mostCovering = %q, want the FILE of the single test with the most executed lines (test_b, 5 lines) = tests/test_api.py", mostCovering)
	}

	n, mostCovering, hasStatic, measured = idx.CoverageFor("pkg/dead.py")
	if !measured || n != 0 || mostCovering != "" || hasStatic {
		t.Errorf("CoverageFor(pkg/dead.py) = %d, %q, %v, %v; want 0 covering tests, no most-covering, hasStatic=false, measured=true (a POSITIVE zero finding)", n, mostCovering, hasStatic, measured)
	}

	n, mostCovering, hasStatic, measured = idx.CoverageFor("pkg/never-measured.py")
	if measured {
		t.Errorf("CoverageFor(pkg/never-measured.py): measured=true, want false — absence of evidence is not evidence of absence")
	}
	_ = n
	_ = mostCovering
	_ = hasStatic
}

// A file with zero covering TESTS but HasStatic true (import/module-load
// time coverage only) must read back hasStatic=true from CoverageFor —
// this, not coveringTests alone, is what distinguishes ReasonImportOnly
// from ReasonUncovered in WidenCandidacyByEvidence.
func TestParseEvidenceIndexCarriesHasStatic(t *testing.T) {
	sel := fakeSelector{index: map[string]lang.FileCoverage{
		"pkg/__init__.py": {Tests: map[string]int{}, HasStatic: true},
	}}
	idx, ok := ParseEvidenceIndex(SelectionEvidence{Ran: true, Raw: []byte("x")}, sel)
	if !ok {
		t.Fatal("ParseEvidenceIndex: ok=false")
	}
	n, _, hasStatic, measured := idx.CoverageFor("pkg/__init__.py")
	if !measured || n != 0 || !hasStatic {
		t.Errorf("CoverageFor(pkg/__init__.py) = %d, hasStatic=%v, measured=%v; want 0 covering tests, hasStatic=true, measured=true", n, hasStatic, measured)
	}
}

func TestMoreSpecificTestPathTieBreak(t *testing.T) {
	sel := fakeSelector{index: map[string]lang.FileCoverage{
		"pkg/utils.py": {Tests: map[string]int{
			"tests/test_api.py::test_a":        2,
			"tests/unit/test_utils.py::test_b": 2,
		}},
	}}
	idx, ok := ParseEvidenceIndex(SelectionEvidence{Ran: true, Raw: []byte("x")}, sel)
	if !ok {
		t.Fatal("ParseEvidenceIndex: ok=false")
	}
	_, mostCovering, _, _ := idx.CoverageFor("pkg/utils.py")
	if mostCovering != "tests/unit/test_utils.py" {
		t.Errorf("mostCovering = %q, want the more specific (deeper) tied path tests/unit/test_utils.py", mostCovering)
	}
}
