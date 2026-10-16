// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// The failure parsers. Each reads its runner's DEFAULT reporter as the jail
// runs it (not a TTY) and returns every failing test, in report order, in the
// form the runner's list gives it, so a killed_by label is the same string
// the critic list and --matrix use — except rspec's, which are full
// descriptions: its "Failed examples:" list does not say where the leaf
// title starts. A row's FirstFailure is firstOf its
// parser and its FailedTests is the parser itself: the label and the salvage
// list can never disagree. Output a parser cannot read — another reporter, a
// file that failed to load, a failing hook — names nothing, never a guess.
// Captured outputs: testdata/runners/*-fail.txt
// (scripts/capture-runner-fixtures.sh).

// firstOf is a FirstFailure from a failure parser: its first entry.
func firstOf(failures func(string) []string) func([]byte) string {
	return func(output []byte) string {
		if f := failures(string(output)); len(f) > 0 {
			return f[0]
		}
		return ""
	}
}

// jestFailures reads jest's "  ● Calc › adds" headings. jest joins titles
// with " › ", so a title holding that separator is split wrongly: its label
// is wrong and its skip pattern misses it, which leaves the failing test in
// and fails the salvage closed. "Test suite failed to run" (a file that did
// not load), "Console" (captured console output) and a heading indented
// further ("●  TCPSERVERWRAP", an open handle --detectOpenHandles reports)
// are not tests.
func jestFailures(out string) []string {
	var ids []string
	for _, line := range strings.Split(out, "\n") {
		name, ok := strings.CutPrefix(line, "  ● ")
		if !ok || name == "Test suite failed to run" || name == "Console" || strings.HasPrefix(name, " ") {
			continue
		}
		ids = append(ids, strings.Join(strings.Split(name, " › "), "::"))
	}
	return ids
}

// vitestFailures reads vitest's " FAIL  <file> > Calc > adds" headings; a
// heading with no title after the file (" FAIL  f.test.mjs [ f.test.mjs ]")
// is the file failing to load. Titles are joined by " > ", with jest's
// caveat for a title that holds it.
func vitestFailures(out string) []string {
	var ids []string
	for _, line := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(line, " FAIL  ")
		if !ok {
			continue
		}
		parts := strings.Split(rest, " > ")
		if len(parts) < 2 {
			continue
		}
		ids = append(ids, strings.Join(parts[1:], "::"))
	}
	return ids
}

var mochaEntry = regexp.MustCompile(`^  \d+\) (.*)$`)

// mochaHook is the title mocha gives a failing hook; the hook is not a test.
var mochaHook = regexp.MustCompile(`^"(before|after) (all|each)" hook`)

// mochaFailures reads the spec reporter's section after "N failing": each
// entry is "  N) <title>" with every further title on its own deeper line,
// the last ending ":". The progress list above it prints leaf titles only,
// so it is not read. An entry with no title ending ":" before a blank line is
// not one this parser understands, and a failing hook names no test.
func mochaFailures(out string) []string {
	lines := strings.Split(out, "\n")
	start := slices.IndexFunc(lines, func(l string) bool {
		f := strings.Fields(l)
		return len(f) == 2 && f[1] == "failing"
	})
	if start < 0 {
		return nil
	}
	var ids []string
	for i := start + 1; i < len(lines); i++ {
		m := mochaEntry.FindStringSubmatch(lines[i])
		if m == nil {
			continue
		}
		titles, ended := []string{m[1]}, strings.HasSuffix(m[1], ":")
		for !ended && i+1 < len(lines) && strings.TrimSpace(lines[i+1]) != "" {
			i++
			t := strings.TrimSpace(lines[i])
			titles, ended = append(titles, t), strings.HasSuffix(t, ":")
		}
		if !ended {
			continue
		}
		titles[len(titles)-1] = strings.TrimSuffix(titles[len(titles)-1], ":")
		if slices.ContainsFunc(titles, mochaHook.MatchString) {
			continue
		}
		ids = append(ids, listedName(idFromFull(strings.Join(titles, " "), titles[len(titles)-1])))
	}
	return ids
}

// nodeFailures reads node's report off a TTY: TAP on node 22, the spec
// reporter on node 23 and later (measured on 22.23 and 24.22). Either way it
// names every failing test with no subtests: a suite, or a test whose
// subtests failed, is rolled up as failed and is not itself the failure, and
// a file that failed to load is not a test.
func nodeFailures(out string) []string {
	if !strings.Contains(out, "TAP version") {
		return nodeSpecFailures(out)
	}
	entries, hasChild := nodeEntries(out)
	var ids []string
	for _, e := range entries {
		if !e.ok && e.typ == "test" && !e.exit && !hasChild[nodeKey(e.path)] {
			ids = append(ids, strings.Join(e.path, "::"))
		}
	}
	return ids
}

var nodeSpecDuration = regexp.MustCompile(` \([0-9.]+m?s\)$`)

// nodeSpecFailures reads the spec reporter's tree, two spaces a level: "▶ name"
// opens a suite (or a test with subtests) and a result line of the same name
// at the same depth closes it, so only a "✖" that closes nothing is a failing
// test. The "✖ failing tests:" section that follows repeats leaf titles
// without their path and is not read, except for what marks a file that
// failed to load: its entry's error is node's own 'test failed'.
func nodeSpecFailures(out string) []string {
	lines := strings.Split(out, "\n")
	tail := slices.Index(lines, "✖ failing tests:")
	if tail < 0 {
		tail = len(lines)
	}
	notLoaded := map[string]bool{}
	for i := tail; i+1 < len(lines); i++ {
		if name, ok := strings.CutPrefix(lines[i], "✖ "); ok && strings.TrimSpace(lines[i+1]) == "'test failed'" {
			notLoaded[nodeSpecDuration.ReplaceAllString(name, "")] = true
		}
	}
	var stack, ids []string
	for _, line := range lines[:tail] {
		trimmed := strings.TrimLeft(line, " ")
		depth := (len(line) - len(trimmed)) / 2
		mark, name, ok := strings.Cut(trimmed, " ")
		if !ok || depth > len(stack) {
			continue
		}
		name = nodeSpecDuration.ReplaceAllString(name, "")
		switch {
		case mark == "▶":
			stack = append(stack[:depth], name)
		case len(stack) == depth+1 && stack[depth] == name:
			stack = stack[:depth] // the line closing a suite
		case mark == "✖" && !(depth == 0 && notLoaded[name]):
			ids = append(ids, strings.Join(append(slices.Clone(stack[:depth]), name), "::"))
		}
	}
	return ids
}

// rspecFailures reads the "Failed examples:" list, one
// "rspec ./spec/f_spec.rb:3 # Calc adds (a+b)" line per failing example. The
// id is the full description: the list does not say where the leaf title
// starts, and the full description is what -E matches. A file that failed to
// load prints no such list.
func rspecFailures(out string) []string {
	lines := strings.Split(out, "\n")
	start := slices.Index(lines, "Failed examples:")
	if start < 0 {
		return nil
	}
	var ids []string
	for _, line := range lines[start+1:] {
		rest, ok := strings.CutPrefix(line, "rspec ")
		if !ok {
			continue
		}
		if _, full, ok := strings.Cut(rest, " # "); ok && full != "" {
			ids = append(ids, full)
		}
	}
	return ids
}

var minitestEntry = regexp.MustCompile(`^\s*\d+\) (Failure|Error):$`)

// minitestFailures reads minitest's "  N) Failure:" / "  N) Error:" entries:
// the next line is "Class#method [file:line]:" (a failure) or
// "Class#method:" (an error).
func minitestFailures(out string) []string {
	lines := strings.Split(out, "\n")
	var ids []string
	for i := 0; i+1 < len(lines); i++ {
		if !minitestEntry.MatchString(lines[i]) {
			continue
		}
		name := strings.TrimSuffix(strings.TrimSpace(lines[i+1]), ":")
		if j := strings.Index(name, " ["); j >= 0 {
			name = name[:j]
		}
		if strings.Contains(name, "#") && !strings.ContainsAny(name, " \t") {
			ids = append(ids, name)
		}
	}
	return ids
}

// phpFailures is every "N) Class::method" line of PHPUnit's failure and
// error sections, verbatim (see phpFirstFailure).
func phpFailures(out string) []string {
	var ids []string
	for _, line := range strings.Split(out, "\n") {
		if id, ok := phpFailureID(line); ok {
			ids = append(ids, id)
		}
	}
	return ids
}

// goFailedTests is every top-level test `go test` reported failing, once
// each: -skip takes one pattern per subtest level, so a failing subtest
// skips its whole parent — a narrower exam, never a wrong one.
func goFailedTests(out string) []string {
	var ids []string
	for _, line := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "--- FAIL: ")
		if !ok {
			continue
		}
		name, _, _ := strings.Cut(rest, " ")
		name, _, _ = strings.Cut(name, "/")
		if name != "" && !slices.Contains(ids, name) {
			ids = append(ids, name)
		}
	}
	return ids
}

// The skip commands: argv rerun without the failing ids, each in its
// runner's own exclusion, verified against the real runner by
// scripts/capture-runner-fixtures.sh (its *-skip checks). Each refuses where
// the row's one-test command refuses — the operator's own name filter would
// be ORed with the skip (rspec's -E) or replaced by it (jest's -t) — and
// when there is nothing to skip.

// alternation is ids, each made literal by quote, joined by "|".
func alternation(ids []string, quote func(string) string) string {
	q := make([]string, len(ids))
	for i, id := range ids {
		q[i] = quote(id)
	}
	return strings.Join(q, "|")
}

// jsNames quotes an id's full name for a JavaScript regex.
func jsNames(id string) string { return jsRegexQuote(fullName(id)) }

// jestDeselectCmd skips by a negative lookahead over full names: jest has no
// skip flag, and -t matches the full name. jest matches -t case-insensitively,
// so a passing test whose name differs only in case is skipped too — a
// narrower exam, never a wrong one.
func jestDeselectCmd(argv, ids []string) ([]string, bool) {
	if len(ids) == 0 || !narrowable(argv, jestFilters...) {
		return nil, false
	}
	return append(slices.Clone(argv), "--testNamePattern", "^(?!(?:"+alternation(ids, jsNames)+")$)"), true
}

// vitestDeselectCmd is jest's lookahead on vitest's -t. The command is the
// operator's as it stands: it already ran once to fail, so no mode is added.
func vitestDeselectCmd(argv, ids []string) ([]string, bool) {
	if len(ids) == 0 || vitestIndex(argv) < 0 || !narrowable(argv, vitestFilters...) {
		return nil, false
	}
	return append(slices.Clone(argv), "-t", "^(?!(?:"+alternation(ids, jsNames)+")$)"), true
}

// mochaDeselectCmd inverts a grep over full titles.
func mochaDeselectCmd(argv, ids []string) ([]string, bool) {
	if len(ids) == 0 || !narrowable(argv, mochaFilters...) {
		return nil, false
	}
	full := func(id string) string { return jsRegexQuote(lastFull(id)) }
	return append(slices.Clone(argv), "--grep", "^(?:"+alternation(ids, full)+")$", "--invert"), true
}

// nodeDeselectCmd uses --test-skip-pattern (node 22.1 or later; an older node
// rejects it and the salvage fails closed). A lookahead in
// --test-name-pattern does not skip: node also runs a test whose suite's own
// name matches (measured on node 22.23).
func nodeDeselectCmd(argv, ids []string) ([]string, bool) {
	if len(ids) == 0 || !narrowable(argv, nodeFilters...) {
		return nil, false
	}
	return nodeWith(argv, "", "--test-skip-pattern", "^(?:"+alternation(ids, jsNames)+")$")
}

// rspecDeselectCmd excludes by a negative lookahead in -E (rspec-core 3.12 or
// later). Its ids are full descriptions (rspecFailures), quoted verbatim.
func rspecDeselectCmd(argv, ids []string) ([]string, bool) {
	if len(ids) == 0 || !narrowable(argv, rspecFilters...) {
		return nil, false
	}
	return append(slices.Clone(argv), "-E", `\A(?!(?:`+alternation(ids, regexQuoter.Replace)+`)\z)`), true
}

var minitestFilters = []string{"-n", "--name", "-e", "--exclude"}

// minitestDeselectCmd passes --exclude to the test file, which only `ruby
// <file.rb>` hands it: rake and testrb take no arguments for the tests.
func minitestDeselectCmd(argv, ids []string) ([]string, bool) {
	if len(ids) == 0 || len(argv) < 2 || filepath.Base(argv[0]) != "ruby" || !narrowable(argv, minitestFilters...) ||
		!slices.ContainsFunc(argv[1:], func(a string) bool { return strings.HasSuffix(a, ".rb") }) {
		return nil, false
	}
	return append(slices.Clone(argv), "--exclude", `/\A(?:`+alternation(ids, regexQuoter.Replace)+`)\z/`), true
}

// phpunitDeselectCmd uses --exclude-filter (PHPUnit 10 or later) over
// Class::method, without namespace or data set: a failing data set skips the
// method's other sets too — narrower, never wrong.
func phpunitDeselectCmd(argv, ids []string) ([]string, bool) {
	if len(ids) == 0 || !narrowable(argv, "--filter", "--exclude-filter") {
		return nil, false
	}
	var methods []string
	for _, id := range ids {
		m, _, _ := strings.Cut(id, " with data set ")
		m = m[strings.LastIndex(m, `\`)+1:]
		if !slices.Contains(methods, m) {
			methods = append(methods, m)
		}
	}
	filter := `/(?:^|\\)(?:` + alternation(methods, regexQuoter.Replace) + `)(?: with data set .*)?$/`
	return append(slices.Clone(argv), "--exclude-filter", filter), true
}

// goDeselectCmd uses -skip (Go 1.20 or later) over top-level test names
// (goFailedTests).
func goDeselectCmd(argv, ids []string) ([]string, bool) {
	if len(ids) == 0 || !goTestCmd(argv) || hasFlag(argv, "-run", "-skip", "--run", "--skip") {
		return nil, false
	}
	return append(slices.Clone(argv), "-skip", "^(?:"+alternation(ids, regexp.QuoteMeta)+")$"), true
}
