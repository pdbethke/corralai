// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"path/filepath"
)

// Lockfile is where a language's package manager records the exact
// dependency versions it resolved: the file Name, beside the manifest
// Manifest, found by walking up from the repository root at most SearchDepth
// directories (the root counts as the first), as the language's own tool
// looks for its manifest.
type Lockfile struct {
	Name        string // e.g. "Package.resolved"
	Manifest    string // e.g. "Package.swift"
	SearchDepth int
}

// Lockfiler is an OPTIONAL plugin extension for a language whose workspace
// trees resolve their dependencies for themselves. The workspace pool copies
// the checkout into private trees; a tree that holds no resolved dependencies
// of its own (they live in a build directory the pool neither copies nor
// links, as SwiftPM's .build) resolves them on its first run, and trees agree
// on the versions only when each holds the checkout's lockfile. A pool for
// such a language is therefore one tree when its trees would not hold the
// lockfile (adequacy.WithLockfile), and the disclosure names the file.
//
// Swift implements it. Who else could, with one declaration:
//   - Rust: cargo builds each tree's own target/ and, with no Cargo.lock in
//     the tree, resolves the dependencies afresh. But a cargo workspace keeps
//     Cargo.lock at the workspace root, not beside a member's Cargo.toml,
//     and LockfileFor finds the nearest manifest, so a member crate would be
//     sent looking in the wrong place. Finding the workspace root comes
//     first.
//   - Ruby: an ignored Gemfile.lock lets each tree's bundler resolve the
//     Gemfile again. Whether trees then disagree was not measured.
//
// The languages whose dependency directory is linked into every tree, read
// only (SharedDependencies: .venv, node_modules, vendor), resolve nothing in a
// tree, so their trees cannot differ by resolution and they need none; the
// parity table says which language is which.
//
// ManifestPins reports whether the manifest in manifestDir declares a
// dependency the lockfile would pin, reading whatever the language must
// (Swift follows its local path: packages, whose remote dependencies the
// root's lockfile records): a package with nothing to pin needs no lockfile
// for its trees to agree (SwiftPM writes no Package.resolved for one). What
// the reading cannot establish counts as pinning. A method, not a field, so
// a reader of the manifest is only ever the language's own.
type Lockfiler interface {
	Lockfile() Lockfile
	ManifestPins(manifestDir string) bool
}

// LockfileFor is the lockfile every workspace tree must hold, as a slash path
// relative to root: the one p declares, beside the nearest manifest at or
// above root, so "../Package.resolved" when root is a directory inside the
// package (a tree copied from root holds no such file). ok is false when p
// declares no lockfile, no manifest is found (the language's own preflight
// refuses that), or the manifest declares nothing the lockfile would pin
// (Lockfiler.ManifestPins).
func LockfileFor(p Plugin, root string) (rel string, ok bool) {
	l, isL := p.(Lockfiler)
	if !isL {
		return "", false
	}
	d := l.Lockfile()
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	dir, found := nearestAncestorWith(abs, []string{d.Manifest}, d.SearchDepth)
	if !found {
		return "", false
	}
	if !l.ManifestPins(dir) {
		return "", false
	}
	r, err := filepath.Rel(abs, filepath.Join(dir, d.Name))
	if err != nil {
		return "", false
	}
	return filepath.ToSlash(r), true
}
