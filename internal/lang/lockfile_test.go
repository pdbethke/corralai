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
// dependency at all, or only a path: one to a local package with none of
// its own (measured with `swift build --build-tests` on Swift 6.3.2). Such a
// package needs no lockfile for its trees to agree, so LockfileFor asks for
// none; one remote dependency is enough to ask, including one a local
// package declares, which SwiftPM records in the root's Package.resolved.
// A path the reading cannot follow (out of the package, not a plain
// literal, a cycle, a local package with no readable manifest) counts as
// pinning, so the rule errs toward one tree.
func TestLockfileForAsksOnlyForAManifestWithSomethingToPin(t *testing.T) {
	swift, _ := ByName("swift")
	const noDeps = "// swift-tools-version:6.0\nimport PackageDescription\nlet package = Package(name: \"Calc\", targets: [.target(name: \"Calc\")])\n"
	for _, c := range []struct {
		name  string
		files map[string]string
		want  bool
	}{
		{"no dependencies", map[string]string{"Package.swift": noDeps}, false},
		{"path to a local package with no dependencies", map[string]string{
			"Package.swift":               "let package = Package(name: \"P\", dependencies: [.package(path: \"Packages/Core\"), .package(name: \"L2\", path: \"./Packages/L2\")])\n",
			"Packages/Core/Package.swift": noDeps,
			"Packages/L2/Package.swift":   noDeps,
		}, false},
		{"two local packages naming one third (no cycle)", map[string]string{
			"Package.swift":        "dependencies: [.package(path: \"A\"), .package(path: \"B\")]\n",
			"A/Package.swift":      "dependencies: [.package(path: \"../Shared\")]\n",
			"B/Package.swift":      "dependencies: [.package(path: \"../Shared\")]\n",
			"Shared/Package.swift": noDeps,
		}, false},
		{"a remote one in a comment", map[string]string{"Package.swift": "// .package(url: \"https://example.com/a.git\", from: \"1.0.0\")\nlet package = Package(name: \"P\")\n"}, false},
		{"url", map[string]string{"Package.swift": remoteManifest}, true},
		{"registry id", map[string]string{"Package.swift": "let package = Package(name: \"P\", dependencies: [.package(id: \"scope.name\", from: \"1.0.0\")])\n"}, true},
		{"path and url", map[string]string{
			"Package.swift":       "dependencies: [.package(path: \"Local\"), .package(url: \"https://example.com/a.git\", branch: \"main\")]\n",
			"Local/Package.swift": noDeps,
		}, true},
		{"a local package with a remote dependency", map[string]string{
			"Package.swift":               "let package = Package(name: \"P\", dependencies: [.package(path: \"Packages/Core\")])\n",
			"Packages/Core/Package.swift": "let package = Package(name: \"Core\", dependencies: [.package(url: \"https://github.com/apple/swift-argument-parser\", from: \"1.0.0\")])\n",
		}, true},
		{"a cycle", map[string]string{
			"Package.swift":   "dependencies: [.package(path: \"A\")]\n",
			"A/Package.swift": "dependencies: [.package(path: \"../B\")]\n",
			"B/Package.swift": "dependencies: [.package(path: \"../A\")]\n",
		}, true},
		{"a path out of the package", map[string]string{"Package.swift": "dependencies: [.package(path: \"../Elsewhere\")]\n"}, true},
		{"an absolute path", map[string]string{"Package.swift": "dependencies: [.package(path: \"/opt/pkgs/Core\")]\n"}, true},
		{"a path that is not a plain literal", map[string]string{
			"Package.swift":      "let base = \"Core\"\ndependencies: [.package(path: base)]\n",
			"Core/Package.swift": noDeps,
		}, true},
		{"a local package with no manifest", map[string]string{"Package.swift": "dependencies: [.package(path: \"Missing\")]\n"}, true},
		{"cannot be read", map[string]string{"Package.swift": "let package = Package(name: \"P\", dependencies: [.package(path: \"L\")]) /*"}, true},
	} {
		dir := t.TempDir()
		for rel, body := range c.files {
			p := filepath.Join(dir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
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
