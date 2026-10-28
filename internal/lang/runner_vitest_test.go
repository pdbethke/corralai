// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"slices"
	"strings"
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
	if r, _ := RunnerFor(ts, Cmd([]string{"node", "./node_modules/vitest/vitest.mjs", "run"})); r.Name != "vitest" {
		t.Errorf("vitest's entry script ran as %q", r.Name)
	}
}

// The unit evidence pass runs vitest in the threads pool: the forks pool
// SIGTERMs its workers, which then write no V8 coverage, and the pass would
// record whatever the main process and subprocesses wrote as a clean unit
// (measured 2026-10-27 on vitest 5.0.3). --pool=threads goes LAST and
// replaces the operator's own --pool in either spelling; every other flag,
// --poolOptions included, is left as the operator wrote it.
func TestVitestEvidenceArgsThreadsPool(t *testing.T) {
	ts, _ := ByName("typescript")
	for _, c := range []struct{ unit, want []string }{
		{[]string{"node", "./node_modules/vitest/vitest.mjs", "run", "tests/a.test.ts"},
			[]string{"node", "./node_modules/vitest/vitest.mjs", "run", "tests/a.test.ts", "--pool=threads"}},
		{[]string{"vitest", "run", "--pool=forks", "tests/a.test.ts"},
			[]string{"vitest", "run", "tests/a.test.ts", "--pool=threads"}},
		{[]string{"vitest", "run", "--pool", "forks", "tests/a.test.ts"},
			[]string{"vitest", "run", "tests/a.test.ts", "--pool=threads"}},
		{[]string{"vitest", "run", "--pool=threads", "tests/a.test.ts"},
			[]string{"vitest", "run", "tests/a.test.ts", "--pool=threads"}},
		{[]string{"vitest", "run", "--poolOptions.forks.singleFork", "tests/a.test.ts"},
			[]string{"vitest", "run", "--poolOptions.forks.singleFork", "tests/a.test.ts", "--pool=threads"}},
	} {
		inv := Cmd([]string{"vitest", "run"})
		if got := EvidenceUnitCmd(ts, inv, c.unit); !slices.Equal(got, c.want) {
			t.Errorf("EvidenceUnitCmd(%q) = %q, want %q", c.unit, got, c.want)
		}
	}
	if got := EvidenceNote(ts, Cmd([]string{"vitest", "run"})); !strings.Contains(got, "--pool=threads") {
		t.Errorf("EvidenceNote = %q, want it to name --pool=threads", got)
	}
	// Already threads: the pass changed nothing, and says nothing.
	if got := EvidenceNote(ts, Cmd([]string{"vitest", "run", "--pool=threads"})); got != "" {
		t.Errorf("EvidenceNote for a threads command = %q, want \"\"", got)
	}
	// A row without the field leaves the command alone and has no note.
	jest := []string{"jest", "tests/a.test.ts"}
	if got := EvidenceUnitCmd(ts, Cmd([]string{"jest"}), jest); !slices.Equal(got, jest) {
		t.Errorf("jest EvidenceUnitCmd = %q, want it unchanged", got)
	}
	if got := EvidenceNote(ts, Cmd([]string{"jest"})); got != "" {
		t.Errorf("jest EvidenceNote = %q, want \"\"", got)
	}
}
