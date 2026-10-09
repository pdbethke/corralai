// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"testing"

	"github.com/pdbethke/corralai/internal/pairing"
)

// This file holds registry-wide checks on the one thing every plugin declares
// about pairing — its TestRules — for invariants internal/pairing relies on
// but cannot see, because only lang can enumerate the registered plugins.

// TestEveryRuleRankAgreesWithItsShape holds every registered plugin to
// "Sibling ⇔ Rank 0" for every rule that produces a candidate. pairing.Dedupe
// decides sibling strength by Shape; reposcan compares Rank across sources.
// The two said the same thing only by convention until the Round B1 final
// review, which showed a rank-less non-Sibling rule silently buying sibling
// strength. Dedupe no longer reads a zero Rank as "sibling", but a forgotten
// Rank is still a wrong Rank — this is where it is caught.
func TestEveryRuleRankAgreesWithItsShape(t *testing.T) {
	for _, name := range pluginNames() {
		for i, r := range registry[name].TestRules() {
			if r.Shape == pairing.Recognize {
				continue // never a candidate, so it has no rank to agree with
			}
			if r.Shape == pairing.Sibling && r.Rank != 0 {
				t.Errorf("%s rule %d (%+v): a Sibling rule must be Rank 0, the most specific rank there is", name, i, r)
			}
			if r.Shape != pairing.Sibling && r.Rank <= 0 {
				t.Errorf("%s rule %d (%+v): a non-Sibling rule must have Rank > 0 — Rank 0 is the sibling rank, so this rule's Rank was probably forgotten", name, i, r)
			}
		}
	}
}
