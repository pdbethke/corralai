// SPDX-License-Identifier: Elastic-2.0

// Package pairingtest holds the ONE independent statement of the rank-merge
// rule that pairing.Dedupe approximates, for tests only — the same shape as
// net/http/httptest. No production package imports it.
//
// It exists as a package, rather than as a helper in a _test.go file, because
// two packages' tests need the same oracle: internal/lang checks it against
// every shipped plugin's REAL TestRules (only lang can enumerate its
// registry), and internal/pairing proves the check can fail at all, with a
// synthetic violator. A _test.go helper is invisible across packages, so the
// alternative was two copies of the oracle — and two copies of an oracle can
// drift apart exactly the way the six TestPaths did.
package pairingtest

import "github.com/pdbethke/corralai/internal/pairing"

// RawForm is one PRE-dedupe candidate, tagged with the two facts the
// principled rule reads.
type RawForm struct {
	Path    string
	Rank    int
	Sibling bool // same-directory match: intrinsically real evidence, always.
	Vacuous bool // non-sibling form whose directory component is empty: asserts nothing.
}

// PrincipledMerge is the principled rule, implemented independently of
// pairing.Dedupe. A raw candidate asserts real directory evidence unless it is
// a sibling (which is ALWAYS real, same-directory evidence, intrinsically) or
// its own directory component has degenerated to empty (asserting nothing —
// it is indistinguishable from a flat, no-context match). When several raw
// forms collide on the same path string: a sibling among them always wins
// outright (Rank 0); otherwise, if at least one colliding form is
// non-vacuous, the merged rank is the MIN (strongest) among the non-vacuous
// forms' ranks — vacuous colliders contribute no information and must not
// drag a real claim down; only when EVERY colliding form is vacuous does the
// merge fall back to the least-informative (max) rank, because in that case
// there is truly nothing to distinguish them.
//
// Ordering matches Dedupe: each surviving path keeps the position of its
// FIRST occurrence in cands. Shape is not modelled; compare with SameMerge.
func PrincipledMerge(cands []RawForm) []pairing.Candidate {
	firstIdx := map[string]int{}
	var order []string
	siblingAny := map[string]bool{}
	haveNonVacuous := map[string]bool{}
	minNonVacuousRank := map[string]int{}
	maxRank := map[string]int{}
	haveAny := map[string]bool{}

	for _, c := range cands {
		if _, ok := firstIdx[c.Path]; !ok {
			firstIdx[c.Path] = len(order)
			order = append(order, c.Path)
		}
		if c.Sibling {
			siblingAny[c.Path] = true
		}
		if !haveAny[c.Path] || c.Rank > maxRank[c.Path] {
			maxRank[c.Path] = c.Rank
		}
		haveAny[c.Path] = true
		if !c.Sibling && !c.Vacuous {
			if !haveNonVacuous[c.Path] || c.Rank < minNonVacuousRank[c.Path] {
				minNonVacuousRank[c.Path] = c.Rank
				haveNonVacuous[c.Path] = true
			}
		}
	}

	out := make([]pairing.Candidate, len(order))
	for i, p := range order {
		var rank int
		switch {
		case siblingAny[p]:
			rank = 0
		case haveNonVacuous[p]:
			rank = minNonVacuousRank[p]
		default:
			rank = maxRank[p]
		}
		out[i] = pairing.Candidate{Path: p, Rank: rank}
	}
	return out
}

// SameMerge reports whether a and b agree on every merged PATH and RANK, in
// order. Shape is deliberately not compared: the property under test is the
// merge, and PrincipledMerge does not model Shape.
func SameMerge(a, b []pairing.Candidate) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Path != b[i].Path || a[i].Rank != b[i].Rank {
			return false
		}
	}
	return true
}
