// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"strings"
	"testing"
)

// THE HAZARD. A mutant is its hunk now, so the file is spliced at grading
// time — and a splice can FAIL. It cannot fail for a mutant this run
// generated, whose anchor was proven unique against these exact bytes; it can
// fail for a REPLAYED set whose parent hash was somehow satisfied by other
// source. That is precisely the case that must not quietly become a number.
//
// Both wrong answers are worse than an error. Scored as a survivor, it is a
// coverage gap that does not exist — the audit's headline claim is "your
// tests miss these bugs", and this bug was never injected. Scored as a kill,
// it is credit for catching nothing. So it takes the compile gate's own path:
// INVALID, disclosed with a reason, excluded from the denominator, and never
// handed to the jail at all.
func TestScoreTreatsAnUnappliableMutantAsInvalid(t *testing.T) {
	j := &gateJail{baselineFiles: gateBase} // testPasses false: a graded mutant is killed
	muts := []Mutant{
		gateMutants()[0], // whole-file, applies, gets graded
		{ID: "m9", Search: "this text is nowhere in the source", Replace: "irrelevant"},
	}
	rep, err := Score(context.Background(), j, map[string]string{}, "x.go", gateBase, muts, []string{"TEST"})
	if err != nil {
		// Emphatically NOT an error: one unappliable mutant must not sink the
		// whole file's exam, the way a jail that cannot RUN does.
		t.Fatalf("Score: %v", err)
	}

	if len(rep.Invalid) != 1 || rep.Invalid[0] != "m9" {
		t.Fatalf("Invalid = %v, want [m9]", rep.Invalid)
	}
	reason := rep.InvalidReasons["m9"]
	if !strings.HasPrefix(reason, "anchor:") {
		t.Errorf("InvalidReasons[m9] = %q, want the anchor: prefix — the COUNT says the exam shrank, only the REASON says why", reason)
	}
	if rep.Total != 1 {
		t.Errorf("Total = %d, want 1: an unappliable mutant never sat the exam, so it cannot be in KillRate's denominator", rep.Total)
	}
	for _, id := range rep.Killed {
		if id == "m9" {
			t.Error("an unappliable mutant was scored as KILLED — credit for catching a bug that was never injected")
		}
	}
	for _, id := range rep.Survived {
		if id == "m9" {
			t.Error("an unappliable mutant was scored as a SURVIVOR — a coverage gap that does not exist")
		}
	}
	if _, ok := rep.PerMutant["m9"]; ok {
		t.Error("an unappliable mutant has a grading entry, but nothing graded it")
	}
	// baseline + canary + the one mutant that applies. The jail must never
	// have been asked about m9: there was no file to hand it.
	if j.testRuns != 3 {
		t.Errorf("testRuns = %d, want 3 (baseline, canary, m1) — the jail was run for a mutant that could not be materialised", j.testRuns)
	}
	if got := rep.KillRate(); got != 1 {
		t.Errorf("KillRate = %v, want 1 (1 of 1 graded)", got)
	}
}
