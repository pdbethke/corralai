// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"bytes"
	"context"
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
// critic seated there is nothing to be incomplete — whether the operator
// turned it off or the run had no developer test for it to judge, which the
// verdict says (CriticNotRun) where the operator's choice needs no word.
func TestATimedOutVerdictSaysTheCriticsReviewDidNotReachIt(t *testing.T) {
	withTest := RunSpec{Repo: "r", Commit: "c", Lang: "go", DevTestPath: "a_test.go", DevTestCode: "package a\n"}
	seated := (&Driver{Assign: RoleAssignment{RoleTestCritic: "critic"}}).timeoutVerdict(&runState{rs: withTest})
	if !seated.CriticIncomplete || seated.CriticNotRun != "" {
		t.Errorf("a timed-out verdict with a seated critic must say its review is incomplete: %+v", seated)
	}
	off := (&Driver{Assign: RoleAssignment{}}).timeoutVerdict(&runState{rs: withTest})
	if off.CriticIncomplete || off.CriticNotRun != "" {
		t.Error("no critic was seated, so nothing is incomplete and no reason is owed")
	}
	none := (&Driver{Assign: RoleAssignment{RoleTestCritic: "critic"}}).timeoutVerdict(&runState{rs: RunSpec{Repo: "r", Commit: "c", Lang: "rust", CodePath: "src/a.rs"}})
	if none.CriticIncomplete || !strings.Contains(none.CriticNotRun, "no developer test to judge") || none.ModelsByRole[RoleTestCritic] != "" {
		t.Errorf("a run with no developer test seats no critic, and says why: %+v", none)
	}
}

// The converged path (tickAggregate) reads the same answer as the timed-out
// one: a run with no developer test seeds no critic task, so the run does not
// wait on one, and its verdict carries no critic in the roster and the reason
// instead of an empty review that reads as "found nothing".
func TestACriticWithNoDeveloperTestIsNotRun(t *testing.T) {
	rs := testRunSpec()
	rs.DevTestPath, rs.DevTestCode = "", ""
	for _, spec := range BuildDAG(rs, decorrelatedAssign(), nil) {
		if spec.Key == RoleTestCritic {
			t.Fatalf("BuildDAG seeded a critic task with no developer test to judge: %+v", spec)
		}
	}
	survivors := []adequacy.Mutant{{ID: "m1", Replace: "c1"}}
	scorer := &fakeScorer{devKillRate: 0.9, devSurvivors: survivors}
	validator := &fakeValidator{mutants: []adequacy.Mutant{{ID: "m0", Replace: "c0"}, survivors[0]}}
	d := newTestDriverWithSpec(t, 7, scorer, validator, 0.5, rs)
	ctx := context.Background()
	ready := claimAllReady(t, d.Q)
	if ready[RoleTestCritic] != nil {
		t.Fatal("a critic task is ready with no developer test to judge")
	}
	mustComplete(t, d.Q, ready[RoleMutantGenerator].ID, "raw mutants")
	if _, err := d.Tick(ctx, 7); err != nil {
		t.Fatal(err)
	}
	tw := claimTaskByID(t, d.Q, d.runs[7].testWriterTaskID)
	mustComplete(t, d.Q, tw.ID, "pool test source")
	v, err := d.Tick(ctx, 7)
	if err != nil || v == nil {
		t.Fatalf("the run must converge without a critic: v=%v err=%v", v, err)
	}
	if v.CriticIncomplete || !strings.Contains(v.CriticNotRun, "no developer test to judge") {
		t.Errorf("CriticIncomplete=%v CriticNotRun=%q, want the reason and no incomplete review", v.CriticIncomplete, v.CriticNotRun)
	}
	if _, seated := v.ModelsByRole[RoleTestCritic]; seated {
		t.Errorf("the roster names a critic that never ran: %v", v.ModelsByRole)
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

// TestCriticNotRunReasonClaimsOnlyWhatItChecked: the reason is decided by an
// empty developer test in ANY language, so it says only that; it used to
// add "so it is graded by the tests that execute it", which is the Rust
// no-test-module case and nothing the check looked at.
func TestCriticNotRunReasonClaimsOnlyWhatItChecked(t *testing.T) {
	assign := RoleAssignment{RoleTestCritic: "google/gemini-3.6-flash"}
	for _, l := range []string{"go", "python", "rust"} {
		got := CriticNotRunReason(RunSpec{Lang: l, CodePath: "x"}, assign)
		if !strings.HasPrefix(got, "no developer test to judge") || strings.Contains(got, "execute") {
			t.Errorf("%s: reason %q", l, got)
		}
	}
}
