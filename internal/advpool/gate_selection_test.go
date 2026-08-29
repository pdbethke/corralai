// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"reflect"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

func repoScorer(sel lang.Selection) JailScorer {
	return JailScorer{
		Lang: "python", Selection: sel, DevTestPath: "tests/test_a.py",
		BaseFiles: map[string]string{"pkg/a.py": "x = 1\n", "tests/test_a.py": "def test_x(): pass\n"},
	}
}

func TestDevPassRunsTheSelection(t *testing.T) {
	s := repoScorer(lang.Selection{
		Cmd:   []string{"python3", "-m", "pytest", "-q", "tests/test_a.py::test_x"},
		Tests: []string{"tests/test_a.py::test_x"}, Method: "coverage-context",
	})
	got := s.devCmd("pkg/a.py", []string{"python3", "-m", "pytest", "-q"})
	want := []string{"python3", "-m", "pytest", "-q", "tests/test_a.py::test_x"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("dev cmd = %v, want the selection's own command %v", got, want)
	}
}

func TestDevPassUncoveredRunsOnlyThePairedTestFile(t *testing.T) {
	s := repoScorer(lang.Selection{Method: "coverage-context"})
	got := s.devCmd("pkg/a.py", []string{"python3", "-m", "pytest", "-q"})
	want := []string{"python3", "-m", "pytest", "-q", "tests/test_a.py"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("uncovered dev cmd = %v, want the paired test file alone %v", got, want)
	}
}

func TestAuthoredPassAppendsTheAuthoredTestsRealPath(t *testing.T) {
	sel := lang.Selection{
		Cmd:   []string{"python3", "-m", "pytest", "-q", "tests/test_a.py::test_x"},
		Tests: []string{"tests/test_a.py::test_x"}, Method: "coverage-context",
	}
	s := repoScorer(sel)
	got := s.authoredCmd("pkg/a.py", []string{"python3", "-m", "pytest", "-q"})
	authored := authoredTestPath("pkg/a.py", s.DevTestPath, s.BaseFiles)
	want := []string{"python3", "-m", "pytest", "-q", "tests/test_a.py::test_x", authored}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("authored cmd = %v, want selection + %q", got, authored)
	}
	// Uncovered: the authored test alone.
	s = repoScorer(lang.Selection{Method: "coverage-context"})
	got = s.authoredCmd("pkg/a.py", []string{"python3", "-m", "pytest", "-q"})
	want = []string{"python3", "-m", "pytest", "-q", authored}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("uncovered authored cmd = %v, want %v", got, want)
	}
}

func TestWholeSuiteLeavesBothPassesUnchanged(t *testing.T) {
	s := repoScorer(lang.Selection{})
	base := []string{"python3", "-m", "pytest", "-q"}
	if got := s.devCmd("pkg/a.py", base); !reflect.DeepEqual(got, base) {
		t.Errorf("zero Selection must leave the dev command as before: %v", got)
	}
	if got := s.authoredCmd("pkg/a.py", base); !reflect.DeepEqual(got, base) {
		t.Errorf("zero Selection must leave the authored command as before: %v", got)
	}
}

func TestAggregateCarriesTheSelectionOntoTheVerdict(t *testing.T) {
	rs := RunSpec{Selection: lang.Selection{Method: "coverage-context", Tests: []string{"a", "b"}, Of: 40}}
	v := verdictFromSpec(rs) // whatever aggregate.go's constructor is named — see Step 3
	if v.TestSelection.Method != "coverage-context" || v.TestSelection.Selected != 2 || v.TestSelection.Of != 40 || v.Uncovered {
		t.Errorf("got %+v", v.TestSelection)
	}
	rs.Selection.Tests = nil
	if v := verdictFromSpec(rs); !v.Uncovered {
		t.Error("an evidence-based empty selection is Uncovered")
	}
	rs.Selection = lang.Selection{Fallback: "no selector for ruby"}
	if v := verdictFromSpec(rs); v.Uncovered || v.TestSelection.Fallback != "no selector for ruby" {
		t.Errorf("a fallback is not uncovered: %+v", v)
	}
}
