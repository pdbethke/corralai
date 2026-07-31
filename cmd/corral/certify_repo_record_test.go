// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"testing"

	"github.com/pdbethke/corralai/internal/reposcan"
)

// TestExclusionEvidence_OnlyClaimsPairingWhenPairingWasAttempted fixes a
// false evidence claim that sat in every scan ever recorded and was invisible
// until `corral scans` could SELECT it: exclusionEvidence returned "paired"
// for EVERY exclusion lacking a preflight state, including files rejected
// before any pairing was attempted. A .editorconfig rejected as `no-language`
// was never paired with anything.
//
// It also contradicted its own neighbouring contract: ungradableEvidence's doc
// states that "" is "the same value an excluded file that was never even a
// candidate gets — (see exclusionEvidence)", and exclusionEvidence never
// returned "".
//
// The split follows internal/reposcan/candidate.go's own ordering, not taste:
// no-language (:215) and is-test (:222) are decided BEFORE TestPaths is called
// (:232), while no-paired-test (:241) and ambiguous-test are decided from its
// result. not-a-regular-file and skipped-dir are walk-time, earlier still.
func TestExclusionEvidence_OnlyClaimsPairingWhenPairingWasAttempted(t *testing.T) {
	for _, c := range []struct {
		reason, preflight, want, why string
	}{
		{reposcan.ReasonNoLanguage, "", "", "no plugin matched, so TestPaths was never called"},
		{reposcan.ReasonIsTest, "", "", "the file IS a test — excluded before pairing"},
		{reposcan.ReasonNotRegularFile, "", "", "rejected at walk time, before language detection"},
		{reposcan.ReasonSkippedDir, "", "", "rejected at walk time, before language detection"},
		{reposcan.ReasonNoPairedTest, "", "paired", "pairing WAS attempted; finding nothing is the evidence"},
		{reposcan.ReasonAmbiguousTest, "", "paired", "pairing was attempted and collided"},
		{reposcan.ReasonNotSelected, "", "paired", "it WAS a candidate — it has a pair; the --top bound excluded it"},
		// A coverage measurement outranks any pairing question, and applies
		// even to a file pairing never looked at: the instrumented run really
		// did measure this exact path.
		{reposcan.ReasonNoLanguage, "executed", "coverage", "the preflight measured this exact path"},
		{reposcan.ReasonNotSelected, "not-executed", "coverage", "the preflight measured this exact path"},
	} {
		t.Run(c.reason+"/"+c.preflight, func(t *testing.T) {
			if got := exclusionEvidence(c.reason, c.preflight); got != c.want {
				t.Fatalf("exclusionEvidence(%q, %q) = %q, want %q — %s", c.reason, c.preflight, got, c.want, c.why)
			}
		})
	}
}

// TestExclusionEvidence_NeverInventsALabel guards the CHECK constraint the
// scan_files schema enforces: only "", "paired", "coverage", "proven" are
// storable, and a typo'd label fails loudly at INSERT. An unknown reason must
// fall back to the no-claim value, never to a guess.
func TestExclusionEvidence_NeverInventsALabel(t *testing.T) {
	valid := map[string]bool{"": true, "paired": true, "coverage": true, "proven": true}
	for _, reason := range []string{"", "some-future-reason", "typo'd"} {
		if got := exclusionEvidence(reason, ""); !valid[got] {
			t.Fatalf("exclusionEvidence(%q, \"\") = %q, which is not a storable evidence value", reason, got)
		}
		if got := exclusionEvidence(reason, ""); got == "paired" {
			t.Fatalf("an UNKNOWN reason must not claim pairing evidence, got %q", got)
		}
	}
}
