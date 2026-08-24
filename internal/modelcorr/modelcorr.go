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
// coefficient. Below it, Compare returns Sufficient=false and zeroed
// coefficients: a Jaccard over three survivors is noise, and emitting it
// anyway is how a number outlives its caveats. Same fail-closed reflex as
// adequacy.Report's CanaryKilled gating KillRate.
const MinSurvivorUnion = 10

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
	// question from Jaccard — report both, never blend them.
	Kappa float64
	// Sufficient is false when UnionSurvivors < MinSurvivorUnion. Callers MUST
	// check it before reading Jaccard or Kappa.
	Sufficient bool
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
		// Both seats were degenerate (all-kill or all-survive); kappa is
		// undefined. Report no coefficient rather than a fabricated 0 or 1.
		p.Sufficient = false
		p.Jaccard = 0
		return p, nil
	}
	p.Kappa = (po - pe) / (1 - pe)
	return p, nil
}
