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
// A language whose dependencies every tree shares through a link (a
// SharedDependencies directory such as node_modules or vendor) or that lives
// outside the tree does not need it: its trees resolve nothing.
type Lockfiler interface {
	Lockfile() Lockfile
}

// LockfileFor is the lockfile p declares, as a slash path relative to root:
// beside the nearest manifest at or above root, so "../Package.resolved"
// when root is a directory inside the package (a tree copied from root holds
// no such file). ok is false when p declares no lockfile or no manifest is
// found; the language's own preflight is what refuses a missing manifest.
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
	r, err := filepath.Rel(abs, filepath.Join(dir, d.Name))
	if err != nil {
		return "", false
	}
	return filepath.ToSlash(r), true
}
