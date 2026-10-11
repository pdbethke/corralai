// SPDX-License-Identifier: Elastic-2.0

// Package treedirs answers one question for every tree walker in corral:
// which directory basenames are NOT this repository's own source.
//
// The answer is composed once, by kind. Dependency directories come from the
// language plugins (lang.AllDependencyDirs) — the language that installs a
// tree is the one that knows its name — so a new language's dirs reach every
// walker the moment it registers. The remaining kinds (tooling, build
// output, fixtures, caches) are language-neutral and listed here. Each
// walker asks for the composition that matches its purpose; each function
// returns a fresh map, so a caller may mutate its result.
package treedirs

import "github.com/pdbethke/corralai/internal/lang"

// tooling is VCS and corral's own record.
var tooling = []string{".git", ".corral"}

// buildOutput is derived from this repository's source.
var buildOutput = []string{"dist", "build", "target"}

// fixtures are test data that deliberately looks like source.
var fixtures = []string{"testdata"}

// caches is Python's bytecode cache. Only the review walk skips it: the
// other walkers key on source extensions it never carries.
var caches = []string{"__pycache__"}

func compose(lists ...[]string) map[string]bool {
	m := map[string]bool{}
	for _, l := range lists {
		for _, n := range l {
			m[n] = true
		}
	}
	return m
}

func depNames(keep func(lang.DepDir) bool) []string {
	var out []string
	for _, d := range lang.AllDependencyDirs() {
		if keep(d) {
			out = append(out, d.Name)
		}
	}
	return out
}

// Dependencies is every plugin's dependency directories: third-party code
// the audit has no business grading, and enormous (node_modules alone is
// routinely tens of thousands of files; a virtualenv's site-packages is
// comparable).
//
// "vendor" covers two languages under the one basename: Go's module vendor
// dir and Composer's PHP tree land at the same repo-relative name.
//
// .tox is a dependency tree rather than build output because a tox
// environment contains full virtualenvs — it is site-packages wearing a
// different name, not a modest build artifact.
func Dependencies() map[string]bool {
	return compose(depNames(func(lang.DepDir) bool { return true }))
}

// SharedDependencies is the dependency directories no test run writes into,
// so they are safe to bind read-only or symlink into every tree: a suite
// that cannot import its own dependencies fails for a reason that has
// nothing to do with the mutant, and no mutant is ever written into them.
//
// Two consumers rely on that, for reasons of their own mechanics:
//
//   - The jail binds these read-only instead of copying them into the seed,
//     which keeps them out of the 64 MiB seed cap.
//   - The workspace pool symlinks them into every private tree. They are
//     git-ignored, so they are not in the universe the pool copies, yet must
//     still be reachable from every tree; they are large build products, so
//     they are linked rather than copied; and no mutant is ever written into
//     them, which is what makes one shared link safe.
//
// .tox is NOT shared. tox creates, updates and reuses its environments
// inside .tox as part of running the suite, so a shared .tox would be N trees
// writing to one directory at once (cross-tree interference on the very
// isolation the pool exists to provide) AND a write through the link into
// the operator's real checkout. A tox suite in a tree simply rebuilds its
// own .tox, which is slower and correct.
func SharedDependencies() map[string]bool {
	return compose(depNames(func(d lang.DepDir) bool { return !d.RunWrites }))
}

// NeverWalked is what the repo scan does not descend into: dependency,
// build-output and VCS trees are not the subject of an audit of THIS repo's
// tests, and letting them into the walk puts vendored third-party code into
// the report's denominator. Fixtures are skipped too: they are not tests.
func NeverWalked() map[string]bool {
	return compose(tooling, depNames(func(lang.DepDir) bool { return true }), buildOutput, fixtures)
}

// Unaccounted is the subset of NeverWalked skipped WITHOUT being listed in
// the report: tooling and dependencies.
//
//   - .git is not source at all — listing its objects would swamp the report.
//   - .corral is corral's OWN record — the ledger directory a scan writes its
//     entry into (and reads its prior from) — not the repository's source:
//     like .git, listing it would report the scan to itself, and digesting it
//     would make every second scan a cache miss.
//   - Dependency trees are enormous; enumerating them buries the handful of
//     entries a reader actually needs in a report that gets signed and
//     anchored.
//
// BUILD OUTPUT (dist, build, target) stays ACCOUNTED: those trees are small
// and derived from this repo, so a reader benefits from seeing the scan chose
// not to look there.
func Unaccounted() map[string]bool {
	return compose(tooling, depNames(func(lang.DepDir) bool { return true }))
}

// NotRead is what the repo reader does not load: tooling and dependencies.
func NotRead() map[string]bool {
	return compose(tooling, depNames(func(lang.DepDir) bool { return true }))
}

// NotReviewed is what the review walk skips: everything NeverWalked skips,
// plus language caches.
func NotReviewed() map[string]bool {
	return compose(tooling, depNames(func(lang.DepDir) bool { return true }), buildOutput, fixtures, caches)
}
