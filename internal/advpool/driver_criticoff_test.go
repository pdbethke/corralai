// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"context"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
)

// criticOffAssign is decorrelatedAssign with NO test-critic. An empty critic
// model is already legal at the guard (CheckDecorrelation only rejects a critic
// that EQUALS the writer) — these tests pin what that legality actually buys.
func criticOffAssign() RoleAssignment {
	return RoleAssignment{
		RoleMutantGenerator: "model-gen",
		RoleTestWriter:      "model-writer",
	}
}

// With no critic assigned, BuildDAG must not seed a test-critic task at all.
// Seeding one with an empty Model would send it to the base backend's default
// model — which on a single-vendor run is exactly the 404 this option exists to
// avoid — and nothing would ever complete it.
func TestBuildDAG_NoCriticAssigned_OmitsCriticTask(t *testing.T) {
	specs := BuildDAG(testRunSpec(), criticOffAssign(), nil)
	for _, s := range specs {
		if s.Role == RoleTestCritic || s.Key == RoleTestCritic {
			t.Fatalf("BuildDAG seeded a test-critic task with no critic assigned: %+v", s)
		}
	}
	// The other two roles must be untouched — this option removes the critic,
	// it does not change the pool.
	var sawGen, sawWriter bool
	for _, s := range specs {
		switch s.Role {
		case RoleMutantGenerator:
			sawGen = true
		case RoleTestWriter:
			sawWriter = true
		}
	}
	if !sawGen || !sawWriter {
		t.Fatalf("mutant-generator/test-writer missing: gen=%v writer=%v", sawGen, sawWriter)
	}
}

// The load-bearing one: the run must still CONVERGE to a verdict.
//
// tickAggregate waits for the critic task to report Done. With no critic task
// ever seeded that wait can never be satisfied, so before this option existed a
// critic-less run would spin to its --timeout and bank an unverified verdict
// instead of certifying — a silent hang, not an error.
func TestTick_NoCriticAssigned_StillConverges(t *testing.T) {
	survivors := []adequacy.Mutant{{ID: "m1", Replace: "c1"}}
	scorer := &fakeScorer{devKillRate: 0.9, devSurvivors: survivors, poolSurvivors: nil}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m0", Replace: "c0"}, survivors[0]}}

	q := newTestQueue(t)
	d, err := NewDriver(q, scorer, validator, criticOffAssign(), 0.5)
	if err != nil {
		t.Fatalf("NewDriver with no critic: %v", err)
	}
	if err := d.StartRun(7, testRunSpec(), nil); err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	if _, err := q.PromoteReady(7); err != nil {
		t.Fatalf("PromoteReady: %v", err)
	}

	ctx := context.Background()
	ready := claimAllReady(t, d.Q)
	if tc := ready[RoleTestCritic]; tc != nil {
		t.Fatalf("a test-critic task was ready despite no critic being assigned: %+v", tc)
	}
	mg := ready[RoleMutantGenerator]
	if mg == nil {
		t.Fatalf("expected mutant-generator ready, got: %v", keysOf(ready))
	}
	mustComplete(t, d.Q, mg.ID, "raw mutants")
	if _, err := d.Tick(ctx, 7); err != nil {
		t.Fatalf("Tick (dev-adequacy): %v", err)
	}

	tw := claimTaskByID(t, d.Q, d.runs[7].testWriterTaskID)
	mustComplete(t, d.Q, tw.ID, "pool test source")
	v, err := d.Tick(ctx, 7)
	if err != nil {
		t.Fatalf("Tick (pool-adequacy + aggregate): %v", err)
	}
	if v == nil {
		t.Fatal("no verdict: the run did not converge without a critic — it would spin to --timeout")
	}
	if v.Status != StatusCertified {
		t.Fatalf("Status = %q, want %q", v.Status, StatusCertified)
	}
	// The measurement this option exists to enable must be unaffected.
	if v.ProvenMissed != 1 {
		t.Fatalf("ProvenMissed = %d, want 1 — dropping the critic must not change the execution-proven result", v.ProvenMissed)
	}
	// No critic ran, so there is no advisory review to report. It must be
	// EMPTY, not a fabricated "nothing found" that reads like a clean review.
	if len(v.VacuousFindings) != 0 {
		t.Fatalf("VacuousFindings = %v, want none when no critic ran", v.VacuousFindings)
	}
	if m := v.ModelsByRole[RoleTestCritic]; m != "" {
		t.Fatalf("ModelsByRole[test-critic] = %q, want empty when no critic ran", m)
	}
	// Timing.Critic must stay at its "did not run" zero — the critic phase
	// never opened at all, since BuildDAG never seeded it a task. This is
	// the genuine case cmd/corral/timing_line.go's "—" is FOR, and it must
	// stay distinct from issue #201's "ran, and never converged" case (see
	// TestTimedOutVerdictAttributesAnOpenPhase).
	if v.Timing.Critic != 0 {
		t.Errorf("Timing.Critic = %v, want 0 — no critic was ever assigned", v.Timing.Critic)
	}
	// The writer DID run (this is what proved the one survivor above) and
	// must report a real duration, never the critic's own "did not run"
	// zero.
	if v.Timing.AuthoredPass <= 0 {
		t.Errorf("Timing.AuthoredPass = %v, want > 0 — the writer phase demonstrably ran and proved the survivor", v.Timing.AuthoredPass)
	}
}
