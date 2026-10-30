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

// seedSkip is one file loadRepoFiles left out of the jail copy because it is
// not text, and its size.
type seedSkip struct {
	Path string
	Size int64
}

// seedSkipNames is how many files the line names before it counts the rest.
const seedSkipNames = 3

// seedSkipsLine says what the jail copy lacks, largest first, so a baseline
// that fails because a test reads one of these files is not left to guess.
// It is one line however many files there are.
func seedSkipsLine(cmdName string, skipped []seedSkip) string {
	these := slices.Clone(skipped)
	slices.SortFunc(these, func(a, b seedSkip) int {
		return cmp.Or(cmp.Compare(b.Size, a.Size), strings.Compare(a.Path, b.Path))
	})
	var names []string
	for _, s := range these[:min(len(these), seedSkipNames)] {
		names = append(names, s.Path)
	}
	if more := len(these) - seedSkipNames; more > 0 {
		names = append(names, fmt.Sprintf("and %d more", more))
	}
	return fmt.Sprintf("%s: the jail copy leaves out %d file(s) that are not UTF-8 text, and a test that reads one fails on unmutated code: %s\n", cmdName, len(skipped), strings.Join(names, ", "))
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

// seedSkippedJSON is ScanRow.SeedSkipped: the paths left out, sorted and
// once each (a scan's seeds for different languages walk the same tree), as
// JSON text; "" when there are none.
func seedSkippedJSON(skipped []seedSkip) string {
	var paths []string
	for _, s := range skipped {
		paths = append(paths, s.Path)
	}
	slices.Sort(paths)
	paths = slices.Compact(paths)
	if len(paths) == 0 {
		return ""
	}
	b, _ := json.Marshal(paths) // a []string always marshals
	return string(b)
}
