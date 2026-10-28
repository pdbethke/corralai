// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"io/fs"
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
	// The note says what the pass changed in the operator's command, by
	// meaning: a replaced pool is named, and the operator's own threads
	// pool, in either spelling, is no change at all.
	for _, c := range []struct {
		argv []string
		want string
	}{
		{[]string{"vitest", "run"}, "the evidence pass runs each unit with --pool=threads"},
		{[]string{"vitest", "run", "--pool=forks"}, "the evidence pass runs each unit with --pool=threads in place of --pool=forks"},
		{[]string{"vitest", "run", "--pool", "forks"}, "the evidence pass runs each unit with --pool=threads in place of --pool forks"},
		{[]string{"vitest", "run", "--pool=threads"}, ""},
		{[]string{"vitest", "run", "--pool", "threads"}, ""},
	} {
		if got := EvidenceNote(ts, Cmd(c.argv)); got != c.want {
			t.Errorf("EvidenceNote(%q) = %q, want %q", c.argv, got, c.want)
		}
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

// The unit evidence pass shares one vitest transform cache among its
// units: --fsModuleCache --fsModuleCachePath <dir>, a pair of options
// vitest takes on its command line only from 5.0 (4.1 has them under
// `experimental` alone). Whether the installed vitest takes them is read
// from its package.json — the one beside the entry script the command
// runs, else node_modules/vitest's — never learned by running it and
// reading an error. A version below the floor, an unreadable file or one
// with no version gives no cache, and the reason names what was read.
func TestVitestEvidenceCache(t *testing.T) {
	ts, _ := ByName("typescript")
	c := EvidenceCacheFor(ts, Cmd([]string{"vitest", "run"}))
	if c == nil || c.Env != "" || c.Args == nil || c.Unsupported == nil || c.Name == "" {
		t.Fatalf("EvidenceCacheFor(vitest) = %+v, want a named cache with Args and a version gate, no Env", c)
	}
	if got, want := c.Args("/corral-cache"), []string{"--fsModuleCache", "--fsModuleCachePath", "/corral-cache"}; !slices.Equal(got, want) {
		t.Errorf("Args = %q, want %q", got, want)
	}
	pkg := func(version string) string { return `{"name":"vitest","version":"` + version + `","type":"module"}` }
	reader := func(files map[string]string, asked *[]string) func(string) ([]byte, error) {
		return func(rel string) ([]byte, error) {
			*asked = append(*asked, rel)
			s, ok := files[rel]
			if !ok {
				return nil, fs.ErrNotExist
			}
			return []byte(s), nil
		}
	}
	const root = "node_modules/vitest/package.json"
	for _, k := range []struct {
		name    string
		argv    []string
		files   map[string]string
		path    string
		wantWhy string // "" = the cache is used; else a substring of the reason
	}{
		{"floor", []string{"vitest", "run"}, map[string]string{root: pkg("5.0.3")}, root, ""},
		{"above", []string{"vitest", "run"}, map[string]string{root: pkg("5.1.0")}, root, ""},
		{"next major", []string{"vitest", "run"}, map[string]string{root: pkg("6.0.0")}, root, ""},
		{"entry script", []string{"node", "./node_modules/vitest/vitest.mjs", "run"}, map[string]string{root: pkg("5.0.4")}, root, ""},
		{"nested entry script", []string{"node", "packages/a/node_modules/vitest/vitest.mjs", "run"},
			map[string]string{"packages/a/node_modules/vitest/package.json": pkg("5.0.3"), root: pkg("4.1.5")}, "packages/a/node_modules/vitest/package.json", ""},
		{"4.1 has it only under experimental", []string{"vitest", "run"}, map[string]string{root: pkg("4.1.5")}, root, "vitest 4.1.5 (" + root + ") is older than 5.0.3"},
		{"a prerelease of the floor", []string{"vitest", "run"}, map[string]string{root: pkg("5.0.3-beta.1")}, root, "vitest 5.0.3-beta.1"},
		{"unreadable", []string{"vitest", "run"}, map[string]string{}, root, "could not read " + root},
		{"no version", []string{"vitest", "run"}, map[string]string{root: `{"name":"vitest"}`}, root, root + " names no version"},
		{"not a version", []string{"vitest", "run"}, map[string]string{root: pkg("next")}, root, `version "next"`},
		{"not json", []string{"vitest", "run"}, map[string]string{root: "nope"}, root, "could not parse " + root},
		{"absolute entry script", []string{"node", "/opt/vitest/node_modules/vitest/vitest.mjs", "run"}, map[string]string{root: pkg("5.0.3")}, "", "outside the tree"},
	} {
		t.Run(k.name, func(t *testing.T) {
			var asked []string
			why := c.Unsupported(k.argv, reader(k.files, &asked))
			switch {
			case k.wantWhy == "" && why != "":
				t.Errorf("Unsupported = %q, want the cache used", why)
			case k.wantWhy != "" && !strings.Contains(why, k.wantWhy):
				t.Errorf("Unsupported = %q, want it to say %q", why, k.wantWhy)
			}
			if k.path != "" && (len(asked) != 1 || asked[0] != k.path) {
				t.Errorf("read %q, want only %q", asked, k.path)
			}
			if k.path == "" && len(asked) != 0 {
				t.Errorf("read %q, want nothing read", asked)
			}
		})
	}
	// A row with a cache but no gate (go) and a row with no cache (jest).
	g, _ := ByName("go")
	if gc := EvidenceCacheFor(g, Cmd([]string{"go", "test", "./..."})); gc == nil || gc.Unsupported != nil {
		t.Errorf("go cache = %+v, want one with no version gate", gc)
	}
}
