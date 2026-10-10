// SPDX-License-Identifier: Elastic-2.0

package pairing

import "testing"

// TestDedupeAttributesLeastSpecificRank is a direct unit test of
// the round-3 fix: rank must denote convention KIND (how much real
// directory evidence a candidate carries), not the position at which a
// candidate happened to survive dedup. Two colliding non-sibling forms must
// be attributed the LEAST specific (highest) of their ranks; a sibling
// (Rank 0) collision must NEVER be devalued by a coincidental collision with
// a weaker form from the same source. Sibling-ness is the candidate's Shape
// (TestDedupeSiblingIsAShapeNotARank), so the sibling rows below say so.
func TestDedupeAttributesLeastSpecificRank(t *testing.T) {
	cases := []struct {
		name string
		in   []Candidate
		want []Candidate
	}{
		{
			name: "no collision: distinct paths pass through unchanged",
			in: []Candidate{
				{Path: "a", Rank: 0},
				{Path: "b", Rank: 1},
			},
			want: []Candidate{
				{Path: "a", Rank: 0},
				{Path: "b", Rank: 1},
			},
		},
		{
			name: "mirror/stripped/flat collision (all non-sibling): attribute the WORST (highest) rank",
			in: []Candidate{
				{Path: "tests/x.py", Rank: 1}, // mirror, listed first
				{Path: "tests/x.py", Rank: 2}, // stripped, degenerates onto the same string
				{Path: "tests/x.py", Rank: 3}, // flat, degenerates onto the same string
			},
			// Must NOT keep rank 1 (the earliest-listed form) — that was
			// exactly the bug: a demo file's degenerate "stripped" match
			// (rank 2) beating a genuine "flat" match (rank 3) from a
			// different, deeper source, because 2 < 3 even though NEITHER
			// carries real directory evidence.
			want: []Candidate{{Path: "tests/x.py", Rank: 3}},
		},
		{
			name: "sibling collides with a degenerate lower-specificity form: sibling (rank 0) wins, never demoted",
			in: []Candidate{
				// tests/test_utils.py, generated three ways for a source that
				// itself lives in a dir literally named "tests" (e.g.
				// tests/utils.py): sibling (rank 0, genuinely same-directory
				// evidence), and its own stripped/flat forms coincidentally
				// produce the identical string.
				{Path: "tests/test_utils.py", Rank: 0, Shape: Sibling},
				{Path: "tests/test_utils.py", Rank: 2, Shape: ParallelTree},
				{Path: "tests/test_utils.py", Rank: 3, Shape: FlatRoot},
			},
			want: []Candidate{{Path: "tests/test_utils.py", Rank: 0, Shape: Sibling}},
		},
		{
			name: "sibling wins regardless of listed order",
			in: []Candidate{
				{Path: "p", Rank: 3, Shape: FlatRoot},
				{Path: "p", Rank: 0, Shape: Sibling},
				{Path: "p", Rank: 2, Shape: ParallelTree},
			},
			// Rank 0 because a Sibling is among them; Shape is the first
			// occurrence's, as Dedupe documents.
			want: []Candidate{{Path: "p", Rank: 0, Shape: FlatRoot}},
		},
		{
			name: "position of the surviving entry is the FIRST occurrence, independent of rank",
			in: []Candidate{
				{Path: "a", Rank: 0},
				{Path: "shared", Rank: 1},
				{Path: "shared", Rank: 3}, // collides with the entry above; "shared" keeps its position 1
				{Path: "b", Rank: 0},
			},
			want: []Candidate{
				{Path: "a", Rank: 0},
				{Path: "shared", Rank: 3},
				{Path: "b", Rank: 0},
			},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Dedupe(c.in)
			if len(got) != len(c.want) {
				t.Fatalf("Dedupe(%+v) = %+v, want %+v", c.in, got, c.want)
			}
			for i := range got {
				if got[i] != c.want[i] {
					t.Errorf("Dedupe(%+v)[%d] = %+v, want %+v\nfull got=%+v", c.in, i, got[i], c.want[i], got)
				}
			}
		})
	}
}

// TestDedupeSiblingIsAShapeNotARank is the final-review I1 case, reproduced
// exactly: a ParallelTree rule that forgot its Rank (so it is Rank 0) collides
// with a Rank-3 FlatRoot on foo.py. Sibling strength is a claim about WHERE the
// test sits (the source's own directory), which only a Sibling rule makes; a
// zero Rank is just the integer's default. Before the fix Dedupe read "Rank 0"
// as "sibling" and handed the forgetful rule's vacuous tests/test_foo.py the
// strongest rank there is.
func TestDedupeSiblingIsAShapeNotARank(t *testing.T) {
	rules := []Rule{
		{Shape: ParallelTree, Name: "test_{base}.py", Dir: "tests"}, // Rank forgotten
		{Shape: FlatRoot, Name: "test_{base}.py", Dir: "tests", MaxDepth: 2, Rank: 3},
	}
	got := Candidates(rules, "foo.py")
	want := []Candidate{{Path: "tests/test_foo.py", Rank: 3, Shape: ParallelTree}}
	if len(got) != 1 || got[0] != want[0] {
		t.Fatalf("Candidates(foo.py) = %+v, want %+v — a non-Sibling rule's zero Rank must not buy sibling strength", got, want)
	}
}
