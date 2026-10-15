// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"bytes"
	"log"
	"strings"
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

// The critic's result says which path produced its review: one typed call,
// or the tool loop after two typed answers that did not parse, and why. The
// driver read only its incomplete marker and dropped the rest, so a run whose
// critic fell back to the loop (seven calls instead of one, on a PHP file in
// 2026-10) could not say why from its output, its verdict or its record.
func TestTheCriticsPathReachesTheRunOutput(t *testing.T) {
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) })

	result := criticIncompletePrefix + "the typed critic's answer did not parse twice (no tests judged; no tests judged), so the tool loop reviewed instead: the critic used all 6 steps without concluding"
	survivors := []adequacy.Mutant{{ID: "m1", Replace: "c1"}}
	scorer := &fakeScorer{devKillRate: 0.9, devSurvivors: survivors, poolSurvivors: nil}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m0", Replace: "c0"}, survivors[0]}}
	d, _ := newTestDriver(t, 3, scorer, validator, 0.5)
	completeFullRun(t, d, 3, result)

	out := buf.String()
	for _, want := range []string{"test-critic", "did not parse twice (no tests judged; no tests judged)", "the tool loop reviewed instead"} {
		if !strings.Contains(out, want) {
			t.Errorf("run output does not carry %q:\n%s", want, out)
		}
	}
}

// A loop fallback appends the model's free-text answer after its note; the
// run output keeps one bounded line of it.
func TestCriticSummaryIsOneBoundedLine(t *testing.T) {
	long := criticIncompletePrefix + "note: " + strings.Repeat("x", 1000) + "\nsecond line"
	got := criticSummary(long)
	if strings.Contains(got, "\n") || strings.Contains(got, "second line") {
		t.Errorf("summary spans lines: %q", got)
	}
	if n := len([]rune(got)); n > 420 {
		t.Errorf("summary is %d runes, want it bounded near 400", n)
	}
	if !strings.HasPrefix(got, "INCOMPLETE — note: ") {
		t.Errorf("summary does not spell out the incomplete marker: %q", got[:40])
	}
	if got := criticSummary("typed critic judged 14 test(s), 0 flagged"); got != "typed critic judged 14 test(s), 0 flagged" {
		t.Errorf("a complete typed result changed: %q", got)
	}
}
