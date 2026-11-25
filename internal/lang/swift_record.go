// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"strings"
)

// swiftRecordHook is the `swift test` row's ReportHook: swift-testing's own
// JUnit record of the run, which `swift test --xunit-output <path>` has it
// write (FileRecordHookAt). XCTest is not part of it: it writes its record
// only when the tests run in parallel (Swift 6.4.2, measured), and adding
// --parallel would change the run, so an XCTest failure is read from the
// printed lines alone, as before.
//
// Where the record lands is not the same in every release, measured with
// `--xunit-output <dir>/rec.xml` (capture-runner-fixtures.sh,
// swift-xunit-files-*.txt): Swift 6.4.2 writes `<dir>/rec-swift-testing.xml`
// and nothing at the path itself; 6.3.2 writes `<dir>/rec.xml`. The hook
// reads the first of the two that holds a record, so one row reads both
// releases CI pins.
//
// It is one `--xunit-output=<token>.xml` word, and the fallback leaves that
// word out, because a fallback to /dev/null would have swift-testing write
// beside it: 6.4.2 exits 1 on a run that passed ("error: invalid access to
// /dev/null-swift-testing"), and the hook must never change the exit code.
//
// Measured on both releases: the run prints what it printed without the
// option and exits with the same code (swift-hook-*-<version>.txt).
var swiftRecordHook = func() *ReportHook {
	h := FileRecordHookAt(
		func() []string { return []string{"--xunit-output=" + recordToken + ".xml"} },
		recordJUnit,
		"corral runs every graded run through `sh -c` and adds `--xunit-output=<a temporary file>` after its options, "+
			"and reads which swift-testing test failed from swift-testing's own record of the run, by the id `swift test list` prints; "+
			"what `swift test` prints and its exit code are unchanged. XCTest writes no record unless its tests run in parallel, "+
			"so an XCTest failure is still read from the printed lines; a command with `--parallel`, `--num-workers` or an `--xunit-output` of its own is not wrapped",
		RecordFile{Suffixes: []string{"-swift-testing.xml", ".xml"}, DropWhenUnset: true},
	)
	h.Wraps = swiftRecordWraps
	return h
}()

// swiftRecordWraps declines what the record cannot be read from as
// swift-testing's alone: a command that is not a `swift test` run
// (swiftTestAt: the compile check's `swift build`, a listing, a `--`); one
// whose tests run in parallel (`--parallel`, or `--num-workers`, which sets
// how many), where XCTest writes the path itself on 6.4.2 and 6.3.2 has one
// file for both frameworks; and one with an `--xunit-output` of its own,
// since 6.3.2 keeps the first one given and 6.4.2 the last (both measured,
// swift-xunit-files-*.txt), so one of the two records would not be written.
func swiftRecordWraps(cmd []string, _ int) bool {
	at, ok := swiftTestAt(cmd)
	if !ok {
		return false
	}
	return !hasFlag(cmd[at+1:], "--parallel", "--num-workers", "--xunit-output")
}

// swiftRecordedFailures is swiftFailures' answer from swift-testing's
// record: every failing testcase's listed id, in the record's order (the
// order its tests finished, which differs between runs, as the printed
// order does; recordThenPrinted names each once). ok is false when junitRecord reads none, and
// when a testcase is not one swift-testing wrote (swiftTestingID), since a
// list missing it would be read as every failure.
func swiftRecordedFailures(out string) ([]string, bool) {
	cases, ok := junitRecord(out)
	if !ok {
		return nil, false
	}
	ids := []string{}
	for _, c := range cases {
		id, ok := swiftTestingID(c.ClassName, c.Name)
		if !ok {
			return nil, false
		}
		ids = append(ids, id)
	}
	return ids, true
}

// swiftTestingID is the id `swift test list` prints for the swift-testing
// test a record's testcase names. The record writes classname as the
// module, then each enclosing type, joined by "." (`CalcTests`,
// `CalcTests.Arithmetic`, `CalcTests.Outer.Inner`), and name as the
// function's signature (`adds()`, `doubles(value:)`), never its display
// name; the list writes `CalcTests.adds()` for a test outside any type and
// `CalcTests.Outer/Inner/nested()` inside one. Each shape was captured on
// Swift 6.3.2 and 6.4.2 and its id found in that release's listing
// (swift-hook-fail-*.txt, swift-hook-edge-*.txt; a suite with a display
// name is written by its type's name too). A parameterised test is one
// testcase, whatever case failed. ok is false for a testcase with no
// classname, or whose name is no function signature (XCTest's record writes
// `testFails`), since that is not swift-testing's record.
func swiftTestingID(classname, name string) (string, bool) {
	module, types, nested := strings.Cut(classname, ".")
	if module == "" || !strings.HasSuffix(name, ")") || strings.Contains(name, "/") {
		return "", false
	}
	if !nested {
		return module + "." + name, true
	}
	return module + "." + strings.ReplaceAll(types, ".", "/") + "/" + name, true
}

// swiftRecordedNames reports whether a recorded id is the test a printed
// name names: a printed function signature names the recorded id that ends
// in it (`fails()` and `CalcTests.Arithmetic/fails()`). A display name and
// an XCTest Class.method name no recorded id. Two suites each with a test
// `fails()` print the same name, so a printed `fails()` is taken as named
// when either is recorded; the printed line cannot say which it was.
func swiftRecordedNames(id, name string) bool {
	if !strings.HasSuffix(name, ")") || strings.HasPrefix(name, `"`) {
		return false
	}
	leaf := id
	if i := strings.LastIndex(id, "/"); i >= 0 {
		leaf = id[i+1:]
	} else if _, after, ok := strings.Cut(id, "."); ok {
		leaf = after
	}
	return leaf == name
}
