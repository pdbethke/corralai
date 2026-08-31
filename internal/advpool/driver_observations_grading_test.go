// SPDX-License-Identifier: Elastic-2.0

package advpool

import "testing"

// The bug-catching scorecard exists to grade the testing agents on
// EXECUTION-PROVEN signal — catches come only from ProvenMissed, and no
// self-report may reach it. That discipline is undermined if a model is
// charged for chances corral's own pipeline never gave it.
//
// Both tests below describe the same root defect: bugCatchObservations never
// consulted poolTestUnsound, even though runState carries it.
// writerObs pulls the test-writer's observation out of a run, reusing the
// existing obsFor helper in driver_test.go rather than shadowing it.
func writerObs(t *testing.T, run *runState, v Verdict) BugCatchObservation {
	t.Helper()
	got, ok := obsFor(bugCatchObservations(run, v), RoleTestWriter)
	if !ok {
		t.Fatal("no observation for the test-writer role")
	}
	return got
}

// TestBugCatch_UnsoundRunIsNotACountedOpportunity: on a run whose authored
// test compiled but never genuinely graded (failed on unmutated code, canary
// never killed, or nothing scored), the test-writer had NO chance to catch
// anything — the pipeline denied it. Charging its survivors to the
// denominator drives recall toward 0% and reports that as a property of the
// MODEL. That is the same false-accusation shape scanstore's own NULL-never-0.0
// rule exists to prevent ("a stored 0.0 would later read as 'your tests caught
// nothing here' about a file corral never graded"), aimed at a model instead
// of a file — and it mattered because ProvenMissed was structurally pinned to
// zero on real repos until 2026-07-31, so every real-repo run scored the
// writer 0%.
func TestBugCatch_UnsoundRunIsNotACountedOpportunity(t *testing.T) {
	run := &runState{poolScored: true, authoredTest: "AUTHORED", poolTestUnsound: true}
	v := Verdict{ProvenMissed: 0, Survivors: 10, PoolTestUnsound: true,
		ModelsByRole: map[string]string{RoleTestWriter: "m"}}

	got := writerObs(t, run, v)
	if got.Opportunities != 0 {
		t.Errorf("Opportunities = %d, want 0 — an ungraded run gave the writer no chance to catch anything", got.Opportunities)
	}
	if got.Catches != 0 {
		t.Errorf("Catches = %d, want 0", got.Catches)
	}
}

// TestBugCatch_UnsoundTestIsNotASoundTest: SoundTests feeds the scorecard's
// PRECISION column (sound/authored). runState.poolScored is set to true BEFORE
// the unsound check in tickPoolAdequacy (driver.go:1259 vs :1260), so
// "poolScored && authoredTest != ”" was true for an UNSOUND run and the
// scorecard counted a test that never graded as a sound one — the exact
// failure precision is supposed to measure, scored as a success.
func TestBugCatch_UnsoundTestIsNotASoundTest(t *testing.T) {
	run := &runState{poolScored: true, authoredTest: "AUTHORED", poolTestUnsound: true}
	v := Verdict{Survivors: 10, PoolTestUnsound: true,
		ModelsByRole: map[string]string{RoleTestWriter: "m"}}

	got := writerObs(t, run, v)
	if got.AuthoredTests != 1 {
		t.Errorf("AuthoredTests = %d, want 1 — the writer DID author a compiling test", got.AuthoredTests)
	}
	if got.SoundTests != 0 {
		t.Errorf("SoundTests = %d, want 0 — it never genuinely graded, which is precisely what precision must penalise", got.SoundTests)
	}
}

// TestBugCatch_SoundRunStillCountsNormally is the guard against over-correcting:
// a run that genuinely graded must still charge its survivors as opportunities
// and credit its catches, or the fix would erase the metric it is protecting.
func TestBugCatch_SoundRunStillCountsNormally(t *testing.T) {
	// primaryWriterMeasured is what "genuinely graded" MEANS — see the flag's
	// own doc. bugCatchObservations asks it directly now, rather than
	// inferring it from a non-empty authoredTest (which is empty on a sound
	// per-survivor run whose parts would not merge).
	run := &runState{poolScored: true, authoredTest: "AUTHORED", primaryWriterMeasured: true}
	v := Verdict{ProvenMissed: 3, Survivors: 10,
		ModelsByRole: map[string]string{RoleTestWriter: "m"}}

	got := writerObs(t, run, v)
	if got.Opportunities != 10 || got.Catches != 3 || got.SoundTests != 1 || got.AuthoredTests != 1 {
		t.Fatalf("sound run must count normally, got %+v", got)
	}
}

// TestBugCatch_WriterFailedIsNotACountedOpportunity: the writer never produced
// a compiling test at all, so there was no graded chance either. It authored
// nothing sound, and its survivors must not inflate the denominator.
func TestBugCatch_WriterFailedIsNotACountedOpportunity(t *testing.T) {
	run := &runState{poolScored: true, testWriterFailed: true}
	v := Verdict{ProvenMissed: 0, Survivors: 7, TestWriterFailed: true,
		ModelsByRole: map[string]string{RoleTestWriter: "m"}}

	got := writerObs(t, run, v)
	if got.Opportunities != 0 {
		t.Errorf("Opportunities = %d, want 0 — no compiling test was ever produced", got.Opportunities)
	}
	if got.SoundTests != 0 {
		t.Errorf("SoundTests = %d, want 0", got.SoundTests)
	}
}
