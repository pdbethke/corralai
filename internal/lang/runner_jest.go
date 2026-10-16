// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"encoding/json"
	"slices"
	"strings"
)

// jest's own name filters; a command carrying one is not narrowed further.
var jestFilters = []string{"-t", "--testNamePattern"}

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
	if json.Unmarshal([]byte(strings.TrimSpace(stdout)), &rep) != nil {
		return nil
	}
	var ids []string
	for _, f := range rep.TestResults {
		for _, a := range f.AssertionResults {
			if id := joinTitles(append(slices.Clone(a.AncestorTitles), a.Title)); id != "" {
				ids = append(ids, id)
			}
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
