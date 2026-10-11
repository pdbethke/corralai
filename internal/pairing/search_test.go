// SPDX-License-Identifier: Elastic-2.0

package pairing

import (
	"errors"
	"testing"
)

// These pin Resolve's stage-ordering contract and Round B1b's review focuses.
// The moved characterization tests of the helpers Resolve is built from live
// in search_pin_test.go, unchanged apart from their package.

func TestResolveStage1HitNeverListsTheUniverse(t *testing.T) {
	rules := []Rule{{Shape: Sibling, Name: "test_{base}.py"}}
	called := false
	res, err := Resolve(rules, "pkg/mod.py", Sources{
		Exists:   func(rel string) bool { return rel == "pkg/test_mod.py" },
		Universe: func() ([]string, error) { called = true; return nil, errors.New("git broke") },
	})
	if err != nil || !res.Found || res.Path != "pkg/test_mod.py" || res.ViaSearch || called {
		t.Fatalf("res=%+v err=%v universeCalled=%v", res, err, called)
	}
}

// Review Focus 2: a stage-1 miss surfaces the lister's error.
func TestResolveSurfacesUniverseErrorOnlyAfterAMiss(t *testing.T) {
	rules := []Rule{{Shape: Sibling, Name: "test_{base}.py"}}
	_, err := Resolve(rules, "pkg/mod.py", Sources{
		Exists:   func(string) bool { return false },
		Universe: func() ([]string, error) { return nil, errors.New("git broke") },
	})
	if err == nil || err.Error() != "git broke" {
		t.Fatalf("want the lister's error, got %v", err)
	}
}

// Review Focus 1: no universe, no roots to speak of — a clean miss that says where it looked.
func TestResolveCleanMissReportsTriedAndRoots(t *testing.T) {
	rules := []Rule{{Shape: Sibling, Name: "test_{base}.py"}}
	res, err := Resolve(rules, "pkg/mod.py", Sources{
		Exists:   func(string) bool { return false },
		Universe: func() ([]string, error) { return nil, nil },
	})
	if err != nil || res.Found || len(res.Tried) != 1 || len(res.Roots) == 0 {
		t.Fatalf("res=%+v err=%v", res, err)
	}
}

// Review Focus 3: a root-level source shares no directory, so search refuses.
func TestResolveRootLevelSourceNeverSearchHits(t *testing.T) {
	rules := []Rule{{Shape: Sibling, Name: "test_{base}.py"}}
	res, _ := Resolve(rules, "mod.py", Sources{
		Exists:   func(string) bool { return false },
		Universe: func() ([]string, error) { return []string{"tests/test_mod.py"}, nil },
	})
	if res.Found {
		t.Fatalf("a zero-correlation basename match must be refused: %+v", res)
	}
}

// Review Focus 4: equal score and depth → lexicographic, whatever the universe order.
func TestResolveTieBreakIsOrderIndependent(t *testing.T) {
	rules := []Rule{{Shape: Sibling, Name: "test_{base}.py"}}
	a := []string{"tests/zz/pkg/test_mod.py", "tests/aa/pkg/test_mod.py"}
	b := []string{a[1], a[0]}
	var got []string
	for _, u := range [][]string{a, b} {
		u := u
		res, _ := Resolve(rules, "src/pkg/mod.py", Sources{
			Exists:   func(string) bool { return false },
			Universe: func() ([]string, error) { return u, nil },
		})
		got = append(got, res.Path)
	}
	if got[0] != got[1] || got[0] != "tests/aa/pkg/test_mod.py" {
		t.Fatalf("got %v", got)
	}
}

// Review Focus 5: a segment that IS an affix is left alone.
func TestNormalizeSegLeavesABareAffixAlone(t *testing.T) {
	for _, s := range []string{"test_", "spec_", "_test", "_spec", "spec", "tests"} {
		if got := normalizeSeg(s); got != s {
			t.Errorf("normalizeSeg(%q) = %q", s, got)
		}
	}
	if got := normalizeSeg("test_itsdangerous"); got != "itsdangerous" {
		t.Errorf("itsdangerous shape: %q", got)
	}
}

// A Resolution carries the winning candidate's own Rank AND Shape through the
// boundary — a field-by-field converter (the old reposcan.SearchResult copy)
// dropped Rank; this pins that neither label is lost again. A search hit
// carries SearchRank and ViaSearch, and no Shape: no rule paired it.
func TestResolveCarriesRankAndShapeOfTheWinner(t *testing.T) {
	rules := []Rule{
		{Shape: Sibling, Name: "test_{base}.py"},
		{Shape: ParallelTree, Name: "test_{base}.py", Dir: "tests", Rank: 1},
	}
	hit, err := Resolve(rules, "pkg/mod.py", Sources{
		Exists:   func(rel string) bool { return rel == "tests/test_mod.py" },
		Universe: func() ([]string, error) { return nil, nil },
	})
	if err != nil || !hit.Found || hit.ViaSearch || hit.Rank != 1 || hit.Shape != ParallelTree {
		t.Fatalf("stage-1 hit: res=%+v err=%v; want Rank 1, Shape %q", hit, err, ParallelTree)
	}

	found, err := Resolve(rules, "pkg/mod.py", Sources{
		Exists:   func(string) bool { return false },
		Universe: func() ([]string, error) { return []string{"tests/unit/pkg/test_mod.py"}, nil },
	})
	if err != nil || !found.Found || !found.ViaSearch || found.Rank != SearchRank || found.Shape != "" {
		t.Fatalf("search hit: res=%+v err=%v; want SearchRank, ViaSearch, empty Shape", found, err)
	}
}

// TestBestClaim pins the collision policy: the single strictly-lowest rank
// wins; any tie for the lowest — including SearchRank against SearchRank —
// has no winner.
func TestBestClaim(t *testing.T) {
	cases := []struct {
		ranks  []int
		winner int
		ok     bool
	}{
		{nil, -1, false},
		{[]int{2}, 0, true},
		{[]int{0, 3}, 0, true},
		{[]int{3, 0, 3}, 1, true},
		{[]int{SearchRank, 2}, 1, true},
		{[]int{1, 1}, -1, false},
		{[]int{0, 0, 2}, -1, false},
		{[]int{2, 1, 1, 1}, -1, false},
		{[]int{SearchRank, SearchRank}, -1, false},
	}
	for _, c := range cases {
		w, ok := BestClaim(c.ranks)
		if w != c.winner || ok != c.ok {
			t.Errorf("BestClaim(%v) = %d, %v; want %d, %v", c.ranks, w, ok, c.winner, c.ok)
		}
	}
}
