// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"testing"
	"time"
)

// sleepyJail holds for `hold` on every run and rejects the mutant whose code
// is "BROKEN" when asked to COMPILE it — so one mutant reaches the gate and
// never reaches the suite, which is the case the spread must exclude.
type sleepyJail struct{ hold time.Duration }

func (j *sleepyJail) RunTest(ctx context.Context, files map[string]string, cmd []string) (bool, error) {
	time.Sleep(j.hold)
	code := files["code.py"]
	if len(cmd) > 0 && cmd[0] == "compile" {
		return code != "BROKEN", nil
	}
	if code == CanaryCode {
		return false, nil
	}
	return true, nil
}

// TestEveryGradedMutantHasADuration is the per-mutant half of "where did the
// 43 minutes go": the dev pass is most of an audit's wall clock, and until
// now the only thing measured inside it was the compliant baseline. Every
// mutant the suite actually ran has to say what it cost, and every mutant it
// did NOT run has to say nothing at all — a compile-gate reject that reported
// a duration of zero would be averaged into the file's spread as a mutant
// that graded instantly.
func TestEveryGradedMutantHasADuration(t *testing.T) {
	j := &sleepyJail{hold: 20 * time.Millisecond}
	mutants := []Mutant{
		{ID: "m1", Code: "a"}, {ID: "m2", Code: "b"}, {ID: "m3", Code: "c"},
		{ID: "m4", Code: "BROKEN"},
	}
	rep, err := Score(context.Background(), j, map[string]string{}, "code.py", "COMPLIANT",
		mutants, []string{"pytest"}, WithMutantCompileCheck([][]string{{"compile"}}))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if len(rep.Invalid) != 1 || rep.Invalid[0] != "m4" {
		t.Fatalf("Invalid = %v, want just m4 — the fixture is not exercising the gate", rep.Invalid)
	}
	for _, id := range []string{"m1", "m2", "m3"} {
		g, ok := rep.PerMutant[id]
		if !ok {
			t.Fatalf("PerMutant has no entry for graded mutant %s — nothing timed the run that produced its verdict", id)
		}
		if g.Duration < 20*time.Millisecond {
			t.Errorf("mutant %s Duration = %v, want at least the jail's own hold", id, g.Duration)
		}
	}
	if len(rep.PerMutant) != 3 {
		t.Errorf("PerMutant has %d entries, want 3 — the compile-gate reject must not appear at all", len(rep.PerMutant))
	}
	if g := rep.PerMutant["m4"]; g.Duration != 0 {
		t.Errorf("the INVALID mutant reports Duration %v; it never ran and must report nothing", g.Duration)
	}
	if rep.MutantDurationMedian < 20*time.Millisecond {
		t.Errorf("MutantDurationMedian = %v, want at least the jail's own hold", rep.MutantDurationMedian)
	}
	if rep.MutantDurationMax < rep.MutantDurationMedian {
		t.Errorf("MutantDurationMax %v < median %v", rep.MutantDurationMax, rep.MutantDurationMedian)
	}
}

// TestUngradedRunMeasuresNoMutantSpread: a suite that cannot pass on the
// unmutated code grades nothing, so the spread is not "zero", it is absent.
func TestUngradedRunMeasuresNoMutantSpread(t *testing.T) {
	rep, err := Score(context.Background(), failingJail{}, map[string]string{}, "code.py", "COMPLIANT",
		[]Mutant{{ID: "m1", Code: "a"}}, []string{"pytest"})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if rep.MutantDurationMedian != 0 || rep.MutantDurationMax != 0 {
		t.Fatalf("an ungraded run reported a mutant spread of %v/%v", rep.MutantDurationMedian, rep.MutantDurationMax)
	}
}

// failingJail never passes: the compliant baseline fails, so Score returns
// before a single mutant is scored.
type failingJail struct{}

func (failingJail) RunTest(ctx context.Context, files map[string]string, cmd []string) (bool, error) {
	return false, nil
}
