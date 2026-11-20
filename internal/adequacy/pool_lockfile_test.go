// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A tree resolves its dependencies for itself when it holds no build
// directory of its own (a SwiftPM tree has no .build), so trees agree on the
// versions only when each holds the checkout's lockfile. The caller passes
// WithLockfile only for a manifest that declares a dependency the lockfile
// would pin (lang.LockfileFor), as this one does. A lockfile the trees would
// not hold (ignored, or absent) then makes the pool one tree, the checkout
// itself, with a note naming the file: never trees that could build
// different dependency versions.
func TestPoolIsOneTreeWhenTheTreesWouldNotHoldTheLockfile(t *testing.T) {
	manifest := map[string]string{"Package.swift": swiftRemoteManifest, "Sources/Calc/Calc.swift": "public func f() {}\n"}

	ignored := newGitRepo(t, manifest, []string{".build/", "Package.resolved"})
	writeFile(t, filepath.Join(ignored, "Package.resolved"), "{}\n")
	absent := newGitRepo(t, manifest, []string{".build/"})

	for name, root := range map[string]string{"ignored": ignored, "absent": absent} {
		p, d, err := NewWorkspacePool(context.Background(), root, 3, time.Minute, WithLockfile("Package.resolved"))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if d.Trees != 1 || !strings.Contains(d.Note, "Package.resolved") {
			t.Errorf("%s: want one tree and a note naming Package.resolved, got %+v", name, d)
		}
		if roots := p.treeRoots(); len(roots) != 1 || roots[0] != root {
			t.Errorf("%s: the one tree must be the checkout itself, got %v", name, roots)
		}
		p.Close()
	}

	// A lockfile above the copied root is in no tree.
	p, d, err := NewWorkspacePool(context.Background(), filepath.Join(absent, "Sources"), 2, time.Minute, WithLockfile("../Package.resolved"))
	if err != nil {
		t.Fatal(err)
	}
	if d.Trees != 1 || !strings.Contains(d.Note, "../Package.resolved") {
		t.Errorf("lockfile above the root: got %+v", d)
	}
	p.Close()
}

// A tracked lockfile, or an untracked one git does not ignore (the pool
// copies both), keeps the trees, and no tree contains or links .build: it is
// SwiftPM's build output and its dependency checkouts, which a run writes, so
// it is neither copied (ignored) nor shared (a dependency directory a run
// writes is never linked).
func TestPoolKeepsItsTreesWhenEachHoldsTheLockfile(t *testing.T) {
	files := map[string]string{"Package.swift": swiftRemoteManifest, "Package.resolved": "{}\n", "Sources/Calc/Calc.swift": "public func f() {}\n"}
	tracked := newGitRepo(t, files, []string{".build/"})
	delete(files, "Package.resolved")
	untracked := newGitRepo(t, files, []string{".build/"})
	writeFile(t, filepath.Join(untracked, "Package.resolved"), "{}\n")

	for name, root := range map[string]string{"tracked": tracked, "untracked, not ignored": untracked} {
		writeFile(t, filepath.Join(root, ".build", "checkouts", "dep", "Dep.swift"), "x\n")
		writeFile(t, filepath.Join(root, ".build", "workspace-state.json"), "{}\n")
		p, d, err := NewWorkspacePool(context.Background(), root, 3, time.Minute, WithLockfile("Package.resolved"))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if d.Trees != 3 || strings.Contains(d.Note, "Package.resolved") {
			t.Errorf("%s: want three trees and no word on the lockfile, got %+v", name, d)
		}
		for _, tree := range p.treeRoots() {
			mustExist(t, filepath.Join(tree, "Package.resolved"))
			mustNotExist(t, filepath.Join(tree, ".build"))
		}
		p.Close()
	}
}

// swiftRemoteManifest declares one dependency a Package.resolved would pin.
const swiftRemoteManifest = `// swift-tools-version:5.9
import PackageDescription
let package = Package(name: "Calc", dependencies: [.package(url: "https://example.com/dep.git", from: "1.0.0")])
`

// A pool asked for one tree runs on the checkout whatever the lockfile, and
// a pool told no lockfile copies trees as it always has.
func TestPoolLockfileOnlyMattersForCopies(t *testing.T) {
	root := newGitRepo(t, map[string]string{"Package.swift": swiftRemoteManifest}, []string{".build/"})
	p, d, _ := NewWorkspacePool(context.Background(), root, 1, time.Minute, WithLockfile("Package.resolved"))
	if d.Trees != 1 || d.Note != "" {
		t.Errorf("n=1: got %+v", d)
	}
	p.Close()
	p, d, _ = NewWorkspacePool(context.Background(), root, 2, time.Minute)
	if d.Trees != 2 || strings.Contains(d.Note, "Package.resolved") {
		t.Errorf("no lockfile declared: got %+v", d)
	}
	p.Close()
	if _, err := os.Stat(root); err != nil {
		t.Errorf("Close must never remove the checkout: %v", err)
	}
}
