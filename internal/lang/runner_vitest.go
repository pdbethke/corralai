// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"path/filepath"
	"slices"
	"strings"
)

// vitestFilters are vitest's name filters, in both spellings it accepts.
var vitestFilters = []string{"-t", "--testNamePattern", "--test-name-pattern"}

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

// vitestListUnits is vitest's own `list --filesOnly` over argv: the test
// files argv collects, collected and not run. --json comes last so it is
// read as a flag, not as the file to write the list to, and prints each file
// as an absolute path (verified on vitest 3.2.7; the plain-text form is
// relative to the configured root, which need not be the working directory).
func vitestListUnits(argv []string) ([]string, bool) {
	cmd, ok := vitestWithMode(argv, "list")
	if !ok {
		return nil, false
	}
	return append(cmd, "--filesOnly", "--json"), true
}

// vitestParseUnits reads `list --filesOnly --json`'s array of files. A file
// in two projects is listed twice; ParseUnitList keeps it once.
func vitestParseUnits(stdout string) []string {
	var files []struct {
		File string `json:"file"`
	}
	if !decodeArray(stdout, &files) {
		return nil
	}
	paths := []string{}
	for _, f := range files {
		paths = append(paths, f.File)
	}
	return paths
}

// vitestUnitCmd runs one test file: argv under `run`, without its shard
// (withoutFlag), with the operator's file filters replaced by the unit, since vitest runs the files ANY filter
// matches. A bare word after a flag written without "=" may be that flag's
// value or a filter, and vitest's options are too many to tell, so such a
// command has no unit form.
func vitestUnitCmd(argv []string, unit string) ([]string, bool) {
	cmd, ok := vitestWithMode(argv, "run")
	if unit == "" || !ok {
		return nil, false
	}
	i := vitestIndex(cmd) + 2 // past the vitest word and `run`
	flags, ok := dropFileArgs(withoutFlag(cmd[i:], "--shard"), true, func(string) (bool, bool) { return false, false })
	if !ok {
		return nil, false
	}
	return append(append(slices.Clone(cmd[:i]), flags...), unit), true
}

// vitestUnitCoruns: vitest selects the files whose path, relative to its
// root, contains a filter — the filter as given, or made relative to that
// root — ignoring case (vitest 3.2.7's filterFiles). The root may sit below
// the working directory the unit is named from, where the filter shrinks to
// as little as the base name (measured: `vitest run --root units/collide
// units/collide/a.vitest.test.mjs` also ran xa.vitest.test.mjs), so unit
// coruns other whenever other contains unit's base name.
func vitestUnitCoruns(unit, other string) bool {
	return unit != other && strings.Contains(strings.ToLower(other), strings.ToLower(filepath.Base(unit)))
}
