// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"slices"
	"strings"
)

// versionAtWord is a Runner.VersionCmd for a runner a command names by one
// word: argv up to and including that word, then --version. Everything the
// operator put after it (flags, files) is dropped, because it is the runner's
// version being asked and not a run. `python3 -m pytest -q` keeps its
// launcher, which is how pytest is reached when it has no binary on PATH.
func versionAtWord(word string) func(argv []string) ([]string, bool) {
	return func(argv []string) ([]string, bool) {
		i := wordIndex(argv, word)
		if i < 0 {
			return nil, false
		}
		return append(slices.Clone(argv[:i+1]), "--version"), true
	}
}

// vitestVersionCmd is argv up to vitest's own binary or entry script, then
// --version (vitestIndex says where that is, including the host-interpreter
// form AGENTS.md gives for the jail).
func vitestVersionCmd(argv []string) ([]string, bool) {
	i := vitestIndex(argv)
	if i < 0 {
		return nil, false
	}
	return append(slices.Clone(argv[:i+1]), "--version"), true
}

// FirstLine is the first non-blank line of a version command's output,
// trimmed; "" when there is none. It is what the ledger records, because a
// version banner's later lines (copyright, build options) are noise that would
// make two runs on one toolchain differ.
func FirstLine(out string) string {
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}
