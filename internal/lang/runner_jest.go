// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"slices"
	"strings"
)

// jest's own name filters, in both spellings its option parser (yargs)
// accepts; a command carrying one is not narrowed further.
var jestFilters = []string{"-t", "--testNamePattern", "--test-name-pattern"}

// jestListCmd asks jest for its JSON report under a name pattern no test
// matches (`^\b$` cannot match a non-empty name): every test is collected and
// reported "pending", none runs. The test file is appended exactly as
// jestSingleCmd appends it, so the list holds every test a single run could
// reach — jest reads it as a path regex, which can match a second file.
func jestListCmd(argv []string, testPath string) ([]string, bool) {
	if !narrowable(argv, jestFilters...) {
		return nil, false
	}
	return append(slices.Clone(argv), "--json", "--testNamePattern", `^\b$`, testPath), true
}

// jestParseList reads the JSON report's tests as ancestorTitles+title ids.
// jest prints the report alone on stdout (test output goes to stderr), so
// anything that is not the report lists nothing.
func jestParseList(stdout string) []string {
	var rep struct {
		TestResults []struct {
			AssertionResults []struct {
				AncestorTitles []string `json:"ancestorTitles"`
				Title          string   `json:"title"`
			} `json:"assertionResults"`
		} `json:"testResults"`
	}
	if !decodeReport(stdout, &rep) {
		return nil
	}
	var ids []string
	for _, f := range rep.TestResults {
		for _, a := range f.AssertionResults {
			ids = append(ids, joinTitles(append(slices.Clone(a.AncestorTitles), a.Title)))
		}
	}
	return ids
}

// jestSingleCmd runs the one test whose full name (jest's fullName: the
// titles joined by spaces) is id's.
func jestSingleCmd(argv []string, testPath, id string) ([]string, bool) {
	if id == "" || !narrowable(argv, jestFilters...) {
		return nil, false
	}
	return append(slices.Clone(argv), "--testNamePattern", "^"+jsRegexQuote(fullName(id))+"$", testPath), true
}

// jestCoruns is fullNameColl without case: jest compiles its name filter
// with the 'i' flag (jest-circus), so `^Calc adds$` also runs "calc adds"
// (measured on jest 30.5). vitest, mocha, node and rspec match case-sensitively.
func jestCoruns(id, other string) bool {
	return id != other && strings.EqualFold(fullName(id), fullName(other))
}

// jestListUnits is jest's own --listTests over argv: the test files argv
// collects, as a JSON array of absolute paths, nothing run (verified on jest
// 30.5.2).
func jestListUnits(argv []string) ([]string, bool) {
	if !narrowable(argv, jestFilters...) {
		return nil, false
	}
	return append(slices.Clone(argv), "--listTests", "--json"), true
}

// jestParseUnits reads --listTests --json's array of paths.
func jestParseUnits(stdout string) []string {
	var paths []string
	if !decodeArray(stdout, &paths) {
		return nil
	}
	return paths
}

// jestUnitCmd runs exactly one listed test file: argv with an ignore
// pattern that ignores every path but <rootDir>/unit. It narrows the set
// argv collects rather than adding to it, so the operator's own path
// patterns and flags all stay as they are.
//
// Not `--runTestsByPath <file>`: that treats EVERY path argument as exact,
// so a test file the operator named on the command line runs beside the unit
// (measured on jest 30.5.2: `jest b.jest.test.js --runTestsByPath
// a.jest.test.js` ran both files' three tests). A pattern on the CLI replaces
// the configured testPathIgnorePatterns, so the pattern is anchored at
// <rootDir> and matches the listed file alone: an unanchored suffix would
// also reach a file the configured patterns ignored. When the configured
// rootDir is not the working directory the unit is named from, nothing
// matches and jest exits non-zero ("No tests found"), a failed unit, never a
// different one. jest substitutes <rootDir> unescaped, so a root holding a
// regular-expression character widens nothing it would not already match.
func jestUnitCmd(argv []string, unit string) ([]string, bool) {
	if unit == "" || !narrowable(argv, jestFilters...) {
		return nil, false
	}
	return append(slices.Clone(argv), "--testPathIgnorePatterns=^(?!<rootDir>"+jsRegexQuote("/"+unit)+"$)"), true
}
