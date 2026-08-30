// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"reflect"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
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
	got := DevCommandArgv(s.Selection, s.Lang, []string{"python3", "-m", "pytest", "-q"}, s.DevTestPath)
	want := []string{"python3", "-m", "pytest", "-q", "tests/test_a.py::test_x"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("dev cmd = %v, want the selection's own command %v", got, want)
	}
}

func TestDevPassUncoveredRunsOnlyThePairedTestFile(t *testing.T) {
	s := repoScorer(lang.Selection{Method: "coverage-context"})
	got := DevCommandArgv(s.Selection, s.Lang, []string{"python3", "-m", "pytest", "-q"}, s.DevTestPath)
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

// C1. The operator's command names a collection target (`pytest tests/`),
// which pytest UNIONS with anything appended — so both passes must build on
// the Selection's stripped Base, never on the raw command, or the narrowing
// is a claim with no run behind it.
func TestBothPassesBuildOnTheSelectionsStrippedBase(t *testing.T) {
	raw := []string{"pytest", "tests/"}
	s := repoScorer(lang.Selection{
		Base:  []string{"pytest"},
		Cmd:   []string{"pytest", "tests/test_a.py::test_x"},
		Tests: []string{"tests/test_a.py::test_x"}, Method: "coverage-context",
	})
	if got, want := DevCommandArgv(s.Selection, s.Lang, raw, s.DevTestPath), []string{"pytest", "tests/test_a.py::test_x"}; !reflect.DeepEqual(got, want) {
		t.Errorf("dev cmd = %v, want %v — tests/ must not survive", got, want)
	}
	authored := authoredTestPath("pkg/a.py", s.DevTestPath, s.BaseFiles)
	if got, want := s.authoredCmd("pkg/a.py", raw), []string{"pytest", "tests/test_a.py::test_x", authored}; !reflect.DeepEqual(got, want) {
		t.Errorf("authored cmd = %v, want %v", got, want)
	}
	// Uncovered: Base alone plus the one test being run.
	u := repoScorer(lang.Selection{Base: []string{"pytest"}, Method: "coverage-context"})
	if got, want := DevCommandArgv(u.Selection, u.Lang, raw, u.DevTestPath), []string{"pytest", "tests/test_a.py"}; !reflect.DeepEqual(got, want) {
		t.Errorf("uncovered dev cmd = %v, want %v", got, want)
	}
	if got, want := u.authoredCmd("pkg/a.py", raw), []string{"pytest", authored}; !reflect.DeepEqual(got, want) {
		t.Errorf("uncovered authored cmd = %v, want %v", got, want)
	}
}

func TestWholeSuiteLeavesBothPassesUnchanged(t *testing.T) {
	s := repoScorer(lang.Selection{})
	base := []string{"python3", "-m", "pytest", "-q"}
	if got := DevCommandArgv(s.Selection, s.Lang, base, s.DevTestPath); !reflect.DeepEqual(got, base) {
		t.Errorf("zero Selection must leave the dev command as before: %v", got)
	}
	if got := s.authoredCmd("pkg/a.py", base); !reflect.DeepEqual(got, base) {
		t.Errorf("zero Selection must leave the authored command as before: %v", got)
	}
}

func TestAggregateCarriesTheSelectionOntoTheVerdict(t *testing.T) {
	rs := RunSpec{Selection: lang.Selection{Method: "coverage-context", Tests: []string{"a", "b"}, Of: 40}}
	v := verdictFromSpec(rs)
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

// TestAggregateCarriesConcurrencyOntoTheVerdict pins the other half of "every
// reader says how many trees scored the file, or why one" — the RunSpec's
// Concurrency must reach the Verdict unchanged, the same way Selection does,
// so a timed-out verdict (built off this same function) still discloses it.
func TestAggregateCarriesConcurrencyOntoTheVerdict(t *testing.T) {
	rs := RunSpec{Concurrency: Concurrency{Trees: 6}}
	if v := verdictFromSpec(rs); v.Concurrency.Trees != 6 || v.Concurrency.Note != "" {
		t.Errorf("got %+v, want Trees 6, no note", v.Concurrency)
	}

	rs = RunSpec{Concurrency: Concurrency{Trees: 6, Shared: []string{".venv"}}}
	if v := verdictFromSpec(rs); len(v.Concurrency.Shared) != 1 || v.Concurrency.Shared[0] != ".venv" {
		t.Errorf("got %+v, want the shared dep dirs preserved", v.Concurrency)
	}

	rs = RunSpec{Concurrency: Concurrency{Trees: 1, Note: "suite is not concurrency-safe: baseline failed under 3"}}
	if v := verdictFromSpec(rs); v.Concurrency.Trees != 1 || v.Concurrency.Note != "suite is not concurrency-safe: baseline failed under 3" {
		t.Errorf("got %+v, want the downgrade note preserved", v.Concurrency)
	}
}

// I2/I3. The narrowing belongs to the CALLER that means "the run's command",
// not to ScoreReport: as a scorer-internal rewrite it silently narrowed a
// caller-supplied command (the matrix's own per-test selector) and silently
// FAILED to narrow the shadow pass, which grades the same mutants against the
// same dev suite and must ask the same question.
func TestDevCommandIsTheRunsNarrowedCommand(t *testing.T) {
	rs := RunSpec{
		Lang: "python", DevTestPath: "tests/test_a.py", TestCmd: "pytest tests/",
		Selection: lang.Selection{
			Base:  []string{"pytest"},
			Cmd:   []string{"pytest", "tests/test_a.py::test_x"},
			Tests: []string{"tests/test_a.py::test_x"}, Method: "coverage-context",
		},
	}
	if got, want := DevCommand(rs), adequacy.ShellJoin([]string{"pytest", "tests/test_a.py::test_x"}); got != want {
		t.Errorf("DevCommand = %q, want %q", got, want)
	}
	// Uncovered: the paired test file alone, on the stripped base.
	u := rs
	u.Selection = lang.Selection{Base: []string{"pytest"}, Method: "coverage-context"}
	if got, want := DevCommand(u), adequacy.ShellJoin([]string{"pytest", "tests/test_a.py"}); got != want {
		t.Errorf("uncovered DevCommand = %q, want %q", got, want)
	}
	// A zero Selection leaves the run's own command byte-identical.
	z := rs
	z.Selection = lang.Selection{}
	if got, want := DevCommand(z), "pytest tests/"; got != want {
		t.Errorf("zero-Selection DevCommand = %q, want the run's own command %q", got, want)
	}
	// Uncovered with no paired test path: nothing to narrow TO, so the base
	// stands rather than becoming a bare `pytest` that collects everything.
	n := u
	n.DevTestPath = ""
	if got, want := DevCommand(n), "pytest tests/"; got != want {
		t.Errorf("uncovered with no DevTestPath = %q, want the base %q", got, want)
	}
}

// I3. ScoreReport must run exactly the command it is handed — the matrix
// issues a per-test selector and a scorer that rewrote it would grade a
// different thing than the row claims.
func TestScoreReportRunsTheCommandItIsGiven(t *testing.T) {
	rec := &cmdRecordingJail{}
	s := repoScorer(lang.Selection{
		Base:  []string{"pytest"},
		Cmd:   []string{"pytest", "tests/test_a.py::test_x"},
		Tests: []string{"tests/test_a.py::test_x"}, Method: "coverage-context",
	})
	s.Jail = rec
	_, _ = s.ScoreReport(context.Background(), "pkg/a.py", "x = 1\n", "", nil,
		"pytest tests/test_a.py::test_other")
	want := []string{"pytest", "tests/test_a.py::test_other"}
	if len(rec.cmds) == 0 || !reflect.DeepEqual(rec.cmds[0], want) {
		t.Errorf("ScoreReport ran %v, want the caller's own command %v", rec.cmds, want)
	}
}

// cmdRecordingJail records the commands it is asked to run and fails every
// baseline, so adequacy.Score returns early — the command is all this needs.
type cmdRecordingJail struct{ cmds [][]string }

func (r *cmdRecordingJail) RunTest(_ context.Context, _ map[string]string, cmd []string) (bool, error) {
	r.cmds = append(r.cmds, append([]string{}, cmd...))
	return false, nil
}

// discoveryJail is a project whose own test command collects nothing at the
// authored test's path: a run only observes the file when the command NAMES
// it. `pytest tests/` with testpaths = ["tests"] and an authored test outside
// that root is exactly this shape.
type discoveryJail struct{ authored string }

func (d discoveryJail) RunTest(_ context.Context, _ map[string]string, cmd []string) (bool, error) {
	for _, a := range cmd {
		if a == d.authored {
			return false, nil // the broken canary at that path failed the run
		}
	}
	return true, nil // never collected it; the run passes regardless
}

// I5. The pre-flight asked "would your command collect the authored test?"
// while checking the BASE command — not the command the authored pass
// actually runs, which appends the authored path precisely so that a
// discovery config which excludes it stops mattering. So an operator whose
// project confines discovery to a test root was refused before any model ran,
// for a problem selection had already solved.
func TestAuthoredCollectionPreflightChecksTheAuthoredPassesCommand(t *testing.T) {
	s := repoScorer(lang.Selection{
		Base:  []string{"pytest"},
		Cmd:   []string{"pytest", "tests/test_a.py::test_x"},
		Tests: []string{"tests/test_a.py::test_x"}, Method: "coverage-context",
	})
	authored := authoredTestPath("pkg/a.py", s.DevTestPath, s.BaseFiles)
	s.Jail = discoveryJail{authored: authored}
	base := []string{"pytest", "tests/"}

	if got := s.AuthoredCommand("pkg/a.py", base); got[len(got)-1] != authored {
		t.Fatalf("AuthoredCommand = %v, want it to name %q", got, authored)
	}
	// The base command alone: correctly reported as not collecting it.
	if ok, err := s.AuthoredTestWouldBeCollected(context.Background(), "pkg/a.py", base); err != nil || ok {
		t.Fatalf("base command: collected=%v err=%v, want false", ok, err)
	}
	// The command the authored pass really runs: collected, so the audit
	// must NOT be refused.
	if ok, err := s.AuthoredTestWouldBeCollected(context.Background(), "pkg/a.py", s.AuthoredCommand("pkg/a.py", base)); err != nil || !ok {
		t.Fatalf("authored pass command: collected=%v err=%v, want true — the selection names the file explicitly", ok, err)
	}
}
