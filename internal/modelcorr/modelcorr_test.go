// SPDX-License-Identifier: Elastic-2.0

package modelcorr

import (
	"fmt"
	"math"
	"testing"
)

func vec(model string, killed map[string]bool) Vector {
	return Vector{Model: model, Killed: killed}
}

func id(i int) string { return fmt.Sprintf("m%d", i) }

// ids returns the inclusive id range [from, to].
func ids(from, to int) []string {
	var out []string
	for i := from; i <= to; i++ {
		out = append(out, id(i))
	}
	return out
}

// set builds an n-mutant vector in which exactly the listed ids are killed.
func set(n int, killed ...string) map[string]bool {
	out := make(map[string]bool, n)
	for i := 1; i <= n; i++ {
		out[id(i)] = false
	}
	for _, k := range killed {
		out[k] = true
	}
	return out
}

// twelve is the small fixture: 12 mutants, the listed ids killed.
func twelve(killedIDs ...string) map[string]bool { return set(12, killedIDs...) }

func TestCompareIdenticalSurvivorsIsTotalOverlap(t *testing.T) {
	// Both kill only m1 and m2 -> both survive the same 10. J = 10/10 = 1.
	a := vec("A", twelve(id(1), id(2)))
	b := vec("B", twelve(id(1), id(2)))
	got, err := Compare(a, b)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if !got.Sufficient {
		t.Fatalf("Sufficient = false, want true (union = %d)", got.UnionSurvivors)
	}
	if got.Jaccard != 1 {
		t.Errorf("Jaccard = %v, want 1 — identical survivor sets are a total shared blind spot", got.Jaccard)
	}
	if got.SharedSurvivors != 10 || got.UnionSurvivors != 10 {
		t.Errorf("shared/union = %d/%d, want 10/10", got.SharedSurvivors, got.UnionSurvivors)
	}
}

func TestCompareDisjointSurvivorsIsZeroOverlap(t *testing.T) {
	// A survives a..f (6), B survives g..l (6). Union 12, intersection 0.
	a := vec("A", twelve(id(7), id(8), id(9), id(10), id(11), id(12)))
	b := vec("B", twelve(id(1), id(2), id(3), id(4), id(5), id(6)))
	got, err := Compare(a, b)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if got.Jaccard != 0 {
		t.Errorf("Jaccard = %v, want 0 — disjoint misses are complementary coverage", got.Jaccard)
	}
	if got.SharedSurvivors != 0 || got.UnionSurvivors != 12 {
		t.Errorf("shared/union = %d/%d, want 0/12", got.SharedSurvivors, got.UnionSurvivors)
	}
}

// THE CENTRAL DESIGN ARGUMENT. Two seats can agree on most KILLS and still
// have ZERO overlap in their MISSES. Both kill m1-m20; A additionally kills
// m21-m30 (surviving m31-m40) while B additionally kills m31-m40 (surviving
// m21-m30). Both run a 75% kill rate and agree on 20 of 40 items, so a
// kill-vector correlation would call them highly correlated — but their blind
// spots are disjoint, so Jaccard-on-survivors correctly reports 0.
func TestSharedKillsDoNotInflateJaccard(t *testing.T) {
	const n = 40
	a := vec("A", set(n, ids(1, 30)...))
	bKilled := append(append([]string{}, ids(1, 20)...), ids(31, 40)...)
	b := vec("B", set(n, bKilled...))

	got, err := Compare(a, b)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if !got.Sufficient {
		t.Fatalf("Sufficient = false with a survivor union of %d — the fixture must clear MinSurvivorUnion or it proves nothing about inflation", got.UnionSurvivors)
	}
	if got.SurvivedA != 10 || got.SurvivedB != 10 {
		t.Fatalf("survivors = %d/%d, want 10/10 — fixture is wrong", got.SurvivedA, got.SurvivedB)
	}
	if got.UnionSurvivors != 20 || got.SharedSurvivors != 0 {
		t.Fatalf("shared/union = %d/%d, want 0/20 — the blind spots must be disjoint", got.SharedSurvivors, got.UnionSurvivors)
	}
	if got.Jaccard != 0 {
		t.Errorf("Jaccard = %v, want 0 — high shared KILL rates must not inflate a survivor-based statistic", got.Jaccard)
	}
}

func TestCompareBelowThresholdIsInsufficient(t *testing.T) {
	a := vec("A", map[string]bool{"m1": false, "m2": true})
	b := vec("B", map[string]bool{"m1": false, "m2": true})
	got, err := Compare(a, b)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if got.Sufficient {
		t.Error("Sufficient = true on a union of 1 survivor — that is noise, not a measurement")
	}
	if got.KappaDefined {
		t.Error("KappaDefined = true below the survivor threshold — no coefficient is reportable there")
	}
	if got.Jaccard != 0 || got.Kappa != 0 {
		t.Errorf("Jaccard/Kappa = %v/%v, want 0/0 when insufficient — a coefficient must not be emitted", got.Jaccard, got.Kappa)
	}
}

// THE HEADLINE RESULT, previously suppressed. Past the MinSurvivorUnion early
// return, p_e == 1 can only mean both seats survived EVERYTHING: union = |M|,
// intersection = |M|, Jaccard 1.0 — a TOTAL shared blind spot, the single most
// important thing this package can say. The old code zeroed that Jaccard and
// set Sufficient=false along with it, making the worst possible finding
// indistinguishable from "not enough data".
//
// Kappa genuinely IS undefined here (1 - p_e is zero), which is a fact about
// kappa and not about Jaccard. Two statistics, two flags.
func TestBothSeatsSurvivingEverythingIsATotalSharedBlindSpot(t *testing.T) {
	// 12 mutants, nothing killed by either seat.
	a := vec("A", twelve())
	b := vec("B", twelve())
	got, err := Compare(a, b)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if !got.Sufficient {
		t.Fatalf("Sufficient = false over a survivor union of %d — the Jaccard question is fully answerable and this is the worst answer there is", got.UnionSurvivors)
	}
	if got.Jaccard != 1 {
		t.Errorf("Jaccard = %v, want 1 — both seats missed every mutant, which is a total shared blind spot", got.Jaccard)
	}
	if got.UnionSurvivors != 12 || got.SharedSurvivors != 12 {
		t.Errorf("shared/union = %d/%d, want 12/12", got.SharedSurvivors, got.UnionSurvivors)
	}
	if got.KappaDefined {
		t.Error("KappaDefined = true where 1 - p_e is zero — kappa has no value here")
	}
	if got.Kappa != 0 {
		t.Errorf("Kappa = %v with KappaDefined false; the zero must be inert, and callers must read the flag, never the number", got.Kappa)
	}
}

// The mirror: both seats killing everything is degenerate for kappa too, but
// there the survivor union is EMPTY, so the Jaccard question is unanswerable
// for the ordinary reason (below threshold) rather than because of kappa.
func TestBothSeatsKillingEverythingIsInsufficientNotABlindSpot(t *testing.T) {
	all := ids(1, 12)
	got, err := Compare(vec("A", twelve(all...)), vec("B", twelve(all...)))
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if got.Sufficient || got.Jaccard != 0 {
		t.Errorf("Sufficient/Jaccard = %v/%v — an EMPTY survivor union answers nothing about shared blind spots", got.Sufficient, got.Jaccard)
	}
	if got.KappaDefined {
		t.Error("KappaDefined = true where both seats killed everything")
	}
}

// The controlled-comparison invariant, enforced in code: two seats that faced
// different mutant sets are not comparable and must fail loudly.
func TestCompareRejectsMismatchedMutantSets(t *testing.T) {
	a := vec("A", map[string]bool{"m1": true, "m2": false})
	b := vec("B", map[string]bool{"m1": true, "m3": false})
	if _, err := Compare(a, b); err == nil {
		t.Fatal("mismatched mutant sets were accepted — the comparison would be confounded by mutant difficulty")
	}
}

func TestKappaIsChanceCorrected(t *testing.T) {
	// Perfect agreement on a balanced split -> kappa 1.
	// Uses 20 mutants (kill m1-m10, survive m11-m20) to exceed MinSurvivorUnion.
	a := vec("A", set(20, ids(1, 10)...))
	b := vec("B", set(20, ids(1, 10)...))
	got, err := Compare(a, b)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if !got.KappaDefined {
		t.Fatal("KappaDefined = false on a balanced split — kappa is perfectly well defined here")
	}
	if math.Abs(got.Kappa-1) > 1e-9 {
		t.Errorf("Kappa = %v, want 1 for perfect agreement", got.Kappa)
	}
}
