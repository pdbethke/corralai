// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"slices"
	"testing"
)

func TestVitestParsesItsRealList(t *testing.T) {
	if got := vitestParseList(readFixture(t, "vitest-list.txt")); !slices.Equal(got, fixtureJSIDs) {
		t.Errorf("vitestParseList = %q, want %q", got, fixtureJSIDs)
	}
	if got, want := vitestParseList("a.test.ts > has::colons\nno separator here\n"), []string{markNotAlone("has::colons")}; !slices.Equal(got, want) {
		t.Errorf("parsed %q, want %q: a title with the id separator is listed but not runnable alone, and a line without vitest's lists nothing", got, want)
	}
}

func TestVitestCommands(t *testing.T) {
	for _, c := range []struct {
		argv, list, single []string
	}{
		{[]string{"npx", "vitest", "run"},
			[]string{"npx", "vitest", "list", "a.test.ts"},
			[]string{"npx", "vitest", "run", "-t", `^Calc adds$`, "a.test.ts"}},
		// a bare vitest would watch on a TTY; the single run is always `run`
		{[]string{"npx", "vitest", "--root", "web"},
			[]string{"npx", "vitest", "list", "--root", "web", "a.test.ts"},
			[]string{"npx", "vitest", "run", "--root", "web", "-t", `^Calc adds$`, "a.test.ts"}},
		{[]string{"npx", "vitest", "watch"},
			[]string{"npx", "vitest", "list", "a.test.ts"},
			[]string{"npx", "vitest", "run", "-t", `^Calc adds$`, "a.test.ts"}},
		// AGENTS.md's jail recipe: a host interpreter running vitest's entry script
		{[]string{"node", "./node_modules/vitest/vitest.mjs", "run"},
			[]string{"node", "./node_modules/vitest/vitest.mjs", "list", "a.test.ts"},
			[]string{"node", "./node_modules/vitest/vitest.mjs", "run", "-t", `^Calc adds$`, "a.test.ts"}},
	} {
		if got, ok := vitestListCmd(c.argv, "a.test.ts"); !ok || !slices.Equal(got, c.list) {
			t.Errorf("list(%q) = %q, %v; want %q", c.argv, got, ok, c.list)
		}
		if got, ok := vitestSingleCmd(c.argv, "a.test.ts", "Calc::adds"); !ok || !slices.Equal(got, c.single) {
			t.Errorf("single(%q) = %q, %v; want %q", c.argv, got, ok, c.single)
		}
	}
	for _, bad := range [][]string{{"npx", "vitest", "run", "-t", "x"}, {"sh", "-c", "npx vitest run"}, {"npx", "jest"}} {
		if _, ok := vitestListCmd(bad, "a.test.ts"); ok {
			t.Errorf("list accepted %q", bad)
		}
		if _, ok := vitestSingleCmd(bad, "a.test.ts", "x"); ok {
			t.Errorf("single accepted %q", bad)
		}
	}
}

func TestVitestEntryScriptIsTheVitestRow(t *testing.T) {
	ts, _ := ByName("typescript")
	if r, _ := RunnerFor(ts, []string{"node", "./node_modules/vitest/vitest.mjs", "run"}); r.Name != "vitest" {
		t.Errorf("vitest's entry script ran as %q", r.Name)
	}
}
