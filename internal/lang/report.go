// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"runtime"
	"slices"
	"strings"
)

// ReportCmd is cmd as grading runs it so that corral can read which test
// failed, and whether that changed anything: the runner row's ReportArgs
// added, then the row's ReportHook wrapped around the result. It is the ONE
// function that produces the graded command, applied to EVERY command grading
// hands its sandbox (cmd/corral composes ReportArgv into the scorer's one
// command rewrite, on the jail and on --substrate workspace alike), so each
// command is judged on its own words: the compile check of a pytest run names
// no row and is left alone, while the grading, one-test, killer and salvage
// commands are rewritten.
//
// WHY THE FLAGS. A row's failure readers (FirstFailure, FailedTests) read
// lines the runner prints only under its default report settings, and a
// project's own configuration can change those: pytest prints `FAILED <node
// id>` lines only for the report characters -r asks for, its default is fE,
// and a project whose addopts set -rXs (Kludex/starlette) replaces that
// default, so no line names a failing test. corral then recorded no killing
// test, confirmed every kill by repeating the whole grading run rather than
// running the blamed test alone, and could not salvage an authored test.
// pytest keeps the LAST -r it is given and addopts are read before the
// command line, so -rfE appended to the command restores the lines whatever
// the project's config says.
//
// WHY THE HOOK. The flags answer one setting. --no-summary and -p no:terminal
// hide the same lines and no flag brings them back, and a printed
// line cannot say where an id with a space in it ends. A row with a
// ReportHook has the runner itself record its failures (pytest: a plugin
// reading pytest's own test reports), which no report setting reaches. The
// flags are still added: the printed lines are what a reader falls back to
// when the hook's record is missing or incomplete, and what a person reading
// the run's output sees.
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
//     that point, gains no second copy. An option after them could be one
//     that undoes them (pytest's -vrs cluster sets -r), so then the flags are
//     appended again, which costs nothing.
//
// A hook is not applied everywhere its flags are: not on Windows, where there
// is no POSIX shell to run its wrapper (reportHookGOOS), and not to a command
// the row's hook declines (ReportHook.Wraps: pytest behind tox, nox, docker
// or podman, or a Python given -I or -E). Those keep the flags alone.
//
// Applying it twice changes nothing: a command a hook has wrapped is
// shell-wrapped, which the second application cannot read and leaves alone.
// So every caller that DERIVES a command (a killer narrowed to one test, a
// deselect, fail-fast, a selection) must derive it from the operator's
// command and apply this last; derived from a wrapped command, it would
// silently narrow nothing.
//
// A command left unchanged runs exactly as it did before this existed: its
// failures are read from whatever the runner printed, and where that names
// none corral records no killing test and confirms its kills by repeating the
// run. That is slower, never wrong. cmd is never modified.
func ReportCmd(p Plugin, inv Invocation, cmd []string) ([]string, bool) {
	out, flagged, hooked := reportCmd(p, inv, cmd)
	return out, flagged || hooked
}

// reportCmd is ReportCmd saying which of its two steps changed the command:
// flagged when the row's ReportArgs were added, hooked when the row's
// ReportHook wrapped it. Neither means out is cmd itself.
func reportCmd(p Plugin, inv Invocation, cmd []string) (out []string, flagged, hooked bool) {
	if len(cmd) == 0 || inv.Script != nil || cmdIsShellWrapped(cmd) {
		return cmd, false, false
	}
	r, ok := namedRunner(p, cmd)
	if !ok || (len(r.ReportArgs) == 0 && r.ReportHook == nil) || (inv.Runner != "" && r.ID != inv.Runner) {
		return cmd, false, false
	}
	at := runnerWordIndex(cmd, r)
	if at < 0 {
		return cmd, false, false
	}
	// A shell behind a prefix (`env A=1 sh -c pytest`) is a wrapper too.
	if shellWrappedBefore(cmd, at) {
		return cmd, false, false
	}
	end := len(cmd)
	if i := slices.Index(cmd[at+1:], "--"); i >= 0 {
		end = at + 1 + i
	}
	out = cmd
	if len(r.ReportArgs) > 0 && !carriesLast(cmd[at+1:end], r.ReportArgs) {
		out = make([]string, 0, len(cmd)+len(r.ReportArgs))
		out = append(out, cmd[:end]...)
		out = append(out, r.ReportArgs...)
		out = append(out, cmd[end:]...)
		end += len(r.ReportArgs)
		flagged = true
	}
	if h := r.ReportHook; h != nil && reportHookGOOS != "windows" && (h.Wraps == nil || h.Wraps(cmd, at)) {
		return h.Wrap(out), flagged, true
	}
	return out, flagged, false
}

// reportHookGOOS is the operating system ReportCmd decides a hook's wrapper
// for. A hook's wrapper is a POSIX shell script (sh, mktemp, cat), so on
// Windows no command is wrapped and a row keeps its report flags alone, as
// before hooks existed. A variable only so a test can stand in for Windows.
var reportHookGOOS = runtime.GOOS

// ReportArgv is ReportCmd as the rewrite hook grading composes into its one
// command door: each command is judged on its own words (inv.WithArgv), and
// handed back unchanged where ReportCmd leaves it.
func ReportArgv(p Plugin, inv Invocation) func([]string) []string {
	return func(cmd []string) []string {
		out, _ := ReportCmd(p, inv.WithArgv(cmd), cmd)
		return out
	}
}

// reportDisclosures is the run-disclosure lines for a command ReportCmd
// changes, one for each step that changed it, none for a command it leaves
// alone.
func reportDisclosures(p Plugin, inv Invocation) []string {
	_, flagged, hooked := reportCmd(p, inv, inv.Argv)
	if !flagged && !hooked {
		return nil
	}
	r, _ := namedRunner(p, inv.Argv)
	var out []string
	if flagged {
		out = append(out, r.Name+": corral adds "+strings.Join(r.ReportArgs, " ")+
			" to every graded run so it can read which test failed; a report setting of the project's own that these replace does not apply to those runs (what passes and fails is unchanged)")
	}
	if hooked {
		out = append(out, r.Name+": "+r.ReportHook.Says)
	}
	return out
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
