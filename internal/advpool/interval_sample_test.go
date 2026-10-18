// SPDX-License-Identifier: Elastic-2.0

package advpool

import "testing"

// The certification gate takes its interval over DISTINCT measured edits,
// and the verdict must carry that sample out, or every reader that prints
// an interval falls back to MutantsTotal, which counts duplicates, and shows
// a band narrower than the evidence (eight copies of one edit as n=8). It
// rides both Verdict construction sites.
func TestVerdictCarriesTheDistinctSample(t *testing.T) {
	v, _ := examEvidenceVerdict(t, false)
	if v.MutantsDistinct != 1 || v.MutantsDistinctSurvived != 0 {
		t.Errorf("converged verdict: distinct=%d survived=%d, want 1 and 0 (eight copies of one killed edit)", v.MutantsDistinct, v.MutantsDistinctSurvived)
	}
	tv := (&Driver{Assign: RoleAssignment{}}).timeoutVerdict(&runState{rs: RunSpec{Repo: "r", Commit: "c", Lang: "go"}, examSample: examSample{distinct: 3, survived: 1}})
	if tv.MutantsDistinct != 3 || tv.MutantsDistinctSurvived != 1 {
		t.Errorf("timeout verdict: distinct=%d survived=%d, want 3 and 1", tv.MutantsDistinct, tv.MutantsDistinctSurvived)
	}
}
