// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"testing"
	"time"
)

// TestScoreReportsTheBaselineDuration asserts that the compliant-baseline
// wall-clock Score has always measured (to derive the per-mutant timeout) is
// surfaced on the Report rather than discarded. countingJail's hold field
// gives a known minimum run time, so the reported duration can be asserted
// as a lower bound without being flaky.
func TestScoreReportsTheBaselineDuration(t *testing.T) {
	fj := &countingJail{hold: 25 * time.Millisecond}
	rep, err := Score(context.Background(), fj, map[string]string{}, "code.py", "COMPLIANT", nil, []string{"pytest"})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if rep.BaselineDuration <= 0 {
		t.Fatal("BaselineDuration is zero — the suite runtime is the input to the whole cost model and was being discarded")
	}
	if rep.BaselineDuration < 20*time.Millisecond {
		t.Fatalf("BaselineDuration = %v, want at least the jail's own delay", rep.BaselineDuration)
	}
}
