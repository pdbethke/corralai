// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"slices"
	"strings"
)

// ReportCmd is cmd with its runner row's ReportArgs applied, and whether it
// changed anything. It is applied to EVERY command grading hands its sandbox
// (cmd/corral composes ReportArgv into the scorer's one command rewrite, on
// the jail and on --substrate workspace alike), so each command is judged on
// its own words: the compile check of a pytest run names no row and is left
// alone, while the grading, one-test, killer and salvage commands get the
// flags.
//
// WHY. A row's failure readers (FirstFailure, FailedTests) read lines the
// runner prints only under its default report settings, and a project's own
// configuration can change those: pytest prints `FAILED <node id>` lines
// only for the report characters -r asks for, its default is fE, and a
// project whose addopts set -rXs (Kludex/starlette) replaces that default,
// so no line names a failing test. corral then recorded no killing test,
// confirmed every kill by repeating the whole grading run rather than running
// the blamed test alone, and could not salvage an authored test. pytest keeps
// the LAST -r it is given and addopts are read before the command line, so
// -rfE appended to the command restores the lines whatever the project's
// config says.
//
// It changes cmd only where it can read it, exactly as FailFastArgsFor and
// RunCacheCmd do, and leaves it unchanged otherwise:
//
//   - the command must NAME the row (namedRunner on its own words, never a
//     language's Default row), and under --runner the named row must be the
//     one the operator chose;
//   - not shell-wrapped (`sh -c 'pytest'`): a word appended there is sh's $0;
//   - not a package script (inv.Script non-nil): what reaches the script is
//     the launcher's business, and no row with ReportArgs runs under one;
//   - the flags go after the runner's word (so `uv run -- pytest` keeps uv's
//     separator) and before the runner's own `--`, after which every word is a
//     path to pytest;
//   - a command already carrying the flags, with no option after them before
//     that point, is unchanged, so applying it twice adds nothing. An option
//     after them could be one that undoes them (pytest's -vrs cluster sets
//     -r), so then the flags are appended again, which costs nothing.
//
// A command left unchanged runs exactly as it did before this existed: corral
// reads no killing test from it and confirms its kills by repeating the run.
// That is slower, never wrong. cmd is never modified.
func ReportCmd(p Plugin, inv Invocation, cmd []string) ([]string, bool) {
	if len(cmd) == 0 || inv.Script != nil || cmdIsShellWrapped(cmd) {
		return cmd, false
	}
	r, ok := namedRunner(p, cmd)
	if !ok || len(r.ReportArgs) == 0 || (inv.Runner != "" && r.ID != inv.Runner) {
		return cmd, false
	}
	at := runnerWordIndex(cmd, r)
	if at < 0 {
		return cmd, false
	}
	// A shell behind a prefix (`env A=1 sh -c pytest`) is a wrapper too:
	// cmdIsShellWrapped reads argv[0] only.
	for i := range at {
		if cmdIsShellWrapped(cmd[i:]) {
			return cmd, false
		}
	}
	end := len(cmd)
	if i := slices.Index(cmd[at+1:], "--"); i >= 0 {
		end = at + 1 + i
	}
	if carriesLast(cmd[at+1:end], r.ReportArgs) {
		return cmd, false
	}
	out := make([]string, 0, len(cmd)+len(r.ReportArgs))
	out = append(out, cmd[:end]...)
	out = append(out, r.ReportArgs...)
	return append(out, cmd[end:]...), true
}

// ReportArgv is ReportCmd as the rewrite hook grading composes into its one
// command door: each command is judged on its own words (inv.WithArgv), and
// handed back unchanged where ReportCmd leaves it.
func ReportArgv(p Plugin, inv Invocation) func([]string) []string {
	return func(cmd []string) []string {
		out, _ := ReportCmd(p, inv.WithArgv(cmd), cmd)
		return out
	}
}

// reportDisclosure is the run-disclosure line for a command ReportCmd
// changes, "" for one it leaves alone.
func reportDisclosure(p Plugin, inv Invocation) string {
	if _, applied := ReportCmd(p, inv, inv.Argv); !applied {
		return ""
	}
	r, _ := namedRunner(p, inv.Argv)
	return r.Name + ": corral adds " + strings.Join(r.ReportArgs, " ") +
		" to every run so it can read which test failed; a report setting of the project's own that these replace does not apply to those runs (what passes and fails is unchanged)"
}

// runnerWordIndex is the index of the first word of cmd that names r by one
// of its Words (cmdHasWord's rule), or -1.
func runnerWordIndex(cmd []string, r Runner) int {
	at := -1
	for _, w := range r.Words {
		if i := wordIndex(cmd, w); i >= 0 && (at < 0 || i < at) {
			at = i
		}
	}
	return at
}

// carriesLast reports whether args appear in opts as consecutive words with
// no option (a word starting with "-") after them: nothing in opts can then
// undo them.
func carriesLast(opts, args []string) bool {
	for i := len(opts) - len(args); i >= 0; i-- {
		if !slices.Equal(opts[i:i+len(args)], args) {
			continue
		}
		return !slices.ContainsFunc(opts[i+len(args):], func(w string) bool { return strings.HasPrefix(w, "-") })
	}
	return false
}
