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
	if got.Jaccard != 0 || got.Kappa != 0 {
		t.Errorf("Jaccard/Kappa = %v/%v, want 0/0 when insufficient — a coefficient must not be emitted", got.Jaccard, got.Kappa)
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
	if math.Abs(got.Kappa-1) > 1e-9 {
		t.Errorf("Kappa = %v, want 1 for perfect agreement", got.Kappa)
	}
}
