// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"bufio"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// unitRows are the rows with a unit pass. A row joins this list when it
// gains ListUnits; TestEveryUnitRowRunsSeveralUnits fails until it does.
var unitRows = []string{"go-test", "gotestsum", "jest", "vitest", "node-test", "rspec", "phpunit", "cargo-nextest", "cargo-test"}

// Every row with a one-unit command has the multi-unit command and names the
// unit a test file belongs to; a row with no unit pass has none of the three,
// so the selection engine never builds a unit command from a row that cannot
// list what it would run.
func TestEveryUnitRowRunsSeveralUnits(t *testing.T) {
	for _, r := range runners {
		hasUnits := r.ListUnits != nil
		if hasUnits != slices.Contains(unitRows, r.ID) {
			t.Errorf("%s: ListUnits set = %v, but unitRows says %v", r.ID, hasUnits, !hasUnits)
		}
		if !hasUnits {
			if r.UnitsCmd != nil || r.UnitsCoruns != nil || r.UnitForFile != nil {
				t.Errorf("%s has no unit pass, yet sets a multi-unit field", r.ID)
			}
			continue
		}
		if r.UnitCmd == nil || r.UnitsCmd == nil || r.UnitForFile == nil {
			t.Errorf("%s: UnitCmd=%v UnitsCmd=%v UnitForFile=%v, want all three", r.ID, r.UnitCmd != nil, r.UnitsCmd != nil, r.UnitForFile != nil)
		}
		// A row whose one-unit run can collect another unit says so for
		// several units too (the converse need not hold: cargo's one-target
		// run is exact, its several-target run is not).
		if r.UnitCoruns != nil && r.UnitsCoruns == nil {
			t.Errorf("%s: a one-unit run can collect another unit, but UnitsCoruns is nil", r.ID)
		}
	}
}

// UnitsCmd with one unit is UnitCmd, refusals included, for every argv shape
// the one-unit tests already hold. Rather than copy those tables (and drift
// from them), the rows are swapped for spies whose UnitCmd also asks the real
// row's UnitsCmd, and the one-unit tests are run through them: every call
// they make is compared.
func TestUnitsCmdWithOneUnitEqualsUnitCmd(t *testing.T) {
	calls := map[string]int{}
	spies := slices.Clone(runners)
	for i := range spies {
		real := runners[i]
		if real.UnitCmd == nil {
			continue
		}
		spies[i].UnitCmd = func(argv []string, unit string) ([]string, bool) {
			one, okOne := real.UnitCmd(argv, unit)
			if real.UnitsCmd == nil {
				t.Errorf("%s: no UnitsCmd", real.ID)
				return one, okOne
			}
			many, okMany := real.UnitsCmd(argv, []string{unit})
			if okOne != okMany || !slices.Equal(one, many) {
				t.Errorf("%s: UnitCmd(%q, %q) = %q, %v but UnitsCmd(…, [unit]) = %q, %v", real.ID, argv, unit, one, okOne, many, okMany)
			}
			calls[real.ID]++
			return one, okOne
		}
	}
	withRunners(t, spies)
	for name, f := range map[string]func(*testing.T){
		"JSRunnersListAndRunOneUnit":              TestJSRunnersListAndRunOneUnit,
		"UnitCmdRefusesANameFilter":               TestUnitCmdRefusesANameFilter,
		"UnitCmdRefusesWhatItCannotNarrow":        TestUnitCmdRefusesWhatItCannotNarrow,
		"UnitCmdThroughAPackageScript":            TestUnitCmdThroughAPackageScript,
		"VitestUnitThatAnotherContainsIsNotAlone": TestVitestUnitThatAnotherContainsIsNotAlone,
		"UnitCmdDropsTheShard":                    TestUnitCmdDropsTheShard,
		"JestUnitThatAnotherEndsWithIsNotAlone":   TestJestUnitThatAnotherEndsWithIsNotAlone,
		"JestUnitRunsUnderProjects":               TestJestUnitRunsUnderProjects,
		"GoRowsListPackagesWithTests":             TestGoRowsListPackagesWithTests,
		"GoUnitCmdReplacesOnlyThePackagePatterns": TestGoUnitCmdReplacesOnlyThePackagePatterns,
		"RspecUnitsFromDryRunJSON":                TestRspecUnitsFromDryRunJSON,
		"PHPUnitUnitsFromListing":                 TestPHPUnitUnitsFromListing,
		"CargoUnitsFromNoRunJSON":                 TestCargoUnitsFromNoRunJSON,
		"CargoUnitCmdKeepsPackageScope":           TestCargoUnitCmdKeepsPackageScope,
		"NextestUnitsFromList":                    TestNextestUnitsFromList,
		"BundledShortNameFilterIsRefused":         TestBundledShortNameFilterIsRefused,
	} {
		t.Run(name, f)
	}
	for _, id := range unitRows {
		if calls[id] == 0 {
			t.Errorf("%s: no one-unit test reached its UnitCmd, so nothing compared it with UnitsCmd", id)
		}
	}
}

// Review Focus 3: an operator command with a name filter has no multi-unit
// form, as it has no one-unit form. The file then grades whole-suite with the
// refusal as its reason, never with the filter ORed into, or dropped from, a
// command corral built.
func TestUnitsCmdRefusesANameFilter(t *testing.T) {
	for _, c := range []struct {
		row   string
		argv  []string
		units []string
	}{
		{"go-test", []string{"go", "test", "-run", "X", "./..."}, []string{"example.com/m/a", "example.com/m/b"}},
		{"go-test", []string{"go", "test", "-skip=X", "./..."}, []string{"example.com/m/a", "example.com/m/b"}},
		{"gotestsum", []string{"gotestsum", "--", "-run", "X", "./..."}, []string{"example.com/m/a", "example.com/m/b"}},
		{"jest", []string{"jest", "-t", "foo"}, []string{"a.test.js", "b.test.js"}},
		{"jest", []string{"jest", "--testNamePattern=foo"}, []string{"a.test.js", "b.test.js"}},
		{"vitest", []string{"vitest", "-t", "foo"}, []string{"a.test.ts", "b.test.ts"}},
		{"vitest", []string{"vitest", "run", "--testNamePattern", "foo"}, []string{"a.test.ts", "b.test.ts"}},
		{"node-test", []string{"node", "--test", "--test-name-pattern=foo"}, []string{"a.test.js", "b.test.js"}},
		{"rspec", []string{"rspec", "-e", "foo"}, []string{"spec/a_spec.rb", "spec/b_spec.rb"}},
		{"phpunit", []string{"phpunit", "--filter", "foo"}, []string{"tests/ATest.php", "tests/BTest.php"}},
		{"cargo-test", []string{"cargo", "test", "--", "foo"}, []string{"a/lib", "b/lib"}},
		{"cargo-nextest", []string{"cargo", "nextest", "run", "-E", "test(foo)"}, []string{"a", "b"}},
	} {
		r := rowByID(t, c.row)
		if cmd, ok := r.UnitsCmd(c.argv, c.units); ok {
			t.Errorf("%s: UnitsCmd(%q, %q) = %q, want refused", c.row, c.argv, c.units, cmd)
		}
	}
}

// A unit list is a SET the caller chose: an empty list, an empty name, or a
// name twice is refused rather than read as "every unit" or run twice
// (PHPUnit runs a file named twice twice: capture phpunit-args-union).
func TestUnitsCmdRefusesAnythingButASet(t *testing.T) {
	argv := map[string][]string{
		"go-test": {"go", "test", "./..."}, "gotestsum": {"gotestsum"}, "jest": {"jest"}, "vitest": {"vitest", "run"},
		"node-test": {"node", "--test"}, "rspec": {"rspec"}, "phpunit": {"phpunit"},
		"cargo-test": {"cargo", "test"}, "cargo-nextest": {"cargo", "nextest", "run"},
	}
	unit := map[string]string{
		"go-test": "example.com/m/a", "gotestsum": "example.com/m/a", "jest": "a.test.js", "vitest": "a.test.ts",
		"node-test": "a.test.js", "rspec": "spec/a_spec.rb", "phpunit": "tests/ATest.php",
		"cargo-test": "a/lib", "cargo-nextest": "a",
	}
	for _, id := range unitRows {
		r := rowByID(t, id)
		if _, ok := r.UnitsCmd(argv[id], []string{unit[id]}); !ok {
			t.Errorf("%s: UnitsCmd(%q, [%q]) refused; the case below proves nothing", id, argv[id], unit[id])
		}
		for _, units := range [][]string{nil, {}, {""}, {unit[id], ""}, {unit[id], unit[id]}} {
			if cmd, ok := r.UnitsCmd(argv[id], units); ok {
				t.Errorf("%s: UnitsCmd(%q, %q) = %q, want refused", id, argv[id], units, cmd)
			}
		}
	}
}

// Each row's multi-unit command, as the real runner ran it over three units
// (a holds one test, b two, c four — capture *-units-multi*.txt): the
// command built for a and b is the one the capture ran, and the runner's own
// output shows a's and b's tests and none of c's. The count is read from the
// output, not reasoned: with weights 1, 2 and 4, a count of 3 is a and b
// exactly.
func TestUnitsCmdRunsExactlyTheNamedUnits(t *testing.T) {
	for _, c := range []struct {
		row, fixture    string
		units           []string
		count           func(string) int
		want            int
		present, absent []string
	}{
		{"go-test", "go-units-multi.txt", []string{"example.com/units/a", "example.com/units/b"}, goPassCount, 3,
			[]string{"--- PASS: TestAdd\n", "--- PASS: TestAddMore\n", "--- PASS: TestNeg\n"}, []string{"TestTwo"}},
		{"gotestsum", "gotestsum-units-multi.txt", []string{"example.com/units/a", "example.com/units/b"}, goPassCount, 3,
			[]string{"--- PASS: TestAdd\n", "--- PASS: TestAddMore\n", "--- PASS: TestNeg\n"}, []string{"TestTwo"}},
		{"jest", "jest-units-multi.txt", []string{"units/multi/jest/a.jest.test.js", "units/multi/jest/b.jest.test.js"}, jsTestsPassed, 3,
			[]string{"a one", "b one", "b two"}, []string{"c one", "c.jest.test.js"}},
		{"vitest", "vitest-units-multi.txt", []string{"units/multi/vitest/a.vitest.test.mjs", "units/multi/vitest/b.vitest.test.mjs"}, jsTestsPassed, 3,
			[]string{"a one", "b one", "b two"}, []string{"c one", "c.vitest.test.mjs"}},
		{"node-test", "node-units-multi.txt", []string{"units/multi/node/a.node.test.js", "units/multi/node/b.node.test.js"}, ran, 3,
			[]string{"a one", "b one", "b two"}, []string{"c one"}},
		{"rspec", "rspec-units-multi.txt", []string{"units/multi/rspec/spec/a_spec.rb", "units/multi/rspec/spec/b_spec.rb"}, ran, 3,
			[]string{"a one", "b one", "b two"}, []string{"Multi C", "c one"}},
		{"phpunit", "phpunit-units-multi.txt", []string{"units/multi/phpunit/tests/MultiATest.php", "units/multi/phpunit/tests/MultiBTest.php"}, ran, 3,
			[]string{"A one", "B one", "B two"}, []string{"Multi C", "C one"}},
		{"cargo-test", "cargo-units-multi.txt", []string{"units-a/lib", "units-b/lib"}, cargoPassCount, 3,
			[]string{"test tests::adds ... ok", "test tests::adds_more ... ok", "test tests::negates ... ok"}, []string{"integrates", "tests::runs"}},
		{"cargo-nextest", "nextest-units-multi.txt", []string{"units-a", "units-b::it"}, nextestPassCount, 3,
			[]string{"units-a tests::adds", "units-a tests::adds_more", "units-b::it integrates"}, []string{"units-a::it", "tests::negates", "tests::runs"}},
	} {
		t.Run(c.row, func(t *testing.T) {
			argv, ran, out := multiFixture(t, c.fixture)
			cmd, ok := rowByID(t, c.row).UnitsCmd(argv, c.units)
			if !ok {
				t.Fatalf("UnitsCmd(%q, %q) refused", argv, c.units)
			}
			if got := strings.Join(cmd, " "); got != ran {
				t.Fatalf("UnitsCmd built\n  %s\nbut the capture ran\n  %s", got, ran)
			}
			if n := c.count(out); n != c.want {
				t.Errorf("the run counted %d tests, want %d:\n%s", n, c.want, out)
			}
			for _, s := range c.present {
				if !strings.Contains(out, s) {
					t.Errorf("the run does not show %q:\n%s", s, out)
				}
			}
			for _, s := range c.absent {
				if strings.Contains(out, s) {
					t.Errorf("the run shows %q, a unit nobody named:\n%s", s, out)
				}
			}
		})
	}
}

// cargo test selects targets by name ACROSS the packages it is given: -p a -p
// b --lib --test it runs both packages' libs and both its, so the run of
// units-a/lib and units-b/test:it also runs units-a/test:it and units-b/lib
// (capture cargo-units-multi-coruns.txt: five tests, not two units' three).
// UnitsCoruns names exactly those, and nothing outside the cross product.
func TestCargoUnitsCorunTheirCrossProduct(t *testing.T) {
	argv, ran, out := multiFixture(t, "cargo-units-multi-coruns.txt")
	r := rowByID(t, "cargo-test")
	units := []string{"units-a/lib", "units-b/test:it"}
	cmd, ok := r.UnitsCmd(argv, units)
	if !ok || strings.Join(cmd, " ") != ran {
		t.Fatalf("UnitsCmd = %q, %v; the capture ran %s", cmd, ok, ran)
	}
	if n := cargoPassCount(out); n != 5 {
		t.Fatalf("the capture counted %d tests, want the measured 5:\n%s", n, out)
	}
	for other, want := range map[string]bool{
		"units-a/test:it": true, "units-b/lib": true,
		"units-a/bin:units-a": false, "units-a/lib": false, "units-b/test:it": false, "units-c/lib": false,
	} {
		if got := r.UnitsCoruns(units, other); got != want {
			t.Errorf("UnitsCoruns(%q, %q) = %v, want %v", units, other, got, want)
		}
	}
	for _, other := range []string{"units-a/bin:units-a", "units-b/test:it"} {
		if r.UnitsCoruns([]string{"units-a/lib", "units-b/lib"}, other) {
			t.Errorf("libs alone corun %s", other)
		}
	}
}

// jest's and vitest's one-unit coruns rules hold for several units: a listed
// file another unit's pattern also keeps is collected by the run.
func TestFileUnitsCorunWhatTheirPatternsKeep(t *testing.T) {
	jest := rowByID(t, "jest")
	units := []string{"a.test.js", "b.test.js"}
	for other, want := range map[string]bool{"x/a.test.js": true, "y/b.test.js": true, "c.test.js": false, "a.test.js": false} {
		if got := jest.UnitsCoruns(units, other); got != want {
			t.Errorf("jest UnitsCoruns(%q, %q) = %v, want %v", units, other, got, want)
		}
	}
	vitest := rowByID(t, "vitest")
	units = []string{"t/a.test.ts", "t/b.test.ts"}
	for other, want := range map[string]bool{"t/xa.test.ts": true, "u/XB.TEST.TS": true, "t/c.test.ts": false, "t/b.test.ts": false} {
		if got := vitest.UnitsCoruns(units, other); got != want {
			t.Errorf("vitest UnitsCoruns(%q, %q) = %v, want %v", units, other, got, want)
		}
	}
}

// Review Focus 2: the authored test is run in the unit it was written into,
// which the map may never have listed. A file-unit runner's unit is the file;
// a Go test file's is its directory's package, by the import path go list
// would print; a Rust tests/<name>.rs is that integration-test target, and a
// test written in a library source file is the library's unit — each in the
// spelling its row lists units in.
func TestUnitForFileNamesTheAuthoredUnit(t *testing.T) {
	gomod, _ := filepath.Abs("testdata/runners/src/units/gomod")
	cargo, _ := filepath.Abs("testdata/runners/src/units/cargo")
	for _, c := range []struct {
		row, root, file, want string
	}{
		{"jest", gomod, "units/jest/new.jest.test.js", "units/jest/new.jest.test.js"},
		{"vitest", gomod, "./t/new.test.ts", "t/new.test.ts"},
		{"node-test", gomod, "test/new.test.js", "test/new.test.js"},
		{"rspec", gomod, "spec/new_spec.rb", "spec/new_spec.rb"},
		{"phpunit", gomod, "tests/NewTest.php", "tests/NewTest.php"},
		// A new test file in an existing package, and in a package with no
		// test file yet (which no listing could have named).
		{"go-test", gomod, "a/new_test.go", "example.com/units/a"},
		{"go-test", gomod, "c/c_test.go", "example.com/units/c"},
		{"gotestsum", gomod, "a/new_test.go", "example.com/units/a"},
		{"cargo-test", cargo, "a/tests/new.rs", "units-a/test:new"},
		{"cargo-test", cargo, "b/tests/new/main.rs", "units-b/test:new"},
		{"cargo-test", cargo, "a/src/lib.rs", "units-a/lib"},
		{"cargo-nextest", cargo, "a/tests/new.rs", "units-a::new"},
		{"cargo-nextest", cargo, "b/src/lib.rs", "units-b"},
		// A module of a pure-library package is compiled into the lib.
		{"cargo-test", cargo, "b/src/extra.rs", "units-b/lib"},
		{"cargo-nextest", cargo, "b/src/extra.rs", "units-b"},
	} {
		got, ok := rowByID(t, c.row).UnitForFile(c.root, c.file)
		if !ok || got != c.want {
			t.Errorf("%s: UnitForFile(%q) = %q, %v, want %q", c.row, c.file, got, ok, c.want)
		}
	}
}

// A file the row cannot place in a unit it could run is refused, so the
// authored pass grades whole-suite with the reason rather than run the
// authored test in a unit that does not hold it.
func TestUnitForFileRefusesWhatNoUnitHolds(t *testing.T) {
	gomod, _ := filepath.Abs("testdata/runners/src/units/gomod")
	cargo, _ := filepath.Abs("testdata/runners/src/units/cargo")
	for _, c := range []struct {
		why, row, root, file string
	}{
		{"no file", "jest", gomod, ""},
		{"an absolute path", "jest", gomod, "/etc/new.test.js"},
		{"a path out of the repository", "rspec", gomod, "../spec/new_spec.rb"},
		{"no repository to read go.mod from", "go-test", "", "a/new_test.go"},
		{"not a Go test file", "go-test", gomod, "a/a.go"},
		{"no go.mod above it", "go-test", cargo, "a/new_test.go"},
		{"a binary's source is no lib", "cargo-test", cargo, "a/src/main.rs"},
		{"a bench is no unit", "cargo-test", cargo, "a/benches/new.rs"},
		{"no Cargo.toml package above it", "cargo-test", gomod, "a/tests/new.rs"},
		{"a nested tests dir is a module, not a target", "cargo-nextest", cargo, "a/tests/common/mod.rs"},
		{"no lib in the package", "cargo-test", cargo, "a/src/bin/x.rs"},
		// units-a has src/lib.rs AND src/main.rs: a module beside them may be
		// the binary's (`mod foo;` in main.rs), which --lib never compiles,
		// so its test would be silently uncollected.
		{"a module of a package that also has a binary", "cargo-test", cargo, "a/src/foo.rs"},
		{"a module of a package that also has a binary", "cargo-nextest", cargo, "a/src/foo.rs"},
	} {
		if got, ok := rowByID(t, c.row).UnitForFile(c.root, c.file); ok {
			t.Errorf("%s: %s UnitForFile(%q) = %q, want refused", c.why, c.row, c.file, got)
		}
	}
}

// A unit is appended as a bare word, so one that starts with "-" would be
// read as an option. Every file-unit row refuses it, as rspec and PHPUnit
// always did (fileUnitForFile does not refuse such a name).
func TestFileUnitsRefuseANameReadAsAnOption(t *testing.T) {
	for id, argv := range map[string][]string{
		"jest": {"jest"}, "vitest": {"vitest", "run"}, "node-test": {"node", "--test"},
		"rspec": {"rspec"}, "phpunit": {"phpunit"},
	} {
		r := rowByID(t, id)
		if _, ok := r.UnitsCmd(argv, []string{"a.test.js"}); !ok {
			t.Errorf("%s refused a plain unit; the case below proves nothing", id)
		}
		for _, units := range [][]string{{"-x.test.js"}, {"a.test.js", "--watch"}} {
			if cmd, ok := r.UnitsCmd(argv, units); ok {
				t.Errorf("%s: UnitsCmd(%q, %q) = %q, want refused", id, argv, units, cmd)
			}
		}
	}
}

// UnitsRunCmd is UnitRunCmd for several units: through the Invocation's
// shape (a package script), and refusing a unit ParseUnitList marked as one
// that cannot run alone.
func TestUnitsRunCmdShapesAndRefusesAMarkedUnit(t *testing.T) {
	js, _ := ByName("javascript")
	script := Invocation{Argv: []string{"npm", "test"}, Script: []string{"jest"}, Pass: "--", Narrow: true}
	got, ok := UnitsRunCmd(js, script, []string{"a.test.js", "b.test.js"})
	want := []string{"npm", "test", "--", "--runTestsByPath", "a.test.js", "b.test.js", `--testPathIgnorePatterns=^(?!.*(?:\/a\.test\.js|\/b\.test\.js)$)`}
	if !ok || !slices.Equal(got, want) {
		t.Errorf("UnitsRunCmd = %q, %v, want %q", got, ok, want)
	}
	if got, ok := UnitsRunCmd(js, Cmd([]string{"jest"}), []string{"a.test.js", markNotAlone("b.test.js")}); ok {
		t.Errorf("a unit marked not-alone was run: %q", got)
	}
}

func rowByID(t *testing.T, id string) Runner {
	t.Helper()
	for _, r := range runners {
		if r.ID == id {
			return r
		}
	}
	t.Fatalf("no row %q", id)
	return Runner{}
}

// multiFixture reads a capture of a multi-unit run: `# argv:` the operator's
// command, `$ ` the command the capture ran (each word space-joined; no word
// of these fixtures holds a space), then the runner's output.
func multiFixture(t *testing.T, name string) (argv []string, ran, out string) {
	t.Helper()
	text := unitFixture(t, name)
	sc := bufio.NewScanner(strings.NewReader(text))
	var head []string
	for len(head) < 2 && sc.Scan() {
		head = append(head, sc.Text())
	}
	a, okA := strings.CutPrefix(head[0], "# argv: ")
	r, okR := strings.CutPrefix(head[min(1, len(head)-1)], "$ ")
	if !okA || !okR {
		t.Fatalf("%s does not open with `# argv:` and `$ ` lines", name)
	}
	_, rest, _ := strings.Cut(text, "\n")
	_, rest, _ = strings.Cut(rest, "\n")
	return strings.Fields(a), r, rest
}

var (
	jsTests     = regexp.MustCompile(`(?m)^\s*Tests:?\s+(\d+) passed`)
	goPass      = regexp.MustCompile(`(?m)^\s*--- PASS`)
	cargoPassed = regexp.MustCompile(`test result: ok\. (\d+) passed`)
	nextestRun  = regexp.MustCompile(`Summary .* (\d+) tests? run: (\d+) passed`)
)

// jsTestsPassed reads jest's and vitest's tests line, not the files line
// above it that ran() would take first.
func jsTestsPassed(s string) int {
	m := jsTests.FindStringSubmatch(s)
	if m == nil {
		return -1
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func goPassCount(s string) int { return len(goPass.FindAllString(s, -1)) }

func cargoPassCount(s string) int {
	n := 0
	for _, m := range cargoPassed.FindAllStringSubmatch(s, -1) {
		k, _ := strconv.Atoi(m[1])
		n += k
	}
	return n
}

func nextestPassCount(s string) int {
	m := nextestRun.FindStringSubmatch(s)
	if m == nil || m[1] != m[2] {
		return -1
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

// A scan line counts a row's units in the row's own noun ("4 of 1,339 test
// files", "packages", "test targets"). There is no fallback noun to print, so
// a unit row without one must fail here, and a row with no unit pass must not
// carry one.
func TestEveryUnitRowNamesItsUnitNoun(t *testing.T) {
	for _, r := range runners {
		hasUnits := slices.Contains(unitRows, r.ID)
		if hasUnits && r.UnitNoun == "" {
			t.Errorf("%s has a unit pass but no UnitNoun: the scan line has no word for its units", r.ID)
		}
		if !hasUnits && r.UnitNoun != "" {
			t.Errorf("%s has no unit pass, yet names a UnitNoun %q", r.ID, r.UnitNoun)
		}
	}
	want := map[string]string{"go-test": "packages", "gotestsum": "packages", "jest": "test files", "vitest": "test files",
		"node-test": "test files", "rspec": "test files", "phpunit": "test files", "cargo-nextest": "test targets", "cargo-test": "test targets"}
	for _, r := range runners {
		if n, ok := want[r.ID]; ok && r.UnitNoun != n {
			t.Errorf("%s UnitNoun = %q, want %q", r.ID, r.UnitNoun, n)
		}
	}
}
