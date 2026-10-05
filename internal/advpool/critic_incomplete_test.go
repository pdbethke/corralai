// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/agentworker"
)

// The driver reads the marker the critic's loop writes; the two constants
// live in different packages (advpool does not import agentworker), so this
// test is what keeps them the same string.
func TestCriticIncompleteMarkerMatchesTheLoops(t *testing.T) {
	if criticIncompletePrefix != agentworker.CriticIncompletePrefix {
		t.Fatalf("advpool reads %q but the critic loop writes %q", criticIncompletePrefix, agentworker.CriticIncompletePrefix)
	}
}

// A critic whose review was cut short is carried onto the converged verdict
// as CriticIncomplete, so its findings count is never read as a complete
// review. It changes nothing else: the critic is advisory, and the status is
// still set by execution.
func TestAConvergedVerdictSaysTheCriticWasCutShort(t *testing.T) {
	survivors := []adequacy.Mutant{{ID: "m1", Replace: "c1"}}
	for _, tc := range []struct {
		result string
		want   bool
	}{
		{"no vacuous tests found", false},
		{criticIncompletePrefix + "the critic used all 6 steps without concluding", true},
	} {
		scorer := &fakeScorer{devKillRate: 0.9, devSurvivors: survivors, poolSurvivors: nil}
		validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m0", Replace: "c0"}, survivors[0]}}
		d, _ := newTestDriver(t, 3, scorer, validator, 0.5)
		v := completeFullRun(t, d, 3, tc.result)
		if v.CriticIncomplete != tc.want {
			t.Errorf("critic result %q: CriticIncomplete = %v, want %v", tc.result, v.CriticIncomplete, tc.want)
		}
		if v.Status != StatusCertified {
			t.Errorf("critic result %q changed the status to %q; the critic is advisory", tc.result, v.Status)
		}
	}
}

// The timed-out path never reads the critic's findings, so with a critic
// seated its review did not reach the verdict at all: incomplete. With no
// critic seated there is nothing to be incomplete.
func TestATimedOutVerdictSaysTheCriticsReviewDidNotReachIt(t *testing.T) {
	seated := (&Driver{Assign: RoleAssignment{RoleTestCritic: "critic"}}).timeoutVerdict(&runState{rs: RunSpec{Repo: "r", Commit: "c", Lang: "go"}})
	if !seated.CriticIncomplete {
		t.Error("a timed-out verdict with a seated critic must say its review is incomplete")
	}
	off := (&Driver{Assign: RoleAssignment{}}).timeoutVerdict(&runState{rs: RunSpec{Repo: "r", Commit: "c", Lang: "go"}})
	if off.CriticIncomplete {
		t.Error("no critic was seated, so nothing is incomplete")
	}
}
