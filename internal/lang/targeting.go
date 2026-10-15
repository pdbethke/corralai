// SPDX-License-Identifier: Elastic-2.0

package lang

import "strings"

// CommandTestTargeter is the OPTIONAL plugin extension for a language whose
// test-listing and single-test commands must be built from the operator's
// own test command rather than from the test file. Plugin.SingleTestCmd is
// handed only a test path and a selector, which is enough for `go test -run`
// or a pytest node id; it is not enough for Rust, where the runner (libtest
// or nextest) and the package filter (`-p crate`) live in the command the
// operator typed, and a test's runnable id is its full path in the crate,
// which the test file alone does not say.
//
// The ids it lists are the runner's own; a name a static lister produced
// (TestNamesInSource) is mapped onto one with ResolveListedTest.
type CommandTestTargeter interface {
	// ListTestsCmdFor returns the command that lists every test testCmd
	// would run, without running them.
	ListTestsCmdFor(testCmd []string) (cmd []string, ok bool)
	// ParseTestListFor reads that command's standard output into test ids
	// in emission order. Duplicates are kept: the same id in two test
	// binaries is two tests, and ResolveListedTest must see both.
	ParseTestListFor(testCmd []string, output string) []string
	// SingleTestCmdFor returns testCmd narrowed to exactly the test whose id
	// is id, or ok=false when it cannot be narrowed to one test (the
	// operator's own filter would be combined with it and run others).
	SingleTestCmdFor(testCmd []string, id string) (cmd []string, ok bool)
}

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
