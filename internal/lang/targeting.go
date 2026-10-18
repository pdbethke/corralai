// SPDX-License-Identifier: Elastic-2.0

package lang

import "strings"

// ResolveListedTest maps a file-relative test name (TestNamesInSource's form,
// e.g. `tests::name` or `name`) onto the one listed id it names: the id equal
// to it, or ending in "::"+name. It resolves nothing when no id matches or
// when several do — the same name in two modules or two test binaries —
// because running that id would run the other test too, and what it killed
// could not be credited to the one named. An id the runner cannot run alone
// (notAlone) is matched by its name like any other, so it still makes a name
// ambiguous; RunsAlone then refuses it if it is the one found.
func ResolveListedTest(listed []string, name string) (string, bool) {
	if name == "" {
		return "", false
	}
	found, n := "", 0
	for _, id := range listed {
		if ln := listedName(id); ln == name || strings.HasSuffix(ln, "::"+name) {
			found = id
			n++
		}
	}
	if n != 1 {
		return "", false
	}
	return found, true
}

// resolveForRow is ResolveListedTest for one runner's list. A row that lists
// a nested test by its full name (ListsFullNames: mocha, rspec) also takes a
// name that spells that full name with "::" between the groups, the form the
// static JavaScript lister writes; it does so only when nothing matched
// exactly, so a name that resolves today keeps its answer, and two ids that
// share a full name leave it unresolved. On any other row the words of a
// nested name could only match some other test's title, so there is no
// fallback.
func resolveForRow(r Runner, listed []string, name string) (string, bool) {
	if id, ok := ResolveListedTest(listed, name); ok || !r.ListsFullNames {
		return id, ok
	}
	if id, n := byFullName(listed, name); n == 1 {
		return id, true
	}
	return "", false
}

// byFullName counts the listed ids whose full name (lastFull) is name with
// every "::" read as a space, and returns the last.
func byFullName(listed []string, name string) (string, int) {
	if name == "" {
		return "", 0
	}
	full := strings.ReplaceAll(name, "::", " ")
	found, n := "", 0
	for _, id := range listed {
		if lastFull(listedName(id)) == full {
			found = id
			n++
		}
	}
	return found, n
}
