// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/pdbethke/corralai/internal/reposcan"
)

func inventoryFixture() scanInventory {
	cands := []reposcan.Candidate{
		{Path: "src/a.py", TestPath: "tests/test_a.py", Lang: "python"},
		{Path: "src/b.py", TestPath: "tests/test_b.py", Lang: "python"},
	}
	excl := []reposcan.Exclusion{
		{Path: "src/lonely.py", Reason: reposcan.ReasonNoPairedTest},
		{Path: "tests/test_a.py", Reason: reposcan.ReasonIsTest},
		{Path: "README.md", Reason: reposcan.ReasonNoLanguage},
	}
	return buildScanInventory("local/demo", 5, "size-only", cands, 2, excl)
}

// TestScanInventory_WalkedIsAuthoritativeNotDerived pins the subtlety a
// consumer would otherwise get wrong. Candidates + excluded does NOT
// necessarily equal walked: the two overlap, because a file can be a candidate
// AND excluded (an "ungoaled" candidate is both). The human report already
// prints walked explicitly for exactly this reason rather than letting a reader
// add the terms up.
//
// So Walked must be carried through as its own measured value and never
// reconstructed by summing — a UI that derived it would silently disagree with
// corral about the size of the repository, and the denominator is precisely
// where a coverage number turns into spin.
func TestScanInventory_WalkedIsAuthoritativeNotDerived(t *testing.T) {
	// A candidate that is ALSO excluded — the overlapping shape.
	cands := []reposcan.Candidate{{Path: "a.py", TestPath: "test_a.py", Lang: "python"}}
	excl := []reposcan.Exclusion{{Path: "a.py", Reason: "ungoaled"}}

	inv := buildScanInventory("local/demo", 1, "size-only", cands, 0, excl)

	summed := inv.Candidates
	for _, n := range inv.ExcludedByReason {
		summed += n
	}
	if summed <= inv.Walked {
		t.Fatalf("fixture does not exercise the overlap: summed=%d walked=%d", summed, inv.Walked)
	}
	if inv.Walked != 1 {
		t.Fatalf("Walked = %d, want the measured value 1 carried through untouched — never derived from the other terms", inv.Walked)
	}
}

// TestScanInventory_ReportsTheFunnelNotAPercentage pins the deliberate absence
// of a headline score. 2 auditable of 6 walked is 33%; of 2 candidates it is
// 100%. Neither is the truth, and baking either into the API would make every
// consumer inherit a spin decision they cannot see. The funnel's terms are all
// present so a consumer can render it honestly.
func TestScanInventory_ReportsTheFunnelNotAPercentage(t *testing.T) {
	var buf bytes.Buffer
	if err := writeScanInventory(&buf, inventoryFixture()); err != nil {
		t.Fatalf("writeScanInventory: %v", err)
	}
	var generic map[string]any
	if err := json.Unmarshal(buf.Bytes(), &generic); err != nil {
		t.Fatalf("emitted invalid JSON: %v", err)
	}
	for _, banned := range []string{"percent", "percentage", "score", "coverage", "certifiable_pct"} {
		if _, present := generic[banned]; present {
			t.Errorf("schema exposes %q — report the funnel's terms, not a collapsed number whose denominator is a positioning choice", banned)
		}
	}
	for _, required := range []string{"walked", "candidates", "jobs", "excluded_by_reason", "languages"} {
		if _, present := generic[required]; !present {
			t.Errorf("schema is missing %q, so a consumer cannot reconstruct the funnel", required)
		}
	}
}

// TestScanInventory_SeparatesCandidatesFromJobs pins that a --top/--diff-base
// bound never masquerades as a property of the repository. Candidates is what
// the repo HAS; Jobs is what this invocation CHOSE. Collapsing them would hide
// that the operator narrowed the surface.
func TestScanInventory_SeparatesCandidatesFromJobs(t *testing.T) {
	cands := []reposcan.Candidate{
		{Path: "a.py", TestPath: "test_a.py", Lang: "python"},
		{Path: "b.py", TestPath: "test_b.py", Lang: "python"},
		{Path: "c.py", TestPath: "test_c.py", Lang: "python"},
	}
	inv := buildScanInventory("local/demo", 3, "size-only", cands, 1, nil) // --top 1
	if inv.Candidates != 3 || inv.Jobs != 1 {
		t.Fatalf("candidates=%d jobs=%d, want 3 and 1 — a bound is a choice, not a property of the repo", inv.Candidates, inv.Jobs)
	}
}

// TestScanInventory_CarriesTheRanking pins that the ordering signal is named.
// A shallow clone has no usable history, so churn silently degrades to size
// alone — a consumer must be able to say which it actually got rather than
// imply a signal that was never available.
func TestScanInventory_CarriesTheRanking(t *testing.T) {
	if inv := buildScanInventory("r", 1, "size-only", nil, 0, nil); inv.Ranking != "size-only" {
		t.Fatalf("Ranking = %q, want it carried through", inv.Ranking)
	}
}

// TestScanInventory_PairingIsInferredNotAsserted keeps the test path present but
// honest: it is a filename-convention guess. psf/requests pairs adapters.py to
// an 8-line test_adapters.py while its real coverage lives in a 108KB
// test_requests.py — scoping to the guess inverted that file's verdict from
// 1.00 to 0.00. A consumer must receive the pairing AND the ambiguity signal.
func TestScanInventory_PairingIsInferredNotAsserted(t *testing.T) {
	inv := buildScanInventory("r", 2, "size-only",
		[]reposcan.Candidate{{Path: "src/adapters.py", TestPath: "tests/test_adapters.py", Lang: "python"}}, 1,
		[]reposcan.Exclusion{{Path: "src/x.py", Reason: reposcan.ReasonAmbiguousTest}})

	if len(inv.Auditable) != 1 || inv.Auditable[0].TestPath != "tests/test_adapters.py" {
		t.Fatalf("auditable = %+v, want the inferred pairing carried", inv.Auditable)
	}
	var sawAmbiguous bool
	for _, l := range inv.Languages {
		if l.Ambiguous > 0 {
			sawAmbiguous = true
		}
	}
	if !sawAmbiguous {
		t.Error("ambiguous pairings must reach the consumer — that is where corral knows it is uncertain and should ask a human")
	}
}
