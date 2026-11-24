// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"slices"
	"strings"
)

// libtestListCmd lists the tests a `cargo test` command runs: `--list` among
// the test binaries' arguments (after `--`, appended to any the operator
// already passes).
func libtestListCmd(testCmd []string, _ string) ([]string, bool) {
	return withTestBinaryArgs(testCmd, "--list"), len(testCmd) > 0
}

// nextestListCmd lists the tests a `cargo nextest run` command runs: its own
// `list` subcommand in place of `run`, in its one-line format, with the
// operator's arguments to the test binaries dropped since listing runs none
// of them, and the run-only options list refuses (nextestListArgs).
func nextestListCmd(testCmd []string, _ string) ([]string, bool) {
	head, _ := splitAtDashDash(testCmd)
	i := slices.Index(head, "run")
	if i < 0 {
		return nil, false
	}
	cmd := append(append(slices.Clone(head[:i]), "list"), nextestListArgs(head[i+1:])...)
	return append(cmd, "--message-format", "oneline"), true
}

// libtestParseList reads libtest's `<id>: test` lines, and nextestParseList
// nextest's one-line `<binary-id> <id>` lines, each ignoring anything else a
// merged stream carries (cargo's indented progress lines, benchmarks).
func libtestParseList(output string) []string {
	return listLines(output, func(line string) (string, bool) {
		return strings.CutSuffix(line, ": test")
	})
}

func nextestParseList(output string) []string {
	return listLines(output, func(line string) (string, bool) {
		if f := strings.Fields(line); len(f) == 2 {
			return f[1], true
		}
		return "", false
	})
}

// libtestSingleCmd narrows a `cargo test` command to the one test id with
// libtest's `--exact <id>`.
func libtestSingleCmd(testCmd []string, _, id string) ([]string, bool) {
	if len(testCmd) == 0 || id == "" {
		return nil, false
	}
	return withTestBinaryArgs(testCmd, "--exact", id), true
}

// nextestSingleCmd narrows a nextest command to the one test id with
// `-E 'test(=<id>)'` placed before any `--`. A command that already carries a
// filterset is refused, because nextest unions filtersets and the run would
// include its tests too.
func nextestSingleCmd(testCmd []string, _, id string) ([]string, bool) {
	if len(testCmd) == 0 || id == "" {
		return nil, false
	}
	head, tail := splitAtDashDash(testCmd)
	for _, a := range head {
		if a == "-E" || a == "--filterset" || a == "--filter-expr" ||
			strings.HasPrefix(a, "--filterset=") || strings.HasPrefix(a, "--filter-expr=") {
			return nil, false
		}
	}
	if shortBundleSets(head, nextestShortFlags, "E") {
		return nil, false
	}
	cmd := append(slices.Clone(head), "-E", "test(="+id+")")
	return append(cmd, tail...), true
}

// nextestIndex is where nextest's word is in argv (`cargo nextest`, or the
// cargo-nextest binary), -1 if neither.
func nextestIndex(argv []string) int {
	return firstIndex(wordIndex(argv, "nextest"), wordIndex(argv, "cargo-nextest"))
}

// cargoKillerCmd narrows a `cargo test` run to the test its failure reader
// named, with libtest's `--exact <id>` (libtestSingleCmd). Only a command
// cargoTestSplit can read is narrowed: one with no test-name filter of its
// own and no `--skip`, `--ignored`, `--list` or unknown option among the test
// binaries' arguments, so `--exact` is the only name filter the binaries get.
// The id is a libtest test path (`tests::adds`); one with a space in it is a
// nextest name (`<binary> <test>`, printed by a wrapper that runs nextest)
// and is refused. A test of the same path in another of the run's test
// binaries runs too: libtest's filter names no binary.
func cargoKillerCmd(argv []string, id string) ([]string, bool) {
	if id == "" || strings.ContainsAny(id, " \t") {
		return nil, false
	}
	if _, _, _, ok := cargoTestSplit(argv); !ok {
		return nil, false
	}
	return libtestSingleCmd(argv, "", id)
}

// nextestKillerCmd narrows a `cargo nextest run` to the test its failure
// reader named: nextest prints a failure as `<binary id> <test>`, and the
// filterset `binary_id(=<binary id>) & test(=<test>)` selects exactly that
// test of that binary. A command nextestRunSplit cannot read, one with a
// filterset of its own (nextest unions filtersets), or an id with no binary
// is refused. A name filter of the operator's own still applies, and nextest
// intersects it with the filterset, so the run can only narrow.
func nextestKillerCmd(argv []string, id string) ([]string, bool) {
	bin, test, ok := strings.Cut(id, " ")
	if !ok || bin == "" || test == "" || strings.ContainsAny(test, " \t") {
		return nil, false
	}
	pre, opts, tail, ok := nextestRunSplit(argv)
	if !ok {
		return nil, false
	}
	cmd := append(slices.Clone(pre), "run")
	cmd = append(cmd, opts...)
	cmd = append(cmd, "-E", "binary_id(="+bin+") & test(="+test+")")
	if tail != nil {
		cmd = append(cmd, tail...)
	}
	return cmd, true
}

// rustFailedTests is the failing tests' ids in a cargo test or nextest run's
// output, in the form each runner names them (rustRun.Failures).
func rustFailedTests(output string) []string {
	return parseRustRun(rustGrammarOf([]byte(output)), []byte(output)).Failures
}

// cargoDeselectCmd reruns a `cargo test` command without the tests ids, with
// libtest's `--exact --skip <path>` per id among the test binaries' arguments.
// --exact is what makes each skip name one test: libtest's --skip matches a
// substring, and `--skip tests::a` alone skipped a test it merely prefixed
// (cargo 1.90.0). Only a command cargoTestSplit reads is rerun (no name
// filter, --skip, --ignored or --list of its own; --exact would change what
// such a filter matches), and only libtest ids: one with a space is a
// nextest name. A test of the same path in another of the run's test
// binaries is skipped too, since libtest's skip names no binary; it can
// only make the rerun run less, never a test the run did not.
func cargoDeselectCmd(argv, ids []string) ([]string, bool) {
	if len(ids) == 0 || slices.ContainsFunc(ids, func(id string) bool { return id == "" || strings.ContainsAny(id, " \t") }) {
		return nil, false
	}
	_, _, tail, ok := cargoTestSplit(argv)
	if !ok {
		return nil, false
	}
	var args []string
	if !slices.Contains(tail, "--exact") {
		args = append(args, "--exact")
	}
	for _, id := range ids {
		args = append(args, "--skip", id)
	}
	return withTestBinaryArgs(argv, args...), true
}

// nextestDeselectCmd reruns a `cargo nextest run` without the tests ids, with
// one filterset that excludes each failing test by its binary and its name:
// `not ((binary_id(=<b>) & test(=<t>)) | …)`. nextest names a failure
// `<binary id> <test>`, so a test of the same name in another binary still
// runs. The command's own `-E` filtersets are folded in (nextestFold), so a
// unit-graded run can be salvaged. An id with no binary, or with a character
// that would end a matcher early, is refused.
func nextestDeselectCmd(argv, ids []string) ([]string, bool) {
	if len(ids) == 0 {
		return nil, false
	}
	terms := make([]string, 0, len(ids))
	for _, id := range ids {
		bin, test, ok := strings.Cut(id, " ")
		if !ok || bin == "" || test == "" || strings.ContainsAny(id, "()|&,\t\"'\\") || strings.Contains(test, " ") {
			return nil, false
		}
		terms = append(terms, "(binary_id(="+bin+") & test(="+test+"))")
	}
	return nextestFold(argv, "not ("+strings.Join(terms, " | ")+")")
}

// nextestSelectCmd reruns a `cargo nextest run` with only the tests ids, by
// exact name within the binaries the command already selects:
// `(<its filtersets>) & (test(=<a>) | test(=<b>) …)`. The ids are test
// names without a binary (libtest paths, lang.InlineTestIDs): the command's
// own unit filterset names the binary. An id with a space or a character
// that would end a matcher early is refused.
func nextestSelectCmd(argv, ids []string) ([]string, bool) {
	if len(ids) == 0 {
		return nil, false
	}
	terms := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" || strings.ContainsAny(id, " ()|&,\t\"'\\") {
			return nil, false
		}
		terms = append(terms, "test(="+id+")")
	}
	return nextestFold(argv, "("+strings.Join(terms, " | ")+")")
}

// nextestFold reruns a `cargo nextest run` under ONE filterset: expr,
// intersected with the command's own.
//
// nextest unions the filtersets it is given, so expr cannot sit beside the
// command's own: their tests would come back. The command's `-E <expr>`
// filtersets, the form the unit command writes (nextestUnitsCmd), are taken
// out and folded in, `((e1) | (e2)) & expr`, which runs what they ran
// narrowed by expr. Any other spelling of a filterset, and any command
// nextestRunSplit refuses for another reason, is refused. A name filter of
// the operator's still applies, intersected with the filterset.
func nextestFold(argv []string, expr string) ([]string, bool) {
	head, rest := splitAtDashDash(argv)
	var kept, sets []string
	for i := 0; i < len(head); i++ {
		if head[i] != "-E" {
			kept = append(kept, head[i])
			continue
		}
		if i+1 >= len(head) || head[i+1] == "" {
			return nil, false
		}
		sets = append(sets, "("+head[i+1]+")")
		i++
	}
	pre, opts, tail, ok := nextestRunSplit(append(kept, rest...))
	if !ok {
		return nil, false
	}
	if len(sets) > 0 {
		expr = "(" + strings.Join(sets, " | ") + ") & " + expr
	}
	cmd := append(slices.Clone(pre), "run")
	cmd = append(cmd, opts...)
	cmd = append(cmd, "-E", expr)
	return append(cmd, tail...), true
}

// cargoSelectCmd reruns a `cargo test` command with only the tests ids:
// `--exact` and one filter per id among the test binaries' arguments.
// libtest ORs its filters, and --exact makes each match one test path
// rather than every path it is a substring of (several exact filters
// checked by hand on cargo 1.90). Only a command cargoTestSplit reads is
// rerun (no name filter, --skip, --ignored or --list of its own), and only
// libtest ids: one with a space is a nextest name. A test of the same path
// in another of the run's test binaries runs too, since libtest's filter
// names no binary; a one-unit command runs one binary.
func cargoSelectCmd(argv, ids []string) ([]string, bool) {
	if len(ids) == 0 || slices.ContainsFunc(ids, func(id string) bool { return id == "" || strings.ContainsAny(id, " \t") || strings.HasPrefix(id, "-") }) {
		return nil, false
	}
	_, _, tail, ok := cargoTestSplit(argv)
	if !ok {
		return nil, false
	}
	var args []string
	if !slices.Contains(tail, "--exact") {
		args = append(args, "--exact")
	}
	return withTestBinaryArgs(argv, append(args, ids...)...), true
}

func isNextest(testCmd []string) bool {
	return cmdHasWord(testCmd, "nextest") || cmdHasWord(testCmd, "cargo-nextest")
}

// splitAtDashDash splits argv at its first "--": head is cargo's side, tail
// starts with the "--" itself (nil when there is none).
func splitAtDashDash(argv []string) (head, tail []string) {
	if i := slices.Index(argv, "--"); i >= 0 {
		return argv[:i], argv[i:]
	}
	return argv, nil
}

// withTestBinaryArgs appends args to the arguments `cargo test` hands its
// test binaries, adding the "--" that separates them when there is none.
func withTestBinaryArgs(testCmd []string, args ...string) []string {
	cmd := slices.Clone(testCmd)
	if !slices.Contains(cmd, "--") {
		cmd = append(cmd, "--")
	}
	return append(cmd, args...)
}
