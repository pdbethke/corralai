// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"regexp"
	"slices"
	"strings"
)

// The failure parsers. Each reads its runner's DEFAULT reporter as the jail
// runs it (not a TTY) and returns every failing test, in report order, in the
// form the runner's list gives it, so a killed_by label is the same string
// the critic list and --matrix use. A row's FirstFailure is firstOf its
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
// not load) and "Console" (captured console output) are not tests.
func jestFailures(out string) []string {
	var ids []string
	for _, line := range strings.Split(out, "\n") {
		name, ok := strings.CutPrefix(line, "  ● ")
		if !ok || name == "Test suite failed to run" || strings.HasPrefix(name, "Console") {
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

// nodeFailures reads the TAP report node prints off a TTY: every failing
// result of type test with no subtests. A suite, or a test whose subtests
// failed, is rolled up as failed and is not itself the failure; a result
// carrying exitCode is the file failing to load.
func nodeFailures(out string) []string {
	entries, hasChild := nodeEntries(out)
	var ids []string
	for _, e := range entries {
		if !e.ok && e.typ == "test" && !e.exit && !hasChild[nodeKey(e.path)] {
			ids = append(ids, strings.Join(e.path, "::"))
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
