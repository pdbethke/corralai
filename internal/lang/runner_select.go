// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"strings"
)

// The helpers the name-pattern runner rows share (jest, vitest, mocha,
// node --test, rspec): their ids are a test's titles from the outermost
// describe in, joined by "::" so ResolveListedTest can match a critic's bare
// test name as a suffix, and each runner selects one test by an anchored
// pattern over the same titles joined by spaces — its "full name".

// fullName is the name a name-pattern runner matches an id by.
func fullName(id string) string { return strings.ReplaceAll(id, "::", " ") }

// fullNameColl is Coruns for a runner that selects by full name: other runs
// too when its full name is the same. Two describe paths can join to one full
// name (`Calc > adds` and a top-level `Calc adds`); measured on jest and
// mocha, a pattern for one runs both.
func fullNameColl(id, other string) bool {
	return id != other && fullName(id) == fullName(other)
}

// joinTitles is an id from a test's titles, outermost first, or "" when a
// title holds the separator itself: such an id would split into the wrong
// titles, and its pattern would select a test that does not exist.
func joinTitles(titles []string) string {
	for _, t := range titles {
		if t == "" || strings.Contains(t, "::") {
			return ""
		}
	}
	return strings.Join(titles, "::")
}

// hasFlag reports whether argv carries any of flags, bare or as flag=value.
func hasFlag(argv []string, flags ...string) bool {
	for _, a := range argv {
		for _, f := range flags {
			if a == f || strings.HasPrefix(a, f+"=") {
				return true
			}
		}
	}
	return false
}

// narrowable reports whether a row may append its own list or selection
// arguments to argv: not empty, not shell-wrapped (appending to `sh -c`
// adds positional parameters to sh, not arguments to the runner), and not
// already carrying one of the runner's own name filters, which would combine
// with the row's and run a set nobody listed.
func narrowable(argv []string, filters ...string) bool {
	return len(argv) > 0 && !cmdIsShellWrapped(argv) && !hasFlag(argv, filters...)
}

// regexQuoter escapes every character a JavaScript or Ruby regular
// expression treats as syntax, and the slash that delimits one.
var regexQuoter = strings.NewReplacer(
	`\`, `\\`, `^`, `\^`, `$`, `\$`, `.`, `\.`, `*`, `\*`, `+`, `\+`, `?`, `\?`,
	`(`, `\(`, `)`, `\)`, `[`, `\[`, `]`, `\]`, `{`, `\{`, `}`, `\}`, `|`, `\|`, `/`, `\/`,
)

// jsRegexQuote makes s match itself literally inside a JavaScript regex.
func jsRegexQuote(s string) string { return regexQuoter.Replace(s) }
