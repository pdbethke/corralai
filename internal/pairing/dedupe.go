// SPDX-License-Identifier: Elastic-2.0

package pairing

// Dedupe drops duplicate paths, keeping each surviving entry at
// the POSITION of its first (most specific) occurrence, and attributing it a
// Rank computed as follows: if ANY candidate producing that path is Rank 0
// (sibling — same directory as the source), the merged entry keeps Rank 0;
// otherwise it takes the LEAST specific (maximum) Rank among every candidate
// that produced that path.
//
// Two things are going on, and they pull in different directions:
//
//  1. Candidates naturally collide when a source file has a shallow
//     directory (e.g. a one-segment dir makes the "strip first segment" and
//     "flat" forms identical strings) — collapsing them to one entry keeps
//     the ordering promise (each entry in the returned slice is distinct)
//     without every plugin special-casing shallow paths.
//  2. If the SURVIVING entry always inherited the EARLIEST colliding form's
//     rank, a path that carries zero real directory evidence would be
//     ranked differently depending on how many more-specific-LOOKING forms
//     happened to also degenerate into it for that particular source's
//     depth — which is exactly what let a demo file (examples/views.py, 1
//     segment: its "stripped" form degenerates to the flat string) beat a
//     genuine flat match (src/flask/views.py, 2 segments: only the true
//     flat form reaches that string) instead of tying with it.
//
// So "attribute the worst rank" is right — MOST of the time. But a Rank 0
// sibling match is not part of that same discard family: it is an
// independent, always-maximally-specific claim ("this test lives in the
// exact literal directory as this source"), and its truth does not depend on
// whatever a DIFFERENT, weaker candidate for the SAME source also happens to
// resolve to. A source whose own directory is coincidentally named exactly
// like the parallel-tree root (e.g. a file at tests/utils.py: its sibling
// tests/test_utils.py string-collides with its own degenerate
// leading-segment-stripped and flat forms, purely because "tests" strips to
// "") must not have its genuine same-directory pairing devalued by that
// coincidence. Hence the asymmetry: Rank 0 always wins the merge; only among
// candidates that are ALL non-sibling does "least specific" apply.
//
// INVARIANT a future plugin must not break (guarded by the property test
// TestDedupeMatchesPrincipledRuleForShippedPlugins in
// internal/lang/dedupe_property_test.go, which runs every registered
// plugin's real TestRules against pairingtest.PrincipledMerge):
// "attribute the max (least-specific) rank among
// colliding non-sibling forms" is only correct because every shipped
// plugin's non-sibling forms are, whenever they collide with something
// weaker, VACUOUS at that collision — their directory component degenerated
// to empty, so they carry no real evidence and "worst of several
// no-evidence guesses" is a fine answer. A form that DOES carry real,
// non-degenerate directory evidence (a genuine parallel-tree or full-mirror
// match) must never be dragged down to a weaker colliding form's rank just
// because the two happen to produce the same string — rank feeds a
// CROSS-SOURCE comparison (reposcan.demoteAmbiguousPairings), so
// understating one source's evidence can hand a wrongful strict win to a
// different source, which is a mispairing, not a safe demotion. If a new
// plugin can produce a collision between a non-vacuous, non-sibling form and
// a weaker one, this function's max-rule silently mis-scores it; the correct
// merge in that case is the MIN (strongest) rank among the non-vacuous
// colliding forms, not the max. The property test above exists to fail
// loudly the day that becomes possible, rather than let it mispair silently.
func Dedupe(cands []Candidate) []Candidate {
	firstIdx := make(map[string]int, len(cands))
	minRank := make(map[string]int, len(cands))
	maxRank := make(map[string]int, len(cands))
	shape := make(map[string]Shape, len(cands))
	var order []string
	for _, c := range cands {
		if _, ok := firstIdx[c.Path]; !ok {
			firstIdx[c.Path] = len(order)
			order = append(order, c.Path)
			shape[c.Path] = c.Shape
			minRank[c.Path] = c.Rank
			maxRank[c.Path] = c.Rank
			continue
		}
		if c.Rank < minRank[c.Path] {
			minRank[c.Path] = c.Rank
		}
		if c.Rank > maxRank[c.Path] {
			maxRank[c.Path] = c.Rank
		}
	}
	out := make([]Candidate, len(order))
	for i, p := range order {
		rank := maxRank[p]
		if minRank[p] == 0 {
			rank = 0
		}
		// A merged entry keeps the Shape of its first occurrence.
		out[i] = Candidate{Path: p, Rank: rank, Shape: shape[p]}
	}
	return out
}
