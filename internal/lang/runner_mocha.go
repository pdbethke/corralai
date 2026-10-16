// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"encoding/json"
	"slices"
	"strings"
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
	if json.Unmarshal([]byte(strings.TrimSpace(stdout)), &rep) != nil {
		return nil
	}
	var ids []string
	for _, t := range rep.Tests {
		ids = append(ids, idFromFull(t.FullTitle, t.Title))
	}
	return ids
}

// mochaSingleCmd runs the one test whose full title is id's full name.
func mochaSingleCmd(argv []string, testPath, id string) ([]string, bool) {
	if id == "" || !narrowable(argv, mochaFilters...) {
		return nil, false
	}
	return append(slices.Clone(argv), "--grep", "^"+jsRegexQuote(lastFull(id))+"$", testPath), true
}
