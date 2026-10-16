// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"slices"
	"strings"
)

var nodeFilters = []string{"--test-name-pattern", "--test-skip-pattern"}

// nodeListCmd lists node:test's tests by RUNNING the test file once with the
// TAP reporter, against the unmutated code, in the same jail as the baseline.
// node 22 has no collect-only mode: a pattern that matches nothing, or a skip
// pattern that matches everything, drops tests from the report rather than
// listing them (measured on node 22.23). The cost is one run of one file per
// run with a whole-test finding; the alternative was no critic check at all
// for the JavaScript and TypeScript default runner. A command that already
// names a reporter is not listed: two reporters on stdout interleave.
func nodeListCmd(argv []string, testPath string) ([]string, bool) {
	if !slices.Contains(argv, "--test") || !narrowable(argv, append(nodeFilters, "--test-reporter", "--test-reporter-destination")...) {
		return nil, false
	}
	return append(slices.Clone(argv), "--test-reporter=tap", testPath), true
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
	var entries []*nodeEntry
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
			cur = &nodeEntry{path: slices.Clone(stack[:level+1])}
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
	typeOf, hasChild := map[string]string{}, map[string]bool{}
	for _, e := range entries {
		typeOf[nodeKey(e.path)] = e.typ
		if len(e.path) > 1 {
			hasChild[nodeKey(e.path[:len(e.path)-1])] = true
		}
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

// nodeEntry is one result in a TAP report: its describe path, its YAML type,
// and whether it reports a process exit code.
type nodeEntry struct {
	path []string
	typ  string
	exit bool
}

func nodeKey(path []string) string { return strings.Join(path, "\x00") }

// tapUnescape undoes node's TAP escaping of a test name (`\#`, `\\`).
func tapUnescape(s string) string {
	return strings.NewReplacer(`\#`, "#", `\\`, `\`).Replace(s)
}

// nodeSingleCmd runs the one test whose full name is id's.
func nodeSingleCmd(argv []string, testPath, id string) ([]string, bool) {
	if id == "" || !slices.Contains(argv, "--test") || !narrowable(argv, nodeFilters...) {
		return nil, false
	}
	return append(slices.Clone(argv), "--test-name-pattern", "^"+jsRegexQuote(fullName(id))+"$", testPath), true
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
