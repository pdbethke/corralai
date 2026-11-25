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

// sgr is an ANSI colour or style sequence (ESC [ params m), and the cursor
// and erase sequences a reporter may print beside them.
var sgr = regexp.MustCompile(`\x1b\[[0-9;?]*[A-Za-z]`)

// plainText is out with its terminal escapes removed: what every parser here
// reads, through the row accessors (FailureParserFor, DeselectorFor,
// FailedUnitsFor). vitest 5 colours its report with no TTY and no colour
// variable (measured on 5.0.3 under the jail's environment,
// testdata/runners/vitest5-fail.txt), so its " FAIL " heading reached the
// parser as ESC[41mESC[1m FAIL ESC[22mESC[49m and named nothing: no killed_by
// label, no salvage, on every vitest 5 run.
func plainText(out string) string {
	if !strings.Contains(out, "\x1b") {
		return out
	}
	return sgr.ReplaceAllString(out, "")
}

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

// vitestFailedUnits reads the same headings for the file each names, a file
// that failed to load included (" FAIL  f.test.mjs [ f.test.mjs ]"), each
// once. A heading under a vitest project carries the project's name before
// the file: "|unit| f.test.mjs" with colour off, and " unit  f.test.mjs"
// once the colour that framed it is removed (vitest 5.0.3,
// testdata/runners/vitest5-fail-projects*.txt).
func vitestFailedUnits(out string) []string {
	var units []string
	for _, line := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(line, " FAIL  ")
		if !ok {
			continue
		}
		rest = withoutVitestProject(rest)
		file, _, _ := strings.Cut(rest, " > ")
		file, _, _ = strings.Cut(file, " [ ")
		if file = strings.TrimSpace(file); file != "" && !slices.Contains(units, file) {
			units = append(units, file)
		}
	}
	return units
}

// withoutVitestProject is a FAIL heading's remainder without the project
// name vitest prints before the file, in either of its two forms.
func withoutVitestProject(rest string) string {
	if strings.HasPrefix(rest, "|") {
		if _, after, ok := strings.Cut(rest[1:], "| "); ok {
			return after
		}
		return rest
	}
	if strings.HasPrefix(rest, " ") {
		if _, after, ok := strings.Cut(strings.TrimPrefix(rest, " "), "  "); ok {
			return after
		}
	}
	return rest
}

// jestFailedUnits reads jest's "FAIL <file>" line, one per failing file, a
// file that failed to load included. A line with more after the file (a
// project's displayName, which no fixture here shows) names nothing rather
// than a guess.
func jestFailedUnits(out string) []string {
	var units []string
	for _, line := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimLeft(line, " "), "FAIL ")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		if len(f) != 1 || slices.Contains(units, f[0]) {
			continue
		}
		units = append(units, f[0])
	}
	return units
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
// subtests failed, is rolled up as failed and is not itself the failure, a
// file that failed to load is not a test, and a todo test that failed is
// not a failure (node does not fail the run for it; node 23's spec report
// leaves it out, and TAP marks it `# TODO`).
func nodeFailures(out string) []string {
	if !strings.Contains(out, "TAP version") {
		return nodeSpecFailures(out)
	}
	entries, hasChild := nodeEntries(out)
	var ids []string
	for _, e := range entries {
		if !e.ok && !e.todo && e.typ == "test" && !e.exit && !hasChild[nodeKey(e.path)] {
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

// phpFailures is every test PHPUnit reported failing or erroring, in the id
// form phpFailureID gives (see phpFirstFailure).
//
// Two sources, both read. The run's own record, when the output carries a
// complete, well-formed JUnit block (the PHPUnit row's failure hook,
// phpunitRecordedFailures), comes first, errors then failures. Then every
// name PHPUnit's printed "N) Class::method" lines give that the record does
// not, in printed order; the printed lines are read with the record block
// left out (withoutRecordBlock), so a line inside the record is never read
// as a printed one. With no record (a command corral does not wrap, output
// recorded before the hook existed, a block the output cap cut, a record
// PHPUnit did not write or that is not well-formed) the printed lines are
// the whole answer, as they were before the hook.
//
// The union is there because the record is not a superset of the printed
// lines (PHPUnit 11.5.57, capture-runner-fixtures.sh phpunit-hook-edge.txt):
// a setUpBeforeClass that throws is printed as `N) Class` and a data
// provider that throws as `N) Class::method` (a "PHPUnit error"), and
// neither is in the JUnit record at all, so a record read alone would name
// fewer tests than the printed lines did, and a run killed only by such an
// error would name no killing test. Adding the printed names is safe in the
// way the printed lines always were: a name here is only a candidate, and a
// kill is credited to it only when it, run alone, fails on the fault and
// passes on the clean code (adequacy's kill confirmation); a salvage that
// deselects a name too many narrows the exam, never widens it. That
// includes the one name the union adds which is not a failure: the printed
// reader takes every numbered section, so a risky test (PHPUnit 11's
// default strictness reports a test with no assertion as risky, in a
// numbered section of its own) is named, as it was before the hook. The
// record lists that test as a plain, passing testcase, never as an <error>
// (measured), so the record adds no name the printed lines do not consider
// failing, and nothing is dropped from it: under a testdox configuration the
// printed lines name nothing at all and the record is the only source.
func phpFailures(out string) []string {
	return recordThenPrinted(out, phpunitRecordedFailures, phpPrintedFailures, func(id, name string) bool { return id == name })
}

// recordThenPrinted is the failing tests of a run whose row has a
// record-file hook: the ONE rule every such row's reader follows, so that a
// row adopting the hook adds a record reader and a printed reader and never
// a union of its own.
//
// recorded reads the run's record (ok false when there is none to trust:
// no complete block, another format, a record its reader refuses); printed
// reads the runner's printed lines, and is handed the output with the record
// block taken out (withoutRecordBlock), so a line inside the record is never
// read as a printed one. With no record the printed names are the whole
// answer, as before the hook. With one, the record's ids come first, then
// each printed name that no recorded id stands for (names(id, name): the
// same test, written as the printed lines write it), in printed order, each
// once.
//
// The printed names are added because no runner's record has yet been a
// superset of its printed lines (PHPUnit's omits errors outside a test
// method; swift-testing's omits a crashed process's tests), so a record read
// alone would name fewer tests than the printed lines did. A name is only a
// candidate: a kill is credited to a named test only where adequacy's kill
// confirmation runs it alone, and a salvage that deselects a name too many
// narrows the exam, never widens it.
func recordThenPrinted(out string, recorded func(string) ([]string, bool), printed func(string) []string, names func(id, name string) bool) []string {
	shown := printed(withoutRecordBlock(out))
	ids, ok := recorded(out)
	if !ok {
		return shown
	}
	for _, name := range shown {
		if !slices.ContainsFunc(ids, func(id string) bool { return names(id, name) }) {
			ids = append(ids, name)
		}
	}
	return ids
}

// phpPrintedFailures is every "N) Class::method" line of PHPUnit's printed
// numbered sections, in printed order.
func phpPrintedFailures(out string) []string {
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
		name, ok := goFailHeading(line)
		if !ok {
			continue
		}
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
	if len(ids) == 0 || !jestNarrowable(argv) {
		return nil, false
	}
	return append(slices.Clone(argv), "--testNamePattern", "^(?!(?:"+alternation(ids, jsNames)+")$)"), true
}

// vitestDeselectCmd is jest's lookahead on vitest's -t. The command is the
// operator's as it stands: it already ran once to fail, so no mode is added.
func vitestDeselectCmd(argv, ids []string) ([]string, bool) {
	if len(ids) == 0 || vitestIndex(argv) < 0 || !vitestNarrowable(argv) {
		return nil, false
	}
	return append(slices.Clone(argv), "-t", "^(?!(?:"+alternation(ids, vitestName)+")$)"), true
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
// (goFailedTests), placed where go test's options end (goTestOptionsEnd,
// the rule fail-fast uses): after -args it would be the test binary's, which
// refuses it as a flag it does not define (go 1.26.11). Only go's own words
// are searched for an operator's -run or -skip; one after -args is the
// binary's.
func goDeselectCmd(argv, ids []string) ([]string, bool) {
	if len(ids) == 0 || !goTestCmd(argv) {
		return nil, false
	}
	end := goTestOptionsEnd(argv, goTestAt(argv))
	if hasFlag(argv[:end], "-run", "-skip", "--run", "--skip") {
		return nil, false
	}
	return slices.Insert(slices.Clone(argv), end, "-skip", "^(?:"+alternation(ids, regexp.QuoteMeta)+")$"), true
}
