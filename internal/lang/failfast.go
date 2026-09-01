// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"path/filepath"
	"strings"
)

// FailFaster is an OPTIONAL plugin extension: the runner's own
// stop-at-first-failure flag, for MUTANT runs only.
//
// WHY IT EXISTS. Scoring costs O(mutants × the target's suite runtime), and a
// KILLED mutant needs exactly ONE failing test to be killed — the rest of the
// selected set is paid for and then discarded. pytest -x, go test -failfast,
// jest --bail and phpunit --stop-on-failure all stop at the first failure, so
// the average killed mutant costs the tests up to its killer instead of the
// whole set. `killed_by` already records the FIRST failure the runner named,
// so nothing recorded changes either.
//
// IT MUST NEVER BE USED ON THE BASELINE. A green baseline is the claim that
// the suite passes; a baseline that stopped early would certify a suite corral
// never fully ran.
//
// THE FLAG IS THE RUNNER'S, NOT THE LANGUAGE'S. A Python repo may be graded by
// pytest or by unittest; a JS repo by node --test, jest, mocha or vitest, and
// only some of those have such a flag. So the decision is made from the
// COMMAND, and a command whose runner corral does not recognise gets ok=false
// — no flag, unchanged behaviour. This matters more than it looks: an
// unrecognised flag makes the runner exit NON-ZERO, which the scorer reads as
// a KILL, so a wrong guess here would silently inflate every kill rate to
// 1.00. adequacy re-runs the compliant baseline once WITH these args before
// trusting them for exactly that reason; this list stays conservative anyway.
type FailFaster interface {
	// FailFastArgs returns the arguments to APPEND to testCmd so its runner
	// stops at the first failing test. ok=false when this command's runner
	// has no such flag corral is sure of.
	FailFastArgs(testCmd []string) (args []string, ok bool)
}

// cmdHasWord reports whether any argv word IS name (or a path ending in it,
// e.g. .venv/bin/pytest). Whole-word only, deliberately: a substring match
// would fire on `sh -c "pytest ..."`, where appending a flag adds a positional
// argument to sh — which becomes $0 — instead of an option to the runner.
func cmdHasWord(cmd []string, name string) bool {
	for _, a := range cmd {
		if a == name || filepath.Base(a) == name || filepath.Base(a) == name+".exe" {
			return true
		}
	}
	return false
}

// cmdIsShellWrapped reports the `sh -c '<script>'` shape, where argv is not
// the runner's argv at all and nothing may be appended to it.
func cmdIsShellWrapped(cmd []string) bool {
	if len(cmd) == 0 {
		return false
	}
	switch filepath.Base(cmd[0]) {
	case "sh", "bash", "zsh", "dash":
		for _, a := range cmd[1:] {
			if a == "-c" {
				return true
			}
		}
	}
	return false
}

// AppendFailFast appends args to cmd, unless there is nothing to append, the
// command is shell-wrapped, or a flag from args is already present (an
// operator who wrote -x themselves must not get a second one).
//
// It NEVER inserts: appending is the only position that is correct for every
// runner here (`python -m pytest` would break on an insert after argv[0]), and
// pytest, go test, jest, mocha and phpunit all accept their options after
// their positional arguments.
func AppendFailFast(cmd, args []string) []string {
	if len(cmd) == 0 || len(args) == 0 || cmdIsShellWrapped(cmd) {
		return cmd
	}
	for _, a := range args {
		flag := strings.SplitN(a, "=", 2)[0]
		for _, c := range cmd {
			if c == flag || strings.HasPrefix(c, flag+"=") {
				return cmd
			}
		}
	}
	out := make([]string, 0, len(cmd)+len(args))
	out = append(out, cmd...)
	return append(out, args...)
}

// FailFastArgsFor is the one lookup a caller needs: the plugin's own
// stop-at-first-failure args for testCmd, or ok=false for a plugin that does
// not implement FailFaster at all (which is simply unchanged behaviour).
func FailFastArgsFor(p Plugin, testCmd []string) ([]string, bool) {
	ff, ok := p.(FailFaster)
	if !ok {
		return nil, false
	}
	return ff.FailFastArgs(testCmd)
}
