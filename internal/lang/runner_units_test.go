// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// unitFixture reads a captured unit listing (scripts/capture-runner-
// fixtures.sh): the stdout of ListUnitsCmd's own command, the working
// directory line included, with the capture directory written as /fixture.
func unitFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/runners/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// Each JavaScript row lists the files the operator's command collects, from
// the real runner's own listing, and runs one of them with every operator
// flag kept. mocha is the fourth JavaScript row and has no unit form; see
// TestMochaHasNoUnitForm.
func TestJSRunnersListAndRunOneUnit(t *testing.T) {
	for _, c := range []struct {
		row, lang, fixture string
		argv               []string
		want               []string
		wantRun            []string // UnitRunCmd for want[0]
	}{
		{
			"jest", "javascript", "jest-units.txt",
			[]string{"npx", "--no-install", "jest", "--ci", "units/jest"},
			[]string{"units/jest/a.jest.test.js", "units/jest/b.jest.test.js"},
			[]string{"npx", "--no-install", "jest", "--ci", "units/jest", "--runTestsByPath", "units/jest/a.jest.test.js", `--testPathIgnorePatterns=^(?!.*\/units\/jest\/a\.jest\.test\.js$)`},
		},
		{
			"vitest", "typescript", "vitest-units.txt",
			[]string{"npx", "--no-install", "vitest", "run", "--reporter=verbose", "units/vitest"},
			[]string{"units/vitest/a.vitest.test.mjs", "units/vitest/b.vitest.test.mjs"},
			[]string{"npx", "--no-install", "vitest", "run", "--reporter=verbose", "units/vitest/a.vitest.test.mjs"},
		},
		{
			"node --test", "javascript", "node-units.txt",
			[]string{"node", "--test", "--test-concurrency", "1", "units/node/a.node.test.js", "units/node/b.node.test.js"},
			[]string{"units/node/a.node.test.js", "units/node/b.node.test.js"},
			[]string{"node", "--test", "--test-concurrency", "1", "units/node/a.node.test.js"},
		},
	} {
		t.Run(c.row, func(t *testing.T) {
			p, _ := ByName(c.lang)
			inv := Cmd(c.argv)
			if r, _ := RunnerFor(p, inv); r.Name != c.row {
				t.Fatalf("%q dispatched to row %q", c.argv, r.Name)
			}
			if _, ok := ListUnitsCmd(p, inv); !ok {
				t.Fatal("no unit listing command")
			}
			if got := ParseUnitList(p, inv, unitFixture(t, c.fixture)); !slices.Equal(got, c.want) {
				t.Errorf("units = %q, want %q", got, c.want)
			}
			got, ok := UnitRunCmd(p, inv, c.want[0])
			if !ok || !slices.Equal(got, c.wantRun) {
				t.Errorf("UnitRunCmd = %q, %v, want %q", got, ok, c.wantRun)
			}
		})
	}
}

// The listing command is the row's own, run under a shell prelude that
// prints the working directory first: jest and vitest list absolute paths,
// and a unit is named relative to the repository the command runs in.
func TestListUnitsCmdReportsTheWorkingDirectory(t *testing.T) {
	js, _ := ByName("javascript")
	inv := Cmd([]string{"npx", "jest"})
	cmd, ok := ListUnitsCmd(js, inv)
	if !ok || len(cmd) < 4 || cmd[0] != "sh" || cmd[1] != "-c" || !slices.Equal(cmd[4:], []string{"npx", "jest", "--listTests", "--json"}) {
		t.Fatalf("ListUnitsCmd = %q, %v", cmd, ok)
	}
	if got := ParseUnitList(js, inv, `["/r/a.test.js"]`); got != nil {
		t.Errorf("a listing without the working-directory line = %q, want nil", got)
	}
	out := unitsCwdMarker + "/r\n" + `["/r/b.test.js","/r/a.test.js","/r/a.test.js","/elsewhere/c.test.js"]` + "\n"
	if got := ParseUnitList(js, inv, out); !slices.Equal(got, []string{"a.test.js", "b.test.js"}) {
		t.Errorf("units = %q: want sorted, deduplicated, repository-relative, none from outside it", got)
	}
}

// Every row that lists units can read its own listing.
func TestEveryUnitListingHasItsParser(t *testing.T) {
	for _, r := range runners {
		if r.ListUnits != nil && r.ParseUnits == nil {
			t.Errorf("%s lists units but has no ParseUnits", r.Name)
		}
	}
}

// A name filter in the operator's command is refused: one unit run under it
// would run less than the unit.
func TestUnitCmdRefusesANameFilter(t *testing.T) {
	js, _ := ByName("javascript")
	ts, _ := ByName("typescript")
	for _, c := range []struct {
		p    Plugin
		argv []string
	}{
		{ts, []string{"vitest", "run", "-t", "foo"}},
		{js, []string{"jest", "-t", "foo"}},
		{js, []string{"node", "--test", "--test-name-pattern=foo"}},
	} {
		inv := Cmd(c.argv)
		if cmd, ok := UnitRunCmd(c.p, inv, "a.test.js"); ok {
			t.Errorf("UnitRunCmd(%q) = %q, want refused", c.argv, cmd)
		}
		if cmd, ok := ListUnitsCmd(c.p, inv); ok {
			t.Errorf("ListUnitsCmd(%q) = %q, want refused", c.argv, cmd)
		}
	}
}

// UnitRunCmd refuses a command it cannot narrow to exactly one unit rather
// than build one that runs more or less.
func TestUnitCmdRefusesWhatItCannotNarrow(t *testing.T) {
	js, _ := ByName("javascript")
	ts, _ := ByName("typescript")
	for _, c := range []struct {
		why  string
		p    Plugin
		argv []string
	}{
		{"a bare word after a vitest flag may be its value or a filter", ts, []string{"vitest", "run", "--config", "v.config.ts"}},
		{"a node flag outside node's own table", js, []string{"node", "--test", "--made-up", "x.test.js"}},
		{"an option after node's first file goes to the test files", js, []string{"node", "--test", "a.test.js", "--foo"}},
		{"a shell-wrapped command", js, []string{"sh", "-c", "jest"}},
	} {
		if cmd, ok := UnitRunCmd(c.p, Cmd(c.argv), "a.test.js"); ok {
			t.Errorf("%s: UnitRunCmd(%q) = %q, want refused", c.why, c.argv, cmd)
		}
	}
}

// Through a package script, a unit run only appends to the script, so it
// passes through the launcher; a script whose own file arguments would have
// to be replaced, or one corral did not read, has no unit form.
func TestUnitCmdThroughAPackageScript(t *testing.T) {
	js, _ := ByName("javascript")
	jestScript := Invocation{Argv: []string{"npm", "test"}, Script: []string{"jest"}, Pass: "--", Narrow: true}
	got, ok := UnitRunCmd(js, jestScript, "a.test.js")
	if want := []string{"npm", "test", "--", "--runTestsByPath", "a.test.js", `--testPathIgnorePatterns=^(?!.*\/a\.test\.js$)`}; !ok || !slices.Equal(got, want) {
		t.Errorf("npm test → jest: %q, %v, want %q", got, ok, want)
	}
	nodeFiles := Invocation{Argv: []string{"npm", "test"}, Script: []string{"node", "--test", "test/"}, Pass: "--", Narrow: true}
	if got, ok := UnitRunCmd(js, nodeFiles, "test/a.test.js"); ok {
		t.Errorf("a script's own file arguments cannot be replaced from outside it: %q", got)
	}
	unread := Invocation{Argv: []string{"npm", "test"}, Script: []string{}}
	if got, ok := UnitRunCmd(js, unread, "a.test.js"); ok {
		t.Errorf("a script corral did not read: %q", got)
	}
}

// vitest selects files by a case-insensitive substring of their path below
// its root (measured on vitest 3.2.7: `vitest run --root units/collide
// units/collide/a.vitest.test.mjs` also ran xa.vitest.test.mjs), so a unit
// whose base name another listed unit contains cannot run alone: it is
// listed, marked, and refused.
func TestVitestUnitThatAnotherContainsIsNotAlone(t *testing.T) {
	ts, _ := ByName("typescript")
	inv := Cmd([]string{"vitest", "run", "units/collide"})
	got := ParseUnitList(ts, inv, unitFixture(t, "vitest-units-collide.txt"))
	if len(got) != 2 || UnitName(got[0]) != "units/collide/a.vitest.test.mjs" || got[0] == UnitName(got[0]) || got[1] != "units/collide/xa.vitest.test.mjs" {
		t.Fatalf("units = %q: want a marked as not alone, xa unmarked", got)
	}
	if cmd, ok := UnitRunCmd(ts, inv, got[0]); ok {
		t.Errorf("a unit that is not alone ran as %q", cmd)
	}
	if _, ok := UnitRunCmd(ts, inv, got[1]); !ok {
		t.Error("xa is contained in no other unit and runs alone")
	}
}

// node lists files only by running them under --test-only, where a file with
// no `only` test reports itself, by path, in place of its tests. A file
// that does carry an `only` test reports that test instead and its path is
// missing, so the listing is refused rather than read as complete.
func TestNodeUnitListingRefusesAFileThatReportsItsOwnTests(t *testing.T) {
	js, _ := ByName("javascript")
	inv := Cmd([]string{"node", "--test", "units/node-only/*.test.js", "units/node/*.test.js"})
	if got := ParseUnitList(js, inv, unitFixture(t, "node-units-only.txt")); got != nil {
		t.Errorf("units = %q, want nil: a file reported its own test", got)
	}
}

// mocha has no one-file form: a spec in .mocharc is ADDED to the files on
// the command line (measured on mocha 11.8.0, capture-runner-fixtures.sh
// mocha-config-spec-merges), so `mocha <file>` runs every configured spec too.
func TestMochaHasNoUnitForm(t *testing.T) {
	js, _ := ByName("javascript")
	r, _ := RunnerByID(js, "mocha")
	if r.ListUnits != nil || r.UnitCmd != nil {
		t.Error("mocha's file argument does not narrow a configured spec")
	}
}

// The node option table is node's own --help: an option that takes a value
// consumes the next word, any other does not. It is checked against the node
// on PATH when that is the version it was read from.
func TestNodeOptionTableMatchesNodeHelp(t *testing.T) {
	ver, err := exec.Command("node", "--version").Output()
	if err != nil {
		t.Skip("SKIP: node is not installed")
	}
	if !strings.HasPrefix(string(ver), nodeOptionsFrom) {
		t.Skipf("SKIP: the table was read from node %s, this is %s", nodeOptionsFrom, strings.TrimSpace(string(ver)))
	}
	help, err := exec.Command("node", "--help").Output()
	if err != nil {
		t.Fatal(err)
	}
	// Each option line is "  <names>  <description>", the names joined by
	// ", ", a value-taking one spelled "--name=..." (an optional value,
	// "--name[=...]", is not read from the next word).
	line := regexp.MustCompile(`^  (-[^ ].*?)(?:  |$)`)
	for _, l := range strings.Split(string(help), "\n") {
		m := line.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		var names []string
		for _, alias := range strings.Split(m[1], ", ") {
			if f := strings.Fields(alias); len(f) > 0 && f[0] != "-" && f[0] != "--" {
				names = append(names, f[0])
			}
		}
		head := strings.Join(names, " ")
		value := strings.Contains(head, "=") && !strings.Contains(head, "[=")
		for _, n := range names {
			n = strings.SplitN(strings.SplitN(n, "=", 2)[0], "[", 2)[0]
			if got, known := nodeOptionTakesValue(n); !known || got != value {
				t.Errorf("%s: table says value=%v known=%v, node --help says value=%v", n, got, known, value)
			}
		}
	}
}

// A launcher's banner before the report is skipped (npm prints "> script"
// lines on stdout), and output with no report line reads as nothing.
func TestDecodeArrayStartsAtTheReportLine(t *testing.T) {
	var got []string
	out := "> pkg@1 test\n> jest --listTests --json\n\n[\"/r/a.test.js\"]\n"
	if !decodeArray(out, &got) || !slices.Equal(got, []string{"/r/a.test.js"}) {
		t.Errorf("decodeArray = %q", got)
	}
	if decodeArray("> pkg@1 test\nNo tests found\n", &got) {
		t.Error("no report line, yet decoded")
	}
}

// A shard of one file can be empty, and an empty run passes (measured:
// capture-runner-fixtures.sh node-shard-of-one-file-is-empty), so each unit
// run drops the operator's shard; the listing, which applied it, keeps it.
func TestUnitCmdDropsTheShard(t *testing.T) {
	js, _ := ByName("javascript")
	ts, _ := ByName("typescript")
	for _, c := range []struct {
		p           Plugin
		argv, want  []string
		listHasFlag string
	}{
		{js, []string{"jest", "--shard=1/2"}, []string{"jest", "--runTestsByPath", "a.test.js", `--testPathIgnorePatterns=^(?!.*\/a\.test\.js$)`}, "--shard=1/2"},
		{js, []string{"jest", "--shard", "1/2", "--ci"}, []string{"jest", "--ci", "--runTestsByPath", "a.test.js", `--testPathIgnorePatterns=^(?!.*\/a\.test\.js$)`}, "--shard"},
		{ts, []string{"vitest", "run", "--shard=1/2"}, []string{"vitest", "run", "a.test.js"}, "--shard=1/2"},
		{ts, []string{"vitest", "run", "--shard", "1/2"}, []string{"vitest", "run", "a.test.js"}, "--shard"},
		{js, []string{"node", "--test", "--test-shard=2/2", "t/*.test.js"}, []string{"node", "--test", "a.test.js"}, "--test-shard=2/2"},
		{js, []string{"node", "--test", "--test-shard", "2/2", "t/*.test.js"}, []string{"node", "--test", "a.test.js"}, "--test-shard"},
	} {
		inv := Cmd(c.argv)
		if got, ok := UnitRunCmd(c.p, inv, "a.test.js"); !ok || !slices.Equal(got, c.want) {
			t.Errorf("UnitRunCmd(%q) = %q, %v, want %q", c.argv, got, ok, c.want)
		}
		if list, ok := ListUnitsCmd(c.p, inv); !ok || !slices.Contains(list, c.listHasFlag) {
			t.Errorf("ListUnitsCmd(%q) = %q, %v: the listing keeps the shard", c.argv, list, ok)
		}
	}
}

// A listed file whose path ends in /unit survives jest's unit ignore
// pattern, so that unit cannot run alone.
func TestJestUnitThatAnotherEndsWithIsNotAlone(t *testing.T) {
	js, _ := ByName("javascript")
	inv := Cmd([]string{"jest"})
	got := ParseUnitList(js, inv, unitsCwdMarker+"/r\n"+`["/r/a.test.js","/r/x/a.test.js"]`+"\n")
	if len(got) != 2 || got[0] == UnitName(got[0]) || got[1] != "x/a.test.js" {
		t.Errorf("units = %q: want a.test.js marked, x/a.test.js not", got)
	}
}

// Under a jest config with projects, the unit run names the unit by its path
// from the working directory and runs it (captured: a.* has two tests). A
// pattern anchored at <rootDir> matched nothing there and, under
// --passWithNoTests, passed (capture-runner-fixtures.sh
// jest-rootDir-pattern-under-projects).
func TestJestUnitRunsUnderProjects(t *testing.T) {
	out := unitFixture(t, "jest-unit-projects.txt")
	if !regexp.MustCompile(`(?m)^Tests:\s+2 passed, 2 total`).MatchString(out) || strings.Contains(out, "b.jest.test.js") {
		t.Errorf("the captured unit run did not run exactly p1/a.jest.test.js:\n%s", out)
	}
}

// No TAP header is no listing, never a listing of no files.
func TestNodeUnitListingWithoutTAPIsNotAListing(t *testing.T) {
	js, _ := ByName("javascript")
	inv := Cmd([]string{"node", "--test"})
	for _, out := range []string{"", "node: bad option: --made-up\n", "TAP version 13\n1..0\n# tests 0\n"} {
		got := ParseUnitList(js, inv, unitsCwdMarker+"/r\n"+out)
		if strings.HasPrefix(out, "TAP") {
			if got == nil || len(got) != 0 {
				t.Errorf("a TAP report of no files = %#v, want an empty list", got)
			}
		} else if got != nil {
			t.Errorf("%q read as %#v, want nil", out, got)
		}
	}
}
