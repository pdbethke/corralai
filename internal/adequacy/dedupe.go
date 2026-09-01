// SPDX-License-Identifier: Elastic-2.0

package adequacy

import "github.com/pdbethke/corralai/internal/lang"

// MutantIdentity is what makes two mutants the same edit: the same anchor
// becoming the same replacement over the same span of the same parent bytes.
// Two mutants sharing it MUST grade identically — it is the same file handed
// to the same suite — which is the whole licence for grading one and
// attributing the answer to both.
type MutantIdentity struct {
	Span            lang.LineRange
	Search, Replace string
	Parent          string
}

// IdentityOf is m's MutantIdentity.
func IdentityOf(m Mutant) MutantIdentity {
	return MutantIdentity{Span: m.Span, Search: m.Search, Replace: m.Replace, Parent: m.ParentSHA256}
}

// DedupeMutants collapses mutants that are the SAME EDIT — identical span,
// identical SEARCH anchor, identical REPLACE — keeping the first occurrence in
// the input's own order and reporting how many were collapsed.
//
// WHY. Scoring costs one whole suite run per mutant. Two identical hunks cost
// two runs for one measurement, and they are not rare: mutant generation is
// sharded, several seats can be aimed at overlapping regions, and a model
// asked for n distinct mutations will sometimes emit the same one twice. The
// second copy adds nothing — same edit, same source, so necessarily the same
// verdict — while inflating both the denominator and the wall clock.
//
// IT NEVER DEDUPES ACROSS FILES. Every caller passes ONE file's mutants (they
// are all single-point edits of the same `original`), and an anchor that reads
// identically in two different files is two different edits. There is no path
// here that could see both, and callers must keep it that way.
//
// THE COUNT IS RETURNED, NOT SWALLOWED. A verdict whose denominator silently
// shrank is a verdict nobody can reconcile against the generator's own output,
// so the collapsed count is disclosed by the caller ("N duplicate mutant(s)
// collapsed") for the same reason Report.Invalid is.
//
// A whole-file (v1) mutant has an empty Search, so its identity is its whole
// REPLACE — which is correct: for that shape the replacement IS the file.
func DedupeMutants(ms []Mutant) (kept []Mutant, collapsed int) {
	seen := make(map[MutantIdentity]bool, len(ms))
	kept = make([]Mutant, 0, len(ms))
	for _, m := range ms {
		id := IdentityOf(m)
		if seen[id] {
			collapsed++
			continue
		}
		seen[id] = true
		kept = append(kept, m)
	}
	return kept, collapsed
}
