// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"reflect"
	"sort"
	"sync"
	"testing"
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
	mutants := []Mutant{{ID: "m1", Code: "x = 1\n"}, {ID: "m2", Code: "x = 2\n"}}
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
	mutants := []Mutant{{ID: "m1", Code: "x = 1\n"}}
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
