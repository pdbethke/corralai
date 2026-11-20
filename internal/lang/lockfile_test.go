// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"testing"
)

// LockfileFor names the lockfile beside the manifest the run's package is
// built from, relative to the root the pool copies: "Package.resolved" when
// Package.swift is at the root, "../Package.resolved" when the root is a
// directory inside the package (which a tree copied from the root does not
// hold at all). Nothing when there is no manifest, or the language declares
// no lockfile.
func TestLockfileForNamesTheLockfileBesideTheManifest(t *testing.T) {
	swift, _ := ByName("swift")

	pkg := t.TempDir()
	if err := os.WriteFile(filepath.Join(pkg, "Package.swift"), []byte(remoteManifest), 0o600); err != nil {
		t.Fatal(err)
	}
	if rel, ok := LockfileFor(swift, pkg); !ok || rel != "Package.resolved" {
		t.Errorf("manifest at the root: got %q, %v; want Package.resolved", rel, ok)
	}
	sub := filepath.Join(pkg, "Sources", "Calc")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	if rel, ok := LockfileFor(swift, sub); !ok || rel != "../../Package.resolved" {
		t.Errorf("manifest above the root: got %q, %v; want ../../Package.resolved", rel, ok)
	}
	if rel, ok := LockfileFor(swift, t.TempDir()); ok {
		t.Errorf("no manifest: got %q, want nothing", rel)
	}
	py, _ := ByName("python")
	if rel, ok := LockfileFor(py, pkg); ok {
		t.Errorf("a language that declares no lockfile: got %q, want nothing", rel)
	}
}

const remoteManifest = `// swift-tools-version:5.9
import PackageDescription
let package = Package(name: "P", dependencies: [.package(url: "https://example.com/a.git", from: "1.0.0")])
`

// SwiftPM writes no Package.resolved for a package with nothing to pin: no
// dependency at all, or only path: ones (measured with `swift build --build-tests` on
// Swift 6.3.2). Such a package needs no lockfile for its trees to agree,
// so LockfileFor asks for none; one remote dependency is enough to ask.
func TestLockfileForAsksOnlyForAManifestWithSomethingToPin(t *testing.T) {
	swift, _ := ByName("swift")
	for _, c := range []struct {
		name, manifest string
		want           bool
	}{
		{"no dependencies", "// swift-tools-version:6.0\nimport PackageDescription\nlet package = Package(name: \"Calc\", targets: [.target(name: \"Calc\")])\n", false},
		{"path only", "let package = Package(name: \"P\", dependencies: [.package(path: \"Local\"), .package(name: \"L2\", path: \"../L2\")])\n", false},
		{"a remote one in a comment", "// .package(url: \"https://example.com/a.git\", from: \"1.0.0\")\nlet package = Package(name: \"P\")\n", false},
		{"url", remoteManifest, true},
		{"registry id", "let package = Package(name: \"P\", dependencies: [.package(id: \"scope.name\", from: \"1.0.0\")])\n", true},
		{"path and url", "dependencies: [.package(path: \"Local\"), .package(url: \"https://example.com/a.git\", branch: \"main\")]\n", true},
		{"cannot be read", "let package = Package(name: \"P\", dependencies: [.package(path: \"L\")]) /*", true},
	} {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "Package.swift"), []byte(c.manifest), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, ok := LockfileFor(swift, dir); ok != c.want {
			t.Errorf("%s: LockfileFor asks = %v, want %v", c.name, ok, c.want)
		}
	}
}

// Every declared lockfile names its file, its manifest and the bound of the
// upward walk for the manifest: a zero bound would find nothing and say nothing,
// which is a missing decision, not an answer.
func TestEveryLockfileDeclarationIsComplete(t *testing.T) {
	for _, n := range Names() {
		p, _ := ByName(n)
		l, ok := p.(Lockfiler)
		if !ok {
			continue
		}
		d := l.Lockfile()
		if d.Name == "" || d.Manifest == "" || d.SearchDepth < 1 {
			t.Errorf("%s: incomplete lockfile declaration %+v", n, d)
		}
	}
}
