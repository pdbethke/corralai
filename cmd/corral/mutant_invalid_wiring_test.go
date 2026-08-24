// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
)

// THE WIRE TEST, and this failure mode has now recurred three times on this
// branch: a value is added to a struct, and a hand-written field-by-field
// mapping somewhere else is never updated, so the feature is inert while every
// unit test passes. Here the CLI's advVerdict is built field-by-field from
// advpool.Verdict, so MutantsInvalid stayed 0 and the count never printed even
// though the gate was excluding mutants correctly.
func TestAdvVerdictCarriesMutantsInvalid(t *testing.T) {
	v := advpool.Verdict{
		DevKillRate:    0,
		MutantsTotal:   4,
		MutantsInvalid: 11,
		Survivors:      4,
		Status:         advpool.StatusNeedsReview,
	}
	got := advVerdictFromPool(v)
	if got.MutantsInvalid != 11 {
		t.Fatalf("MutantsInvalid = %d, want 11 — the CLI mapping dropped it, so the operator never learns the exam shrank", got.MutantsInvalid)
	}
	if got.MutantsTotal != 4 {
		t.Errorf("MutantsTotal = %d, want 4 (the GRADED count)", got.MutantsTotal)
	}
}

// And it must actually reach the printed page.
func TestRenderedVerdictShowsInvalidCount(t *testing.T) {
	var b strings.Builder
	renderAdvVerdict(&b, "x.go", advVerdict{
		MutantsTotal: 4, MutantsInvalid: 11, Survivors: 4, Status: advpool.StatusNeedsReview,
	})
	out := b.String()
	if !strings.Contains(out, "11") || !strings.Contains(strings.ToLower(out), "invalid") {
		t.Errorf("rendered verdict hides the invalid count:\n%s", out)
	}
}

// A clean run must NOT print the line — noise on every audit would train an
// operator to ignore it.
func TestRenderedVerdictOmitsInvalidWhenZero(t *testing.T) {
	var b strings.Builder
	renderAdvVerdict(&b, "x.go", advVerdict{MutantsTotal: 4, MutantsInvalid: 0, Survivors: 0})
	if strings.Contains(strings.ToLower(b.String()), "invalid") {
		t.Errorf("invalid line printed on a clean run:\n%s", b.String())
	}
}
