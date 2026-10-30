// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bufio"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"unicode/utf8"
)

// seedSkip is one file left out of the jail copy, its size, and why: a
// reason token the ledger records as-is (seedSkipReasons).
type seedSkip struct {
	Path string
	Size int64
	Why  string
}

// skipNotText is loadRepoFiles' reason: not UTF-8 text, outside a test tree.
const skipNotText = "not-text"

// seedSkipReasons is how the run's line says each reason, in the order it
// says them. A reason missing here is still said, by its token.
var seedSkipReasons = []struct{ why, says string }{
	{skipNotText, "not UTF-8 text"},
	{"gitignored", "gitignored"},
}

// seedSkipNames is how many files of each reason the line names before it
// counts the rest.
const seedSkipNames = 3

// seedSkipsLine says what the jail copy lacks, by reason, largest first, so a
// baseline that fails because a test reads one of these files is not left to
// guess. It is one line however many files there are.
func seedSkipsLine(cmdName string, skipped []seedSkip) string {
	order := map[string]int{}
	says := map[string]string{}
	for i, r := range seedSkipReasons {
		order[r.why], says[r.why] = i, r.says
	}
	these := slices.Clone(skipped)
	slices.SortFunc(these, func(a, b seedSkip) int {
		oa, ka := order[a.Why]
		ob, kb := order[b.Why]
		if !ka {
			oa = len(order)
		}
		if !kb {
			ob = len(order)
		}
		return cmp.Or(cmp.Compare(oa, ob), strings.Compare(a.Why, b.Why), cmp.Compare(b.Size, a.Size), strings.Compare(a.Path, b.Path))
	})
	var groups []string
	for start := 0; start < len(these); {
		end := start
		for end < len(these) && these[end].Why == these[start].Why {
			end++
		}
		group := these[start:end]
		var names []string
		for _, s := range group[:min(len(group), seedSkipNames)] {
			names = append(names, s.Path)
		}
		if more := len(group) - seedSkipNames; more > 0 {
			names = append(names, fmt.Sprintf("and %d more", more))
		}
		say := says[group[0].Why]
		if say == "" {
			say = group[0].Why
		}
		groups = append(groups, fmt.Sprintf("%d %s (%s)", len(group), say, strings.Join(names, ", ")))
		start = end
	}
	return fmt.Sprintf("%s: the jail copy leaves out %d file(s), and a test that reads one fails on unmutated code: %s\n", cmdName, len(skipped), strings.Join(groups, "; "))
}

// streamText is how many bytes r holds and whether they are UTF-8 text,
// read a rune at a time so a file of any size is measured without being held.
// utf8.RuneError of width 1 is an invalid byte; a real U+FFFD is width 3.
func streamText(r io.Reader) (int64, bool, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	var n int64
	text := true
	for {
		c, size, err := br.ReadRune()
		if errors.Is(err, io.EOF) {
			return n, text, nil
		}
		if err != nil {
			return n, false, err
		}
		n += int64(size)
		if c == utf8.RuneError && size == 1 {
			text = false
		}
	}
}

// seedSkippedJSON is ScanRow.SeedSkipped: every file left out with its
// reason, sorted by path and once each (a scan's seeds for different
// languages walk the same tree), as JSON text; "" when there are none.
func seedSkippedJSON(skipped []seedSkip) string {
	type left struct {
		Path string `json:"path"`
		Why  string `json:"why"`
	}
	var out []left
	for _, s := range skipped {
		out = append(out, left{s.Path, s.Why})
	}
	slices.SortFunc(out, func(a, b left) int { return cmp.Or(strings.Compare(a.Path, b.Path), strings.Compare(a.Why, b.Why)) })
	out = slices.Compact(out)
	if len(out) == 0 {
		return ""
	}
	b, _ := json.Marshal(out) // strings only: always marshals
	return string(b)
}
