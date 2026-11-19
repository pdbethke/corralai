// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"slices"
	"strconv"
	"strings"
	"testing"
)

// Every assertion below reads a capture of the real `swift test` (Swift 6.3.2,
// testdata/runners/swift-*.txt, PROVENANCE.txt), from the fixture package in
// testdata/runners/src/swift, never output typed here.

// swiftRan is the number of tests a captured run executed, from its own
// summaries: swift-testing's "Test run with N tests" plus XCTest's last
// "Executed N tests" (its nested suites repeat the total). It is the count the
// capture script's swiftran reads.
func swiftRan(t *testing.T, out string) int {
	t.Helper()
	swiftTesting, xctest := 0, 0
	for _, line := range strings.Split(out, "\n") {
		if _, rest, ok := strings.Cut(line, "Test run with "); ok {
			n, err := strconv.Atoi(strings.Fields(rest)[0])
			if err != nil {
				t.Fatalf("unreadable swift-testing summary %q", line)
			}
			swiftTesting = n
		}
		if rest, ok := strings.CutPrefix(strings.TrimSpace(line), "Executed "); ok {
			n, err := strconv.Atoi(strings.Fields(rest)[0])
			if err != nil {
				t.Fatalf("unreadable XCTest summary %q", line)
			}
			xctest = n
		}
	}
	return swiftTesting + xctest
}

// The listing names every test of both frameworks, in the forms `swift test
// list` prints them, and nothing else.
func TestSwiftParsesItsRealList(t *testing.T) {
	want := []string{
		"CalcXCTests.CalcXCTests/testAdds",
		"CalcXCTests.CalcXCTests/testAddsTwice",
		"CalcXCTests.CalcXCTests/testFails",
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
		{"swift-single-xctest.txt", "CalcXCTests.CalcXCTests/testAdds", `^CalcXCTests\.CalcXCTests/testAdds(?:/|$)`, "testAddsTwice"},
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
		{"CalcXCTests.CalcXCTests/testAdds", "CalcXCTests.CalcXCTests/testAddsTwice", false},
	} {
		if got := r.Coruns(c.id, c.other); got != c.want {
			t.Errorf("Coruns(%q, %q) = %v, want %v", c.id, c.other, got, c.want)
		}
	}
}

// FirstFailure names the first failing test as each framework prints it:
// swift-testing by its function or display name alone (its failure line names
// no suite or target), XCTest as Class.method. A build failure ran no test.
func TestSwiftFirstFailure(t *testing.T) {
	for _, c := range []struct{ fixture, want string }{
		{"swift-fail-testing.txt", `"adds two numbers"`},
		{"swift-fail-xctest.txt", "CalcXCTests.testFails"},
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
