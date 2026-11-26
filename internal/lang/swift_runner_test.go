// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Every assertion below reads a capture of the real `swift test` (Swift 6.3.2,
// testdata/runners/swift-*.txt, PROVENANCE.txt), from the fixture package in
// testdata/runners/src/swift, never output typed here.

// swiftRan is the number of tests a captured run executed, from its own
// summaries: every swift-testing "Test run with N tests" plus every XCTest
// run's total, the "Executed N tests" that closes its outermost suite ('All
// tests' or 'Selected tests'; the suites nested in it repeat their part of
// the total). Swift 6.4.2 under swiftbuild runs each test product on its own
// and prints one XCTest and one swift-testing summary for each, so a run can
// hold several of each and they are summed (TestSwiftRanSumsEveryProductsRun).
// It is the count the capture script's swiftran reads.
func swiftRan(t *testing.T, out string) int {
	t.Helper()
	swiftTesting, xctest, depth, outermost := 0, 0, 0, false
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if _, rest, ok := strings.Cut(line, "Test run with "); ok {
			n, err := strconv.Atoi(strings.Fields(rest)[0])
			if err != nil {
				t.Fatalf("unreadable swift-testing summary %q", line)
			}
			swiftTesting += n
		}
		if strings.HasPrefix(trimmed, "Test Suite '") {
			switch {
			case strings.Contains(trimmed, "' started at "):
				depth++
			case strings.Contains(trimmed, "' passed at "), strings.Contains(trimmed, "' failed at "):
				depth--
				outermost = depth == 0
			}
			continue
		}
		if rest, ok := strings.CutPrefix(trimmed, "Executed "); ok {
			n, err := strconv.Atoi(strings.Fields(rest)[0])
			if err != nil {
				t.Fatalf("unreadable XCTest summary %q", line)
			}
			if outermost {
				xctest += n
			}
			outermost = false
		}
	}
	return swiftTesting + xctest
}

// SWIFT 6.4.2 PRINTS A SUMMARY PER TEST PRODUCT. Under swiftbuild each test
// target is its own product, run on its own, and each run prints its own
// XCTest and swift-testing summaries; reading only the last of each counted 5
// where 8 ran (scripts/capture-runner-fixtures.sh --swift-only through
// swift:6.4.2-resolute, 2026-11-20; those captures are not committed, the
// committed ones are 6.3.2's). Two committed runs back to back have that
// shape: each run's summaries count once, and the totals add.
func TestSwiftRanSumsEveryProductsRun(t *testing.T) {
	_, _, one := multiFixture(t, "swift-units-one.txt")
	_, _, multi := multiFixture(t, "swift-units-multi.txt")
	if n := swiftRan(t, one+multi); n != 3+8 {
		t.Errorf("two runs back to back executed %d tests, want 3 + 8", n)
	}
	if n := swiftRan(t, multi+one); n != 8+3 {
		t.Errorf("two runs back to back executed %d tests, want 8 + 3", n)
	}
}

// The listing names every test of both frameworks, in the forms `swift test
// list` prints them, and nothing else.
func TestSwiftParsesItsRealList(t *testing.T) {
	want := []string{
		"CalcXCTests.AdderTests/testAdds",
		"CalcXCTests.AdderTests/testAddsTwice",
		"CalcXCTests.AdderTests/testFails",
		"CalcTests.Arithmetic/adds()",
		"CalcTests.Arithmetic/doubles(value:)",
		"CalcTests.Arithmetic/fails()",
		"CalcTests.parses()",
		"CalcTests.parsesTwice()",
	}
	if got := swiftParseList(readFixture(t, "swift-list.txt")); !slices.Equal(got, want) {
		t.Errorf("swiftParseList = %q, want %q", got, want)
	}
	// A run's output (build lines, both frameworks' reports, a compiler
	// error) holds no listed id, so none is read from it.
	for _, f := range []string{"swift-pass.txt", "swift-fail-testing.txt", "swift-fail-xctest.txt", "swift-build-fail.txt"} {
		if got := swiftParseList(readFixture(t, f)); len(got) != 0 {
			t.Errorf("swiftParseList(%s) = %q, want no ids", f, got)
		}
	}
}

func TestSwiftListCmd(t *testing.T) {
	r := rowByID(t, "swift-test")
	for _, c := range []struct {
		argv []string
		want []string
	}{
		{[]string{"swift", "test"}, []string{"swift", "test", "list"}},
		{[]string{"swift", "test", "--parallel"}, []string{"swift", "test", "--parallel", "list"}},
		{[]string{"/usr/bin/swift", "test", "-c", "release"}, []string{"/usr/bin/swift", "test", "-c", "release", "list"}},
	} {
		got, ok := r.ListCmd(c.argv, "Tests/CalcTests/CalcTests.swift")
		if !ok || !slices.Equal(got, c.want) {
			t.Errorf("ListCmd(%q) = %q, %v, want %q", c.argv, got, ok, c.want)
		}
	}
	// `swift test list` lists every test whatever --filter or --skip select
	// (measured: swift-list-ignores-filter in the capture script), so a
	// command carrying one has no listing of the tests it runs.
	for _, argv := range [][]string{
		{"swift", "test", "--filter", "X"},
		{"swift", "test", "--filter=X"},
		{"swift", "test", "--skip", "X"},
		{"swift", "test", "-s", "X"},
		{"swift", "build"},
		{"swift", "test", "list"},
		{"sh", "-c", "swift test"},
		nil,
	} {
		if got, ok := r.ListCmd(argv, ""); ok {
			t.Errorf("ListCmd(%q) = %q, want refused", argv, got)
		}
	}
}

// The shorter of two ids sharing a prefix, in each framework: the command the
// row builds is the one the capture ran, and that run executed exactly one
// test, not its longer neighbour.
func TestSwiftSingleCmdRunsExactlyOneTest(t *testing.T) {
	for _, c := range []struct {
		fixture, id, pattern, neighbour string
	}{
		{"swift-single-testing.txt", "CalcTests.parses()", `^CalcTests\.parses\(\)(?:/|$)`, "parsesTwice()"},
		{"swift-single-xctest.txt", "CalcXCTests.AdderTests/testAdds", `^CalcXCTests\.AdderTests/testAdds(?:/|$)`, "testAddsTwice"},
		// The two other shapes `swift test list` prints: a test inside a
		// suite (run under its display name) and a parameterised test, whose
		// id carries a ":" and whose run is one test of three cases.
		{"swift-single-suite.txt", "CalcTests.Arithmetic/adds()", `^CalcTests\.Arithmetic/adds\(\)(?:/|$)`, "doubles(value:)"},
		{"swift-single-param.txt", "CalcTests.Arithmetic/doubles(value:)", `^CalcTests\.Arithmetic/doubles\(value:\)(?:/|$)`, "adds two numbers"},
	} {
		t.Run(c.fixture, func(t *testing.T) {
			if got := swiftFilterFor(c.id); got != c.pattern {
				t.Errorf("swiftFilterFor(%q) = %q, want %q", c.id, got, c.pattern)
			}
			argv, ran, out := multiFixture(t, c.fixture)
			cmd, ok := rowByID(t, "swift-test").SingleCmd(argv, "", c.id)
			if !ok {
				t.Fatalf("SingleCmd(%q, %q) refused", argv, c.id)
			}
			if got := strings.Join(cmd, " "); got != ran {
				t.Fatalf("SingleCmd built\n  %s\nbut the capture ran\n  %s", got, ran)
			}
			if n := swiftRan(t, out); n != 1 {
				t.Errorf("the run executed %d tests, want 1:\n%s", n, out)
			}
			if strings.Contains(out, c.neighbour) {
				t.Errorf("the run shows %s, which shares the id's prefix:\n%s", c.neighbour, out)
			}
		})
	}
}

// A test's own --filter or --skip would combine with the row's (two --filter
// options select their union: swift-filters-union in the capture script), so
// such a command has no one-test form; nor has anything that is not `swift
// test` itself.
func TestSwiftSingleCmdRefuses(t *testing.T) {
	r := rowByID(t, "swift-test")
	for _, argv := range [][]string{
		{"swift", "test", "--filter", "X"},
		{"swift", "test", "--filter=X"},
		{"swift", "test", "--skip", "X"},
		{"swift", "test", "--skip=X"},
		{"swift", "test", "-s", "X"},
		{"swift", "test", "--specifier", "X"},
		{"swift", "build"},
		{"swift", "test", "list"},
		{"sh", "-c", "swift test"},
		nil,
	} {
		if got, ok := r.SingleCmd(argv, "", "CalcTests.parses()"); ok {
			t.Errorf("SingleCmd(%q) = %q, want refused", argv, got)
		}
	}
	if got, ok := r.SingleCmd([]string{"swift", "test"}, "", ""); ok {
		t.Errorf("SingleCmd with no id = %q, want refused", got)
	}
}

// An id another listed id extends by "/" (a suite and a test inside it) is
// one the row's pattern would select both of.
func TestSwiftCoruns(t *testing.T) {
	r := rowByID(t, "swift-test")
	for _, c := range []struct {
		id, other string
		want      bool
	}{
		{"CalcTests.Arithmetic", "CalcTests.Arithmetic/adds()", true},
		{"CalcTests.parses()", "CalcTests.parsesTwice()", false},
		{"CalcXCTests.AdderTests/testAdds", "CalcXCTests.AdderTests/testAddsTwice", false},
	} {
		if got := r.Coruns(c.id, c.other); got != c.want {
			t.Errorf("Coruns(%q, %q) = %v, want %v", c.id, c.other, got, c.want)
		}
	}
}

// FirstFailure names the first failing test as each framework prints it:
// swift-testing by its function or display name alone (its failure line names
// no suite or target), XCTest as Class.method (AdderTests, in target
// CalcXCTests: the line names the class, not the target). A build failure ran
// no test.
func TestSwiftFirstFailure(t *testing.T) {
	for _, c := range []struct{ fixture, want string }{
		{"swift-fail-testing.txt", `"adds two numbers"`},
		{"swift-fail-xctest.txt", "AdderTests.testFails"},
		{"swift-build-fail.txt", ""},
		{"swift-pass.txt", ""},
	} {
		if got := swiftFirstFailure([]byte(readFixture(t, c.fixture))); got != c.want {
			t.Errorf("swiftFirstFailure(%s) = %q, want %q", c.fixture, got, c.want)
		}
	}
	// Every failing test, each shape swift-testing prints: a display name, a
	// plain function, a parameterised test with its case count.
	want := []string{`"adds two numbers"`, "fails()", "doubles(value:)"}
	if got := swiftFailures(readFixture(t, "swift-fail-testing.txt")); !slices.Equal(got, want) {
		t.Errorf("swiftFailures = %q, want %q", got, want)
	}
}

// Units are test targets, listed from `swift package describe --type json`,
// run with a --filter on the target's module name.
func TestSwiftUnitsFromDescribe(t *testing.T) {
	p, _ := ByName("swift")
	inv := Cmd([]string{"swift", "test"})
	list, ok := ListUnitsCmd(p, inv)
	if !ok || !slices.Equal(list[len(list)-5:], []string{"swift", "package", "describe", "--type", "json"}) {
		t.Fatalf("ListUnitsCmd = %q, %v", list, ok)
	}
	pp, ok := ListUnitsCmd(p, Cmd([]string{"swift", "test", "--package-path", "pkg", "--parallel"}))
	if !ok || !slices.Equal(pp[len(pp)-7:], []string{"swift", "package", "--package-path", "pkg", "describe", "--type", "json"}) {
		t.Errorf("ListUnitsCmd with --package-path = %q, %v; `swift package` takes it before describe and nothing else of a test run's", pp, ok)
	}
	if got := ParseUnitList(p, inv, readFixture(t, "swift-units.txt")); !slices.Equal(got, []string{"CalcTests", "CalcXCTests"}) {
		t.Errorf("ParseUnitList = %q, want both test targets", got)
	}
	if got := swiftParseUnits(readFixture(t, "swift-build-fail.txt")); got != nil {
		t.Errorf("swiftParseUnits(not a description) = %q, want nil", got)
	}

	r := rowByID(t, "swift-test")
	argv, ran, out := multiFixture(t, "swift-units-one.txt")
	cmd, ok := r.UnitCmd(argv, "CalcXCTests")
	if !ok || strings.Join(cmd, " ") != ran {
		t.Fatalf("UnitCmd = %q, %v; the capture ran %s", cmd, ok, ran)
	}
	if n := swiftRan(t, out); n != 3 {
		t.Errorf("the one-target run executed %d tests, want CalcXCTests' 3:\n%s", n, out)
	}
	if strings.Contains(out, "parses()") {
		t.Errorf("the one-target run shows a CalcTests test:\n%s", out)
	}

	argv, ran, out = multiFixture(t, "swift-units-multi.txt")
	cmd, ok = r.UnitsCmd(argv, []string{"CalcTests", "CalcXCTests"})
	if !ok || strings.Join(cmd, " ") != ran {
		t.Fatalf("UnitsCmd = %q, %v; the capture ran %s", cmd, ok, ran)
	}
	if n := swiftRan(t, out); n != 8 {
		t.Errorf("the two-target run executed %d tests, want 8:\n%s", n, out)
	}

	// A target whose name is not a Swift identifier has a different module
	// name, which is what a test id starts with; no such target was run, so
	// it gets no unit command rather than a guessed one.
	if got, ok := r.UnitCmd([]string{"swift", "test"}, "My-Tests"); ok {
		t.Errorf("UnitCmd(My-Tests) = %q, want refused", got)
	}
}

func TestSwiftUnitForFile(t *testing.T) {
	r := rowByID(t, "swift-test")
	for _, c := range []struct {
		file, want string
		ok         bool
	}{
		{"Tests/CalcTests/CalcTests.swift", "CalcTests", true},
		{"Tests/CalcXCTests/Deeper/More.swift", "CalcXCTests", true},
		{"Sources/Calc/Calc.swift", "", false},
		{"Tests/Loose.swift", "", false},
		{"pkg/Tests/CalcTests/CalcTests.swift", "", false},
	} {
		got, ok := r.UnitForFile("", c.file)
		if got != c.want || ok != c.ok {
			t.Errorf("UnitForFile(%q) = %q, %v, want %q, %v", c.file, got, ok, c.want, c.ok)
		}
	}
}

// Why swiftFilterFor does not end in `$` alone: swift-testing matches
// --filter against the id followed by its source location, so the id
// anchored at both ends built, selected no test in either framework, and
// passed (swift-filter-dollar.txt). A pattern like that would be a run of
// nothing graded as a pass.
func TestSwiftDollarAnchoredFilterRunsNothingAndPasses(t *testing.T) {
	_, ran, out := multiFixture(t, "swift-filter-dollar.txt")
	if ran != `swift test --filter ^CalcTests\.parses\(\)$` {
		t.Fatalf("the capture ran %s", ran)
	}
	for _, s := range []string{"Build complete!", "Executed 0 tests, with 0 failures", "✔ Test run with 0 tests", "\n# exit: 0\n"} {
		if !strings.Contains(out, s) {
			t.Errorf("the capture does not show %q:\n%s", s, out)
		}
	}
	if n := swiftRan(t, out); n != 0 {
		t.Errorf("the run executed %d tests, want 0", n)
	}
	if got := swiftFilterFor("CalcTests.parses()"); strings.HasSuffix(got, `\)$`) {
		t.Errorf("swiftFilterFor ends in the pattern that selected nothing: %s", got)
	}
}

// The killer command (confirm-alone) narrows the failing run to the one test
// swift-testing's own record named, by the id `swift test list` prints, with
// the same --filter a one-test run uses; the ids here are what the row reads
// from the committed failing run on each toolchain (all three failures sit
// in a suite: one with a display name, one parameterised). A name only the
// printed lines give (a function signature with no module, a quoted display
// name, XCTest's Class.method, which names no target) is no listed id, so no
// killer is built from it, nor for a command that already filters by name.
func TestSwiftKillerCmdNarrowsToTheRecordedID(t *testing.T) {
	r := rowByID(t, "swift-test")
	if r.KillerCmd == nil {
		t.Fatal("the swift-test row has no KillerCmd")
	}
	for _, v := range []string{"6.3.2", "6.4.2"} {
		b, err := os.ReadFile(filepath.Join("testdata", "runners", "swift-hook-fail-"+v+".txt")) // #nosec G304 -- test fixture under testdata
		if err != nil {
			t.Fatal(err)
		}
		got := swiftFailures(string(b))
		for _, id := range []string{`CalcTests.Arithmetic/adds()`, `CalcTests.Arithmetic/doubles(value:)`, `CalcTests.Arithmetic/fails()`} {
			if !slices.Contains(got, id) {
				t.Errorf("%s: the failing run names %q, which lacks %q", v, got, id)
			}
		}
	}
	for id, want := range map[string]string{
		`CalcTests.Arithmetic/adds()`:          `^CalcTests\.Arithmetic/adds\(\)(?:/|$)`,
		`CalcTests.Arithmetic/doubles(value:)`: `^CalcTests\.Arithmetic/doubles\(value:\)(?:/|$)`,
		`CalcTests.parses()`:                   `^CalcTests\.parses\(\)(?:/|$)`,
	} {
		cmd, ok := r.KillerCmd([]string{"swift", "test"}, id)
		if !ok || !slices.Equal(cmd, []string{"swift", "test", "--filter", want}) {
			t.Errorf("%s: got %q, %v; want swift test --filter %s", id, cmd, ok, want)
		}
	}
	for _, id := range []string{"", `adds()`, `"adds two numbers"`, `AdderTests.testFails`, `CalcXCTests.AdderTests/testFails`, `x)`, `Calc.x)`} {
		if cmd, ok := r.KillerCmd([]string{"swift", "test"}, id); ok {
			t.Errorf("%q is no swift-testing id the record gives, but got %q", id, cmd)
		}
	}
	if cmd, ok := r.KillerCmd([]string{"swift", "test", "--filter", "Calc"}, `CalcTests.parses()`); ok {
		t.Errorf("a command with its own --filter: got %q, want no killer", cmd)
	}
}

// The killer commands above, run on a failing run of the fixture on each
// toolchain (scripts/capture-runner-fixtures.sh, swift-killer-*.txt): each
// ran exactly one test, which failed, so the kill it confirms is that test's
// alone. The commands are the --filter strings the test above expects.
func TestSwiftKillerCommandsRanOneFailingTest(t *testing.T) {
	for _, v := range []string{"6.3.2", "6.4.2"} {
		for _, shape := range []string{"suite", "param"} {
			f := "swift-killer-" + shape + "-" + v + ".txt"
			b, err := os.ReadFile(filepath.Join("testdata", "runners", f)) // #nosec G304 -- test fixture under testdata
			if err != nil {
				t.Fatal(err)
			}
			out := string(b)
			if n := swiftRan(t, out); n != 1 {
				t.Errorf("%s: ran %d tests, want 1", f, n)
			}
			if !strings.Contains(out, "\n# exit: 1\n") {
				t.Errorf("%s: want the run to fail (# exit: 1)", f)
			}
		}
	}
}

// Salvage: the failing tests of the committed failing run (both frameworks
// failing) are named by what can be skipped, the three swift-testing record
// ids and XCTest's Class.method, and the rerun skips each: a record id by
// the one-test pattern, an XCTest name in any target. Measured on Swift
// 6.3.2 and 6.4.2 (swift-salvage-*.txt): the four skips ran the other four
// tests and passed, and with a unit --filter the two left in that target.
// The display-name test's printed twin ("adds two numbers") is not among
// them: the record already names it. A swift-testing name only the printed
// lines give is not skippable, so with no record nothing is salvaged.
func TestSwiftSalvageSkipsTheRecordedAndXCTestFailures(t *testing.T) {
	r := rowByID(t, "swift-test")
	if r.FailedTests == nil || r.DeselectCmd == nil {
		t.Fatal("the swift-test row cannot salvage")
	}
	want := []string{`CalcTests.Arithmetic/adds()`, `CalcTests.Arithmetic/doubles(value:)`, `CalcTests.Arithmetic/fails()`, `AdderTests.testFails`}
	skips := []string{"swift", "test",
		"--skip", `^CalcTests\.Arithmetic/adds\(\)(?:/|$)`,
		"--skip", `^CalcTests\.Arithmetic/doubles\(value:\)(?:/|$)`,
		"--skip", `^CalcTests\.Arithmetic/fails\(\)(?:/|$)`,
		"--skip", `^[^.]+\.AdderTests/testFails(?:/|$)`}
	for _, v := range []string{"6.3.2", "6.4.2"} {
		b, err := os.ReadFile(filepath.Join("testdata", "runners", "swift-hook-fail-"+v+".txt")) // #nosec G304 -- test fixture under testdata
		if err != nil {
			t.Fatal(err)
		}
		got := r.FailedTests(string(b))
		slices.Sort(got)
		w := slices.Clone(want)
		slices.Sort(w)
		if !slices.Equal(got, w) {
			t.Errorf("%s: FailedTests = %q, want %q", v, got, w)
		}
		cmd, ok := r.DeselectCmd([]string{"swift", "test"}, want)
		if !ok || !slices.Equal(cmd, skips) {
			t.Errorf("%s: DeselectCmd = %q, %v\nwant %q", v, cmd, ok, skips)
		}
		for _, f := range []string{"swift-salvage-" + v + ".txt", "swift-salvage-unit-" + v + ".txt"} {
			b, err := os.ReadFile(filepath.Join("testdata", "runners", f)) // #nosec G304 -- test fixture under testdata
			if err != nil {
				t.Fatal(err)
			}
			n := map[bool]int{true: 2, false: 4}[strings.Contains(f, "-unit-")]
			if ran := swiftRan(t, string(b)); ran != n || !strings.Contains(string(b), "\n# exit: 0\n") {
				t.Errorf("%s: ran %d tests, want %d, and a passing run", f, ran, n)
			}
		}
	}
	// Printed lines alone (no record): the swift-testing names cannot be
	// skipped, so there is no rerun; nor for a bare name, a display name,
	// or none.
	b, err := os.ReadFile(filepath.Join("testdata", "runners", "swift-fail-testing.txt")) // #nosec G304 -- test fixture under testdata
	if err != nil {
		t.Fatal(err)
	}
	if cmd, ok := r.DeselectCmd([]string{"swift", "test"}, r.FailedTests(string(b))); ok {
		t.Errorf("printed swift-testing names only: got %q, want no rerun", cmd)
	}
	for _, ids := range [][]string{nil, {`adds()`}, {`"adds two numbers"`}, {`CalcTests.parses()`, `fails()`}} {
		if cmd, ok := r.DeselectCmd([]string{"swift", "test"}, ids); ok {
			t.Errorf("%q: got %q, want no rerun", ids, cmd)
		}
	}
}
