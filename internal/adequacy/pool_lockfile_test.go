// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A tree resolves its dependencies for itself when it holds no build
// directory of its own (a SwiftPM tree has no .build), so trees agree on the
// versions only when each holds the checkout's lockfile. The caller passes
// WithLockfile only for a manifest that declares a dependency the lockfile
// would pin (lang.LockfileFor), as this one does. A lockfile the trees could
// not be given (absent from the checkout, a symlink, or above the copied
// root) then makes the pool one tree, the checkout itself, with a note naming
// the file: never trees that could build different dependency versions.
func TestPoolIsOneTreeWhenTheTreesWouldNotHoldTheLockfile(t *testing.T) {
	manifest := map[string]string{"Package.swift": swiftRemoteManifest, "Sources/Calc/Calc.swift": "public func f() {}\n"}

	absent := newGitRepo(t, manifest, []string{".build/", "Package.resolved"})
	symlinked := newGitRepo(t, manifest, []string{".build/", "Package.resolved"})
	writeFile(t, filepath.Join(symlinked, "elsewhere.resolved"), "{}\n")
	if err := os.Symlink("elsewhere.resolved", filepath.Join(symlinked, "Package.resolved")); err != nil {
		t.Fatal(err)
	}

	for name, root := range map[string]string{"absent": absent, "a symlink, ignored": symlinked} {
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

// A lockfile git ignores (Cargo.lock in most Rust libraries) but the
// checkout holds is copied from the checkout into every tree, so the trees
// keep the checkout's versions and stay parallel; the note says the file was
// put in each tree, since a reader would assume an ignored file is not.
func TestPoolCopiesAnIgnoredLockfileIntoEveryTree(t *testing.T) {
	root := newGitRepo(t, map[string]string{"Package.swift": swiftRemoteManifest, "Sources/Calc/Calc.swift": "public func f() {}\n"}, []string{".build/", "Package.resolved"})
	writeFile(t, filepath.Join(root, "Package.resolved"), "{\"pins\": \"the checkout's\"}\n")
	p, d, err := NewWorkspacePool(context.Background(), root, 3, time.Minute, WithLockfile("Package.resolved"))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if d.Trees != 3 || !strings.Contains(d.Note, "Package.resolved") {
		t.Errorf("want three trees and a note naming Package.resolved, got %+v", d)
	}
	for _, tree := range p.treeRoots() {
		b, err := os.ReadFile(filepath.Join(tree, "Package.resolved")) // #nosec G304 -- a tree this test's pool made
		if err != nil || string(b) != "{\"pins\": \"the checkout's\"}\n" {
			t.Errorf("%s: want the checkout's Package.resolved, got %q, %v", tree, b, err)
		}
	}
}

// A lockfile whose directory is a tracked symlink is never written through:
// copyTree copies the link as a link, and creating the lockfile beneath it
// would write into what it names, here the checkout's own file. The pool is
// one tree, and the checkout's lockfile is untouched.
func TestPoolNeverWritesALockfileThroughASymlinkedDirectory(t *testing.T) {
	root := newGitRepo(t, map[string]string{"Package.swift": swiftRemoteManifest, "Sources/Calc/Calc.swift": "public func f() {}\n"}, []string{".build/", "Package.resolved"})
	writeFile(t, filepath.Join(root, "real", "Package.resolved"), "{\"pins\": 1}\n")
	if err := os.Symlink(filepath.Join(root, "real"), filepath.Join(root, "ws")); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "ws"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "link"}} {
		cmd := exec.Command("git", args...) // #nosec G204 -- test fixture, literal args
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	p, d, err := NewWorkspacePool(context.Background(), root, 2, time.Minute, WithLockfile("ws/Package.resolved"))
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
	if d.Trees != 1 || !strings.Contains(d.Note, "ws/Package.resolved") {
		t.Errorf("want one tree and a note naming ws/Package.resolved, got %+v", d)
	}
	if b, err := os.ReadFile(filepath.Join(root, "real", "Package.resolved")); err != nil || string(b) != "{\"pins\": 1}\n" { // #nosec G304 -- this test's checkout
		t.Errorf("the checkout's lockfile changed: %q, %v", b, err)
	}
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
