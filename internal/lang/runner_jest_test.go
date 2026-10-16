// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"slices"
	"testing"
)

// The ids the shared fixture suite (testdata/runners/src) lists, in the
// order every JS runner reported them: titles from the outermost describe
// in, joined by "::".
var fixtureJSIDs = []string{"Calc::adds (a+b)", "Calc::adds", "Calc::neg::adds", "top level", "Calc adds"}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/runners/" + name) // #nosec G304 -- test fixture under testdata
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestJestParsesItsRealList(t *testing.T) {
	if got := jestParseList(readFixture(t, "jest-list.json")); !slices.Equal(got, fixtureJSIDs) {
		t.Errorf("jestParseList = %q, want %q", got, fixtureJSIDs)
	}
	if got := jestParseList("FAIL not json"); got != nil {
		t.Errorf("non-JSON output parsed to %q", got)
	}
}

func TestJestCommands(t *testing.T) {
	argv := []string{"npx", "jest"}
	list, ok := jestListCmd(argv, "src/calc.test.js")
	if want := []string{"npx", "jest", "--json", "--testNamePattern", `^\b$`, "src/calc.test.js"}; !ok || !slices.Equal(list, want) {
		t.Errorf("list = %q, %v; want %q", list, ok, want)
	}
	one, ok := jestSingleCmd(argv, "src/calc.test.js", "Calc::adds (a+b)")
	if want := []string{"npx", "jest", "--testNamePattern", `^Calc adds \(a\+b\)$`, "src/calc.test.js"}; !ok || !slices.Equal(one, want) {
		t.Errorf("single = %q, %v; want %q", one, ok, want)
	}
	for _, bad := range [][]string{{"npx", "jest", "-t", "x"}, {"npx", "jest", "--testNamePattern=x"}, {"sh", "-c", "npx jest"}, nil} {
		if _, ok := jestListCmd(bad, "a.test.js"); ok {
			t.Errorf("list accepted %q", bad)
		}
		if _, ok := jestSingleCmd(bad, "a.test.js", "x"); ok {
			t.Errorf("single accepted %q", bad)
		}
	}
	if _, ok := jestSingleCmd(argv, "a.test.js", ""); ok {
		t.Error("single accepted an empty id")
	}
}

// The jest row is what dispatch finds for a jest command, in both JS
// languages, and it refuses the fixture's colliding full name.
func TestJestRowDispatch(t *testing.T) {
	for _, l := range []string{"javascript", "typescript"} {
		p, _ := ByName(l)
		argv := []string{"npx", "jest"}
		if !ResolvesByList(p, Cmd(argv)) {
			t.Errorf("%s: jest does not resolve by list", l)
		}
		ids := ParseTestList(p, Cmd(argv), readFixture(t, "jest-list.json"))
		if _, ok := ResolveForRun(p, Cmd(argv), ids, "Calc::adds"); ok {
			t.Errorf("%s: Calc::adds resolved; its pattern also runs the top-level \"Calc adds\"", l)
		}
		if id, ok := ResolveForRun(p, Cmd(argv), ids, "top level"); !ok || id != "top level" {
			t.Errorf("%s: top level = %q, %v", l, id, ok)
		}
	}
}

// jest builds its name filter case-insensitively (jest-circus: new
// RegExp(pattern, 'i')); measured on jest 30.5, ^Calc adds$ ran both
// "Calc adds" and "calc adds". So two names differing only in case corun.
func TestJestCorunsIgnoresCase(t *testing.T) {
	js, _ := ByName("javascript")
	argv := []string{"npx", "jest"}
	if _, ok := ResolveForRun(js, Cmd(argv), []string{"Calc adds", "calc adds"}, "Calc adds"); ok {
		t.Error("Calc adds resolved; jest's -t would also run calc adds")
	}
	vt, _ := ByName("typescript")
	if _, ok := ResolveForRun(vt, Cmd([]string{"npx", "vitest", "run"}), []string{"Calc adds", "calc adds"}, "Calc adds"); !ok {
		t.Error("vitest matches case-sensitively; Calc adds should resolve")
	}
}
