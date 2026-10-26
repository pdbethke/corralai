// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"slices"
	"strings"
)

// jest's own name filters, in both spellings its option parser (yargs)
// accepts; a command carrying one is not narrowed further.
var jestFilters = []string{"-t", "--testNamePattern", "--test-name-pattern"}

// jestShortFlags are jest 30.5.2's other single-letter options (bail, config,
// expand, maxWorkers, onlyChanged, onlyFailures, runInBand, updateSnapshot).
// Its option parser (yargs) reads a bundle of them letter by letter and
// hands the next word to the last: `-ct foo`, `-it foo` and `-ut foo` each
// ran 1 of the file's 2 tests (measured), a name filter hasFlag cannot see.
const jestShortFlags = "bceowfiu"

// jestNarrowable is narrowable for a jest command: no name filter in either
// spelling, and none set inside a bundle of short options after the jest
// word. `-tfoo` is refused too, though yargs reads it as letters (measured:
// it filtered nothing), since a reading nobody can see is not a guarantee.
func jestNarrowable(argv []string) bool {
	i := max(wordIndex(argv, "jest"), entryScriptIndex(argv, "jest", "bin/jest.js"), 0)
	return narrowable(argv, jestFilters...) && !shortBundleSets(argv[i:], jestShortFlags, "t")
}

// jestListCmd asks jest for its JSON report under a name pattern no test
// matches (`^\b$` cannot match a non-empty name): every test is collected and
// reported "pending", none runs. The test file is appended exactly as
// jestSingleCmd appends it, so the list holds every test a single run could
// reach — jest reads it as a path regex, which can match a second file.
func jestListCmd(argv []string, testPath string) ([]string, bool) {
	if !jestNarrowable(argv) {
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
	if id == "" || !jestNarrowable(argv) {
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
	if !jestNarrowable(argv) {
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

// jestUnitCmd runs exactly one listed test file: argv, without its shard
// (the listing applied it), with --runTestsByPath and the unit, and an
// ignore pattern that ignores every path not ending in /unit.
//
// --runTestsByPath reads the unit as an exact path from the working
// directory, which is what the unit is named from, so a unit cannot match
// nothing: not under a configured rootDir elsewhere, not under `projects`,
// each with its own rootDir (measured on jest 30.5.2: a pattern anchored at
// <rootDir> matched nothing under projects, and with --passWithNoTests jest
// then passed having run no test). --runTestsByPath alone is not enough: it
// reads EVERY path argument as exact, so a test file the operator named
// would run beside the unit (`jest b.jest.test.js --runTestsByPath
// a.jest.test.js` ran both). The ignore pattern drops it. A CLI pattern
// replaces the configured testPathIgnorePatterns, which is harmless here:
// only the paths on the command line are candidates, and the unit was
// listed under the configured ones. A listed file whose path ends in
// /unit would also survive the pattern; jestUnitCoruns marks that unit.
func jestUnitCmd(argv []string, unit string) ([]string, bool) {
	if unit == "" || !jestNarrowable(argv) {
		return nil, false
	}
	return append(withoutFlag(argv, "--shard"), "--runTestsByPath", unit, "--testPathIgnorePatterns=^(?!.*"+jsRegexQuote("/"+unit)+"$)"), true
}

// jestUnitCoruns: other survives unit's ignore pattern when its path ends
// in /unit.
func jestUnitCoruns(unit, other string) bool {
	return unit != other && strings.HasSuffix(other, "/"+unit)
}
