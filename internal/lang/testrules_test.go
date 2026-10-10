// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"path/filepath"
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
		rules := registry[name].TestRules()
		ext, ok := sourceExt(rules)
		if !ok {
			t.Fatalf("plugin %q declares no Sibling rule, so its source extension cannot be derived", name)
		}
		for i, r := range rules {
			// Whether a Shape produces candidates is pairing's shapeTable's
			// call, not this test's: ask it, through a top-level source every
			// candidate-producing Shape answers for (depth 0 is within any
			// FlatRoot's MaxDepth).
			if len(pairing.Candidates([]pairing.Rule{r}, "x"+ext)) == 0 {
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

// TestEveryCandidateIsATestAndNoSourceIs is the round trip pairing.IsTest
// depends on (see pairing.Rule's doc): IsTest reads every rule's Name as a
// "this file IS a test" pattern, which is only right while each Name carries
// a test marker beyond {base} and the extension. So, for every registered
// plugin and every source in the property corpus (none of whose base names
// carries a marker): the source itself is NOT a test, and every candidate the
// plugin derives for it IS one. A Rust-style rule whose Name is just
// "{base}.rs" under tests/ fails the first half — it makes src/lib.rs a test,
// silently removing real code from the audit (Round B1 final review, I2).
func TestEveryCandidateIsATestAndNoSourceIs(t *testing.T) {
	for _, name := range pluginNames() {
		p := registry[name]
		rules := p.TestRules()
		ext, ok := sourceExt(rules)
		if !ok {
			t.Fatalf("plugin %q declares no Sibling rule, so its source extension cannot be derived", name)
		}
		checked := 0
		for _, dir := range corpusDirs {
			for _, base := range corpusBases {
				src := filepath.ToSlash(filepath.Join(dir, base+ext))
				if !p.Detect(src) {
					t.Fatalf("%s does not detect corpus path %q", name, src)
				}
				if pairing.IsTest(rules, src) {
					t.Errorf("%s: IsTest(%q) = true — a source with no test marker was classified as a test, so it would never be audited; some rule's Name has no marker beyond {base} and the extension", name, src)
				}
				for _, c := range pairing.Candidates(rules, src) {
					if cp := filepath.ToSlash(c.Path); !pairing.IsTest(rules, cp) {
						t.Errorf("%s: candidate %q for %q is not IsTest — the plugin would pair a source with a file it does not itself recognize as a test", name, cp, src)
					}
				}
				checked++
			}
		}
		if checked == 0 {
			t.Fatalf("%s: empty corpus — property was not actually exercised", name)
		}
	}
}
