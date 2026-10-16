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
// of them.
func nextestListCmd(testCmd []string, _ string) ([]string, bool) {
	head, _ := splitAtDashDash(testCmd)
	i := slices.Index(head, "run")
	if i < 0 {
		return nil, false
	}
	cmd := slices.Clone(head)
	cmd[i] = "list"
	return append(cmd, "--message-format", "oneline"), true
}

// libtestParseList reads libtest's `<id>: test` lines, and nextestParseList
// nextest's one-line `<binary-id> <id>` lines, each ignoring anything else a
// merged stream carries (cargo's indented progress lines, benchmarks).
func libtestParseList(output string) []string {
	return rustListLines(output, func(line string) (string, bool) {
		return strings.CutSuffix(line, ": test")
	})
}

func nextestParseList(output string) []string {
	return rustListLines(output, func(line string) (string, bool) {
		if f := strings.Fields(line); len(f) == 2 {
			return f[1], true
		}
		return "", false
	})
}

func rustListLines(output string, id func(line string) (string, bool)) []string {
	var ids []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		if v, ok := id(line); ok {
			ids = append(ids, v)
		}
	}
	return ids
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
	cmd := append(slices.Clone(head), "-E", "test(="+id+")")
	return append(cmd, tail...), true
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
