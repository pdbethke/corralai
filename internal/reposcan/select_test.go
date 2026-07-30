// SPDX-License-Identifier: Elastic-2.0

package reposcan

import "testing"

func TestSelectBoundsAndAccountsTheRemainder(t *testing.T) {
	ranked := []Candidate{
		{Path: "a.go"}, {Path: "b.go"}, {Path: "c.go"}, {Path: "d.go"},
	}
	sel, excl := Select(ranked, 2)

	if len(sel) != 2 || sel[0].Path != "a.go" || sel[1].Path != "b.go" {
		t.Fatalf("selected = %+v, want the ranked head a.go,b.go", sel)
	}
	if len(excl) != 2 {
		t.Fatalf("excluded = %+v, want 2 accounted entries", excl)
	}
	for _, e := range excl {
		if e.Reason != ReasonNotSelected {
			t.Errorf("%s reason = %q, want %q", e.Path, e.Reason, ReasonNotSelected)
		}
	}
	// The invariant this unit exists to protect.
	if len(sel)+len(excl) != len(ranked) {
		t.Errorf("selected+excluded = %d, want %d — a candidate was dropped",
			len(sel)+len(excl), len(ranked))
	}
}

func TestSelectUnboundedTakesEverything(t *testing.T) {
	ranked := []Candidate{{Path: "a.go"}, {Path: "b.go"}}
	for _, limit := range []int{0, -1} {
		sel, excl := Select(ranked, limit)
		if len(sel) != 2 || len(excl) != 0 {
			t.Errorf("limit %d: selected %d excluded %d, want 2 and 0", limit, len(sel), len(excl))
		}
	}
}

func TestSelectLimitAboveCountTakesEverything(t *testing.T) {
	ranked := []Candidate{{Path: "a.go"}}
	sel, excl := Select(ranked, 25)
	if len(sel) != 1 || len(excl) != 0 {
		t.Errorf("selected %d excluded %d, want 1 and 0", len(sel), len(excl))
	}
}

// The returned head aliases ranked's backing array, whose tail is exactly the
// region the exclusions were built from. Without a capacity bound an append by
// any future caller silently overwrites a candidate already reported as
// not-selected.
func TestSelectHeadCannotClobberTheExcludedTail(t *testing.T) {
	ranked := []Candidate{{Path: "a.go"}, {Path: "b.go"}, {Path: "c.go"}}
	sel, excl := Select(ranked, 1)

	sel = append(sel, Candidate{Path: "intruder.go"}) //nolint:staticcheck // the append IS the test
	_ = sel
	if ranked[1].Path != "b.go" {
		t.Errorf("an append to the selected head clobbered ranked[1] (now %q)", ranked[1].Path)
	}
	if excl[0].Path != "b.go" {
		t.Errorf("the exclusion list no longer matches the tree: %q", excl[0].Path)
	}
}
