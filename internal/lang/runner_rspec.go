// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"encoding/json"
	"slices"
	"strings"
)

var rspecFilters = []string{"-e", "--example", "-E", "--example-matches"}

// rspecListCmd collects without running (`--dry-run`) through the JSON
// formatter.
func rspecListCmd(argv []string, testPath string) ([]string, bool) {
	if !narrowable(argv, rspecFilters...) {
		return nil, false
	}
	return append(slices.Clone(argv), "--dry-run", "--format", "json", testPath), true
}

// rspecParseList reads the JSON formatter's examples. The report is read from
// its first "{", since bundler and spec helpers can print before it.
func rspecParseList(stdout string) []string {
	i := strings.Index(stdout, "{")
	if i < 0 {
		return nil
	}
	var rep struct {
		Examples []struct {
			Description     string `json:"description"`
			FullDescription string `json:"full_description"`
		} `json:"examples"`
	}
	if json.NewDecoder(strings.NewReader(stdout[i:])).Decode(&rep) != nil {
		return nil
	}
	var ids []string
	for _, e := range rep.Examples {
		if id := idFromFull(e.FullDescription, e.Description); id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// rspecSingleCmd runs the one example whose full description is id's full
// name: -E (--example-matches, rspec 3.12 and later) with \A and \z, which
// unlike -e's substring match cannot also select a longer description.
func rspecSingleCmd(argv []string, testPath, id string) ([]string, bool) {
	if id == "" || !narrowable(argv, rspecFilters...) {
		return nil, false
	}
	return append(slices.Clone(argv), "-E", `\A`+regexQuoter.Replace(fullName(id))+`\z`, testPath), true
}
