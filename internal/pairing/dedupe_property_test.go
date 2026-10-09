// SPDX-License-Identifier: Elastic-2.0

// External test package: these tests use pairingtest, which imports pairing,
// so an in-package test importing it would be a cycle.
package pairing_test

import (
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/pairing"
	"github.com/pdbethke/corralai/internal/pairing/pairingtest"
)

// Dedupe documents an invariant it does not enforce: its max-rank merge
// agrees with the principled rule (pairingtest.PrincipledMerge) only while no
// rule set produces a collision that mixes a non-vacuous, non-sibling form
// with a weaker one. TestDedupeMatchesPrincipledRuleForShippedPlugins in
// internal/lang checks that over every shipped plugin's real TestRules. This
// file proves that check has teeth, and that the sibling exemption it leans
// on is load-bearing.

// --- The synthetic violator: proof the property check can fail. ---
//
// violatorPlugin is a stand-in for a FUTURE plugin (a hypothetical Python
// spec/ family, or a JS full-mirror form) that emits a non-vacuous,
// non-sibling candidate carrying real directory evidence (a literal "x"
// path segment that never degenerates) which string-collides with a
// WEAKER, vacuous form of the same source. Its candidates go through the
// REAL production Dedupe, exactly as every shipped plugin's do.
type violatorPlugin struct{}

// sourceBase is codePath's file name without its extension.
func sourceBase(codePath string) string {
	b := filepath.Base(codePath)
	return b[:len(b)-len(filepath.Ext(b))]
}

// TestPaths keeps the original method name so the test below reads unchanged.
func (violatorPlugin) TestPaths(codePath string) []pairing.Candidate {
	base := sourceBase(codePath)
	strong := pairing.Candidate{Path: filepath.Join("spec", "x", "test_"+base+".ext"), Rank: 1}
	weak := pairing.Candidate{Path: filepath.Join("spec", "x", "test_"+base+".ext"), Rank: 4}
	return pairing.Dedupe([]pairing.Candidate{strong, weak})
}

// violatorRawForms is this file's model of the SAME two candidates, tagged
// per the principled rule: strong asserts real evidence (its "x" segment is
// a literal, never-degenerating directory component); weak is vacuous (no
// real evidence — a stand-in for a degenerate mirror/stripped/flat form).
func violatorRawForms(codePath string) []pairingtest.RawForm {
	base := sourceBase(codePath)
	return []pairingtest.RawForm{
		{Path: filepath.Join("spec", "x", "test_"+base+".ext"), Rank: 1, Vacuous: false},
		{Path: filepath.Join("spec", "x", "test_"+base+".ext"), Rank: 4, Vacuous: true},
	}
}

// TestSyntheticViolatorIsDetected proves the equivalence check has teeth: it
// is not a tautology that only ever passes. Dedupe's max-with-
// sibling-exemption rule UNDERSTATES the violator's strong (Rank 1, real
// evidence) claim to Rank 4 (dragged down by the weaker, vacuous collider) —
// exactly the "inflated/understated rank crosses into a different source's
// comparison" failure mode described in the task: understating source A's
// evidence can hand a strict win to some other source B in
// reposcan.demoteAmbiguousPairings, which is a MISPAIRING, not an honest
// demotion. The principled rule (independently computed here) instead keeps
// the strong claim's Rank 1. If a real future plugin ever does this,
// production's Dedupe result and this file's principled computation
// diverge — this test asserts that divergence is real and gets caught, not
// papered over.
func TestSyntheticViolatorIsDetected(t *testing.T) {
	codePath := "irrelevant/x.ext" // violatorPlugin ignores the directory entirely by construction
	got := violatorPlugin{}.TestPaths(codePath)
	principled := pairingtest.PrincipledMerge(violatorRawForms(codePath))

	if pairingtest.SameMerge(got, principled) {
		t.Fatalf("expected the property check to FLAG the synthetic violator (production %+v vs principled %+v), but they matched — the check cannot detect a future plugin that breaks the invariant", got, principled)
	}

	// Pin the exact numbers so the reasoning above is falsifiable, not just
	// "not equal": production understates the strong claim to the weak
	// collider's Rank 4; the principled rule keeps it at Rank 1.
	wantPath := filepath.Join("spec", "x", "test_x.ext")
	if len(got) != 1 || got[0].Path != wantPath || got[0].Rank != 4 {
		t.Fatalf("production pairing.Dedupe(violator) = %+v, want [{%s 4}]", got, wantPath)
	}
	if len(principled) != 1 || principled[0].Path != wantPath || principled[0].Rank != 1 {
		t.Fatalf("PrincipledMerge(violator) = %+v, want [{%s 1}]", principled, wantPath)
	}
}

// --- The sibling exemption is load-bearing in both directions. ---
//
// dedupeWithoutSiblingExemption is a LOCAL, test-only reimplementation of
// Dedupe with the `if minRank[p] == 0 { rank = 0 }` line deleted —
// i.e. pure "attribute the max rank among every colliding candidate,
// sibling or not." It exists only to demonstrate the exemption changes
// behavior on a real collision shape; production code is untouched.
func dedupeWithoutSiblingExemption(cands []pairing.Candidate) []pairing.Candidate {
	firstIdx := map[string]int{}
	maxRank := map[string]int{}
	var order []string
	for _, c := range cands {
		if _, ok := firstIdx[c.Path]; !ok {
			firstIdx[c.Path] = len(order)
			order = append(order, c.Path)
			maxRank[c.Path] = c.Rank
			continue
		}
		if c.Rank > maxRank[c.Path] {
			maxRank[c.Path] = c.Rank
		}
	}
	out := make([]pairing.Candidate, len(order))
	for i, p := range order {
		out[i] = pairing.Candidate{Path: p, Rank: maxRank[p]}
	}
	return out
}

// TestSiblingExemptionIsLoadBearing exercises the exact collision shape
// behind the "requests" end-to-end fixture in
// internal/reposcan/candidate_pairing_test.go (tests/utils.py: a source file
// that lives IN the parallel test root itself, whose sibling form
// string-collides with its own degenerate stripped/flat forms) and shows
// that Dedupe (WITH the exemption) and
// dedupeWithoutSiblingExemption (without it) disagree on it — i.e. deleting
// the exemption is not a no-op. The same asymmetry is what
// internal/pairing/dedupe_test.go pins directly at the unit level (the
// "sibling collides with a degenerate lower-specificity form" and "sibling
// wins regardless of listed order" subcases of
// TestDedupeAttributesLeastSpecificRank) and what
// TestEnumeratePairingConventions's
// "python: requests tests/test_utils.py — pre-existing tests/utils.py
// misclassification wins the strict-rank tiebreak" subcase pins end-to-end.
//
// cands is the raw, pre-dedupe list Python's rules produce for tests/utils.py
// (a fixture of that one collision, pinned in lang's
// TestPythonCandidatesOrder family and the reposcan golden — not a restated
// rule set): both siblings, the full mirror, and the stripped and flat forms,
// which degenerate onto the sibling's own string because "tests" strips to "".
func TestSiblingExemptionIsLoadBearing(t *testing.T) {
	cands := []pairing.Candidate{
		{Path: "tests/test_utils.py", Rank: 0},       // sibling test_{base}.py
		{Path: "tests/utils_test.py", Rank: 0},       // sibling {base}_test.py
		{Path: "tests/tests/test_utils.py", Rank: 1}, // full mirror
		{Path: "tests/test_utils.py", Rank: 2},       // stripped: "tests" -> ""
		{Path: "tests/test_utils.py", Rank: 3},       // flat
	}

	withExemption := pairing.Dedupe(cands)
	withoutExemption := dedupeWithoutSiblingExemption(cands)

	if pairingtest.SameMerge(withExemption, withoutExemption) {
		t.Fatalf("expected the sibling exemption to change the result for tests/utils.py's collision (with=%+v, without=%+v) — if these ever match, the exemption is dead code and the referenced dedupe_test.go/candidate_pairing_test.go subcases must be re-checked", withExemption, withoutExemption)
	}

	const wantPath = "tests/test_utils.py"
	found := false
	for _, c := range withExemption {
		if c.Path == wantPath {
			found = true
			if c.Rank != 0 {
				t.Errorf("with exemption: %s Rank = %d, want 0 (sibling must win)", wantPath, c.Rank)
			}
		}
	}
	if !found {
		t.Fatalf("with exemption: %s not present in %+v", wantPath, withExemption)
	}

	found = false
	for _, c := range withoutExemption {
		if c.Path == wantPath {
			found = true
			if c.Rank == 0 {
				t.Errorf("without exemption: %s Rank = 0, want it demoted (this is the regression the exemption prevents)", wantPath)
			}
		}
	}
	if !found {
		t.Fatalf("without exemption: %s not present in %+v", wantPath, withoutExemption)
	}
}
