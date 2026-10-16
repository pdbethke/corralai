// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"slices"
	"strings"
)

// ListTestsCmdFor lists the tests the operator's command runs. libtest gets
// `--list` among the test binaries' arguments (after `--`, appended to any
// the operator already passes); nextest gets its own `list` subcommand in
// place of `run`, in its one-line format, with the operator's arguments to
// the test binaries dropped since listing runs none of them.
func (rustPlugin) ListTestsCmdFor(testCmd []string) ([]string, bool) {
	if isNextest(testCmd) {
		head, _ := splitAtDashDash(testCmd)
		i := slices.Index(head, "run")
		if i < 0 {
			return nil, false
		}
		cmd := slices.Clone(head)
		cmd[i] = "list"
		return append(cmd, "--message-format", "oneline"), true
	}
	return withTestBinaryArgs(testCmd, "--list"), len(testCmd) > 0
}

// ParseTestListFor reads libtest's `<id>: test` lines or nextest's one-line
// `<binary-id> <id>` lines, ignoring anything else a merged stream carries
// (cargo's indented progress lines, benchmarks).
func (rustPlugin) ParseTestListFor(testCmd []string, output string) []string {
	nextest := isNextest(testCmd)
	var ids []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		if nextest {
			if f := strings.Fields(line); len(f) == 2 {
				ids = append(ids, f[1])
			}
			continue
		}
		if id, ok := strings.CutSuffix(line, ": test"); ok {
			ids = append(ids, id)
		}
	}
	return ids
}

// SingleTestCmdFor narrows the operator's command to the one test id:
// libtest's `--exact <id>`, or nextest's `-E 'test(=<id>)'` placed before any
// `--`. A nextest command that already carries a filterset is refused,
// because nextest unions filtersets and the run would include its tests too.
func (rustPlugin) SingleTestCmdFor(testCmd []string, id string) ([]string, bool) {
	if len(testCmd) == 0 || id == "" {
		return nil, false
	}
	if isNextest(testCmd) {
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
	return withTestBinaryArgs(testCmd, "--exact", id), true
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
