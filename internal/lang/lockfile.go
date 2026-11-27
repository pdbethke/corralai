// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"path/filepath"
	"strings"
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
// such a language therefore puts the checkout's lockfile in each tree, copying
// it even when git ignores it, and is one tree when the checkout has none to
// copy (adequacy.WithLockfile); the disclosure names the file either way.
//
// Swift, Rust and Ruby implement it.
//
// The languages whose dependency directory is linked into every tree, read
// only (SharedDependencies: .venv, node_modules, vendor), resolve nothing in a
// tree, so their trees cannot differ by resolution and they need none; the
// parity table says which language is which.
//
// LockfileDir is the directory the language's own tool keeps the lockfile
// in for the package whose manifest is in manifestDir: manifestDir itself
// for SwiftPM, the workspace root for cargo (a member crate's Cargo.lock is
// written at its workspace's root). An answer the language cannot give is
// manifestDir, where a member's checkout has no lockfile, so the pool errs
// toward one tree.
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
	LockfileDir(manifestDir string) string
	ManifestPins(manifestDir string) bool
}

// LockfileFor is the lockfile every workspace tree must hold, as a slash path
// relative to root: the one p declares, in the directory the language keeps
// it (Lockfiler.LockfileDir) for the nearest manifest at or above root, as
// the language's own tool looks when the test command runs there, so
// "../Package.resolved" when root is a directory inside the package (a tree
// copied from root cannot hold such a file). Only when root has no manifest
// at or above it is the one nearest the audited file codePath (relative to
// root, or absolute; "" for none) used, looked for from its directory up to
// root: a crate below a root that is not one, which the test command reaches
// with --manifest-path. The file's own manifest is not preferred over
// root's: a path dependency under root (a crate, or a local Swift package)
// has its own manifest, but the lockfile is the one the root's build writes.
// Paths are compared with their symlinks resolved, as cargo reports them.
//
// ok is false when p declares no lockfile, no manifest is found, or the
// manifest beside the lockfile declares nothing it would pin
// (Lockfiler.ManifestPins). No manifest at root or above, nor between the
// file and root, means the language's tool has none to resolve from there:
// Swift's preflight refuses the run; a Rust command reaching a crate outside
// both is graded with no lockfile rule, so its trees resolve for themselves.
func LockfileFor(p Plugin, root, codePath string) (rel string, ok bool) {
	l, isL := p.(Lockfiler)
	if !isL {
		return "", false
	}
	d := l.Lockfile()
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", false
	}
	if r, err := filepath.EvalSymlinks(abs); err == nil {
		abs = r
	}
	dir, found := nearestAncestorWith(abs, []string{d.Manifest}, d.SearchDepth)
	if !found {
		dir, found = manifestDirBelow(abs, codePath, d.Manifest)
	}
	if !found {
		return "", false
	}
	dir = l.LockfileDir(dir)
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
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

// manifestDirBelow is the nearest directory holding manifest from codePath's
// directory up to root (resolved, absolute), never above it; found is false
// for an empty codePath or one outside root.
func manifestDirBelow(root, codePath, manifest string) (string, bool) {
	if strings.TrimSpace(codePath) == "" {
		return "", false
	}
	file := filepath.FromSlash(codePath)
	if !filepath.IsAbs(file) {
		file = filepath.Join(root, file)
	} else if r, err := filepath.EvalSymlinks(filepath.Dir(file)); err == nil {
		file = filepath.Join(r, filepath.Base(file))
	}
	rel, err := filepath.Rel(root, filepath.Dir(filepath.Clean(file)))
	if err != nil || !filepath.IsLocal(rel) && rel != "." {
		return "", false
	}
	steps := 1
	if rel != "." {
		steps += len(strings.Split(rel, string(filepath.Separator)))
	}
	return nearestAncestorWith(filepath.Join(root, rel), []string{manifest}, steps)
}
