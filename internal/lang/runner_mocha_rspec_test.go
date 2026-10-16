// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"slices"
	"testing"
)

func TestMochaParsesItsRealList(t *testing.T) {
	want := []string{"top level", "Calc adds", "Calc::adds (a+b)", "Calc::adds", "Calc neg::adds"}
	if got := mochaParseList(readFixture(t, "mocha-list.json")); !slices.Equal(got, want) {
		t.Errorf("mochaParseList = %q, want %q", got, want)
	}
}

func TestMochaCommands(t *testing.T) {
	argv := []string{"npx", "mocha", "--reporter", "spec"}
	if got, ok := mochaListCmd(argv, "test/a.js"); !ok || !slices.Equal(got, []string{"npx", "mocha", "--reporter", "spec", "--dry-run", "--reporter", "json", "test/a.js"}) {
		t.Errorf("list = %q, %v", got, ok)
	}
	if got, ok := mochaSingleCmd(argv, "test/a.js", "Calc neg::adds"); !ok || !slices.Equal(got, []string{"npx", "mocha", "--reporter", "spec", "--grep", `^Calc neg adds$`, "test/a.js"}) {
		t.Errorf("single = %q, %v", got, ok)
	}
	for _, bad := range [][]string{{"mocha", "-g", "x"}, {"mocha", "--fgrep=x"}, {"mocha", "--invert"}, {"sh", "-c", "mocha"}} {
		if _, ok := mochaSingleCmd(bad, "a.js", "x"); ok {
			t.Errorf("single accepted %q", bad)
		}
	}
}

func TestRspecParsesItsRealList(t *testing.T) {
	want := []string{"Calc::adds (a+b)", "Calc::adds", "Calc::adds x", "Calc neg::adds", "Calc adds::x", "top::level"}
	out := "Bundler noise before the report\n" + readFixture(t, "rspec-list.json")
	if got := rspecParseList(out); !slices.Equal(got, want) {
		t.Errorf("rspecParseList = %q, want %q", got, want)
	}
}

func TestRspecCommands(t *testing.T) {
	argv := []string{"bundle", "exec", "rspec"}
	if got, ok := rspecListCmd(argv, "spec/a_spec.rb"); !ok || !slices.Equal(got, []string{"bundle", "exec", "rspec", "--dry-run", "--format", "json", "spec/a_spec.rb"}) {
		t.Errorf("list = %q, %v", got, ok)
	}
	if got, ok := rspecSingleCmd(argv, "spec/a_spec.rb", "Calc::adds (a+b)"); !ok || !slices.Equal(got, []string{"bundle", "exec", "rspec", "-E", `\ACalc adds \(a\+b\)\z`, "spec/a_spec.rb"}) {
		t.Errorf("single = %q, %v", got, ok)
	}
	for _, bad := range [][]string{{"rspec", "-e", "x"}, {"rspec", "--example-matches=x"}, {"sh", "-c", "rspec"}} {
		if _, ok := rspecSingleCmd(bad, "a_spec.rb", "x"); ok {
			t.Errorf("single accepted %q", bad)
		}
	}
}

// The fixture's `Calc > adds x` and `Calc adds > x` share a full name; the
// rspec row leaves both unchecked and resolves the rest.
func TestRspecRowRefusesTheCollision(t *testing.T) {
	rb, _ := ByName("ruby")
	argv := []string{"rspec"}
	ids := ParseTestList(rb, argv, readFixture(t, "rspec-list.json"))
	for _, name := range []string{"Calc::adds x", "Calc adds::x"} {
		if _, ok := ResolveForRun(rb, argv, ids, name); ok {
			t.Errorf("%s resolved; another example has its full name", name)
		}
	}
	if id, ok := ResolveForRun(rb, argv, ids, "level"); !ok || id != "top::level" {
		t.Errorf("level = %q, %v", id, ok)
	}
}
