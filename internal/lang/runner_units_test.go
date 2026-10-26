// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"maps"
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

// unitCase checks one row end to end over a captured listing: the command
// dispatches to the row, its listing reads as want, and the first unit (or
// run) runs as wantRun.
type unitCase struct {
	row, lang, fixture string
	argv               []string
	want               []string
	run                string // the unit UnitRunCmd is asked for; want[0] when empty
	wantRun            []string
}

func (c unitCase) check(t *testing.T) {
	t.Helper()
	p, _ := ByName(c.lang)
	inv := Cmd(c.argv)
	if r, _ := RunnerFor(p, inv); r.Name != c.row {
		t.Fatalf("%q dispatched to row %q, want %q", c.argv, r.Name, c.row)
	}
	if _, ok := ListUnitsCmd(p, inv); !ok {
		t.Fatalf("%q: no unit listing command", c.argv)
	}
	got := ParseUnitList(p, inv, unitFixture(t, c.fixture))
	if !slices.Equal(got, c.want) {
		t.Errorf("%s: units = %q, want %q", c.fixture, got, c.want)
	}
	unit := c.run
	if unit == "" {
		unit = c.want[0]
	}
	if cmd, ok := UnitRunCmd(p, inv, unit); !ok || !slices.Equal(cmd, c.wantRun) {
		t.Errorf("UnitRunCmd(%q, %q) = %q, %v, want %q", c.argv, unit, cmd, ok, c.wantRun)
	}
}

// listCmd is ListUnitsCmd without the working-directory prelude.
func listCmd(t *testing.T, lang string, argv []string) ([]string, bool) {
	t.Helper()
	p, _ := ByName(lang)
	cmd, ok := ListUnitsCmd(p, Cmd(argv))
	if !ok {
		return nil, false
	}
	return cmd[4:], true
}

// Go's units are the packages with test files that the operator's package
// patterns match, from go list under the operator's build context: the
// tagged package's only test file needs -tags extra (captured), and c has
// no test file. A pattern go cannot load is no listing, nor is no output.
func TestGoRowsListPackagesWithTests(t *testing.T) {
	ab := []string{"example.com/units/a", "example.com/units/b"}
	for _, c := range []unitCase{
		{row: "go test", lang: "go", fixture: "go-units.txt", argv: []string{"go", "test", "-count=1", "./..."}, want: ab,
			wantRun: []string{"go", "test", "-count=1", "example.com/units/a"}},
		{row: "go test", lang: "go", fixture: "go-units-tags.txt", argv: []string{"go", "test", "-tags", "extra", "./..."},
			want: append(slices.Clone(ab), "example.com/units/tagged"), run: "example.com/units/tagged",
			wantRun: []string{"go", "test", "-tags", "extra", "example.com/units/tagged"}},
		{row: "gotestsum", lang: "go", fixture: "go-units.txt", argv: []string{"gotestsum", "--format", "standard-verbose", "--", "-count=1", "./..."}, want: ab,
			wantRun: []string{"gotestsum", "--format", "standard-verbose", "--", "-count=1", "example.com/units/a"}},
	} {
		c.check(t)
	}
	tmpl := []string{"-e", "-f", goUnitTemplate}
	for _, c := range []struct {
		argv, want []string
	}{
		{[]string{"go", "test", "-race", "-tags", "extra", "-run", "^$"}, nil},
		{[]string{"go", "test", "-C", "sub", "-race", "-tags=extra", "-v", "-json", "-timeout", "5m", "./..."}, append(append([]string{"go", "list", "-C", "sub", "-race", "-tags=extra"}, tmpl...), "./...")},
		{[]string{"go", "test", "-v"}, append([]string{"go", "list"}, tmpl...)},
		{[]string{"gotestsum"}, append(append([]string{"go", "list"}, tmpl...), "./...")},
		{[]string{"gotestsum", "--format", "testname", "--", "-v"}, append([]string{"go", "list"}, tmpl...)},
		{[]string{"gotestsum", "--", "-mod=vendor", "./a", "./b"}, append(append([]string{"go", "list", "-mod=vendor"}, tmpl...), "./a", "./b")},
	} {
		got, ok := listCmd(t, "go", c.argv)
		if c.want == nil {
			if ok {
				t.Errorf("ListUnitsCmd(%q) = %q, want refused", c.argv, got)
			}
		} else if !ok || !slices.Equal(got, c.want) {
			t.Errorf("ListUnitsCmd(%q) = %q, %v, want %q", c.argv, got, ok, c.want)
		}
	}
	gp, _ := ByName("go")
	inv := Cmd([]string{"go", "test", "./..."})
	if got := ParseUnitList(gp, inv, unitFixture(t, "go-units-error.txt")); got != nil {
		t.Errorf("a pattern go could not load = %q, want nil", got)
	}
	if got := ParseUnitList(gp, inv, unitsCwdMarker+"/r\n"); got != nil {
		t.Errorf("no output = %#v, want nil: go printed nothing, it listed nothing", got)
	}
	if got := ParseUnitList(gp, inv, unitsCwdMarker+"/r\nnone example.com/m/c\n"); got == nil || len(got) != 0 {
		t.Errorf("packages without tests = %#v, want an empty list", got)
	}
}

// The one-package command is the operator's with only its package patterns
// replaced; a command under which a package could run no test (-run, -skip,
// -list, -c) or that go test reads otherwise (-args, file arguments, an
// unlisted option before a word) has no unit form.
func TestGoUnitCmdReplacesOnlyThePackagePatterns(t *testing.T) {
	gp, _ := ByName("go")
	pkg := "example.com/m/pkg"
	for _, c := range []struct{ argv, want []string }{
		{[]string{"go", "test", "-race", "./..."}, []string{"go", "test", "-race", pkg}},
		{[]string{"go", "test", "./a", "./b", "-count", "1"}, []string{"go", "test", pkg, "-count", "1"}},
		{[]string{"go", "test", "-v"}, []string{"go", "test", "-v", pkg}},
		{[]string{"go", "test", "--timeout=5m", "-test.v", "./..."}, []string{"go", "test", "--timeout=5m", "-test.v", pkg}},
		{[]string{"gotestsum"}, []string{"gotestsum", "--", pkg}},
		{[]string{"gotestsum", "-f", "testname", "--", "-race", "./..."}, []string{"gotestsum", "-f", "testname", "--", "-race", pkg}},
	} {
		if got, ok := UnitRunCmd(gp, Cmd(c.argv), pkg); !ok || !slices.Equal(got, c.want) {
			t.Errorf("UnitRunCmd(%q) = %q, %v, want %q", c.argv, got, ok, c.want)
		}
	}
	for _, argv := range [][]string{
		{"go", "test", "-run", "TestX", "./..."},
		{"go", "test", "-skip=TestX", "./..."},
		{"go", "test", "-list", ".", "./..."},
		{"go", "test", "-c", "./..."},
		{"go", "test", "./...", "-args", "-v"},
		{"go", "test", "a_test.go", "a.go"},
		{"go", "test", "-made-up", "x", "./..."},
		{"go", "test", "-timeout"},
		{"sh", "-c", "go test ./..."},
		{"gotestsum", "./a"},
		{"gotestsum", "--packages", "./a", "--", "-v"},
		{"gotestsum", "--watch"},
		{"gotestsum", "--raw-command", "--", "go", "test"},
		{"gotestsum", "--made-up", "x", "--", "./..."},
	} {
		if got, ok := UnitRunCmd(gp, Cmd(argv), pkg); ok {
			t.Errorf("UnitRunCmd(%q) = %q, want refused", argv, got)
		}
	}
	if got, ok := UnitRunCmd(gp, Cmd([]string{"go", "test", "./..."}), "./pkg"); ok {
		t.Errorf("a relative path is no listed import path: %q", got)
	}
}

// rspec's units are the files of the dry run's examples, once each; one
// unit is the operator's command with its own paths replaced (rspec runs
// every path it is given: capture rspec-paths-union).
func TestRspecUnitsFromDryRunJSON(t *testing.T) {
	unitCase{row: "rspec", lang: "ruby", fixture: "rspec-units.txt",
		argv:    []string{"ruby", "-S", "rspec", "units/rspec"},
		want:    []string{"units/rspec/spec/a_spec.rb", "units/rspec/spec/b_spec.rb"},
		wantRun: []string{"ruby", "-S", "rspec", "units/rspec/spec/a_spec.rb"}}.check(t)
	rb, _ := ByName("ruby")
	for _, c := range []struct{ argv, want []string }{
		{[]string{"bundle", "exec", "rspec", "--format", "progress", "-t", "~slow", "spec"}, []string{"bundle", "exec", "rspec", "--format", "progress", "-t", "~slow", "a_spec.rb"}},
		{[]string{"rspec", "spec/x", "--require", "spec_helper", "-Ilib"}, []string{"rspec", "--require", "spec_helper", "-Ilib", "a_spec.rb"}},
		{[]string{"rspec", "--no-profile", "spec"}, []string{"rspec", "--no-profile", "a_spec.rb"}},
	} {
		if got, ok := UnitRunCmd(rb, Cmd(c.argv), "a_spec.rb"); !ok || !slices.Equal(got, c.want) {
			t.Errorf("UnitRunCmd(%q) = %q, %v, want %q", c.argv, got, ok, c.want)
		}
	}
	for _, argv := range [][]string{
		{"rspec", "spec/a_spec.rb:12"},
		{"rspec", "spec/a_spec.rb[1:2]"},
		{"rspec", "-e", "adds"},
		{"rspec", "--made-up", "spec"},
		{"rspec", "--", "spec"},
		{"rspec", "-p", "--format", "json"},
		{"rspec", "-eadds"},
		{"rspec", "-Eadds"},
		{"rspec", "-bwE", "adds"},
	} {
		if got, ok := UnitRunCmd(rb, Cmd(argv), "a_spec.rb"); ok {
			t.Errorf("UnitRunCmd(%q) = %q, want refused", argv, got)
		}
		if got, ok := ListUnitsCmd(rb, Cmd(argv)); ok {
			t.Errorf("ListUnitsCmd(%q) = %q, want refused", argv, got)
		}
	}
	if got := ParseUnitList(rb, Cmd([]string{"rspec"}), unitsCwdMarker+"/r\n"+`{"version":"3.13.6"}`); got != nil {
		t.Errorf("a report without examples = %#v, want nil", got)
	}
}

// minitest has no listing command, so Ruby under minitest pairs by name;
// the row says so on the support page.
func TestMinitestHasNoUnitForm(t *testing.T) {
	rb, _ := ByName("ruby")
	r, _ := RunnerByID(rb, "minitest")
	if r.ListUnits != nil || r.UnitCmd != nil || !strings.Contains(r.Note, "no listing command; Ruby falls back to name pairing under minitest") {
		t.Errorf("minitest: units %v, note %q", r.ListUnits != nil, r.Note)
	}
}

// PHPUnit's units are its own --list-test-files, which applies the
// configuration, --testsuite and the group options (captured: under --group
// slow only ATest is listed). One unit replaces the operator's file
// arguments, since PHPUnit runs them all (capture phpunit-args-union).
func TestPHPUnitUnitsFromListing(t *testing.T) {
	conf := []string{"vendor/bin/phpunit", "-c", "units/phpunit/phpunit.xml"}
	for _, c := range []unitCase{
		{row: "phpunit", lang: "php", fixture: "phpunit-units.txt", argv: append(slices.Clone(conf), "--testsuite", "unit"),
			want:    []string{"units/phpunit/tests/ATest.php", "units/phpunit/tests/BTest.php"},
			wantRun: append(slices.Clone(conf), "--testsuite", "unit", "units/phpunit/tests/ATest.php")},
		{row: "phpunit", lang: "php", fixture: "phpunit-units-group.txt", argv: append(slices.Clone(conf), "--group", "slow", "units/phpunit"),
			want:    []string{"units/phpunit/tests/ATest.php"},
			wantRun: append(slices.Clone(conf), "--group", "slow", "units/phpunit/tests/ATest.php")},
	} {
		c.check(t)
	}
	php, _ := ByName("php")
	if got, ok := UnitRunCmd(php, Cmd([]string{"php", "-d", "memory_limit=1G", "vendor/bin/phpunit", "tests", "-dxdebug.mode=off", "--colors=never"}), "tests/ATest.php"); !ok ||
		!slices.Equal(got, []string{"php", "-d", "memory_limit=1G", "vendor/bin/phpunit", "-dxdebug.mode=off", "--colors=never", "tests/ATest.php"}) {
		t.Errorf("UnitRunCmd = %q, %v", got, ok)
	}
	for _, argv := range [][]string{
		{"phpunit", "--filter", "testAdds"},
		{"phpunit", "--made-up", "tests"},
		{"phpunit", "--", "tests"},
	} {
		if got, ok := UnitRunCmd(php, Cmd(argv), "tests/ATest.php"); ok {
			t.Errorf("UnitRunCmd(%q) = %q, want refused", argv, got)
		}
	}
	script := Invocation{Argv: []string{"composer", "test"}, Script: []string{"phpunit"}, Pass: "--", Narrow: true}
	if got, ok := UnitRunCmd(php, script, "tests/ATest.php"); !ok || !slices.Equal(got, []string{"composer", "test", "--", "tests/ATest.php"}) {
		t.Errorf("composer test → phpunit: %q, %v", got, ok)
	}
	inv := Cmd([]string{"phpunit"})
	for _, out := range []string{"", "PHPUnit 11.5.57 by Sebastian Bergmann and contributors.\n\nCannot open file \"x.xml\".\n"} {
		if got := ParseUnitList(php, inv, unitsCwdMarker+"/r\n"+out); got != nil {
			t.Errorf("%q = %#v, want nil: no file heading, no listing", out, got)
		}
	}
}

// cargo's units are the test targets `--no-run` builds, named by package
// and kind. Two packages' libs, and their tests/it.rs, share a target name
// that cargo selects in every package the command selects (capture
// cargo-target-name-across-packages), so each unit names its package and
// runs under `-p <package>` alone.
func TestCargoUnitsFromNoRunJSON(t *testing.T) {
	unitCase{row: "cargo test", lang: "rust", fixture: "cargo-units.txt",
		argv: []string{"cargo", "test", "-p", "units-a"}, want: []string{"units-a/bin:units-a", "units-a/lib", "units-a/test:it"}, run: "units-a/lib",
		wantRun: []string{"cargo", "test", "-p", "units-a", "--lib"}}.check(t)
	// The jail returns stdout and stderr as one stream, so cargo's progress
	// lines arrive between its JSON messages (indexmap, Task 11a: "could not
	// read the unit listing"). They are cargo's stderr, not a malformed
	// message, and the listing is the same.
	unitCase{row: "cargo test", lang: "rust", fixture: "cargo-units-jail.txt",
		argv: []string{"cargo", "test", "-p", "units-a"}, want: []string{"units-a/bin:units-a", "units-a/lib", "units-a/test:it"}, run: "units-a/lib",
		wantRun: []string{"cargo", "test", "-p", "units-a", "--lib"}}.check(t)
	unitCase{row: "cargo test", lang: "rust", fixture: "cargo-units-workspace.txt",
		argv:    []string{"cargo", "test", "--workspace"},
		want:    []string{"units-a/bin:units-a", "units-a/lib", "units-a/test:it", "units-b/lib", "units-b/test:it"},
		run:     "units-b/test:it",
		wantRun: []string{"cargo", "test", "-p", "units-b", "--test", "it"}}.check(t)
	rs, _ := ByName("rust")
	inv := Cmd([]string{"cargo", "test", "--workspace"})
	for _, out := range []string{
		"",
		`{"reason":"build-finished","success":false}`,
		`{"reason":"compiler-artifact","package_id":"p#x@1","target":{"kind":["lib"],"name":"x"},"profile":{"test":true},"executable":"/x"}`,
		`{"reason":"compiler-artifact","package_id":"what","target":{"kind":["lib"],"name":"x"},"profile":{"test":true},"executable":"/x"}` + "\n" + `{"reason":"build-finished","success":true}`,
		"error: could not find `Cargo.toml`",
	} {
		if got := ParseUnitList(rs, inv, unitsCwdMarker+"/r\n"+out+"\n"); got != nil {
			t.Errorf("%q = %#v, want nil: no successful build or no package name, no listing", out, got)
		}
	}
}

// A package id names its package three ways: `<src>#<name>@<ver>`, the
// same without the name when it is the path's last element (measured on
// cargo 1.90 for a package `same` in a directory `same`), and the older
// `<name> <ver> (<src>)`.
func TestCargoPackageName(t *testing.T) {
	for id, want := range map[string]string{
		"path+file:///w/a#units-a@0.1.0":                                    "units-a",
		"path+file:///w/same#0.1.0":                                         "same",
		"registry+https://github.com/rust-lang/crates.io-index#serde@1.0.0": "serde",
		"units-a 0.1.0 (path+file:///w/a)":                                  "units-a",
	} {
		if got, ok := cargoPackageName(id); !ok || got != want {
			t.Errorf("cargoPackageName(%q) = %q, %v, want %q", id, got, ok, want)
		}
	}
}

// One target is the operator's command with its own package and target
// selection replaced by the unit's (cargo runs every target selected, in
// every package selected: captures cargo-targets-union,
// cargo-workspace-plus-p-adds) and features, profile and test-binary
// arguments kept. A TESTNAME, a test-binary filter, --skip or --ignored
// could leave the target nothing to run, which passes, so such a command
// has no unit form.
func TestCargoUnitCmdKeepsPackageScope(t *testing.T) {
	rs, _ := ByName("rust")
	for _, c := range []struct {
		argv []string
		unit string
		want []string
	}{
		{[]string{"cargo", "test", "-p", "a"}, "a/test:it", []string{"cargo", "test", "-p", "a", "--test", "it"}},
		{[]string{"cargo", "+nightly", "test", "--workspace", "--exclude", "c", "--lib", "--tests", "-F", "x", "--", "--test-threads", "1"}, "b/bin:b",
			[]string{"cargo", "+nightly", "test", "-F", "x", "-p", "b", "--bin", "b", "--", "--test-threads", "1"}},
		{[]string{"cargo", "test", "-pa", "--package=b", "--all", "--test", "it", "--release", "--target-dir", "t", "--", "--nocapture"}, "b/lib",
			[]string{"cargo", "test", "--release", "--target-dir", "t", "-p", "b", "--lib", "--", "--nocapture"}},
	} {
		if got, ok := UnitRunCmd(rs, Cmd(c.argv), c.unit); !ok || !slices.Equal(got, c.want) {
			t.Errorf("UnitRunCmd(%q, %q) = %q, %v, want %q", c.argv, c.unit, got, ok, c.want)
		}
	}
	if got, ok := listCmd(t, "rust", []string{"cargo", "test", "-p", "a", "--message-format", "short", "--", "--test-threads=1"}); !ok ||
		!slices.Equal(got, []string{"cargo", "test", "-p", "a", "--no-run", "--message-format=json"}) {
		t.Errorf("listing = %q, %v", got, ok)
	}
	for _, argv := range [][]string{
		{"cargo", "test", "adds"},
		{"cargo", "test", "--", "adds"},
		{"cargo", "test", "--", "--skip", "slow"},
		{"cargo", "test", "--", "--skip=slow"},
		{"cargo", "test", "--", "--ignored"},
		{"cargo", "test", "--doc"},
		{"cargo", "test", "--no-run"},
		{"cargo", "build"},
		{"cargo", "test", "--made-up", "x"},
	} {
		if got, ok := UnitRunCmd(rs, Cmd(argv), "a/lib"); ok {
			t.Errorf("UnitRunCmd(%q) = %q, want refused", argv, got)
		}
	}
	for _, unit := range []string{"lib", "test:it", "a/example:x", "a/test:", "a/test:a b", "a/doc", "a b/lib"} {
		if got, ok := UnitRunCmd(rs, Cmd([]string{"cargo", "test"}), unit); ok {
			t.Errorf("unit %q ran as %q", unit, got)
		}
	}
}

// nextest's units are its test binaries with a test the run's filters
// match, by binary id, which is already package-qualified (`units-a::it`)
// and so unique across a workspace; one binary runs under
// `-E 'binary_id(=<id>)'`, which nextest ANDs with name filters (capture
// nextest-name-filter-and-binary) but ORs with another filterset
// (nextest-filtersets-union), so a command with its own -E, however
// written, has no unit form.
func TestNextestUnitsFromList(t *testing.T) {
	all := []string{"units-a", "units-a::bin/units-a", "units-a::it", "units-b", "units-b::it"}
	for _, c := range []unitCase{
		{row: "cargo nextest", lang: "rust", fixture: "nextest-units.txt", argv: []string{"cargo", "nextest", "run", "--workspace"}, want: all,
			wantRun: []string{"cargo", "nextest", "run", "--workspace", "-E", "binary_id(=units-a)"}},
		{row: "cargo nextest", lang: "rust", fixture: "nextest-units-filter.txt", argv: []string{"cargo", "nextest", "run", "--workspace", "integrates", "--", "--exact"},
			want:    []string{"units-a::it", "units-b::it"},
			wantRun: []string{"cargo", "nextest", "run", "--workspace", "integrates", "-E", "binary_id(=units-a::it)", "--", "--exact"}},
	} {
		c.check(t)
	}
	got, ok := listCmd(t, "rust", []string{"cargo", "nextest", "run", "--workspace", "--retries", "2", "--no-fail-fast", "-j4", "-P", "ci", "--test-threads", "1", "adds", "--", "--skip", "x"})
	if want := []string{"cargo", "nextest", "list", "--workspace", "-P", "ci", "adds", "--message-format", "json", "--", "--skip", "x"}; !ok || !slices.Equal(got, want) {
		t.Errorf("listing = %q, %v, want %q: list refuses run-only options", got, ok, want)
	}
	rs, _ := ByName("rust")
	for _, argv := range [][]string{
		{"cargo", "nextest", "run", "-E", "test(adds)"},
		{"cargo", "nextest", "run", "--filterset=test(adds)"},
		{"cargo", "nextest", "run", "-Etest(adds)"},
		{"cargo", "nextest", "run", "-rEtest(adds)"},
		{"cargo", "nextest", "run", "-R1234"},
		{"cargo", "nextest", "run", "--partition", "count:1/2"},
		{"cargo", "nextest", "run", "--no-run"},
		{"cargo", "nextest", "run", "--made-up", "x"},
	} {
		if got, ok := UnitRunCmd(rs, Cmd(argv), "units-a"); ok {
			t.Errorf("UnitRunCmd(%q) = %q, want refused", argv, got)
		}
		if got, ok := ListUnitsCmd(rs, Cmd(argv)); ok {
			t.Errorf("ListUnitsCmd(%q) = %q, want refused", argv, got)
		}
	}
	if got, ok := UnitRunCmd(rs, Cmd([]string{"cargo", "nextest", "run"}), "units-a) | all("); ok {
		t.Errorf("a unit that is no binary id ran as %q", got)
	}
	if got := ParseUnitList(rs, Cmd([]string{"cargo", "nextest", "run"}), unitsCwdMarker+"/r\nerror: no Cargo.toml\n"); got != nil {
		t.Errorf("no report = %#v, want nil", got)
	}
}

// helpOptions reads a runner's --help into option name → whether it takes
// its value from the next word. An option line is indented at most eight
// columns (descriptions sit deeper); its names, joined by ", " or "|", end
// at two spaces ("|" joins PHPUnit's `-c|--configuration`). A spelling after the last name decides for every name on
// the line: " ARG", " <arg>" or " [ARG]" reads the next word, while
// "[=ARG]", "=<arg>", "..." and pflag's " int[=2]" do not. Ruby's
// `--[no-]x` is both names, the negation reading no word.
func helpOptions(t *testing.T, help string) map[string]bool {
	t.Helper()
	line := regexp.MustCompile(`^(?: {1,8}|\t)(-[^ ].*?)(?:  |$)`)
	name := regexp.MustCompile(`^(--\[no-\][A-Za-z0-9][A-Za-z0-9-]*|--?[A-Za-z0-9][A-Za-z0-9_-]*)(.*)$`)
	out := map[string]bool{}
	for _, l := range strings.Split(help, "\n") {
		m := line.FindStringSubmatch(strings.TrimRight(l, " \r"))
		if m == nil {
			continue
		}
		var parts []string
		for _, p := range strings.Split(m[1], ", ") {
			parts = append(parts, strings.ReplaceAll(p, "|-", ", -"))
		}
		parts = strings.Split(strings.Join(parts, ", "), ", ")
		last := name.FindStringSubmatch(parts[len(parts)-1])
		if last == nil {
			continue
		}
		suffix := last[2]
		value := strings.HasPrefix(suffix, " ") && !regexp.MustCompile(`^ \w+\[=`).MatchString(suffix)
		for _, part := range parts {
			n := name.FindStringSubmatch(part)
			if n == nil {
				continue
			}
			if base, ok := strings.CutPrefix(n[1], "--[no-]"); ok {
				out["--"+base] = value
				out["--no-"+base] = false
				continue
			}
			out[n[1]] = value
		}
	}
	return out
}

// Every option table is its runner's own --help: each option listed with
// the right value-ness, and none the help does not print. The help files
// are the capture script's; Go's is go's own, read when it is the Go the
// table was read from. nextest's run-only options are exactly those `list`
// does not print, plus the run's --message-format.
func TestUnitOptionTablesMatchTheirHelp(t *testing.T) {
	check := func(name string, table optionTable, help map[string]bool, exceptions map[string]bool) {
		t.Helper()
		if len(help) < 5 {
			t.Fatalf("%s: read %d options from its help", name, len(help))
		}
		for opt, value := range help {
			if want, ok := exceptions[opt]; ok {
				value = want
			}
			if got, known := table.takesValue(opt); !known || got != value {
				t.Errorf("%s %s: table value=%v known=%v, help value=%v", name, opt, got, known, value)
			}
		}
		for _, set := range []map[string]bool{table.values, table.flags} {
			for opt := range set {
				if _, ok := help[opt]; !ok {
					t.Errorf("%s: the table lists %s, which its help does not", name, opt)
				}
			}
		}
	}
	read := func(f string) map[string]bool { return helpOptions(t, unitFixture(t, f)) }
	check("gotestsum", gotestsumOptions, read("gotestsum-help.txt"), nil)
	check("rspec", rspecOptions, read("rspec-help.txt"), nil)
	// `--help` prints --colors with a value PHPUnit reads only after "="
	// (capture phpunit-colors-takes-no-next-word), and the one line too long
	// for its column runs into the description after a single space.
	check("phpunit", phpunitOptions, read("phpunit-help.txt"), map[string]bool{"--colors": false, "--do-not-fail-on-phpunit-deprecation": false})
	check("cargo test", cargoTestOptions, read("cargo-test-help.txt"), nil)
	check("libtest", libtestOptions, read("libtest-help.txt"), nil)
	run, list := read("nextest-run-help.txt"), read("nextest-list-help.txt")
	check("nextest run", nextestRunOptions, run, nil)
	want := map[string]bool{"--message-format": true}
	for opt := range run {
		if _, ok := list[opt]; !ok {
			want[opt] = true
		}
	}
	if !maps.Equal(want, nextestRunOnly) {
		t.Errorf("nextestRunOnly = %v, want %v", slices.Sorted(maps.Keys(nextestRunOnly)), slices.Sorted(maps.Keys(want)))
	}

	ver, err := exec.Command("go", "env", "GOVERSION").Output()
	if err != nil || !strings.HasPrefix(string(ver), goOptionsFrom) {
		t.Skipf("SKIP go: the table was read from %s, this is %s", goOptionsFrom, strings.TrimSpace(string(ver)))
	}
	goHelp := map[string]bool{}
	for _, topic := range []string{"testflag", "build", "test"} {
		out, err := exec.Command("go", "help", topic).Output() // #nosec G204 -- a fixed go help topic
		if err != nil {
			t.Fatal(err)
		}
		for opt, value := range helpOptions(t, string(out)) {
			goHelp[strings.TrimPrefix(opt, "-")] = value
		}
	}
	check("go test", goTestOptions, goHelp, nil)
}

// The one-test listing drops the run-only options `cargo nextest list`
// refuses, as the unit listing does (capture
// nextest-list-refuses-every-run-only: list refused each one), and a
// one-test run refuses an -E written attached, which hasFlag misses and
// nextest would OR with the test's own.
func TestNextestTestListDropsRunOnlyOptions(t *testing.T) {
	rs, _ := ByName("rust")
	inv := Cmd([]string{"cargo", "nextest", "run", "--retries", "2", "--fail-fast", "--no-fail-fast", "--test-threads", "4", "-j4", "--no-capture", "-p", "x", "--run-ignored", "all", "--", "--nocapture"})
	got, ok := ListTestsCmd(rs, inv, "src/lib.rs")
	if want := []string{"cargo", "nextest", "list", "-p", "x", "--run-ignored", "all", "--message-format", "oneline"}; !ok || !slices.Equal(got, want) {
		t.Errorf("ListTestsCmd = %q, %v, want %q", got, ok, want)
	}
	for _, argv := range [][]string{{"cargo", "nextest", "run", "-Etest(x)"}, {"cargo", "nextest", "run", "-rEtest(x)"}} {
		if got, ok := SingleTestCmd(rs, Cmd(argv), "src/lib.rs", "tests::a"); ok {
			t.Errorf("SingleTestCmd(%q) = %q, want refused", argv, got)
		}
	}
	if got, ok := SingleTestCmd(rs, Cmd([]string{"cargo", "nextest", "run", "-r", "-p", "x"}), "src/lib.rs", "tests::a"); !ok || !slices.Contains(got, "test(=tests::a)") {
		t.Errorf("a bundle with no -E still runs one test: %q, %v", got, ok)
	}
}

// A name filter set inside a bundle of short options is a name filter
// (measured, capture-runner-fixtures.sh jest-bundled-short-t-filters,
// jest-it-filters, vitest-ut-filters), so every jest and vitest command that
// appends a selection refuses it: list, one test, salvage, units.
func TestBundledShortNameFilterIsRefused(t *testing.T) {
	js, _ := ByName("javascript")
	ts, _ := ByName("typescript")
	for _, c := range []struct {
		p    Plugin
		argv []string
	}{
		{js, []string{"npx", "jest", "-ct", "foo"}},
		{js, []string{"npx", "jest", "-it", "foo"}},
		{js, []string{"npx", "jest", "-tfoo"}},
		{js, []string{"node", "./node_modules/jest/bin/jest.js", "-bt", "foo"}},
		{ts, []string{"npx", "vitest", "run", "-ut", "foo"}},
		{ts, []string{"npx", "vitest", "run", "-tfoo"}},
	} {
		inv := Cmd(c.argv)
		if cmd, ok := ListTestsCmd(c.p, inv, "a.test.js"); ok {
			t.Errorf("ListTestsCmd(%q) = %q", c.argv, cmd)
		}
		if cmd, ok := SingleTestCmd(c.p, inv, "a.test.js", "x"); ok {
			t.Errorf("SingleTestCmd(%q) = %q", c.argv, cmd)
		}
		if cmd, ok := DeselectCmd(c.p, inv, []string{"x"}); ok {
			t.Errorf("DeselectCmd(%q) = %q", c.argv, cmd)
		}
		if cmd, ok := ListUnitsCmd(c.p, inv); ok {
			t.Errorf("ListUnitsCmd(%q) = %q", c.argv, cmd)
		}
		if cmd, ok := UnitRunCmd(c.p, inv, "a.test.js"); ok {
			t.Errorf("UnitRunCmd(%q) = %q", c.argv, cmd)
		}
	}
	// A bundle without the filter letter is no filter.
	if _, ok := UnitRunCmd(js, Cmd([]string{"npx", "jest", "-ci"}), "a.test.js"); !ok {
		t.Error("-ci sets no name filter")
	}
	if !strings.Contains(unitFixture(t, "jest-bundled-filter.txt"), "1 skipped, 1 passed") {
		t.Error("the captured -ct run should have filtered a's two tests to one")
	}
}

// THE LISTING MUST BIND THE TOOLCHAIN IT RUNS. The jail binds a command's
// toolchain from InterpretersIn, and ListUnitsCmd passes the listing as the
// prelude's positional parameters, which the script runs through `exec "$@"`.
// Reading only the script's words found printf and pwd, so a Go under ~/sdk or
// a rustup cargo was never bound and every jailed listing exited 127 with
// `exec: go: not found` (gin and indexmap, Task 11a) while the unit runs and
// the scan's graded runs beside it saw the same toolchain.
func TestListUnitsCmdNamesTheOperatorsInterpreter(t *testing.T) {
	for _, c := range []struct {
		lang string
		argv []string
		want string
	}{
		{"go", []string{"/home/u/sdk/go/bin/go", "test", "./..."}, "/home/u/sdk/go/bin/go"},
		{"rust", []string{"/home/u/.cargo/bin/cargo", "test"}, "/home/u/.cargo/bin/cargo"},
		{"javascript", []string{"/home/u/.nvm/versions/node/v22/bin/npx", "jest"}, "/home/u/.nvm/versions/node/v22/bin/npx"},
	} {
		p, _ := ByName(c.lang)
		cmd, ok := ListUnitsCmd(p, Cmd(c.argv))
		if !ok {
			t.Fatalf("%s: no listing for %q", c.lang, c.argv)
		}
		if got := InterpretersIn(cmd); !slices.Contains(got, c.want) {
			t.Errorf("%s: InterpretersIn(%q) = %q, want it to include %q", c.lang, cmd, got, c.want)
		}
	}
}

// A positional parameter is a program only where the script runs it: `"$@"`
// in command position. Anywhere else (an argument list, `set --`), the words
// after $0 are data, and binding their first word would mount whatever a
// caller's argument happened to name.
func TestInterpretersInReadsPositionalsOnlyWhereTheScriptRunsThem(t *testing.T) {
	for _, c := range []struct {
		argv []string
		want []string
	}{
		{[]string{"sh", "-c", `exec "$@"`, "x", "/opt/t/bin/tool", "a"}, []string{"/opt/t/bin/tool"}},
		{[]string{"sh", "-c", `printf ok && exec "$@"`, "x", "go", "list"}, []string{"printf", "go"}},
		{[]string{"sh", "-c", `echo "$@"`, "x", "/opt/t/bin/tool"}, []string{"echo"}},
		{[]string{"sh", "-c", `set -- "$@" -v; tool "$@"`, "x", "/opt/t/bin/other"}, []string{"set", "tool"}},
		{[]string{"sh", "-c", `exec "$@"`}, nil},
	} {
		if got := InterpretersIn(c.argv); !slices.Equal(got, c.want) {
			t.Errorf("InterpretersIn(%q) = %q, want %q", c.argv, got, c.want)
		}
	}
}

// A coverage wrapper names the operator's interpreter shell-quoted in its
// script (shellQuote: 'go'). The quotes are the shell's, not the program's
// name, so the interpreter is the word without them; read with them, the
// jail looked up a program called 'go' and bound nothing.
func TestInterpretersInReadsAQuotedProgramWithoutItsQuotes(t *testing.T) {
	for _, c := range []struct {
		argv []string
		want []string
	}{
		{[]string{"sh", "-c", `'/home/u/sdk/go/bin/go' 'test' './...'; rc=$?`}, []string{"/home/u/sdk/go/bin/go"}},
		{[]string{"sh", "-c", `f=$(mktemp) && 'go' 'test' x`}, []string{"mktemp", "go"}},
		{[]string{"sh", "-c", `"/opt/n/bin/node" x.js`}, []string{"/opt/n/bin/node"}},
		// The shell's own reading, now that the lexer honours quoting: it
		// was returned as written while the old lexer could only guess.
		{[]string{"sh", "-c", `'it'\''s' x`}, []string{"it's"}},
	} {
		if got := InterpretersIn(c.argv); !slices.Equal(got, c.want) {
			t.Errorf("InterpretersIn(%q) = %q, want %q", c.argv, got, c.want)
		}
	}
}

// A HEREDOC BODY IS DATA, NOT COMMANDS. Every JavaScript coverage command
// writes its reducer and preload through `cat > … <<'CORRAL_JS_EOF'`, and
// reading those bodies as shell made the jail look up every JS token as a
// program: once the preload contained `lineLengths(code)`, the word `code`
// resolved to the host's VS Code snap and every jailed unit of commander and
// jest failed "code resolves to a snap" (Task 11 B2 round 1).
func TestInterpretersInSkipsHeredocBodies(t *testing.T) {
	got := InterpretersIn([]string{"sh", "-c", "d=$(mktemp -d) && cat > \"$d/x.js\" <<'EOF'\ncode(1)\nconst f = 2\nEOF\ncat <<-\"T\"\n\tvim x\n\tT\nnode \"$d/x.js\""})
	if want := []string{"mktemp", "cat", "cat", "node"}; !slices.Equal(got, want) {
		t.Errorf("InterpretersIn = %q, want %q", got, want)
	}
	c, _ := jsPlugin{}.CoverageCmd([]string{"node", "--test"})
	words := InterpretersIn(c)
	for _, bad := range []string{"code", "const", "function", "return"} {
		if slices.Contains(words, bad) {
			t.Errorf("the JS coverage command's interpreters include %q, a word of its heredoc: %q", bad, words)
		}
	}
	if !slices.Contains(words, "node") {
		t.Errorf("the JS coverage command's interpreters lost node: %q", words)
	}
}

// ONLY A HEREDOC CORRAL CAN BE SURE OF IS SKIPPED (review I5). Skipping
// must never hide a command: a here-string, an arithmetic shift, `<<` inside
// a quoted string, a delimiter that is not a word, an unquoted heredoc
// (whose $(…) the shell runs), and a quoted heredoc whose closing line never
// comes all leave every following command visible. Only a quoted-delimiter
// heredoc whose closing line is found — the shape of every heredoc corral
// writes — is data.
func TestInterpretersInHidesNoCommandBehindAHeredocItCannotBeSureOf(t *testing.T) {
	for _, c := range []struct {
		name, script string
		want         []string
	}{
		{"here-string", "cat <<<foo\nnode t.js", []string{"cat", "node"}},
		{"arithmetic shift", "n=$((1<<4))\nnode t.js", []string{"node"}},
		{"quoted text", "echo \"a <<'X' b\"\nnode t.js", []string{"echo", "node"}},
		{"non-word delimiter", "cat <<END-OF\nx\nEND-OF\nnode t.js", []string{"cat", "x", "END-OF", "node"}},
		{"unquoted heredoc runs its substitutions", "cat <<EOF\n$(python3 gen.py)\nEOF\nnode t.js", []string{"cat", "python3", "EOF", "node"}},
		{"no closing line", "cat <<'EOF'\nnode t.js", []string{"cat", "node"}},
		{"quoted heredoc is data", "cat <<'EOF'\ncode(1)\nEOF\nnode t.js", []string{"cat", "node"}},
		{"dashed quoted heredoc is data", "cat <<-\"T\"\n\tvim x\n\tT\nnode t.js", []string{"cat", "node"}},
	} {
		got := InterpretersIn([]string{"sh", "-c", c.script})
		for _, w := range c.want {
			if !slices.Contains(got, w) {
				t.Errorf("%s: InterpretersIn = %q, missing %q", c.name, got, w)
			}
		}
		if c.name == "quoted heredoc is data" && slices.Contains(got, "code") || c.name == "dashed quoted heredoc is data" && slices.Contains(got, "vim") {
			t.Errorf("%s: InterpretersIn = %q read the heredoc body", c.name, got)
		}
	}
}

// Several quoted heredocs on one line are read in order: each body follows
// the previous one's delimiter line, closed by its own (review I6). The
// second match once indexed the FIRST heredoc's delimiter line and panicked
// ("index out of range [9] with length 1") on `cat <<'A' <<'B'`, an input
// that can come from an operator's own test command.
func TestInterpretersInReadsTwoHeredocsOnOneLineInOrder(t *testing.T) {
	got := InterpretersIn([]string{"sh", "-c", "cat <<'A' <<'B'\ncode a\nA\nvim b\nB\nnode t.js"})
	if want := []string{"cat", "node"}; !slices.Equal(got, want) {
		t.Errorf("InterpretersIn = %q, want %q: both bodies stripped, the command after both kept", got, want)
	}
}

// No heredoc line, however malformed, may panic: the script is an
// operator's, and a crash there takes corral down with it.
func TestWithoutHeredocBodiesNeverPanics(t *testing.T) {
	for _, s := range []string{
		"cat <<'A' <<'B'", "cat <<'A' <<'B'\nA", "cat <<'A' <<'B'\nA\nB", "<<'A'<<'B'<<'C'\nA\nB\nC\n",
		"<<''", "<<'", "<<\"\"\n\n", "x <<'A' y <<\"A\"\nA\nA", "<<<'A'\nA", "<<-'A'\n\tA\n<<'B'", "\n\n<<'A'", "<<'A'\n",
		"cat <<'A' <<'BB' <<'C'\nlong body line here\nA\nB\nBB\nC\nnode x", "a<<'A'b<<'B'c\nB\nA",
	} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("withoutHeredocBodies(%q) panicked: %v", s, r)
				}
			}()
			_ = withoutHeredocBodies(s)
			_ = InterpretersIn([]string{"sh", "-c", s})
		}()
	}
}

// THE JAIL BINDS WHAT THE OPERATOR'S ARGV NAMES, read whole (ruling R29,
// final review I3). Two negative controls, both measured on the reviewer's
// scratch copy before the fix:
//
//   - a JavaScript coverage command whose NODE_OPTIONS value holds spaces
//     and escaped quotes split into the junk program `}--require`, which
//     swallowed the operator's own quoted node path, so only the reducer's
//     bare `node` was bound and a node not on PATH was `not found`;
//   - a repository's test-file path `tests/x;/home/u/.local/bin/evil;.test.js`
//     put /home/u/.local/bin/evil in command position, and the jail looked
//     it up and asked to bind its toolchain root.
//
// Every coverage wrapper is checked, since every one embeds operator words.
func TestInterpretersInReadsCorralsOwnQuotingWhole(t *testing.T) {
	const node = "/home/u/.nvm/versions/node/v22/bin/node"
	const evil = "/home/u/.local/bin/evil"
	hostile := "tests/x;" + evil + ";.test.js"
	for _, c := range []struct {
		lang string
		argv []string
		want string
	}{
		{"javascript", []string{node, "--test", "a.test.js"}, node},
		{"typescript", []string{node, "--test", "a.test.ts"}, node},
		{"javascript", []string{"node", "--test", hostile}, "node"},
		{"typescript", []string{"vitest", "run", hostile}, "vitest"},
		{"ruby", []string{"/home/u/.rbenv/shims/rspec", hostile}, "/home/u/.rbenv/shims/rspec"},
		{"php", []string{"/home/u/php/bin/php", "vendor/bin/phpunit", hostile}, "/home/u/php/bin/php"},
		{"go", []string{"/home/u/sdk/go/bin/go", "test", "./" + hostile}, "/home/u/sdk/go/bin/go"},
		{"rust", []string{"/home/u/.cargo/bin/cargo", "test", "--test", hostile}, "/home/u/.cargo/bin/cargo"},
		{"python", []string{"/home/u/.venv/bin/python", "-m", "pytest", hostile}, "/home/u/.venv/bin/python"},
	} {
		p, _ := ByName(c.lang)
		cmd, ok := p.(CoverageReporter).CoverageCmd(c.argv)
		if !ok {
			t.Fatalf("%s CoverageCmd refused %q", c.lang, c.argv)
		}
		got := InterpretersIn(cmd)
		if !slices.Contains(got, c.want) {
			t.Errorf("%s %q: InterpretersIn = %q, missing the operator's %s", c.lang, c.argv, got, c.want)
		}
		for _, w := range got {
			if strings.Contains(w, "--require") || strings.Contains(w, "evil") || strings.Contains(w, ".test.") || strings.Contains(w, "'") || strings.Contains(w, `"`) {
				t.Errorf("%s %q: InterpretersIn names %q, which is no program the command runs (full: %q)", c.lang, c.argv, w, got)
			}
		}
	}
}

// The quote-aware lexer splits only where the shell does: inside '…', "…"
// and after a backslash a separator is text, and a command substitution —
// bare or inside double quotes — is read for the commands it runs.
func TestShellCommandWordsHonoursQuoting(t *testing.T) {
	for _, c := range []struct {
		script string
		want   []string
	}{
		{`'a;b' x; c`, []string{"a;b", "c"}},
		{`"a b" x && c`, []string{"a b", "c"}},
		{`a\;b x | c`, []string{"a;b", "c"}},
		{`X="1 2; evil" Y='3|4' run "$@"`, []string{"run"}},
		{`v="$(rustc -vV | sed -n p)/x" && go test`, []string{"rustc", "sed", "go"}},
		{`d=$(mktemp -d) && node "$d/r.js" 2>&1 >&2; cat x`, []string{"mktemp", "node", "cat"}},
		{`R="-r$d/x ${RUBYOPT:-}" ${RUNNER:-rspec a} b`, []string{"${RUNNER:-rspec a}"}},
		{`echo 'it'\''s'; 'it'\''s' x`, []string{"echo", "it's"}},
		{`# 'a comment; evil'` + "\nnode t.js", []string{"node"}},
		{`exec "$@"`, []string{"$@"}},
	} {
		if got := shellCommandWords(c.script); !slices.Equal(got, c.want) {
			t.Errorf("shellCommandWords(%q) = %q, want %q", c.script, got, c.want)
		}
	}
}
