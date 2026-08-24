// SPDX-License-Identifier: Elastic-2.0

package modelcorr

import (
	"testing"

	"github.com/pdbethke/corralai/internal/mutantattempts"
)

func TestFromAttemptsPairsTheTwoSeats(t *testing.T) {
	var as []mutantattempts.Attempt
	for i := 1; i <= 12; i++ {
		primary, shadow := "survived", "survived"
		if i <= 2 {
			primary, shadow = "killed", "killed"
		}
		as = append(as,
			mutantattempts.Attempt{RecordID: 1, Path: "a.go", MutantID: id(i), Model: "A", Role: "test-writer", Outcome: primary},
			mutantattempts.Attempt{RecordID: 1, Path: "a.go", MutantID: id(i), Model: "B", Role: "test-writer-shadow", Shadow: true, Outcome: shadow},
		)
	}
	pairs, err := FromAttempts(as)
	if err != nil {
		t.Fatalf("FromAttempts: %v", err)
	}
	if len(pairs) != 1 {
		t.Fatalf("got %d pairs, want 1", len(pairs))
	}
	if !pairs[0].Sufficient || pairs[0].Jaccard != 1 {
		t.Errorf("Jaccard = %v (sufficient=%v), want 1 — identical survivor sets", pairs[0].Jaccard, pairs[0].Sufficient)
	}
}

func TestFromAttemptsIgnoresAnUnpairedSeat(t *testing.T) {
	as := []mutantattempts.Attempt{
		{RecordID: 1, Path: "a.go", MutantID: "m1", Model: "A", Role: "test-writer", Outcome: "killed"},
	}
	pairs, err := FromAttempts(as)
	if err != nil {
		t.Fatalf("FromAttempts: %v", err)
	}
	if len(pairs) != 0 {
		t.Errorf("got %d pairs from a single unpaired seat, want 0", len(pairs))
	}
}

// IMPORTANT 5: with no run discriminator, two audits of the SAME path merged
// last-write-wins and the correlation was computed over a pooled vector — the
// cross-run pooling this package exists to refuse. record_id separates them.
func TestFromAttemptsDoesNotPoolTwoAuditsOfOnePath(t *testing.T) {
	var as []mutantattempts.Attempt
	for _, rec := range []int64{1, 2} {
		for i := 1; i <= 12; i++ {
			primary, shadow := "survived", "survived"
			if rec == 1 && i <= 2 {
				// Run 1's primary kills two; run 2's kills nothing. Pooled,
				// the two runs' vectors would collapse into one.
				primary, shadow = "killed", "killed"
			}
			as = append(as,
				mutantattempts.Attempt{RecordID: rec, Path: "a.go", MutantID: id(i), Model: "A", Role: "test-writer", Outcome: primary},
				mutantattempts.Attempt{RecordID: rec, Path: "a.go", MutantID: id(i), Model: "B", Role: "test-writer-shadow", Shadow: true, Outcome: shadow},
			)
		}
	}
	pairs, err := FromAttempts(as)
	if err != nil {
		t.Fatalf("FromAttempts: %v", err)
	}
	if len(pairs) != 2 {
		t.Fatalf("got %d pairs from two audits of one path, want 2 — the runs were pooled into a single comparison", len(pairs))
	}
	byUnion := map[int]bool{}
	for _, p := range pairs {
		byUnion[p.UnionSurvivors] = true
	}
	if !byUnion[10] || !byUnion[12] {
		t.Errorf("survivor unions = %v, want one run with 10 and one with 12 — each run must be measured over its OWN outcomes", byUnion)
	}
}
