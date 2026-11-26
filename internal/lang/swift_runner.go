// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// The `swift test` row's commands and readers. Every shape below was run on
// Swift 6.3.2 against the fixture package in testdata/runners/src/swift, one
// swift-testing target and one XCTest target, and every reader is tested
// against those captures (testdata/runners/swift-*.txt, PROVENANCE.txt).
//
// A test id is what `swift test list` prints: `Target.Class/testMethod` for
// XCTest, `Target.Suite/function(labels:)` or `Target.function()` for
// swift-testing (a test outside any suite has no suite segment).

// swiftNameFilters are the options that choose which tests a `swift test`
// run executes. Two --filter options select the union of their tests
// (capture script: swift-filters-union), and --skip removes tests from
// whatever --filter selects, so a command carrying any of them has no
// one-test or one-unit form: the row's filter would combine with the
// operator's and run a set nobody listed. -s is --specifier, which
// `swift test --help` lists without a description and which was not run.
var swiftNameFilters = []string{"--filter", "--skip", "-s", "--specifier"}

// swiftTestAt is the index of `test` in a `swift test` run: the word after
// `swift`, with no subcommand after it (`swift test list` lists, it does not
// run), no "--" (after which an appended option would be an argument) and no
// shell around it. ok=false for anything else.
func swiftTestAt(argv []string) (int, bool) {
	i := wordIndex(argv, "swift")
	if i < 0 || i+1 >= len(argv) || argv[i+1] != "test" || cmdIsShellWrapped(argv) || slices.Contains(argv, "--") {
		return 0, false
	}
	if i+2 < len(argv) && (argv[i+2] == "list" || argv[i+2] == "last") {
		return 0, false
	}
	return i + 1, true
}

// swiftNarrowable reports whether the row may add its own --filter to argv:
// a `swift test` run that narrowable allows (no name filter of its own) and
// that has no --list-tests. -s may also be written with its value attached
// (-sX) or after -v or -q in one word, which shortBundleSets reads; that
// refuses an -Xswiftc value such as -suppress-warnings too, which costs a
// whole-suite run, never a wrong one.
func swiftNarrowable(argv []string) bool {
	_, ok := swiftTestAt(argv)
	return ok && narrowable(argv, swiftNameFilters...) && !hasFlag(argv, "--list-tests", "-l") &&
		!shortBundleSets(argv, "vq", "s")
}

// swiftListCmd is argv with `list` appended: `swift test <options> list`
// lists the tests of the package argv builds (run with --parallel and with
// --package-path before `list`). It takes no --filter or --skip and lists
// every test whatever they select (capture script:
// swift-list-ignores-filter), so a command with a name filter has no listing
// of the tests it runs and is refused.
func swiftListCmd(argv []string, _ string) ([]string, bool) {
	if !swiftNarrowable(argv) {
		return nil, false
	}
	return append(slices.Clone(argv), "list"), true
}

// swiftListedID is one line of `swift test list`: an XCTest
// `Target.Class/method`, or a swift-testing `Target.(Suite/)*function(...)`.
// Build progress goes to stderr, which the listing does not read; any other
// line is no id.
var swiftListedID = regexp.MustCompile(`^\w+\.(?:(?:\w+/)*\w+\([^()\s]*\)|\w+/\w+)$`)

// swiftParseList reads `swift test list`'s stdout into test ids, in the
// order listed. A test whose name is no ASCII identifier (Swift allows
// others) is not read, so it cannot be resolved or run alone; nothing
// guesses at it.
func swiftParseList(stdout string) []string {
	return listLines(stdout, func(line string) (string, bool) { return line, swiftListedID.MatchString(line) })
}

// swiftFilterFor is the --filter pattern that selects the one listed test
// id. Both frameworks match --filter as a regular expression against the
// id, but swift-testing matches the id followed by "/<file>:<line>:<column>",
// its source location, so a pattern anchored with `$` alone selects nothing
// and the run passes having run no test (swift-filter-dollar.txt: build
// complete, 0 tests, exit 0). The pattern therefore ends at "/" or at the
// end. It ran exactly one test for each id shape `swift test list` printed
// (swift-single-*.txt): an XCTest Target.Class/method, a top-level
// swift-testing function, one in a suite with a display name, and a
// parameterised one whose labels carry a ":"; in each case not the test whose
// name the id is a prefix of.
func swiftFilterFor(id string) string { return "^" + regexp.QuoteMeta(id) + "(?:/|$)" }

// swiftSingleCmd runs exactly the one listed test id.
func swiftSingleCmd(argv []string, _, id string) ([]string, bool) {
	if id == "" || !swiftNarrowable(argv) {
		return nil, false
	}
	return append(slices.Clone(argv), "--filter", swiftFilterFor(id)), true
}

// swiftKillerCmd narrows the failing run to the one test killedBy names,
// with the one-test --filter swiftSingleCmd uses, but only for an id
// swift-testing's own record gives (swiftRecordedID): `Module.fn(...)` or
// `Module.Type/.../fn(...)`, as `swift test list` prints it. Every other
// name a run gives comes from the printed lines (swiftPrintedFailures) and
// is no id: a swift-testing function or quoted display name carries no
// suite or module, so a filter built from it could run a test of the same
// name elsewhere, and XCTest's Class.method carries no target. Those kills
// are confirmed by repeating the grading run, as before.
//
// Overloads share an id: two parameterised tests of one name and argument
// labels (`f(value: Int)` and `f(value: String)`) are one id in the record,
// `KTests.f(value:)`, which `swift test list` prints only with each one's
// `/<file>:<line>:<column>` added, so the filter runs both (measured on
// Swift 6.3.2 and 6.4.2: "Test run with 2 tests"). As with jest's and
// rspec's shared names, a kill confirmed that way is still a run in which
// the blamed name failed.
func swiftKillerCmd(argv []string, killedBy string) ([]string, bool) {
	if !swiftRecordedID(killedBy) {
		return nil, false
	}
	return swiftSingleCmd(argv, "", killedBy)
}

// swiftRecordedID reports whether name has the shape of an id
// swiftTestingID builds from swift-testing's record: a module, ".", then a
// function signature, with any enclosing types between them joined by "/".
// A printed swift-testing name has no module before its signature, a
// display name is quoted, and an XCTest name ends in no ")".
func swiftRecordedID(name string) bool {
	if strings.HasPrefix(name, `"`) || !strings.HasSuffix(name, ")") {
		return false
	}
	sig := strings.Index(name, "(")
	if sig < 0 {
		return false
	}
	module, _, ok := strings.Cut(name[:sig], ".")
	return ok && module != "" && !strings.Contains(module, "/")
}

// swiftCoruns: the pattern for id also selects every listed id that extends
// it by "/" (a suite id and the tests inside it), because the pattern ends at
// "/". `swift test list` lists tests, not suites, so two listed ids do not
// extend each other in the fixture; this answers for a list that does.
func swiftCoruns(id, other string) bool { return strings.HasPrefix(other, id+"/") }

// swiftTestingFailed is swift-testing's line for a test that failed: its
// function signature or quoted display name, with the number of cases when
// it is parameterised. The line names no suite and no target, so the name is
// not an id. Its per-issue lines ("recorded an issue") and its suite and run
// summaries are not matched.
var swiftTestingFailed = regexp.MustCompile(`^✘ Test (".*"|\S+\))(?: with \d+ test cases?)? failed after `)

// xctestFailed is XCTest's line for a test case that failed, as XCTest
// prints it on Linux: Class.method, without the target the listed id carries
// (swift-fail-xctest.txt: class AdderTests in target CalcXCTests prints
// `Test Case 'AdderTests.testFails' failed`).
var xctestFailed = regexp.MustCompile(`^Test Case '([^']+)' failed \(`)

// swiftPrintedFailures names the failing tests in a `swift test` run's
// printed lines, in the order printed and each once, as each framework
// names them: a swift-testing test by its function or display name, an
// XCTest test as Class.method. On Linux XCTest reports first and
// swift-testing after it, whether or not the XCTest part failed. A build
// that failed ran no test, and names none.
func swiftPrintedFailures(output string) []string {
	var names []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		var m []string
		if m = swiftTestingFailed.FindStringSubmatch(line); m == nil {
			m = xctestFailed.FindStringSubmatch(line)
		}
		if m != nil && !slices.Contains(names, m[1]) {
			names = append(names, m[1])
		}
	}
	return names
}

// swiftFailures names the failing tests of a `swift test` run: the ids
// swift-testing's own record gives (swiftRecordedFailures, the row's
// failure hook), then each printed name no recorded id stands for
// (recordThenPrinted, swiftRecordedNames), which is every XCTest failure
// (XCTest writes no record on a run the hook wraps), a swift-testing test
// with a display name (the record does not carry it, so its printed name
// cannot be matched to its id, and the test is named twice, once in each
// form), and every swift-testing failure the record omits. It omits the
// tests of a test process that crashed: Swift 6.4.2's record is then
// well-formed and lacks that process's suite, while 6.3.2's is cut short and
// is not read (swift-hook-crash-*.txt). With no record, the printed lines
// are the whole answer, as before the hook.
func swiftFailures(output string) []string {
	return recordThenPrinted(output, swiftRecordedFailures, swiftPrintedFailures, swiftRecordedNames)
}

// swiftFirstFailure is the first test swiftFailures names, a label for the
// record. From swift-testing's record it is the id `swift test list` prints
// (`CalcTests.Arithmetic/adds()`), which SingleCmd takes and the critic's
// list uses; from the printed lines it is the bare name they print, which
// cannot be narrowed back to one listed id, since no swift-testing failure
// line names the test's suite or target. The row's KillerCmd
// (swiftKillerCmd) is built from the first kind only.
var swiftFirstFailure = firstOf(swiftFailures)

// A Swift test unit is a test TARGET, as cargo's is: the module a test id
// starts with, `swift test --filter '^(?:Target)\.'` running it alone.

// swiftListUnits describes the package argv tests: `swift package describe
// --type json`, with argv's --package-path (which `swift package` takes
// before the subcommand, not after it) and nothing else of argv's, since
// describing reads the manifest and builds nothing. Refused where argv has no
// unit form.
func swiftListUnits(argv []string) ([]string, bool) {
	at, ok := swiftTestAt(argv)
	if !ok || !swiftNarrowable(argv) {
		return nil, false
	}
	cmd := append(slices.Clone(argv[:at]), "package")
	opts := argv[at+1:]
	for i, a := range opts {
		if a == "--package-path" && i+1 < len(opts) {
			cmd = append(cmd, a, opts[i+1])
		} else if strings.HasPrefix(a, "--package-path=") {
			cmd = append(cmd, a)
		}
	}
	return append(cmd, "describe", "--type", "json"), true
}

// swiftParseUnits reads the package description into its test targets'
// names, sorted. The description is read by decodeReport, so lines before it
// (a resolution's, should the stream carry stderr) are skipped. nil when
// there is no description to read.
func swiftParseUnits(stdout string) []string {
	var d struct {
		Targets []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"targets"`
	}
	if !decodeReport(stdout, &d) || d.Targets == nil {
		return nil
	}
	units := []string{}
	for _, t := range d.Targets {
		if t.Type == "test" && t.Name != "" {
			units = append(units, t.Name)
		}
	}
	slices.Sort(units)
	return units
}

// swiftModuleName is a target name that is its own module name. A test id
// starts with the target's MODULE name, which SwiftPM derives from a target
// name that is not an identifier by replacing characters (`c99name` in the
// description); no such target was run, so the row runs only targets whose
// name needs no derivation.
var swiftModuleName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// swiftUnitsCmd runs exactly these test targets: one --filter matching the
// ids that start with any of their module names and a ".". The dot keeps one
// target from selecting another whose name it begins (CalcTests and
// CalcTestsMore). Run for one target (swift-units-one.txt: CalcXCTests' three
// tests and none of CalcTests') and for both (swift-units-multi.txt).
func swiftUnitsCmd(argv []string, units []string) ([]string, bool) {
	if !unitSet(units) || !swiftNarrowable(argv) {
		return nil, false
	}
	for _, u := range units {
		if !swiftModuleName.MatchString(u) {
			return nil, false
		}
	}
	return append(slices.Clone(argv), "--filter", `^(?:`+alternation(units, regexp.QuoteMeta)+`)\.`), true
}

// swiftUnitForFile is the test target a test file belongs to by SwiftPM's
// default layout, Tests/<Target>/...; a target declared with its own `path:`
// in Package.swift is not read. ok=false outside that layout, and for a
// target swiftUnitsCmd cannot run.
func swiftUnitForFile(_, testFile string) (string, bool) {
	f, ok := repoRelative(testFile)
	if !ok {
		return "", false
	}
	return SwiftTestTarget(f)
}

// swiftDisclose names the dependencies Package.swift pins to a branch when
// there is no Package.resolved beside it to fix their revisions: SwiftPM then
// resolves each branch to whatever it holds when the audit runs. corral does
// not add --disable-automatic-resolution to the operator's command; it says
// this. What an extra workspace tree costs is the pool's to say, not the
// runner's (adequacy.NewWorkspacePool), since it holds for every language
// with a build directory.
//
// The manifest is the nearest Package.swift at or above repoDir, where
// Preflight finds it. A command that builds a package elsewhere
// (--package-path) is not read for pins: its manifest is not that one, and a
// line about the wrong manifest would be false. A versioned manifest
// (Package@swift-5.9.swift), which SwiftPM prefers on a matching toolchain,
// is not read either.
func swiftDisclose(repoDir string, argv []string) []string {
	for _, a := range argv {
		if a == "--package-path" || strings.HasPrefix(a, "--package-path=") {
			return nil
		}
	}
	abs, err := filepath.Abs(repoDir)
	if err != nil {
		return nil
	}
	dir, ok := nearestAncestorWith(abs, []string{"Package.swift"}, swiftPackageSearchDepth)
	if !ok {
		return nil
	}
	if _, err := os.Stat(filepath.Join(dir, "Package.resolved")); err == nil {
		return nil
	}
	src, err := os.ReadFile(filepath.Join(dir, "Package.swift")) // #nosec G304 -- the operator's own manifest; only scanned, never executed; the url: literal of each branch-pinned dependency is printed back to the operator
	if err != nil {
		return nil
	}
	pins := swiftBranchPins(string(src))
	if len(pins) == 0 {
		return nil
	}
	return []string{"runner: swift test — Package.swift pins " + strings.Join(pins, ", ") + " to a branch, and there is no Package.resolved beside it, so SwiftPM resolves each such branch to whatever it holds when the audit runs, not to a recorded revision; commit a Package.resolved to grade fixed revisions"}
}
