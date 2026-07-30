// SPDX-License-Identifier: Elastic-2.0

package lang

import "testing"

// TestDedupeCandidatesAttributesLeastSpecificRank is a direct unit test of
// the round-3 fix: rank must denote convention KIND (how much real
// directory evidence a candidate carries), not the position at which a
// candidate happened to survive dedup. Two colliding non-sibling forms must
// be attributed the LEAST specific (highest) of their ranks; a sibling
// (Rank 0) collision must NEVER be devalued by a coincidental collision with
// a weaker form from the same source.
func TestDedupeCandidatesAttributesLeastSpecificRank(t *testing.T) {
	cases := []struct {
		name string
		in   []TestCandidate
		want []TestCandidate
	}{
		{
			name: "no collision: distinct paths pass through unchanged",
			in: []TestCandidate{
				{Path: "a", Rank: 0},
				{Path: "b", Rank: 1},
			},
			want: []TestCandidate{
				{Path: "a", Rank: 0},
				{Path: "b", Rank: 1},
			},
		},
		{
			name: "mirror/stripped/flat collision (all non-sibling): attribute the WORST (highest) rank",
			in: []TestCandidate{
				{Path: "tests/x.py", Rank: 1}, // mirror, listed first
				{Path: "tests/x.py", Rank: 2}, // stripped, degenerates onto the same string
				{Path: "tests/x.py", Rank: 3}, // flat, degenerates onto the same string
			},
			// Must NOT keep rank 1 (the earliest-listed form) — that was
			// exactly the bug: a demo file's degenerate "stripped" match
			// (rank 2) beating a genuine "flat" match (rank 3) from a
			// different, deeper source, because 2 < 3 even though NEITHER
			// carries real directory evidence.
			want: []TestCandidate{{Path: "tests/x.py", Rank: 3}},
		},
		{
			name: "sibling collides with a degenerate lower-specificity form: sibling (rank 0) wins, never demoted",
			in: []TestCandidate{
				// tests/test_utils.py, generated three ways for a source that
				// itself lives in a dir literally named "tests" (e.g.
				// tests/utils.py): sibling (rank 0, genuinely same-directory
				// evidence), and its own stripped/flat forms coincidentally
				// produce the identical string.
				{Path: "tests/test_utils.py", Rank: 0},
				{Path: "tests/test_utils.py", Rank: 2},
				{Path: "tests/test_utils.py", Rank: 3},
			},
			want: []TestCandidate{{Path: "tests/test_utils.py", Rank: 0}},
		},
		{
			name: "sibling wins regardless of listed order",
			in: []TestCandidate{
				{Path: "p", Rank: 3},
				{Path: "p", Rank: 0},
				{Path: "p", Rank: 2},
			},
			want: []TestCandidate{{Path: "p", Rank: 0}},
		},
		{
			name: "position of the surviving entry is the FIRST occurrence, independent of rank",
			in: []TestCandidate{
				{Path: "a", Rank: 0},
				{Path: "shared", Rank: 1},
				{Path: "shared", Rank: 3}, // collides with the entry above; "shared" keeps its position 1
				{Path: "b", Rank: 0},
			},
			want: []TestCandidate{
				{Path: "a", Rank: 0},
				{Path: "shared", Rank: 3},
				{Path: "b", Rank: 0},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := dedupeCandidates(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("dedupeCandidates(%+v) = %+v, want %+v", c.in, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("dedupeCandidates(%+v)[%d] = %+v, want %+v\nfull got=%+v", c.in, i, got[i], c.want[i], got)
				}
			}
		})
	}
}
