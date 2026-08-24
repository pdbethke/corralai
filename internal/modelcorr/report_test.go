// SPDX-License-Identifier: Elastic-2.0

package modelcorr

import (
	"testing"

	"github.com/pdbethke/corralai/internal/scanstore"
)

func TestFromAttemptsPairsTheTwoSeats(t *testing.T) {
	var as []scanstore.MutantAttempt
	for i := 1; i <= 12; i++ {
		primary, shadow := "survived", "survived"
		if i <= 2 {
			primary, shadow = "killed", "killed"
		}
		as = append(as,
			scanstore.MutantAttempt{ScanID: 1, Path: "a.go", MutantID: id(i), Model: "A", Role: "test-writer", Outcome: primary},
			scanstore.MutantAttempt{ScanID: 1, Path: "a.go", MutantID: id(i), Model: "B", Role: "test-writer-shadow", Shadow: true, Outcome: shadow},
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
	as := []scanstore.MutantAttempt{
		{ScanID: 1, Path: "a.go", MutantID: "m1", Model: "A", Role: "test-writer", Outcome: "killed"},
	}
	pairs, err := FromAttempts(as)
	if err != nil {
		t.Fatalf("FromAttempts: %v", err)
	}
	if len(pairs) != 0 {
		t.Errorf("got %d pairs from a single unpaired seat, want 0", len(pairs))
	}
}
