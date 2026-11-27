// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"slices"
)

var mochaFilters = []string{"-g", "--grep", "-f", "--fgrep", "-i", "--invert"}

// mochaListCmd collects without running (`--dry-run`, mocha 9 and later)
// and reports through the JSON reporter. mocha takes the last --reporter, so
// an operator's own reporter is overridden (measured on mocha 11.8).
func mochaListCmd(argv []string, testPath string) ([]string, bool) {
	if !narrowable(argv, mochaFilters...) {
		return nil, false
	}
	return append(slices.Clone(argv), "--dry-run", "--reporter", "json", testPath), true
}

// mochaParseList reads the JSON report's tests. fullTitle alone is already
// joined by spaces, so the leaf title is read beside it.
func mochaParseList(stdout string) []string {
	var rep struct {
		Tests []struct {
			Title     string `json:"title"`
			FullTitle string `json:"fullTitle"`
		} `json:"tests"`
	}
	if !decodeReport(stdout, &rep) {
		return nil
	}
	var ids []string
	for _, t := range rep.Tests {
		ids = append(ids, idFromFull(t.FullTitle, t.Title))
	}
	return ids
}

// mochaSingleCmd runs the one test whose full title is id's full name, in
// testPath: the killer command's filter with the file named.
func mochaSingleCmd(argv []string, testPath, id string) ([]string, bool) {
	cmd, ok := mochaKillerCmd(argv, id)
	if !ok {
		return nil, false
	}
	return append(cmd, testPath), true
}

// mochaKillerCmd narrows argv to the test whose full title is id's, as
// mochaFailures names it: --grep matches a test's full title (its suites'
// titles and its own, joined by spaces), anchored so that no longer title
// containing it runs. argv already scopes the files. Two tests sharing one
// full title both run (mocha-fullname-collision), as with jest's name
// pattern; a kill confirmed that way is still a run in which the blamed
// name failed. It declines an argv that filters by name already, which
// mocha would combine with this one.
func mochaKillerCmd(argv []string, id string) ([]string, bool) {
	if id == "" || !narrowable(argv, mochaFilters...) {
		return nil, false
	}
	return append(slices.Clone(argv), "--grep", "^"+jsRegexQuote(lastFull(id))+"$"), true
}
