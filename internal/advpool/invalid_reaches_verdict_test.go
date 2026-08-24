// SPDX-License-Identifier: Elastic-2.0

package advpool

import "testing"

// THE FOURTH RECURRENCE of one failure shape: a value added in one place, a
// second construction or conversion site missed, every unit test green.
//
//  1. MutantAttemptSink had no adapter at any composition root.
//  2. certify_local resolved --shadow-writer-model and dropped it before RunSpec.
//  3. advVerdictFromPool never mapped MutantsInvalid.
//  4. THIS: advpool builds a Verdict in TWO places — driver.go's literal and
//     aggregate.go's — and only the first carried MutantsInvalid. The count was
//     right in the log and ZERO in the printed, SIGNED verdict, which is the
//     copy an auditor actually reads.
func TestMutantsInvalidReachesTheSignedVerdict(t *testing.T) {
	d, missionID := newScoredRun(t, scoredRun{
		survivors: 2, provenMissed: 0, mutantsTotal: 5, invalid: 3,
		writerModel: "claude-sonnet-5", criticModel: "gemini-3.5-flash",
	})
	v := drivePoolToConvergence(t, d, missionID)
	if v == nil {
		t.Fatal("run did not converge")
	}
	if v.MutantsInvalid != 3 {
		t.Fatalf("Verdict.MutantsInvalid = %d, want 3 — the count died on the aggregate path, so the SIGNED verdict hides that the exam shrank", v.MutantsInvalid)
	}
	// The graded total must exclude them, or the summary contradicts the rate.
	if v.MutantsTotal != 2 {
		t.Errorf("Verdict.MutantsTotal = %d, want 2 (5 emitted - 3 invalid)", v.MutantsTotal)
	}
}
