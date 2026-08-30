// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
)

// TestSlowMachineTimeoutIsNotAKill pins the distinction a timeout alone cannot
// make.
//
// A timed-out mutant used to be scored as KILLED outright, on the reasoning
// that a hanging suite is a caught divergence. That is true for a
// non-terminating mutant and FALSE for a merely slow machine — and a loaded
// shared runner (where the GitHub Action executes) produces the second while
// looking exactly like the first. Counting both as kills inflates the kill
// rate: crediting the tests with catching something they never detected, the
// same defect class as scoring a compiler-rejected mutant as caught.
//
// Here EVERYTHING times out, including the compliant baseline — the signature
// of a loaded box. Nothing may be inferred: not a kill, not a survivor.
func TestSlowMachineTimeoutIsNotAKill(t *testing.T) {
	// The box is healthy when the run starts — the baseline passes, so grading
	// begins — and is loaded by the time the mutant runs. That is the real
	// shape of the failure: a timeout derived from an idle measurement, applied
	// on a busy machine.
	var baselineCalls atomic.Int32
	j := &timeoutFakeJail{
		guard: guardPathForTimeoutTest,
		baseline: func() (bool, error) {
			if baselineCalls.Add(1) == 1 {
				return true, nil // healthy at the start
			}
			return false, fmt.Errorf("%w: box is wedged now", ErrTestTimeout)
		},
		mutantRun: func() (bool, error) { return false, fmt.Errorf("%w: box is wedged now", ErrTestTimeout) },
	}
	rep, err := scoreOneMutant(j)
	if err == nil {
		t.Fatalf("Score returned no error when the baseline RE-PROBE also timed out; report = %+v", rep)
	}
	if len(rep.Killed) != 0 {
		t.Fatalf("Killed = %v, want none: a slow box is not a caught bug", rep.Killed)
	}
}

// A mutant that times out while the compliant baseline still finishes really is
// non-terminating — and the kill must be recorded on the FIRST probe, not after
// exhausting a second full budget. That fast path is what
// TestScoreJSNonTerminatingMutantIsKilledFast depends on.
func TestNonTerminatingMutantIsStillAFastKill(t *testing.T) {
	var baselineCalls atomic.Int32
	j := &timeoutFakeJail{
		guard: guardPathForTimeoutTest,
		baseline: func() (bool, error) {
			baselineCalls.Add(1)
			return true, nil // the box is fine
		},
		mutantRun: func() (bool, error) { return false, fmt.Errorf("%w: infinite loop", ErrTestTimeout) },
	}
	rep, err := scoreOneMutant(j)
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if len(rep.Killed) != 1 {
		t.Fatalf("Killed = %v, want 1: a non-terminating mutant is a real kill", rep.Killed)
	}
	// baseline, canary-adjacent setup, plus ONE probe — the mutant is never re-run.
	if got := baselineCalls.Load(); got < 1 {
		t.Errorf("baseline probe ran %d time(s), want at least the one probe", got)
	}
}

// timeoutFakeJail answers the compliant baseline and the mutant separately, so
// a test can make the BOX look slow (both time out) or make only the MUTANT
// hang (baseline still finishes).
type timeoutFakeJail struct {
	guard     string
	baseline  func() (bool, error)
	mutantRun func() (bool, error)
}

func (j *timeoutFakeJail) RunTest(ctx context.Context, files map[string]string, testCmd []string) (bool, error) {
	switch files[j.guard] {
	case compliantForTimeoutTest:
		return j.baseline()
	case mutantForTimeoutTest:
		return j.mutantRun()
	default:
		// The canary: a deliberately broken variant the suite must FAIL on.
		return false, nil
	}
}

const (
	compliantForTimeoutTest = "package p // compliant"
	mutantForTimeoutTest    = "package p // mutant"
	guardPathForTimeoutTest = "p.go"
)

func scoreOneMutant(j Jail) (Report, error) {
	return Score(
		context.Background(), j,
		map[string]string{guardPathForTimeoutTest: compliantForTimeoutTest},
		guardPathForTimeoutTest, compliantForTimeoutTest,
		[]Mutant{{ID: "m1", Replace: mutantForTimeoutTest}},
		[]string{"go", "test", "./..."},
	)
}
