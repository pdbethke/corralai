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
	for _, name := range Names() {
		rules := registry[name].TestRules()
		ext, ok := sourceExt(rules)
		if !ok {
			t.Fatalf("plugin %q declares no rule Name with an extension, so its source extension cannot be derived", name)
		}
		for i, r := range rules {
			// Whether a Shape produces candidates is pairing's shapeTable's
			// call, not this test's: ask it, through a top-level source every
			// candidate-producing Shape answers for (depth 0 is within any
			// FlatRoot's MaxDepth).
			if len(pairing.Candidates([]pairing.Rule{r}, "x"+ext)) == 0 {
				continue // never a candidate, so it has no rank to agree with
			}
			// A content Shape's candidate is what the source itself declares
			// (an inline test module is the source; pairing.Shape.Content),
			// which is at least as specific as a sibling: Rank 0 as well.
			zeroRank := r.Shape == pairing.Sibling || r.Shape.Content()
			if zeroRank && r.Rank != 0 {
				t.Errorf("%s rule %d (%+v): a Sibling or content rule must be Rank 0, the most specific rank there is", name, i, r)
			}
			if !zeroRank && r.Rank <= 0 {
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
//
// Two refinements arrived with Rust, both read from pairing rather than
// restated here, and both applied to every plugin alike (no exemption):
//
//   - A corpus path under a directory a TestDir rule declares (cargo's
//     tests/) IS a test by that directory, so the first half asks that the
//     NAME never makes a source a test: IsTest must equal DirMarksTest.
//     Every pre-Rust plugin declares no TestDir, so for them this is the
//     original "IsTest is false".
//   - A content Shape's candidate is the source itself (an inline test
//     module lives in the file it tests), so it must equal the source, and is
//     not asked to be a test file.
func TestEveryCandidateIsATestAndNoSourceIs(t *testing.T) {
	for _, name := range Names() {
		p := registry[name]
		rules := p.TestRules()
		ext, ok := sourceExt(rules)
		if !ok {
			t.Fatalf("plugin %q declares no rule Name with an extension, so its source extension cannot be derived", name)
		}
		checked := 0
		for _, dir := range corpusDirs {
			for _, base := range corpusBases {
				src := filepath.ToSlash(filepath.Join(dir, base+ext))
				if !p.Detect(src) {
					t.Fatalf("%s does not detect corpus path %q", name, src)
				}
				if pairing.IsTest(rules, src) != pairing.DirMarksTest(rules, src) {
					t.Errorf("%s: IsTest(%q) = %v but DirMarksTest = %v — a source with no test marker was classified by its NAME, so it would never be audited; some rule's Name has no marker beyond {base} and the extension", name, src, pairing.IsTest(rules, src), pairing.DirMarksTest(rules, src))
				}
				for _, c := range pairing.Candidates(rules, src) {
					cp := filepath.ToSlash(c.Path)
					if c.Shape.Content() {
						if cp != src {
							t.Errorf("%s: content candidate %q for %q is not the source itself", name, cp, src)
						}
						continue
					}
					if !pairing.IsTest(rules, cp) {
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
