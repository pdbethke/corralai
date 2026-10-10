// SPDX-License-Identifier: Elastic-2.0
package pairing

import (
	"reflect"
	"testing"
)

func paths(cs []Candidate) []string {
	var out []string
	for _, c := range cs {
		out = append(out, c.Path)
	}
	return out
}

func TestEveryShapeProducesItsPath(t *testing.T) {
	rules := []Rule{
		{Shape: Sibling, Name: "test_{base}.py", Rank: 0},
		{Shape: BesideDir, Name: "{base}.test.js", Dir: "__tests__", Rank: 1},
		{Shape: ParallelTree, Name: "test_{base}.py", Dir: "tests", KeepLeading: true, Rank: 1},
		{Shape: ParallelTree, Name: "test_{base}.py", Dir: "tests", Rank: 2},
		{Shape: FlatRoot, Name: "test_{base}.py", Dir: "tests", MaxDepth: 2, Rank: 3},
		{Shape: Recognize, Name: "spec_{base}.rb"},
	}
	got := paths(Candidates(rules, "pkg/sub/mod.py"))
	want := []string{"pkg/sub/test_mod.py", "pkg/sub/__tests__/mod.test.js", "tests/pkg/sub/test_mod.py", "tests/sub/test_mod.py", "tests/test_mod.py"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

// Review Focus 1: a top-level source gets no "./" and no doubled slash.
func TestTopLevelSourceHasCleanPaths(t *testing.T) {
	rules := []Rule{{Shape: Sibling, Name: "test_{base}.py"}, {Shape: ParallelTree, Name: "test_{base}.py", Dir: "tests", Rank: 2}}
	got := paths(Candidates(rules, "foo.py"))
	want := []string{"test_foo.py", "tests/test_foo.py"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

// Review Focus 2: substitution is one literal replace.
func TestBaseIsSubstitutedOnceLiterally(t *testing.T) {
	got := paths(Candidates([]Rule{{Shape: Sibling, Name: "test_{base}.py"}}, "{base}.py"))
	if !reflect.DeepEqual(got, []string{"test_{base}.py"}) {
		t.Fatalf("got %v", got)
	}
}

// Review Focus 3: roots are trimmed, deduped, order-stable, DefaultRoot first.
func TestRootsTrimDedupeOrder(t *testing.T) {
	rules := []Rule{
		{Shape: BesideDir, Name: "x", Dir: "__tests__"},
		{Shape: ParallelTree, Name: "x", Dir: "test/"},
		{Shape: ParallelTree, Name: "x", Dir: "tests"},
		{Shape: Sibling, Name: "x"},
		{Shape: Recognize, Dir: "specs"},
	}
	if got := Roots(rules); !reflect.DeepEqual(got, []string{"tests", "__tests__", "test"}) {
		t.Fatalf("got %v", got)
	}
}

// Review Focus 4: FlatRoot's depth bound is inclusive at MaxDepth.
func TestFlatRootDepthBound(t *testing.T) {
	r := []Rule{{Shape: FlatRoot, Name: "test_{base}.py", Dir: "tests", MaxDepth: 2}}
	if got := paths(Candidates(r, "a/b/x.py")); !reflect.DeepEqual(got, []string{"tests/test_x.py"}) {
		t.Fatalf("depth 2 must get the flat candidate: %v", got)
	}
	if got := Candidates(r, "a/b/c/x.py"); len(got) != 0 {
		t.Fatalf("depth 3 must not: %v", got)
	}
}

// TestIsTestIsStrictMightBeTestIsGenerous pins the two questions apart. IsTest
// removes a file from the audit, so a directory alone never makes a file a
// test (a fixture under specs/ is not one). MightBeTest bounds a diff before
// any evidence exists, so the same fixture IS a possible test: a false yes
// there costs one instrumented run, a false no is a false green.
func TestIsTestIsStrictMightBeTestIsGenerous(t *testing.T) {
	py := []Rule{{Shape: Sibling, Name: "test_{base}.py"}}
	all := [][]Rule{py}
	for _, c := range []struct {
		rel        string
		is, mighty bool
	}{
		{"pkg/test_mod.py", true, true},
		{"pkg/mod.py", false, false},
		{"specs/fixtures/data.json", false, true}, // Review Focus 5
		{"testing/helpers.go", false, true},       // Review Focus 5
		{"lib/foo_spec.rb", true, true},           // a Generic marker, any language
	} {
		if got := IsTest(py, c.rel); got != c.is {
			t.Errorf("IsTest(%q) = %v, want %v", c.rel, got, c.is)
		}
		if got := MightBeTest(all, c.rel); got != c.mighty {
			t.Errorf("MightBeTest(%q) = %v, want %v", c.rel, got, c.mighty)
		}
	}
}

// TestGenericMarkersKeepTheOldChecksExactly pins the pattern semantics the
// characterization goldens forced: "{base}_test{ext}" is a substring check
// (foo_test.bar.go), the test_ prefix needs no extension, {ext} is never
// optional (a PyInstaller .spec is not a test), and only MightBeTest folds case.
func TestGenericMarkersKeepTheOldChecksExactly(t *testing.T) {
	for _, c := range []struct {
		rel        string
		is, mighty bool
	}{
		{"pkg/foo_test.bar.go", true, true},
		{"scripts/test_runner", true, true},
		{"packaging/server.spec", false, false},
		{"pkg/Test_Foo.py", false, true},
	} {
		if got := IsTest(nil, c.rel); got != c.is {
			t.Errorf("IsTest(%q) = %v, want %v", c.rel, got, c.is)
		}
		if got := MightBeTest(nil, c.rel); got != c.mighty {
			t.Errorf("MightBeTest(%q) = %v, want %v", c.rel, got, c.mighty)
		}
	}
}
