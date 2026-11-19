// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/sandbox"
)

// warmCapJail is a RunCacheJail whose cache was warmed by a run that took
// warm, and whose mutant run "takes" mutantTakes: it finishes and PASSES when
// its budget (the context deadline) is at least that, and times out
// otherwise, without sleeping. The compliant code passes at once. It records
// each run's budget by the code it ran, so a test can read the cap a mutant
// and its re-probe were given.
type warmCapJail struct {
	warm        time.Duration
	mutantTakes time.Duration
	mu          sync.Mutex
	budgets     map[string][]time.Duration
}

func (j *warmCapJail) RunTest(ctx context.Context, files map[string]string, cmd []string) (bool, error) {
	code := files["a.py"]
	if code == CanaryCode {
		return false, nil
	}
	var budget time.Duration
	if dl, ok := ctx.Deadline(); ok {
		budget = time.Until(dl)
		j.mu.Lock()
		if j.budgets == nil {
			j.budgets = map[string][]time.Duration{}
		}
		j.budgets[code] = append(j.budgets[code], budget)
		j.mu.Unlock()
	}
	if code == warmCapMutantCode && budget > 0 && budget < j.mutantTakes {
		return false, fmt.Errorf("%w: ran past its %s budget", ErrTestTimeout, budget)
	}
	return true, nil
}

func (j *warmCapJail) WarmRun(ctx context.Context, files map[string]string, cmd []string) (bool, []byte, error) {
	ok, err := j.RunTest(ctx, files, cmd)
	return ok, nil, err
}
func (j *warmCapJail) RunCache() sandbox.SharedCache { return sandbox.SharedCache{Target: "/cache"} }
func (j *warmCapJail) Warmed() bool                  { return j.warm > 0 }
func (j *warmCapJail) WarmDuration() time.Duration   { return j.warm }
func (j *warmCapJail) Close() error                  { return nil }

func (j *warmCapJail) budgetsFor(code string) []time.Duration {
	j.mu.Lock()
	defer j.mu.Unlock()
	return append([]time.Duration(nil), j.budgets[code]...)
}

const (
	warmCapCode       = "x = 0\n"
	warmCapMutantCode = "x = 1\n"
)

// near reports whether a budget read from a deadline is the cap want: a
// deadline is read a moment after it is set, so it is at most want and not
// more than a second under it.
func near(got, want time.Duration) bool { return got <= want && got > want-time.Second }

// The cap rule itself: max(multiple × this command's baseline, multiple × the
// warm run, the floor), under the ceiling. It can only grow from the warm run.
func TestMutantTimeoutCapIsFlooredByTheWarmRun(t *testing.T) {
	for _, c := range []struct {
		name             string
		base, warm, want time.Duration
	}{
		{"a frozen 1s baseline after a 20s warm run", time.Second, 20 * time.Second, 60 * time.Second},
		{"no warm run: the 30s floor", time.Second, 0, minMutantTimeout},
		{"the larger of the two wins", 100 * time.Second, 20 * time.Second, 300 * time.Second},
		{"a 20s baseline alone, as before", 20 * time.Second, 0, 60 * time.Second},
		{"the ceiling still holds", 2 * time.Minute, 3 * time.Minute, maxMutantTimeout},
	} {
		if got := mutantTimeoutCap(c.base, c.warm, minMutantTimeout); got != c.want {
			t.Errorf("%s: cap = %v, want %v", c.name, got, c.want)
		}
	}
}

// A frozen baseline is a full cache hit; a mutant rebuilds what it changed.
// With the jail warmed by a 20s run, a mutant that finishes (and passes) in
// 40s is a SURVIVOR, graded under a 60s cap, never a timeout credited as a
// kill by the re-probe. Without a warm run, the same jail is what it was: the
// 30s floor, a timeout, and a re-probed kill.
func TestScoreFloorsTheMutantCapAtTheWarmRun(t *testing.T) {
	mutants := []Mutant{{ID: "m", Search: "x = 0\n", Replace: warmCapMutantCode}}
	score := func(j *warmCapJail) Report {
		t.Helper()
		rep, err := Score(context.Background(), j, nil, "a.py", warmCapCode, mutants, []string{"pytest", "-q"})
		if err != nil {
			t.Fatalf("Score: %v", err)
		}
		return rep
	}

	warmed := &warmCapJail{warm: 20 * time.Second, mutantTakes: 40 * time.Second}
	rep := score(warmed)
	mb := warmed.budgetsFor(warmCapMutantCode)
	if len(mb) == 0 || !near(mb[0], 60*time.Second) {
		t.Fatalf("mutant budgets = %v, want the first at 60s (3 × the 20s warm run), not the 30s floor or 3s", mb)
	}
	if !hasID(rep.Survived, "m") || hasID(rep.Killed, "m") {
		t.Errorf("Killed = %v, Survived = %v: a mutant that finished within the warm-floored cap must be graded by its run, not credited by timeout", rep.Killed, rep.Survived)
	}
	if g := rep.PerMutant["m"]; g.KillConfirmation == KillConfirmedReprobed {
		t.Errorf("mutant m credited %q: it finished within the cap", g.KillConfirmation)
	}

	cold := &warmCapJail{mutantTakes: 40 * time.Second}
	crep := score(cold)
	cb := cold.budgetsFor(warmCapMutantCode)
	if len(cb) == 0 || !near(cb[0], minMutantTimeout) {
		t.Fatalf("unwarmed mutant budgets = %v, want the %v floor", cb, minMutantTimeout)
	}
	if !hasID(crep.Killed, "m") {
		t.Fatalf("unwarmed: Killed = %v, want m killed by timeout as before", crep.Killed)
	}
	// The re-probe ran the compliant code under the mutant's own cap.
	compliant := cold.budgetsFor(warmCapCode)
	if len(compliant) == 0 || !near(compliant[len(compliant)-1], minMutantTimeout) {
		t.Errorf("unwarmed re-probe budgets = %v, want the last at %v", compliant, minMutantTimeout)
	}
}

// The re-probe uses the same warm-floored cap as the mutant run: a mutant
// that truly hangs on a warmed jail is still a re-probed kill, and the
// compliant re-run was given 60s, not 30s.
func TestScoreReprobeUsesTheWarmFlooredCap(t *testing.T) {
	j := &warmCapJail{warm: 20 * time.Second, mutantTakes: time.Hour}
	rep, err := Score(context.Background(), j, nil, "a.py", warmCapCode,
		[]Mutant{{ID: "m", Search: "x = 0\n", Replace: warmCapMutantCode}}, []string{"pytest", "-q"})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if !hasID(rep.Killed, "m") {
		t.Fatalf("Killed = %v, want the hanging mutant killed", rep.Killed)
	}
	if confirmed := rep.PerMutant["m"].KillConfirmation; confirmed != KillConfirmedReprobed {
		t.Errorf("KillConfirmation = %q, want %q", confirmed, KillConfirmedReprobed)
	}
	compliant := j.budgetsFor(warmCapCode)
	if len(compliant) == 0 || !near(compliant[len(compliant)-1], 60*time.Second) {
		t.Errorf("re-probe budgets = %v, want the last (the re-probe) at 60s", compliant)
	}
}
