// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

// THE HAZARD. A source whose tests live INSIDE it (a Rust #[cfg(test)] mod)
// can be mutated at the test module by a model's search/replace anchor. The
// mutated assertion then fails and "kills" itself: a fabricated kill. The
// module is the audit's evidence, never its target, so such a mutant is
// invalid, not graded.
const evidenceBase = "a\nb\nc\nd\ne\nf\n"

func evidenceMutants() []Mutant {
	return []Mutant{
		{ID: "above", Search: "a\n", Replace: "A\n", Span: lang.LineRange{Start: 1, End: 1}},
		{ID: "inside", Search: "e\n", Replace: "E\n", Span: lang.LineRange{Start: 5, End: 5}},
		{ID: "straddle", Search: "c\nd\n", Replace: "C\nD\n", Span: lang.LineRange{Start: 3, End: 4}},
		{ID: "unknown", Search: "b\n", Replace: "B\n"},
	}
}

func TestMutantTouchingAnInlineTestModuleIsInvalid(t *testing.T) {
	j := &gateJail{baselineFiles: evidenceBase}
	rep, err := Score(context.Background(), j, map[string]string{}, "x.go", evidenceBase,
		evidenceMutants(), []string{"TEST"},
		WithEvidenceLines([]lang.LineRange{{Start: 4, End: 6}}))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	invalid := map[string]bool{}
	for _, id := range rep.Invalid {
		invalid[id] = true
	}
	for _, id := range []string{"inside", "straddle", "unknown"} {
		if !invalid[id] {
			t.Errorf("%s is not invalid; Invalid = %v", id, rep.Invalid)
		}
	}
	if invalid["above"] {
		t.Error("a mutant wholly above the test module was marked invalid")
	}
	if got := rep.InvalidReasons["inside"]; got != "edits the source's own tests (lines 4-6), which are the audit's evidence" {
		t.Errorf("inside reason = %q", got)
	}
	if got := rep.InvalidReasons["unknown"]; !strings.Contains(got, "unknown") {
		t.Errorf("unknown-span reason = %q, want it to say the edited lines are unknown", got)
	}
	if rep.Total != 1 {
		t.Errorf("Total = %d, want 1 (only the mutant above the module is graded)", rep.Total)
	}
	for _, id := range append(append([]string{}, rep.Killed...), rep.Survived...) {
		if id != "above" {
			t.Errorf("%s was graded; an evidence-touching mutant must never be", id)
		}
	}
}

// With no evidence lines supplied nothing changes: zero-span fixtures and
// every language without an inline test module grade as before.
func TestNoEvidenceLinesLeavesMutantsAlone(t *testing.T) {
	j := &gateJail{baselineFiles: evidenceBase}
	rep, err := Score(context.Background(), j, map[string]string{}, "x.go", evidenceBase,
		evidenceMutants(), []string{"TEST"})
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if len(rep.Invalid) != 0 || rep.Total != 4 {
		t.Fatalf("Invalid = %v, Total = %d; want none invalid, 4 graded", rep.Invalid, rep.Total)
	}
}

// The exact boundaries: a mutant whose span ENDS on the module's first line
// (its #[cfg(test)]) and one whose span STARTS on its last line (its closing
// brace) each edit the module, and are invalid.
func TestMutantOnAnInlineTestModulesBoundaryLineIsInvalid(t *testing.T) {
	mutants := []Mutant{
		{ID: "ends-on-first", Search: "c\nd\n", Replace: "C\nD\n", Span: lang.LineRange{Start: 3, End: 4}},
		{ID: "starts-on-last", Search: "e\nf\n", Replace: "E\nF\n", Span: lang.LineRange{Start: 5, End: 6}},
		{ID: "above", Search: "c\n", Replace: "C\n", Span: lang.LineRange{Start: 3, End: 3}},
	}
	j := &gateJail{baselineFiles: evidenceBase}
	rep, err := Score(context.Background(), j, map[string]string{}, "x.go", evidenceBase,
		mutants, []string{"TEST"}, WithEvidenceLines([]lang.LineRange{{Start: 4, End: 5}}))
	if err != nil {
		t.Fatalf("Score: %v", err)
	}
	if !slices.Contains(rep.Invalid, "ends-on-first") || !slices.Contains(rep.Invalid, "starts-on-last") || slices.Contains(rep.Invalid, "above") {
		t.Errorf("Invalid = %v; want ends-on-first and starts-on-last, not above", rep.Invalid)
	}
}
