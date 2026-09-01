// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

// Two mutants that are the same edit are one measurement. The first is kept,
// in the input's own order, and the collapse is COUNTED — never silently
// dropped, because the denominator has to be reconcilable.
func TestDedupeMutantsCollapsesIdenticalHunks(t *testing.T) {
	span := lang.LineRange{Start: 3, End: 4}
	in := []Mutant{
		{ID: "m1", Span: span, Search: "a == b", Replace: "a != b"},
		{ID: "m2", Span: span, Search: "a == b", Replace: "a >= b"},                             // different replacement
		{ID: "m3", Span: span, Search: "a == b", Replace: "a != b"},                             // exact duplicate of m1
		{ID: "m4", Span: lang.LineRange{Start: 9, End: 9}, Search: "a == b", Replace: "a != b"}, // different span
	}
	kept, collapsed := DedupeMutants(in)
	if collapsed != 1 {
		t.Fatalf("collapsed = %d, want 1", collapsed)
	}
	if len(kept) != 3 || kept[0].ID != "m1" || kept[1].ID != "m2" || kept[2].ID != "m4" {
		t.Fatalf("kept = %v, want m1,m2,m4 in input order", ids(kept))
	}
}

// A mutant derived from DIFFERENT source is a different mutant even when the
// hunk reads the same — the parent hash is part of its identity.
func TestDedupeMutantsKeepsDifferentParents(t *testing.T) {
	in := []Mutant{
		{ID: "m1", ParentSHA256: "aa", Search: "x", Replace: "y"},
		{ID: "m2", ParentSHA256: "bb", Search: "x", Replace: "y"},
	}
	if kept, collapsed := DedupeMutants(in); collapsed != 0 || len(kept) != 2 {
		t.Fatalf("collapsed %d kept %v — an edit of different bytes is a different edit", collapsed, ids(kept))
	}
}

// THE ACCEPTANCE FOR ITEM 3. Collapsing duplicates must not move ANY number:
// the duplicate is graded once and answered twice, so it is still in
// Killed/Survived, still carries its own killed_by, and still counts in the
// denominator. All that changes is how many suite runs were paid for.
func TestDuplicateMutantsAreGradedOnceAndAnsweredTwice(t *testing.T) {
	set := []Mutant{
		{ID: "m1", Replace: "kill1\n"},
		{ID: "m1-dup", Replace: "kill1\n"}, // the same edit
		{ID: "m2", Replace: "kill3\n"},
		{ID: "m5", Replace: "harmless\n"},
		{ID: "m5-dup", Replace: "harmless\n"}, // the same edit, a survivor
	}
	f := &fakeSuite{}
	rep, err := Score(context.Background(), f, map[string]string{}, "a.py", "ORIGINAL\n",
		set, ffCmd, WithFailureParser(pythonFailureParser(t)))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if rep.DuplicateMutants != 2 {
		t.Errorf("DuplicateMutants = %d, want 2 — the saving must be disclosed", rep.DuplicateMutants)
	}
	// THE DENOMINATOR IS UNTOUCHED.
	if rep.Total != 5 {
		t.Errorf("Total = %d, want 5 — collapsing must not shrink the exam", rep.Total)
	}
	if strings.Join(rep.Killed, ",") != "m1,m1-dup,m2" {
		t.Errorf("Killed = %v, want every copy present in input order", rep.Killed)
	}
	if strings.Join(rep.Survived, ",") != "m5,m5-dup" {
		t.Errorf("Survived = %v", rep.Survived)
	}
	if rep.PerMutant["m1"].KilledBy != rep.PerMutant["m1-dup"].KilledBy || rep.PerMutant["m1"].KilledBy == "" {
		t.Errorf("killed_by differs between identical edits: %q vs %q", rep.PerMutant["m1"].KilledBy, rep.PerMutant["m1-dup"].KilledBy)
	}
	// ...and the duplicate really was not run: 5 mutants, 3 distinct edits.
	graded := 0
	for _, r := range f.runs {
		_ = r
		graded++
	}
	// baseline + canary + 3 distinct mutants = 5 runs.
	if graded != 5 {
		t.Errorf("%d jail runs, want 5 (baseline, canary, 3 distinct edits) — a duplicate was run again", graded)
	}
}

// A set with no duplicates must be completely untouched.
func TestDedupeIsANoOpWithoutDuplicates(t *testing.T) {
	rep, err := Score(context.Background(), &fakeSuite{}, map[string]string{}, "a.py", "ORIGINAL\n",
		ffMutants(), ffCmd, WithFailureParser(pythonFailureParser(t)))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if rep.DuplicateMutants != 0 {
		t.Errorf("DuplicateMutants = %d on a set with none", rep.DuplicateMutants)
	}
	if rep.Total != len(ffMutants()) {
		t.Errorf("Total = %d, want %d", rep.Total, len(ffMutants()))
	}
}

func ids(ms []Mutant) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.ID
	}
	return out
}
