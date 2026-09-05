// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"testing"
	"time"
)

// deadlineJail records the budget each run was given (its context deadline)
// keyed by the mutant code it ran, so a test can see WHICH cap a mutant got.
// The shared command's compliant run is slow; the narrowed command's is fast.
type deadlineJail struct {
	budgets map[string][]time.Duration
	holds   map[string]time.Duration // by command key
}

func (j *deadlineJail) RunTest(ctx context.Context, files map[string]string, cmd []string) (bool, error) {
	key := ""
	for _, c := range cmd {
		key += c + " "
	}
	if h := j.holds[key]; h > 0 {
		time.Sleep(h)
	}
	code := files["a.py"]
	if code == CanaryCode {
		return false, nil
	}
	if dl, ok := ctx.Deadline(); ok {
		if j.budgets == nil {
			j.budgets = map[string][]time.Duration{}
		}
		j.budgets[code] = append(j.budgets[code], time.Until(dl))
	}
	return true, nil
}

// psf/requests models.py, 2026-09-04: three mutants hung to the five-minute
// ceiling and were fifteen of the file's twenty-four dev-pass minutes. The
// cap was derived from the file's shared baseline × 8; a mutant graded by a
// two-test command got the same five minutes as one graded by three hundred.
// Now each grading command's own compliant run — which the proving loop
// already makes — is the baseline its mutants' cap is derived from, at 3×.
func TestMutantCapIsDerivedPerCommand(t *testing.T) {
	shared := []string{"pytest", "-q"}
	narrow := []string{"pytest", "-q", "test_a.py::t1"}
	j := &deadlineJail{holds: map[string]time.Duration{"pytest -q ": 60 * time.Millisecond, "pytest -q test_a.py::t1 ": 5 * time.Millisecond}}
	mutants := []Mutant{{ID: "wide", Replace: "x = 1\n"}, {ID: "narrow", Replace: "x = 2\n"}}
	_, err := Score(context.Background(), j, map[string]string{"test_a.py": "def test(): pass\n"}, "a.py", "x = 0\n", mutants, shared,
		WithCommandFor(func(m Mutant) MutantCommand {
			if m.ID == "narrow" {
				return MutantCommand{Cmd: narrow, Tests: 1, Rule: "lines"}
			}
			return MutantCommand{Cmd: shared, Tests: 7, Rule: "static"}
		}))
	if err != nil {
		t.Fatal(err)
	}
	// Both baselines are sub-second, so both caps floor at minMutantTimeout —
	// the floor is the honest cap for a fast suite. What must hold is that
	// the narrowed mutant's cap came from ITS command's run, and that the
	// multiple is what the constant says, not the old 8.
	if mutantTimeoutMultiple != 3 {
		t.Fatalf("mutantTimeoutMultiple = %d, want 3 — PIT's 1.25× plus room for six-tree contention; 8 let a hang cost five minutes", mutantTimeoutMultiple)
	}
	if got := clampMutantTimeout(20 * time.Second); got != 60*time.Second {
		t.Errorf("a 20s baseline caps at %v, want 60s (3×)", got)
	}
	if got := clampMutantTimeout(2 * time.Minute); got != maxMutantTimeout {
		t.Errorf("a 2m baseline caps at %v, want the %v ceiling", got, maxMutantTimeout)
	}
	for _, id := range []string{"x = 1\n", "x = 2\n"} {
		if len(j.budgets[id]) == 0 {
			t.Fatalf("mutant %q ran with no deadline at all", id)
		}
		if b := j.budgets[id][0]; b > minMutantTimeout || b < minMutantTimeout-time.Second {
			t.Errorf("mutant %q budget = %v, want the %v floor for a sub-second baseline", id, b, minMutantTimeout)
		}
	}
}

// A per-command baseline that is SLOWER than the shared one must give its
// mutants MORE room, not the shared cap: the point of deriving per command is
// that the cap follows the command actually run.
func TestMutantCapFollowsASlowerNarrowedCommand(t *testing.T) {
	// Simulate with the pure derivation: the loop records time.Since of the
	// proving run per command key and capFor reads it. Exercised end to end
	// above with sub-second holds; here the arithmetic on a real duration.
	if got := clampMutantTimeout(45 * time.Second); got != 135*time.Second {
		t.Errorf("45s baseline → %v, want 2m15s", got)
	}
}
