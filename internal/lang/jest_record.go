// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"slices"
	"strings"
)

// jestRecordHook is the jest row's ReportHook: jest's own JSON account of
// the run, which `--json --outputFile=<file>` writes to the file and not to
// stdout (FileRecordHook). Measured on jest 30.5.2
// (capture-runner-fixtures.sh jest-hook-*.txt): the exit code is unchanged
// on a failing run and a passing one, and jest prints what it printed
// without the flags plus one line, `Test results written to: <file>`, on
// stdout and stderr together: --json moves jest-cli's own messages from
// stdout to stderr, so either stream alone differs. The
// JSON is written whatever reporters run, and the record's ids are the ones
// the printed `●` headings give (jestRecordedFailures).
//
// It never writes a project's file, which is the condition every hook is
// held to (vitest has none because no flag meets it). `json` and
// `outputFile` are command-line options, not configuration: a jest.config.js
// or package.json `jest` block naming them is not read for them, so the
// command line's file is the one written; a reporter the configuration
// names with an `outputFile` option is dropped by the row's
// --reporters=default, with or without the hook; a testResultsProcessor
// is the project's own code and writes what it writes either way (each
// measured on jest 30.5.2, capture-runner-fixtures.sh jest-wrote-*).
//
// The fallback is the zero RecordFile's: jest writes its JSON to /dev/null
// and passes a passing run (jest-hook-fallback-exit).
var jestRecordHook = func() *ReportHook {
	h := FileRecordHook(
		func() []string { return []string{"--json", "--outputFile=" + recordToken} },
		recordJestJSON,
		"corral runs every graded run through `sh -c` and adds `--json --outputFile=<a temporary file>` after jest's options, "+
			"and reads which test failed from that JSON record, jest's own account of the run, by the titles the printed `●` headings give (a title holding ` › ` whole); "+
			"the exit code is unchanged, and so is what jest prints, on stdout and stderr together (`--json` moves jest's own messages to stderr), but for one more line, `Test results written to: <that file>`. "+
			"A command naming `--json` or `--outputFile` of its own is not wrapped",
	)
	h.Wraps = jestRecordWraps
	return h
}()

// jestJSONOptions are the options a command can name jest's JSON output
// with. The hook declines such a command: a later --outputFile would move
// the operator's own JSON (from stdout, or from their file) into corral's
// record, which changes what the run prints or writes.
var jestJSONOptions = []string{"--json", "--no-json", "--outputFile", "--output-file"}

// jestRecordWraps declines a command that names jest's JSON output itself.
func jestRecordWraps(cmd []string, at int) bool {
	return !hasFlag(cmd[at+1:], jestJSONOptions...)
}

// jestFailedTests is the jest row's failure reader: the run's JSON record
// first (jestRecordedFailures), then each test the printed `●` headings
// name that the record does not, by recordThenPrinted, the rule every
// record-file row follows; the printed headings alone where there is no
// record.
//
// A recorded test and a printed heading are the same test when the
// heading is the record's titles as jest prints them, joined by " › " and
// read back by jestFailures: a title holding " › " is split there, so
// `Sep` › `a › b` is printed as `Sep::a::b`, and is the record's `Sep::a › b`
// all the same. That printed form is the record's id with every " › "
// made "::" (jestPrintedAs).
func jestFailedTests(out string) []string {
	return recordThenPrinted(out, jestRecordedFailures, jestFailures, func(id, name string) bool {
		return sameID(id, name) || jestPrintedAs(id) == name
	})
}

// jestPrintedAs is the id jestFailures reads from the printed heading of
// the test the record names id: the record's titles joined by "::", each
// " › " inside a title split as jest's heading splits it.
func jestPrintedAs(id string) string { return strings.ReplaceAll(id, " › ", "::") }

// jestRecordedFailures is every failing test jest's JSON record names, in
// its order, by the id jestFailures reads from the printed headings: the
// ancestor titles and the title, joined by "::" (a title holding " › "
// kept whole). ok is false when jsonRecord reads none, the JSON has no
// testResults (it is not jest's), or a failing test has no title, since a
// list missing it would be read as every failure.
//
// Only an assertion result whose status is "failed" is a failure: jest
// 30.5.2 records a todo as "todo", a test.failing that failed as expected
// as "passed" and one that passed as "failed", a test whose beforeAll
// threw as "failed", and a test whose afterAll threw as "passed" (jest
// prints that hook as "Test suite failed to run", which jestFailures does
// not name either). A file that failed to load has no assertion results.
func jestRecordedFailures(out string) ([]string, bool) {
	var rep struct {
		TestResults *[]struct {
			AssertionResults []struct {
				AncestorTitles []string `json:"ancestorTitles"`
				Title          string   `json:"title"`
				Status         string   `json:"status"`
			} `json:"assertionResults"`
		} `json:"testResults"`
	}
	if !jsonRecord(out, recordJestJSON, &rep) || rep.TestResults == nil {
		return nil, false
	}
	var ids []string
	for _, file := range *rep.TestResults {
		for _, a := range file.AssertionResults {
			if a.Status != "failed" {
				continue
			}
			if a.Title == "" {
				return nil, false
			}
			ids = append(ids, strings.Join(append(slices.Clone(a.AncestorTitles), a.Title), "::"))
		}
	}
	return ids, true
}
