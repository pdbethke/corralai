// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
)

// completeRunTolerantOfWriterRetries drives a run to a verdict the way
// completeFullRun does, but keeps completing the test-writer task while the
// driver keeps reissuing it. completeFullRun completes that task exactly once
// and demands a verdict on the next tick, so it cannot express a retry at all
// — which is precisely the behaviour under test here.
func completeRunTolerantOfWriterRetries(t *testing.T, d *Driver, missionID int64, criticResult string) *Verdict {
	t.Helper()
	ctx := context.Background()

	ready := claimAllReady(t, d.Q)
	tc, mg := ready[RoleTestCritic], ready[RoleMutantGenerator]
	if tc == nil || mg == nil {
		t.Fatalf("expected test-critic and mutant-generator both ready, got: %v", keysOf(ready))
	}
	mustComplete(t, d.Q, tc.ID, criticResult)
	mustComplete(t, d.Q, mg.ID, "raw mutants")
	if _, err := d.Tick(ctx, missionID); err != nil {
		t.Fatalf("Tick (dev-adequacy): %v", err)
	}

	// Bounded well above MaxTestWriterAttempts so an unexpected loop fails the
	// test rather than hanging it.
	for i := 0; i < MaxTestWriterAttempts+3; i++ {
		tw := claimTaskByID(t, d.Q, d.runs[missionID].testWriterTaskID)
		mustComplete(t, d.Q, tw.ID, "pool test source")
		v, err := d.Tick(ctx, missionID)
		if err != nil {
			t.Fatalf("Tick (pool-adequacy + aggregate): %v", err)
		}
		if v != nil {
			return v
		}
	}
	t.Fatal("run never converged — the test-writer was reissued more times than the attempt cap allows")
	return nil
}

// TestTick_PoolAdequacy_CleanCodeFailureIsRetried closes the gap a
// gemini-3.6-flash audit of pallets/flask exposed on 2026-07-31, the first run
// whose authored test was actually retained and could be read.
//
// The writer produced 13 tests against flask's real internals. TEN PASSED on
// clean code. Three carried wrong API assumptions (with_appcontext applied to
// an already-built click Command; -A and --debug rejected as SystemExit(2)).
// Because CompliantPass is all-or-nothing per FILE, those three discarded all
// thirteen — Total=0, nothing scored, the entire run zeroed, including ten
// tests that might well have killed survivors.
//
// And the writer was never told. The COMPILE-failure path has a corrective
// retry that feeds the compiler's own error back (see renderTestWriterWithRepair
// and the CompileError type, which exists precisely because a bare "does not
// compile" taught the model nothing). The CLEAN-CODE-failure path had no retry
// at all: tickPoolAdequacy saw CompliantPass=false, logged, set poolTestUnsound
// and converged. A test that compiles and then fails on unmutated code is just
// as diagnosable — pytest says exactly what broke — and the model never saw it.
func TestTick_PoolAdequacy_CleanCodeFailureIsRetried(t *testing.T) {
	survivors := []adequacy.Mutant{{ID: "m1", Replace: "c1"}, {ID: "m2", Replace: "c2"}}

	calls := 0
	scorer := &fakeScorer{
		devKillRate:  0.9,
		devSurvivors: survivors,
		poolReportFn: func(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (adequacy.Report, error) {
			calls++
			if calls == 1 {
				// First attempt: fails on unmutated code, exactly like the
				// three bad tests in the measured flask run.
				return adequacy.Report{CompliantPass: false, Total: 0}, nil
			}
			// The repaired attempt grades properly and kills one survivor.
			return adequacy.Report{
				CompliantPass: true, CanaryKilled: true,
				Total: 2, Killed: []string{"m1"}, Survived: []string{"m2"},
			}, nil
		},
	}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m0", Replace: "c0"}, survivors[0], survivors[1]}}
	d, _ := newTestDriver(t, 2, scorer, validator, 0.1)

	v := completeRunTolerantOfWriterRetries(t, d, 2, "no findings")

	if calls < 2 {
		t.Fatalf("the authored test was scored %d time(s) — a clean-code failure must be retried with the failure fed back, not converged as unsound on the first attempt", calls)
	}
	if v.PoolTestUnsound {
		t.Error("PoolTestUnsound = true, but the retry produced a soundly-grading test — the recovered run must not carry the failed attempt's diagnosis")
	}
	if v.ProvenMissed != 1 {
		t.Errorf("ProvenMissed = %d, want 1 — the repaired test killed m1, and that proof must survive the retry", v.ProvenMissed)
	}
}

// TestTick_PoolAdequacy_CleanCodeFailureStillConvergesWhenRetriesExhaust is the
// guard against the retry becoming a hang or a fabrication: a writer that never
// produces a passing test must still converge, still report ProvenMissed=0, and
// still be marked unsound rather than spinning to the run deadline or inventing
// proof from a run that graded nothing.
func TestTick_PoolAdequacy_CleanCodeFailureStillConvergesWhenRetriesExhaust(t *testing.T) {
	survivors := []adequacy.Mutant{{ID: "m1", Replace: "c1"}, {ID: "m2", Replace: "c2"}}
	scorer := &fakeScorer{
		devKillRate:  0.9,
		devSurvivors: survivors,
		poolReportFn: func(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (adequacy.Report, error) {
			return adequacy.Report{CompliantPass: false, Total: 0}, nil // never recovers
		},
	}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m0", Replace: "c0"}, survivors[0], survivors[1]}}
	d, _ := newTestDriver(t, 2, scorer, validator, 0.1)

	v := completeRunTolerantOfWriterRetries(t, d, 2, "no findings")

	if v.ProvenMissed != 0 {
		t.Errorf("ProvenMissed = %d, want 0 — a run that never graded must never report proof", v.ProvenMissed)
	}
	if !v.PoolTestUnsound {
		t.Error("PoolTestUnsound = false, want true — exhausting the retries must still land on the honest diagnosis")
	}
	if v.Status != StatusNeedsReview {
		t.Errorf("Status = %q, want needs-review", v.Status)
	}
}
