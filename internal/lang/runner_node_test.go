// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"slices"
	"testing"
)

func TestNodeParsesItsRealList(t *testing.T) {
	if got := nodeParseList(readFixture(t, "node-tap.txt")); !slices.Equal(got, fixtureJSIDs) {
		t.Errorf("nodeParseList = %q, want %q", got, fixtureJSIDs)
	}
	// A file that throws while loading is reported as a failed test named
	// after the file, with the process's exit code; it is not a test.
	if got := nodeParseList(readFixture(t, "node-tap-loadfail.txt")); got != nil {
		t.Errorf("a file that did not load listed %q", got)
	}
}

// A test with subtests (t.test) runs its own body to reach them, so neither
// the parent nor its subtests can be run alone and credited alone: both are
// listed, marked, and never resolved.
func TestNodeMarksSubtestsOfATestNotAlone(t *testing.T) {
	tap := `TAP version 13
# Subtest: parent
    # Subtest: child
    ok 1 - child
      ---
      type: 'test'
      ...
    1..1
ok 1 - parent
  ---
  type: 'test'
  ...
# Subtest: S
    # Subtest: has \# hash
    ok 1 - has \# hash
      ---
      type: 'test'
      ...
    1..1
ok 2 - S
  ---
  type: 'suite'
  ...
1..2
`
	ids := nodeParseList(tap)
	if want := []string{markNotAlone("parent::child"), markNotAlone("parent"), "S::has # hash"}; !slices.Equal(ids, want) {
		t.Errorf("nodeParseList = %q, want %q", ids, want)
	}
	js, _ := ByName("javascript")
	argv := []string{"node", "--test"}
	for _, name := range []string{"child", "parent"} {
		if id, ok := ResolveForRun(js, Cmd(argv), ids, name); ok {
			t.Errorf("%s resolved to %q; it cannot run alone", name, id)
		}
	}
	if id, ok := ResolveForRun(js, Cmd(argv), ids, "has # hash"); !ok || id != "S::has # hash" {
		t.Errorf("has # hash = %q, %v", id, ok)
	}
}

func TestNodeCoruns(t *testing.T) {
	ids := fixtureJSIDs // Calc::adds (a+b), Calc::adds, Calc::neg::adds, top level, Calc adds
	for _, c := range []struct {
		id   string
		runs []string
	}{
		{"Calc::adds", []string{"Calc adds"}},               // full-name equality
		{"Calc adds", []string{"Calc::adds"}},               // and back
		{"top level", nil},                                  // unique
		{"Calc::adds (a+b)", nil},                           // unique
		{"adds", []string{"Calc::adds", "Calc::neg::adds"}}, // a leaf name: measured, ^adds$ runs both
		{"Calc", ids[:3]},                                   // a suite name runs the suite
		{"neg", []string{"Calc::neg::adds"}},
	} {
		var got []string
		for _, o := range ids {
			if nodeCoruns(c.id, o) {
				got = append(got, o)
			}
		}
		if !slices.Equal(got, c.runs) {
			t.Errorf("nodeCoruns(%q) also runs %q, want %q", c.id, got, c.runs)
		}
	}
}

func TestNodeCommands(t *testing.T) {
	argv := []string{"node", "--experimental-strip-types", "--test"}
	if got, ok := nodeListCmd(argv, "a.test.ts"); !ok || !slices.Equal(got, []string{"node", "--experimental-strip-types", "--test", "--test-reporter=tap", "a.test.ts"}) {
		t.Errorf("list = %q, %v", got, ok)
	}
	if got, ok := nodeSingleCmd(argv, "a.test.ts", "Calc::neg::adds"); !ok || !slices.Equal(got, []string{"node", "--experimental-strip-types", "--test", "--test-name-pattern", `^Calc neg adds$`, "a.test.ts"}) {
		t.Errorf("single = %q, %v", got, ok)
	}
	for _, bad := range [][]string{{"node", "a.test.js"}, {"node", "--test", "--test-name-pattern=x"}, {"sh", "-c", "node --test"}} {
		if _, ok := nodeSingleCmd(bad, "a.js", "x"); ok {
			t.Errorf("single accepted %q", bad)
		}
	}
	if _, ok := nodeListCmd([]string{"node", "--test", "--test-reporter=spec"}, "a.js"); ok {
		t.Error("list accepted a command with its own reporter")
	}
}
