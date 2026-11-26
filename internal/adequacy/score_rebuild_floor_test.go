// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// rebuildFloorJail is a Jail that is NOT a RunCacheJail and still reports a
// warm duration: the workspace substrate's shape, where the duration is a
// measured run that paid for a rebuild (WorkspacePool.MeasureRebuild), not a
// warming run of a shared cache. Its mutant run "takes" mutantTakes without
// sleeping: it finishes when its budget is at least that, and times out
// otherwise. mutantPasses says how the mutant's run ends when it finishes.
type rebuildFloorJail struct {
	warm         time.Duration
	mutantTakes  time.Duration
	mutantPasses bool
	mu           sync.Mutex
	budgets      []time.Duration
}

func (j *rebuildFloorJail) RunTest(ctx context.Context, files map[string]string, cmd []string) (bool, error) {
	code := files["a.py"]
	if code == CanaryCode {
		return false, nil
	}
	if code != warmCapMutantCode {
		return true, nil
	}
	var budget time.Duration
	if dl, ok := ctx.Deadline(); ok {
		budget = time.Until(dl)
	}
	j.mu.Lock()
	j.budgets = append(j.budgets, budget)
	j.mu.Unlock()
	if budget > 0 && budget < j.mutantTakes {
		return false, fmt.Errorf("%w: ran past its %s budget", ErrTestTimeout, budget)
	}
	return j.mutantPasses, nil
}

func (j *rebuildFloorJail) WarmDuration() time.Duration { return j.warm }

func (j *rebuildFloorJail) mutantBudgets() []time.Duration {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]time.Duration(nil), j.budgets...)
}

// plainSlowMutantJail is the same jail with no warm duration at all: the shape
// of every language that measures no rebuild run. The inner jail is a named
// field, not an embed, so WarmDuration is not promoted.
type plainSlowMutantJail struct{ inner *rebuildFloorJail }

func (j plainSlowMutantJail) RunTest(ctx context.Context, files map[string]string, cmd []string) (bool, error) {
	return j.inner.RunTest(ctx, files, cmd)
}

// A frozen 1 s baseline, a 20 s measured rebuild run, and a mutant that
// rebuilds and finishes in 40 s: the cap is at least 60 s (3 × the rebuild
// run), and the mutant is graded by its own result, a survivor when its run
// passes and a kill when it fails, never a timeout credited by the re-probe.
func TestScoreFloorsTheCapAtAMeasuredRebuildRun(t *testing.T) {
	mutants := []Mutant{{ID: "m", Search: "x = 0\n", Replace: warmCapMutantCode}}
	for _, passes := range []bool{true, false} {
		j := &rebuildFloorJail{warm: 20 * time.Second, mutantTakes: 40 * time.Second, mutantPasses: passes}
		rep, err := Score(context.Background(), j, nil, "a.py", warmCapCode, mutants, []string{"pytest", "-q"})
		if err != nil {
			t.Fatalf("Score: %v", err)
		}
		b := j.mutantBudgets()
		if len(b) == 0 || b[0] < 59*time.Second {
			t.Fatalf("mutant budgets = %v, want the first at least 60s (3 × the 20s rebuild run)", b)
		}
		g := rep.PerMutant["m"]
		if g.KillConfirmation == KillConfirmedReprobed {
			t.Errorf("mutant passes=%v credited %q: it finished within the rebuild-floored cap", passes, g.KillConfirmation)
		}
		if passes && (!hasID(rep.Survived, "m") || hasID(rep.Killed, "m")) {
			t.Errorf("passing mutant: Killed = %v, Survived = %v, want a survivor", rep.Killed, rep.Survived)
		}
		if !passes && (!hasID(rep.Killed, "m") || hasID(rep.Survived, "m")) {
			t.Errorf("failing mutant: Killed = %v, Survived = %v, want a kill by its run", rep.Killed, rep.Survived)
		}
	}
}

// A jail that reports no warm duration gets the cap it got before: the 30 s
// floor, a timeout, and a kill credited by the re-probe. Every jail that made
// no rebuild run (any but a WorkspacePool after MeasureRebuild) is this one.
func TestScoreWithoutARebuildRunKeepsTheCapItHad(t *testing.T) {
	inner := &rebuildFloorJail{mutantTakes: 40 * time.Second, mutantPasses: true}
	var j Jail = plainSlowMutantJail{inner}
	if _, ok := j.(WarmDurationJail); ok {
		t.Fatal("the plain jail must not report a warm duration")
	}
	rep, err := Score(context.Background(), j, nil, "a.py", warmCapCode,
		[]Mutant{{ID: "m", Search: "x = 0\n", Replace: warmCapMutantCode}}, []string{"pytest", "-q"})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	b := inner.mutantBudgets()
	if len(b) == 0 || !near(b[0], minMutantTimeout) {
		t.Fatalf("mutant budgets = %v, want the first at the %v floor", b, minMutantTimeout)
	}
	if !hasID(rep.Killed, "m") || rep.PerMutant["m"].KillConfirmation != KillConfirmedReprobed {
		t.Fatalf("Killed = %v (%q), want m killed by timeout and re-probe, as before", rep.Killed, rep.PerMutant["m"].KillConfirmation)
	}
}

// CapFloorApplies is the one answer to "does a floor reach this cap": not when
// the caller set the per-mutant timeout. Score's own budget follows it, so a
// record built from it cannot disagree with what was graded.
func TestCapFloorAppliesOnlyWithoutAnExplicitTimeout(t *testing.T) {
	if !CapFloorApplies(0) || CapFloorApplies(time.Minute) {
		t.Fatal("CapFloorApplies(0) must be true and CapFloorApplies(1m) false")
	}
	j := &rebuildFloorJail{warm: 20 * time.Second, mutantTakes: 40 * time.Second, mutantPasses: true}
	if _, err := Score(context.Background(), j, nil, "a.py", warmCapCode,
		[]Mutant{{ID: "m", Search: "x = 0\n", Replace: warmCapMutantCode}}, []string{"pytest", "-q"},
		WithMutantTimeout(45*time.Second)); err != nil {
		t.Fatal(err)
	}
	if b := j.mutantBudgets(); len(b) == 0 || !near(b[0], 45*time.Second) {
		t.Fatalf("budgets = %v, want the explicit 45s, not the 60s floor", b)
	}
}

// probedRebuildJail is rebuildFloorJail reporting the rebuild probe it ran
// (RebuildProbeJail), as a WorkspacePool does once MeasureRebuild has passed.
// The probe "takes" probeTakes the way the mutant takes mutantTakes: it pays
// the same rebuild, which is the point of it.
type probedRebuildJail struct {
	*rebuildFloorJail
	path, compliant, probed string
	probeTakes              time.Duration
	probeRuns               int
}

func (j *probedRebuildJail) RunTest(ctx context.Context, files map[string]string, cmd []string) (bool, error) {
	if files["a.py"] != j.probed {
		return j.rebuildFloorJail.RunTest(ctx, files, cmd)
	}
	j.mu.Lock()
	j.probeRuns++
	j.mu.Unlock()
	if dl, ok := ctx.Deadline(); ok && time.Until(dl) < j.probeTakes {
		return false, fmt.Errorf("%w: the probe ran past its budget", ErrTestTimeout)
	}
	return true, nil
}

func (j *probedRebuildJail) RebuildProbe() (codePath, compliant, probed string) {
	return j.path, j.compliant, j.probed
}

const warmCapProbeCode = "# corral rebuild probe\n" + warmCapCode

// A timed-out mutant is confirmed as a kill by re-running the unmutated code
// under the same budget. Where the mutant pays a rebuild the unmutated code
// does not (the reason a rebuild floor exists), that run must pay it too, or
// a box that cannot finish a REBUILD in the budget still finishes the cheap
// unmutated run, and a slow rebuild is credited as a kill. So when the jail
// ran a rebuild probe of this file, the re-probe runs the probe: here the
// probe takes as long as the mutant, the budget is shorter than both, and the
// mutant is not credited; with a fast probe it is, exactly as before.
func TestATimedOutMutantIsConfirmedByTheRebuildProbe(t *testing.T) {
	mutants := []Mutant{{ID: "m", Search: "x = 0\n", Replace: warmCapMutantCode}}
	for _, c := range []struct {
		name       string
		path       string
		probeTakes time.Duration
		wantKill   bool
		wantProbe  bool
	}{
		{"probe pays the mutant's rebuild", "a.py", 40 * time.Second, false, true},
		{"probe is fast", "a.py", time.Second, true, true},
		// A probe of another file says nothing about this one: the
		// unmutated code is re-run, as before.
		{"probe of another file", "b.py", 40 * time.Second, true, false},
	} {
		j := &probedRebuildJail{
			rebuildFloorJail: &rebuildFloorJail{warm: time.Second, mutantTakes: 40 * time.Second, mutantPasses: true},
			path:             c.path, compliant: warmCapCode, probed: warmCapProbeCode, probeTakes: c.probeTakes,
		}
		rep, err := Score(context.Background(), j, nil, "a.py", warmCapCode, mutants, []string{"pytest", "-q"})
		if got := j.probeRuns > 0; got != c.wantProbe {
			t.Errorf("%s: probe ran %d time(s), want ran = %v", c.name, j.probeRuns, c.wantProbe)
		}
		if c.wantKill {
			if err != nil || rep.PerMutant["m"].KillConfirmation != KillConfirmedReprobed {
				t.Errorf("%s: err %v, m = %+v, want a kill confirmed by the re-probe", c.name, err, rep.PerMutant["m"])
			}
			continue
		}
		if err == nil && hasID(rep.Killed, "m") {
			t.Errorf("%s: m credited as a kill (%q) though the probe, which pays its rebuild, did not finish in the budget either", c.name, rep.PerMutant["m"].KillConfirmation)
		}
		if err == nil || !strings.Contains(err.Error(), "rebuild probe") {
			t.Errorf("%s: err = %v, want the report failed naming the rebuild probe that timed out", c.name, err)
		}
	}
}
