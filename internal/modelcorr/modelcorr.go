// SPDX-License-Identifier: Elastic-2.0

// Package modelcorr measures whether two models in the SAME seat fail in the
// same places. It is pure data+logic — no I/O, no DB, no external
// dependencies — following the precedent internal/rolemodel sets.
//
// WHY SURVIVORS AND NOT KILLS. The obvious statistic (a correlation over the
// two kill vectors) is actively misleading. Two writers that each kill 95% of
// mutants agree on ~90% of items BY CONSTRUCTION: the agreement is driven by
// mutants being easy, not by shared blind spots, so two genuinely independent
// models read as highly correlated. Base rates swamp the signal.
//
// The question an adversarial pool actually cares about is conditional: when
// one writer fails, does the other fail too? That lives entirely in the
// SURVIVOR sets, so Jaccard-over-survivors is the headline and kills are
// excluded outright.
//
// WHAT A HIGH JACCARD DOES NOT MEAN. It does not prove shared training
// lineage. It is equally consistent with both models being weak on the same
// genuinely hard mutants. The defensible claim is "these two seats are not
// buying independent coverage on this repo" — never "these models are secretly
// the same model."
package modelcorr

import "fmt"

// MinSurvivorUnion is the smallest survivor union that yields a reportable
// coefficient. Below it, Compare returns Sufficient=false with zeroed
// coefficients rather than a number.
//
// SET FROM MEASUREMENT, NOT FROM TASTE. It was originally 10, guessed before
// any run existed, and the spec listed it as an open risk to be settled
// against real distributions. Those distributions arrived and said 10 is
// unreachable:
//
//	internal/modelcorr/modelcorr.go   thorough tests   3 mutants   0 survivors
//	internal/transparency/rekor.go    0% coverage     13 mutants   3 survivors
//	internal/transparency/rekor.go    0% coverage     15 mutants   4 survivors
//
// The universe is the DEV SUITE'S SURVIVORS, so a union of 10 needs ten missed
// bugs in one file. The second and third rows are the best case available — a
// 328-line file nothing exercises — and they top out at four. A floor of 10
// does not make the statistic careful; it makes it silent forever.
//
// READ THE DENOMINATORS. A Jaccard over three or four survivors is COARSE: it
// can only take a handful of values, and one mutant moving between sets swings
// it hard. That is precisely why Pair carries |M|, |S_A|, |S_B| and the
// intersection, and why they must be reported beside any coefficient. This
// floor separates "no evidence" from "little evidence"; it does not turn
// little evidence into much.
//
// Revisit it as real runs accumulate — with data this time.
const MinSurvivorUnion = 3

// Vector is one seat's per-mutant outcome for a single run: mutant ID -> was
// it killed. Both vectors in a Compare MUST cover the identical mutant set.
type Vector struct {
	Model  string
	Killed map[string]bool
}

// Pair is the computed comparison between two seats over one mutant set.
// Denominators are always carried: a bare coefficient with no N is the number
// that ends up on a slide outliving its caveats.
type Pair struct {
	ModelA, ModelB  string
	Mutants         int // |M|
	SurvivedA       int // |S_A|
	SurvivedB       int // |S_B|
	SharedSurvivors int // |S_A ∩ S_B|
	UnionSurvivors  int // |S_A ∪ S_B|

	// Jaccard is |S_A ∩ S_B| / |S_A ∪ S_B| — "of everything either writer
	// missed, what fraction did both miss?" 1 is a total shared blind spot,
	// 0 is complementary coverage. Meaningless unless Sufficient.
	Jaccard float64
	// Kappa is Cohen's kappa over the full vector: is their agreement more
	// than chance predicts from their individual kill rates? A DIFFERENT
	// question from Jaccard — report both, never blend them. Meaningless, and
	// NOT zero, unless KappaDefined.
	Kappa float64
	// Sufficient governs JACCARD ALONE: it is false exactly when
	// UnionSurvivors < MinSurvivorUnion, i.e. when the survivor union is too
	// small for the headline coefficient to mean anything. Callers MUST check
	// it before reading Jaccard.
	Sufficient bool
	// KappaDefined governs KAPPA ALONE. Cohen's kappa divides by (1 - p_e),
	// which is zero when both seats are degenerate over the same outcome —
	// chance agreement is already total, so "agreement beyond chance" has no
	// value, not a value of 0. Callers MUST check this before reading Kappa.
	//
	// TWO INDEPENDENT STATISTICS, TWO INDEPENDENT SUFFICIENCY FLAGS. One bool
	// gating both is precisely the blending the spec forbids ("report both;
	// never blend them into one score"), and it suppressed the single most
	// important result this package can produce: past the MinSurvivorUnion
	// return, p_e == 1 means both seats survived EVERYTHING, so the union is
	// |M| >= MinSurvivorUnion and the intersection is |M| — Jaccard 1.0, a
	// TOTAL shared blind spot, which used to be zeroed and reported as
	// indistinguishable from "insufficient data".
	KappaDefined bool
}

// Compare computes the pairwise statistics for two seats that faced the
// identical mutant set. A mismatch is an error, not a best-effort intersection:
// two writers scored against different mutants is confounded by mutant
// difficulty, which is exactly the comparison this package exists to avoid.
func Compare(a, b Vector) (Pair, error) {
	if len(a.Killed) != len(b.Killed) {
		return Pair{}, fmt.Errorf("modelcorr: mutant sets differ in size (%s=%d, %s=%d) — the comparison would be confounded", a.Model, len(a.Killed), b.Model, len(b.Killed))
	}
	p := Pair{ModelA: a.Model, ModelB: b.Model, Mutants: len(a.Killed)}
	var bothKilled, bothSurvived int
	for id, aKilled := range a.Killed {
		bKilled, ok := b.Killed[id]
		if !ok {
			return Pair{}, fmt.Errorf("modelcorr: mutant %q absent from %s — the two seats did not face the same set", id, b.Model)
		}
		switch {
		case aKilled && bKilled:
			bothKilled++
		case !aKilled && !bKilled:
			bothSurvived++
		}
		if !aKilled {
			p.SurvivedA++
		}
		if !bKilled {
			p.SurvivedB++
		}
		if !aKilled || !bKilled {
			p.UnionSurvivors++
		}
	}
	p.SharedSurvivors = bothSurvived
	if p.UnionSurvivors < MinSurvivorUnion {
		return p, nil // Sufficient stays false; coefficients stay zero.
	}
	p.Sufficient = true
	p.Jaccard = float64(p.SharedSurvivors) / float64(p.UnionSurvivors)

	n := float64(p.Mutants)
	po := float64(bothKilled+bothSurvived) / n
	killedA := n - float64(p.SurvivedA)
	killedB := n - float64(p.SurvivedB)
	pe := (killedA/n)*(killedB/n) + (float64(p.SurvivedA)/n)*(float64(p.SurvivedB)/n)
	if pe == 1 {
		// Both seats were degenerate over the same outcome, so kappa is
		// undefined: report NO coefficient rather than a fabricated 0 or 1.
		//
		// The Jaccard above is untouched and stays reported. It is not
		// affected by kappa's degeneracy — and past the MinSurvivorUnion
		// return this branch can only be reached by both seats surviving
		// everything, which is Jaccard 1.0: the total shared blind spot this
		// package exists to surface.
		return p, nil // KappaDefined stays false; Kappa stays zero.
	}
	p.KappaDefined = true
	p.Kappa = (po - pe) / (1 - pe)
	return p, nil
}
