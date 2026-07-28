// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"path/filepath"
	"strings"
)

// splitPath decomposes codePath into its directory (empty string for a
// top-level file — never "."), its base name WITHOUT extension, and its
// extension (including the leading dot). Shared by every non-Go plugin's
// TestPaths, since the parallel-tree and sibling forms all start here.
func splitPath(codePath string) (dir, base, ext string) {
	ext = filepath.Ext(codePath)
	b := filepath.Base(codePath)
	base = strings.TrimSuffix(b, ext)
	dir = filepath.Dir(codePath)
	if dir == "." {
		dir = ""
	}
	return dir, base, ext
}

// joinDir joins dir and name, treating an empty dir as "no directory"
// (plain name) rather than filepath.Join's "./name".
func joinDir(dir, name string) string {
	if dir == "" {
		return name
	}
	return filepath.Join(dir, name)
}

// stripFirstSegment removes the leading path component of a dir, e.g.
// "aisuite/agents" -> "agents", "agents" -> "", "" -> "". This is how a
// source file under one top-level directory (a package name, or a `src/`
// layout) maps onto a parallel test tree: real-world convention REPLACES the
// leading directory rather than nesting the whole original path under
// `tests/` (`aisuite/agents/artifact_store.py` pairs with
// `tests/agents/test_artifact_store.py`, not
// `tests/aisuite/agents/test_artifact_store.py`).
func stripFirstSegment(dir string) string {
	if dir == "" {
		return ""
	}
	parts := strings.SplitN(filepath.ToSlash(dir), "/", 2)
	if len(parts) == 2 {
		return filepath.FromSlash(parts[1])
	}
	return ""
}

// dirDepth returns the number of path segments in dir (0 for a top-level
// file). Used to bound how far a "no directory context at all" flat-tree
// test candidate is allowed to reach: a flat candidate is a plausible
// convention for a shallow source but a collision magnet for a deep one
// (e.g. `examples/javascript/js_example/views.py`, 3 segments deep, would
// otherwise generate the same flat candidate as a genuine top-level
// `src/flask/views.py`).
func dirDepth(dir string) int {
	if dir == "" {
		return 0
	}
	return len(strings.Split(filepath.ToSlash(dir), "/"))
}

// dedupeCandidates drops duplicate paths, keeping each surviving entry at
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
func dedupeCandidates(cands []TestCandidate) []TestCandidate {
	firstIdx := make(map[string]int, len(cands))
	minRank := make(map[string]int, len(cands))
	maxRank := make(map[string]int, len(cands))
	var order []string
	for _, c := range cands {
		if _, ok := firstIdx[c.Path]; !ok {
			firstIdx[c.Path] = len(order)
			order = append(order, c.Path)
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
	out := make([]TestCandidate, len(order))
	for i, p := range order {
		rank := maxRank[p]
		if minRank[p] == 0 {
			rank = 0
		}
		out[i] = TestCandidate{Path: p, Rank: rank}
	}
	return out
}
