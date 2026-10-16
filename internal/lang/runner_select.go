// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"encoding/json"
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

// notAlone marks a listed id corral cannot run by itself: a title holding
// the id separator (its pattern would name titles that do not exist), or a
// node test with subtests (running either runs the parent's body). Such an id
// stays IN the list, because a list without it would let a critic's name
// resolve to the one remaining test that shares it, and that test's kills
// would refute a claim about the other. ResolveListedTest matches it by its
// name; RunsAlone refuses it.
const notAlone = "\x00"

func markNotAlone(id string) string { return notAlone + id }

// listedName is a listed id without its notAlone mark.
func listedName(id string) string { return strings.TrimPrefix(id, notAlone) }

// joinTitles is an id from a test's titles, outermost first; marked notAlone
// when a title is empty or holds the separator itself.
func joinTitles(titles []string) string {
	id := strings.Join(titles, "::")
	for _, t := range titles {
		if t == "" || strings.Contains(t, "::") {
			return markNotAlone(id)
		}
	}
	return id
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

// idFromFull is the id of a test a runner reports as a full name and a leaf
// title (mocha's fullTitle/title, rspec's full_description/description):
// everything before the leaf is one segment, which may itself hold "::"
// (`RSpec.describe MyApp::Calc`, the usual Rails idiom). The LAST "::" is the
// boundary, so a critic's bare test name still matches as the suffix and
// lastFull restores the full name. Marked notAlone when the leaf holds "::"
// or the full name does not end in it.
func idFromFull(full, leaf string) string {
	if leaf == "" || strings.Contains(leaf, "::") {
		return markNotAlone(full)
	}
	if full == leaf {
		return leaf
	}
	prefix, ok := strings.CutSuffix(full, " "+leaf)
	if !ok {
		return markNotAlone(full)
	}
	return prefix + "::" + leaf
}

// lastFull is the full name of an idFromFull id: its last "::" back to a space.
func lastFull(id string) string {
	if i := strings.LastIndex(id, "::"); i >= 0 {
		return id[:i] + " " + id[i+2:]
	}
	return id
}

// lastFullColl is Coruns for the idFromFull rows (mocha, rspec).
func lastFullColl(id, other string) bool {
	return id != other && lastFull(id) == lastFull(other)
}

// decodeReport decodes the JSON report a runner prints on stdout into v,
// starting at the first line that begins with "{" and stopping after the one
// value. A package launcher prints its own lines around the report — npm and
// pnpm a "> script" banner before it, yarn one before and "Done in" after
// (measured on npm 10.9, pnpm 9.15, yarn 1.22) — and bundler or a spec
// helper can print before rspec's.
func decodeReport(stdout string, v any) bool {
	i := 0
	for i < len(stdout) && stdout[i] != '{' {
		nl := strings.IndexByte(stdout[i:], '\n')
		if nl < 0 {
			return false
		}
		i += nl + 1
	}
	if i >= len(stdout) {
		return false
	}
	return json.NewDecoder(strings.NewReader(stdout[i:])).Decode(v) == nil
}
