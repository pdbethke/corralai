// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
)

// seedSkip is one file loadRepoFiles left out of the jail copy, and why.
type seedSkip struct {
	Path string
	Size int64
	Why  string
}

// The reasons a file is left out of the seed, in the order the run names them.
const (
	skipOverSize = "over the 1 MiB per-file limit"
	skipNotText  = "not UTF-8 text"
)

// seedSkipNames is how many files of each reason the line names before it
// counts the rest: REA alone leaves out seven built files.
const seedSkipNames = 3

// seedSkipsLine says what the jail copy lacks, grouped by reason, largest
// first, so a baseline that fails because a test reads one of these files is
// not left to guess. It is one line however many files there are.
func seedSkipsLine(cmdName string, skipped []seedSkip) string {
	var groups []string
	for _, why := range []string{skipOverSize, skipNotText} {
		var these []seedSkip
		for _, s := range skipped {
			if s.Why == why {
				these = append(these, s)
			}
		}
		if len(these) == 0 {
			continue
		}
		slices.SortFunc(these, func(a, b seedSkip) int {
			return cmp.Or(cmp.Compare(b.Size, a.Size), strings.Compare(a.Path, b.Path))
		})
		var names []string
		for _, s := range these[:min(len(these), seedSkipNames)] {
			if why == skipOverSize {
				names = append(names, fmt.Sprintf("%s %.1f MiB", s.Path, float64(s.Size)/(1<<20)))
			} else {
				names = append(names, s.Path)
			}
		}
		if more := len(these) - seedSkipNames; more > 0 {
			names = append(names, fmt.Sprintf("and %d more", more))
		}
		groups = append(groups, fmt.Sprintf("%d %s (%s)", len(these), why, strings.Join(names, ", ")))
	}
	return fmt.Sprintf("%s: the jail copy leaves out %d file(s), and a test that reads one fails on unmutated code: %s\n", cmdName, len(skipped), strings.Join(groups, "; "))
}
