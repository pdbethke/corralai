// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"path/filepath"
	"slices"
	"strings"
)

var vitestFilters = []string{"-t", "--testNamePattern"}

// vitestIndex is the position of the word that is vitest in argv: the vitest
// binary, or its entry script run by a host interpreter
// (`node ./node_modules/vitest/vitest.mjs`, the shape AGENTS.md gives for the
// jail, where node_modules/.bin is not on PATH). -1 when there is none.
func vitestIndex(argv []string) int {
	if i := wordIndex(argv, "vitest"); i >= 0 {
		return i
	}
	return slices.IndexFunc(argv, func(a string) bool {
		b := filepath.Base(a)
		return b == "vitest.mjs" || b == "vitest.js"
	})
}

// vitestWithMode is argv with vitest's subcommand set to mode: an existing
// run/watch/dev replaced, else mode inserted after the vitest word.
func vitestWithMode(argv []string, mode string) ([]string, bool) {
	i := vitestIndex(argv)
	if i < 0 || !narrowable(argv, vitestFilters...) {
		return nil, false
	}
	cmd := slices.Clone(argv)
	if i+1 < len(cmd) && (cmd[i+1] == "run" || cmd[i+1] == "watch" || cmd[i+1] == "dev") {
		cmd[i+1] = mode
		return cmd, true
	}
	return slices.Insert(cmd, i+1, mode), true
}

// vitestListCmd is vitest's own `list` subcommand over the test file: it
// collects without running.
func vitestListCmd(argv []string, testPath string) ([]string, bool) {
	cmd, ok := vitestWithMode(argv, "list")
	if !ok {
		return nil, false
	}
	return append(cmd, testPath), true
}

// vitestParseList reads `list`'s `<file> > <title> > <title>` lines. A title
// that itself contains " > " cannot be told from a describe boundary; its id
// then names titles that do not exist, and its one-test run selects nothing,
// which is never credited.
func vitestParseList(stdout string) []string {
	var ids []string
	for _, line := range strings.Split(stdout, "\n") {
		parts := strings.Split(strings.TrimSpace(line), " > ")
		if len(parts) < 2 {
			continue
		}
		ids = append(ids, joinTitles(parts[1:]))
	}
	return ids
}

// vitestSingleCmd runs the one test whose full name is id's, always under
// `run`: a bare vitest watches when it has a terminal.
func vitestSingleCmd(argv []string, testPath, id string) ([]string, bool) {
	cmd, ok := vitestWithMode(argv, "run")
	if id == "" || !ok {
		return nil, false
	}
	return append(cmd, "-t", "^"+jsRegexQuote(fullName(id))+"$", testPath), true
}
