// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingJail records every command it is asked to run, keyed by the
// mutated file's content, and passes everything.
type recordingJail struct {
	mu   sync.Mutex
	cmds map[string][][]string
}

func (r *recordingJail) RunTest(_ context.Context, files map[string]string, cmd []string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cmds == nil {
		r.cmds = map[string][][]string{}
	}
	r.cmds[files["a.py"]] = append(r.cmds[files["a.py"]], append([]string{}, cmd...))
	for _, v := range files {
		if v == CanaryCode {
			// The canary check must fail (as every other jail fixture in
			// this package special-cases): a jail that "passes" on
			// deliberately invalid source would trip Score's fail-closed
			// canary gate and stop before any mutant ever runs.
			return false, nil
		}
	}
	return true, nil
}

func TestScoreRunsEachMutantWithItsOwnCommand(t *testing.T) {
	j := &recordingJail{}
	base := map[string]string{"test_a.py": "def test(): pass\n"}
	mutants := []Mutant{{ID: "m1", Replace: "x = 1\n"}, {ID: "m2", Replace: "x = 2\n"}}
	suite := []string{"pytest", "-q"}
	rep, err := Score(context.Background(), j, base, "a.py", "x = 0\n", mutants, suite,
		WithCommandFor(func(m Mutant) MutantCommand {
			if m.ID == "m1" {
				return MutantCommand{Cmd: []string{"pytest", "-q", "test_a.py::t1"}, Tests: 1, Rule: "lines"}
			}
			return MutantCommand{Cmd: suite, Tests: 7, Rule: "static"}
		}))
	if err != nil {
		t.Fatal(err)
	}
	if got := j.cmds["x = 0\n"]; len(got) == 0 || !reflect.DeepEqual(got[0], suite) {
		t.Errorf("the baseline must run the shared command, got %v", got)
	}
	if got := j.cmds["x = 1\n"]; !reflect.DeepEqual(got, [][]string{{"pytest", "-q", "test_a.py::t1"}}) {
		t.Errorf("m1 must run its own command, got %v", got)
	}
	if got := j.cmds["x = 2\n"]; !reflect.DeepEqual(got, [][]string{suite}) {
		t.Errorf("m2 must run the command its rule chose, got %v", got)
	}
	// Compared field by field rather than by DeepEqual over the whole map:
	// every graded mutant now also carries a measured Duration, which is a
	// wall clock and cannot be a golden value.
	want := map[string]MutantGrading{"m1": {TestsRun: 1, Rule: "lines"}, "m2": {TestsRun: 7, Rule: "static"}}
	if len(rep.PerMutant) != len(want) {
		t.Fatalf("PerMutant = %+v, want an entry per graded mutant %+v", rep.PerMutant, want)
	}
	for id, w := range want {
		got := rep.PerMutant[id]
		if got.TestsRun != w.TestsRun || got.Rule != w.Rule {
			t.Errorf("PerMutant[%s] = %+v, want TestsRun=%d Rule=%s", id, got, w.TestsRun, w.Rule)
		}
	}
}

func TestScoreWithoutCommandForIsUnchanged(t *testing.T) {
	j := &recordingJail{}
	mutants := []Mutant{{ID: "m1", Replace: "x = 1\n"}}
	suite := []string{"pytest", "-q"}
	rep, err := Score(context.Background(), j, map[string]string{}, "a.py", "x = 0\n", mutants, suite)
	if err != nil {
		t.Fatal(err)
	}
	var seen [][]string
	for _, c := range j.cmds {
		seen = append(seen, c...)
	}
	sort.Slice(seen, func(i, k int) bool { return len(seen[i]) < len(seen[k]) })
	for _, c := range seen {
		if !reflect.DeepEqual(c, suite) {
			t.Errorf("every run must use the shared command when CommandFor is nil: %v", c)
		}
	}
	// PerMutant is populated on EVERY graded run now — each mutant is timed —
	// but a run with no CommandFor still records no command evidence, which
	// is what "unchanged" means here. Nothing downstream reads the grading
	// MODE off this map (the driver records that from the call it made), so
	// filling it in cannot flip a verdict's disclosure.
	g, ok := rep.PerMutant["m1"]
	if !ok {
		t.Fatal("the graded mutant has no PerMutant entry, so nothing timed its run")
	}
	if g.TestsRun != 0 || g.Rule != "" {
		t.Errorf("PerMutant[m1] = %+v, want no command evidence when CommandFor is nil", g)
	}
}

// A GRADING COMMAND THAT FAILS ON THE COMPLIANT CODE PROVES NOTHING. This is
// the regression for a fabricated "proven missed".
//
// The compliant baseline was proven for ONE command — the shared one — while
// each mutant could be graded by a DIFFERENT command that was never run
// against compliant code. In the proving pass every survivor runs the authored
// test ALONE, so an authored file pytest does not collect (a class named
// CalcTest rather than TestCalc — a common LLM shape) exits 5, "no tests ran",
// for every survivor, and every survivor was marked KILLED. The driver then
// signed each one as a gap the authored test proved by execution.
//
// The mutant here is genuinely unobservable by the authored test (2*2 == 2+2),
// so the only honest verdicts are "survived" or "could not measure". It is the
// latter, because the command that would decide fails on the unmutated code —
// which the scorer now checks once per distinct command before grading.
func TestScoreRefusesToGradeWithACommandThatFailsOnCompliantCode(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not installed — this proof needs the real interpreter")
	}
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	code := "def add(a, b):\n    return a + b\n"
	for name, body := range map[string]string{
		"calc.py":              code,
		"conftest.py":          "",
		"tests/test_calc.py":   "from calc import add\n\ndef test_add():\n    assert add(1, 2) == 3\n",
		"tests/test_corral.py": "from calc import add\n\nclass CalcTest:\n    def test_sub(self):\n        assert add(2, 2) == 4\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	shared := []string{"python3", "-m", "pytest", "-q", "tests/test_calc.py"}
	alone := []string{"python3", "-m", "pytest", "-q", "tests/test_corral.py"}

	// The premise, stated by execution rather than assumed: the shared
	// command passes on compliant code and the alone command does not.
	for _, tc := range []struct {
		cmd  []string
		want int
	}{{shared, 0}, {alone, 5}} {
		c := exec.Command(tc.cmd[0], tc.cmd[1:]...)
		c.Dir = root
		err := c.Run()
		got := 0
		if ee, ok := err.(*exec.ExitError); ok {
			got = ee.ExitCode()
		}
		if got != tc.want {
			t.Fatalf("premise: %v exited %d on compliant code, want %d (pytest's no-tests-collected code)", tc.cmd[4:], got, tc.want)
		}
	}

	j := NewWorkspaceRunner(root, 60*time.Second)
	m := Mutant{ID: "m1", Search: "return a + b", Replace: "return a * b"}
	rep, err := Score(context.Background(), j, map[string]string{}, "calc.py", code, []Mutant{m}, shared,
		WithCommandFor(func(Mutant) MutantCommand {
			return MutantCommand{Cmd: alone, Tests: 1, Rule: "authored-alone"}
		}))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if len(rep.Killed) != 0 {
		t.Fatalf("m1 was counted KILLED by a command that exits 5 on compliant code — a fabricated proof. report: killed=%v survived=%v unmeasured=%v", rep.Killed, rep.Survived, rep.Unmeasured)
	}
	if len(rep.Unmeasured) != 1 || rep.Unmeasured[0] != "m1" {
		t.Fatalf("m1 must be UNMEASURED (its command cannot distinguish the mutant from compliant code), got killed=%v survived=%v unmeasured=%v", rep.Killed, rep.Survived, rep.Unmeasured)
	}
	if rep.Total != 0 {
		t.Errorf("an unmeasured mutant must not be in the denominator, Total=%d", rep.Total)
	}
	if !strings.Contains(rep.UnmeasuredReasons["m1"], "FAILS ON THE COMPLIANT CODE") {
		t.Errorf("the reason must say what happened, got %q", rep.UnmeasuredReasons["m1"])
	}
}
