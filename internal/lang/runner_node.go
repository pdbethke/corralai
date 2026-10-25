// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"path/filepath"
	"slices"
	"strings"
)

var nodeFilters = []string{"--test-name-pattern", "--test-skip-pattern"}

// nodeWith is argv with a row's flags right after --test and testPath (when
// given) at the end. node reads its options only BEFORE the first file: one
// after it is handed to the test files as an argument, so `node --test
// tests/ --test-name-pattern x` runs every test in tests/ (measured on node
// 22.23). Through a package script the flags then land mid-script, which
// shape refuses unless --test is the script's last word.
func nodeWith(argv []string, testPath string, flags ...string) ([]string, bool) {
	i := slices.Index(argv, "--test")
	if i < 0 {
		return nil, false
	}
	cmd := slices.Insert(slices.Clone(argv), i+1, flags...)
	if testPath != "" {
		cmd = append(cmd, testPath)
	}
	return cmd, true
}

// nodeListCmd lists node:test's tests by RUNNING the test file once with the
// TAP reporter, against the unmutated code, in the same jail as the baseline.
// node 22 has no collect-only mode: a pattern that matches nothing, or a skip
// pattern that matches everything, drops tests from the report rather than
// listing them (measured on node 22.23). The cost is one run of one file per
// run with a whole-test finding; the alternative was no critic check at all
// for the JavaScript and TypeScript default runner. A command that already
// names a reporter is not listed: two reporters on stdout interleave.
func nodeListCmd(argv []string, testPath string) ([]string, bool) {
	if !narrowable(argv, append(nodeFilters, "--test-reporter", "--test-reporter-destination")...) {
		return nil, false
	}
	return nodeWith(argv, testPath, "--test-reporter=tap")
}

// nodeParseList reads the TAP report into the ids of its tests. node nests a
// subtest four spaces deeper per level, and each result's YAML says
// `type: 'test'` or `type: 'suite'`. A test with subtests, and every subtest
// of a test, is listed but marked notAlone: running either runs the parent's
// body too, so what it killed cannot be credited to one, yet its name must
// still make a critic's name ambiguous. Left out entirely: a result whose
// YAML carries exitCode, which is the FILE failing to load (node reports it
// as a test named after the file), not a test.
func nodeParseList(stdout string) []string {
	entries, hasChild := nodeEntries(stdout)
	typeOf := map[string]string{}
	for _, e := range entries {
		typeOf[nodeKey(e.path)] = e.typ
	}
	var ids []string
	for _, e := range entries {
		if e.typ != "test" || e.exit {
			continue
		}
		alone := !hasChild[nodeKey(e.path)]
		for k := 1; k < len(e.path); k++ {
			alone = alone && typeOf[nodeKey(e.path[:k])] == "suite"
		}
		id := joinTitles(e.path)
		if !alone && id == listedName(id) {
			id = markNotAlone(id)
		}
		ids = append(ids, id)
	}
	return ids
}

// nodeEntry is one result in a TAP report: its describe path, whether it
// passed, its YAML type, and whether it reports a process exit code.
type nodeEntry struct {
	path []string
	ok   bool
	typ  string
	exit bool
}

// nodeEntries walks a TAP report into its results, and which of them have
// subtests. nodeParseList and nodeFailures read the same walk.
func nodeEntries(stdout string) (entries []*nodeEntry, hasChild map[string]bool) {
	var stack []string
	var cur *nodeEntry // the result whose YAML block is being read
	for _, line := range strings.Split(stdout, "\n") {
		trimmed := strings.TrimLeft(line, " ")
		level := (len(line) - len(trimmed)) / 4
		if name, ok := strings.CutPrefix(trimmed, "# Subtest: "); ok {
			stack = append(stack[:min(level, len(stack))], tapUnescape(name))
			cur = nil
			continue
		}
		if strings.HasPrefix(trimmed, "ok ") || strings.HasPrefix(trimmed, "not ok ") {
			if level >= len(stack) {
				cur = nil
				continue
			}
			cur = &nodeEntry{path: slices.Clone(stack[:level+1]), ok: strings.HasPrefix(trimmed, "ok ")}
			entries = append(entries, cur)
			continue
		}
		if cur == nil {
			continue
		}
		switch {
		case strings.HasPrefix(trimmed, "type: "):
			cur.typ = strings.Trim(strings.TrimPrefix(trimmed, "type: "), "'")
		case strings.HasPrefix(trimmed, "exitCode:"):
			cur.exit = true
		case trimmed == "...":
			cur = nil
		}
	}
	hasChild = map[string]bool{}
	for _, e := range entries {
		if len(e.path) > 1 {
			hasChild[nodeKey(e.path[:len(e.path)-1])] = true
		}
	}
	return entries, hasChild
}

func nodeKey(path []string) string { return strings.Join(path, "\x00") }

// tapUnescape undoes node's TAP escaping of a test name (`\#`, `\\`).
func tapUnescape(s string) string {
	return strings.NewReplacer(`\#`, "#", `\\`, `\`).Replace(s)
}

// nodeSingleCmd runs the one test whose full name is id's.
func nodeSingleCmd(argv []string, testPath, id string) ([]string, bool) {
	if id == "" || !narrowable(argv, nodeFilters...) {
		return nil, false
	}
	return nodeWith(argv, testPath, "--test-name-pattern", "^"+jsRegexQuote(fullName(id))+"$")
}

// nodeCoruns is wider than full-name equality: node runs every test or suite
// whose OWN name matches the pattern as well as its full name (measured on
// node 22.23: ^adds$ ran both Calc > adds and Calc > neg > adds), and a
// matching suite runs all of its tests. So id coruns other when id's full
// name equals any of other's titles, or any leading run of them joined by
// spaces — a suite's full name, or other's own.
func nodeCoruns(id, other string) bool {
	if id == other {
		return false
	}
	want := fullName(id)
	segs := strings.Split(other, "::")
	for k := 1; k <= len(segs); k++ {
		if segs[k-1] == want || strings.Join(segs[:k], " ") == want {
			return true
		}
	}
	return false
}

// nodeListUnits lists the test files argv collects by running them under
// --test-only with the TAP reporter: node 22 has no list mode, and under
// --test-only a file with no `only` test runs none of its tests and reports
// ITSELF, by the path node resolved (relative to the working directory),
// as one result (measured on node 22.23.4; nodeParseUnits for the case
// where a file carries an `only` test). Node's own file resolution — its
// default globs, the operator's files and globs — is what decides the list.
// The files' top-level code runs, against the unmutated tree, as nodeListCmd's
// run does.
func nodeListUnits(argv []string) ([]string, bool) {
	if !narrowable(argv, append(nodeFilters, "--test-reporter", "--test-reporter-destination")...) {
		return nil, false
	}
	return nodeWith(argv, "", "--test-only", "--test-reporter=tap")
}

// nodeUnitExts are the extensions node's test runner loads.
var nodeUnitExts = []string{".js", ".cjs", ".mjs", ".jsx", ".ts", ".cts", ".mts", ".tsx"}

// nodeParseUnits reads nodeListUnits' TAP: each top-level result is a file.
// A file that carries an `only` test reports that test in its place under
// --test-only and its path appears nowhere, so a top-level result that is
// not a file path means the list is incomplete, and nil is returned rather
// than a list missing a file. A file that failed to load (its result
// carries exitCode) is still a unit the command collects; its own run will
// fail and say so.
func nodeParseUnits(stdout string) []string {
	// No TAP header is no listing (node did not start, or crashed before
	// reporting), never a listing of no files.
	if !strings.HasPrefix(strings.TrimLeft(stdout, "\n"), "TAP version ") {
		return nil
	}
	entries, _ := nodeEntries(stdout)
	units := []string{}
	for _, e := range entries {
		if len(e.path) != 1 {
			continue
		}
		name := e.path[0]
		if e.typ != "test" || !slices.Contains(nodeUnitExts, strings.ToLower(filepath.Ext(name))) {
			return nil
		}
		units = append(units, name)
	}
	return units
}

// nodeUnitCmd runs one test file: argv with the operator's files and globs
// replaced by the unit, every option kept but its shard (withoutFlag). Options come before the first
// file (nodeWith), and node's own option table (runner_node_options.go)
// says which of them read the next word; anything it cannot place is
// refused.
func nodeUnitCmd(argv []string, unit string) ([]string, bool) {
	if unit == "" || !narrowable(argv, nodeFilters...) || !slices.Contains(argv, "--test") {
		return nil, false
	}
	flags, ok := dropFileArgs(withoutFlag(argv[1:], "--test-shard"), false, nodeOptionTakesValue)
	if !ok {
		return nil, false
	}
	return append(append([]string{argv[0]}, flags...), unit), true
}
