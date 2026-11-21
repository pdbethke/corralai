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
