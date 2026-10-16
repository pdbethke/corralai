// SPDX-License-Identifier: Elastic-2.0

package lang

import "strings"

// ResolveListedTest maps a file-relative test name (TestNamesInSource's form,
// e.g. `tests::name` or `name`) onto the one listed id it names: the id equal
// to it, or ending in "::"+name. It resolves nothing when no id matches or
// when several do — the same name in two modules or two test binaries —
// because running that id would run the other test too, and what it killed
// could not be credited to the one named.
func ResolveListedTest(listed []string, name string) (string, bool) {
	if name == "" {
		return "", false
	}
	found, n := "", 0
	for _, id := range listed {
		if id == name || strings.HasSuffix(id, "::"+name) {
			found = id
			n++
		}
	}
	if n != 1 {
		return "", false
	}
	return found, true
}
