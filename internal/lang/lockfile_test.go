// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"fmt"
	"os"
	"os/exec"
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
	if rel, ok := LockfileFor(swift, pkg, ""); !ok || rel != "Package.resolved" {
		t.Errorf("manifest at the root: got %q, %v; want Package.resolved", rel, ok)
	}
	sub := filepath.Join(pkg, "Sources", "Calc")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	if rel, ok := LockfileFor(swift, sub, ""); !ok || rel != "../../Package.resolved" {
		t.Errorf("manifest above the root: got %q, %v; want ../../Package.resolved", rel, ok)
	}
	if rel, ok := LockfileFor(swift, t.TempDir(), ""); ok {
		t.Errorf("no manifest: got %q, want nothing", rel)
	}
	py, _ := ByName("python")
	if rel, ok := LockfileFor(py, pkg, ""); ok {
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
		if _, ok := LockfileFor(swift, dir, ""); ok != c.want {
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

// cargo keeps Cargo.lock at the workspace root, not beside a member's
// Cargo.toml, and LockfileFor asks cargo where that root is
// (`cargo locate-project --workspace`) rather than reading members, exclude
// and globs a second time. Measured on cargo 1.90, building each case: a
// member's lockfile is written at the root; `package.workspace = "<path>"`
// puts it there; a package in workspace.exclude keeps its own.
func TestRustLockfileIsWhereCargoKeepsIt(t *testing.T) {
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("SKIP: no cargo on PATH; the cargo-backed lockfile location cannot be checked here")
	}
	rust, _ := ByName("rust")
	const pkg = "[package]\nname = \"%s\"\nversion = \"0.1.0\"\nedition = \"2021\"\n%s[dependencies]\nitoa = \"1\"\n"
	write := func(t *testing.T, root string, files map[string]string) {
		t.Helper()
		for rel, body := range files {
			p := filepath.Join(root, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, c := range []struct {
		name  string
		files map[string]string
		root  string // relative to the temp dir: the pool's root
		code  string // the audited file, relative to root
		want  string
	}{
		{"a crate of its own", map[string]string{"Cargo.toml": fmt.Sprintf(pkg, "a", ""), "src/lib.rs": ""}, ".", "", "Cargo.lock"},
		{"the workspace root", map[string]string{"Cargo.toml": "[workspace]\nmembers = [\"crates/*\"]\nresolver = \"2\"\n", "crates/m/Cargo.toml": fmt.Sprintf(pkg, "m", ""), "crates/m/src/lib.rs": ""}, ".", "", "Cargo.lock"},
		{"a member, by a glob", map[string]string{"Cargo.toml": "[workspace]\nmembers = [\"crates/*\"]\nresolver = \"2\"\n", "crates/m/Cargo.toml": fmt.Sprintf(pkg, "m", ""), "crates/m/src/lib.rs": ""}, "crates/m", "", "../../Cargo.lock"},
		{"package.workspace names the root", map[string]string{"root/Cargo.toml": "[workspace]\nmembers = [\"../pkg\"]\nresolver = \"2\"\n", "pkg/Cargo.toml": fmt.Sprintf(pkg, "p", "workspace = \"../root\"\n"), "pkg/src/lib.rs": ""}, "pkg", "", "../root/Cargo.lock"},
		{"excluded from the workspace above", map[string]string{"Cargo.toml": "[workspace]\nexclude = [\"ex\"]\n", "ex/Cargo.toml": fmt.Sprintf(pkg, "ex", ""), "ex/src/lib.rs": ""}, "ex", "", "Cargo.lock"},
		// From a root with a manifest, root's lockfile, whatever file is
		// audited: a member's, or a path dependency's that pins nothing
		// itself (its lockfile is the one the root's build writes).
		{"a member's file, from the workspace root", map[string]string{"Cargo.toml": "[workspace]\nmembers = [\"crates/*\"]\nresolver = \"2\"\n", "crates/m/Cargo.toml": fmt.Sprintf(pkg, "m", ""), "crates/m/src/lib.rs": ""}, ".", "crates/m/src/lib.rs", "Cargo.lock"},
		{"a path dependency's file", map[string]string{"Cargo.toml": fmt.Sprintf(pkg, "a", "") + "helper = { path = \"helper\" }\n", "src/lib.rs": "", "helper/Cargo.toml": "[package]\nname = \"helper\"\nversion = \"0.1.0\"\nedition = \"2021\"\n", "helper/src/lib.rs": ""}, ".", "helper/src/lib.rs", "Cargo.lock"},
		// From a root with no manifest, the audited file's crate.
		{"a crate below a root with no manifest", map[string]string{"rust/Cargo.toml": fmt.Sprintf(pkg, "r", ""), "rust/src/lib.rs": ""}, ".", "rust/src/lib.rs", "rust/Cargo.lock"},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			write(t, dir, c.files)
			if rel, ok := LockfileFor(rust, filepath.Join(dir, filepath.FromSlash(c.root)), c.code); !ok || rel != c.want {
				t.Errorf("got %q, %v; want %q", rel, ok, c.want)
			}
		})
	}
}

// cargo prints the workspace root with symlinks resolved, so a checkout
// reached through a symlinked path (macOS's /var, a linked home) is compared
// with its own symlinks resolved too: the member's lockfile is still
// "../../Cargo.lock", never a path through the link's target.
func TestRustLockfileThroughASymlinkedCheckout(t *testing.T) {
	if _, err := exec.LookPath("cargo"); err != nil {
		t.Skip("SKIP: no cargo on PATH; the cargo-backed lockfile location cannot be checked here")
	}
	rust, _ := ByName("rust")
	real := t.TempDir()
	for rel, body := range map[string]string{
		"Cargo.toml":          "[workspace]\nmembers = [\"crates/*\"]\nresolver = \"2\"\n",
		"crates/m/Cargo.toml": "[package]\nname = \"m\"\nversion = \"0.1.0\"\nedition = \"2021\"\n[dependencies]\nitoa = \"1\"\n",
		"crates/m/src/lib.rs": "",
	} {
		p := filepath.Join(real, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if rel, ok := LockfileFor(rust, filepath.Join(link, "crates", "m"), ""); !ok || rel != "../../Cargo.lock" {
		t.Errorf("member through a link: got %q, %v; want ../../Cargo.lock", rel, ok)
	}
	if rel, ok := LockfileFor(rust, link, "crates/m/src/lib.rs"); !ok || rel != "Cargo.lock" {
		t.Errorf("root through a link: got %q, %v; want Cargo.lock", rel, ok)
	}
}

// When cargo cannot say where the workspace root is (no cargo, or a manifest
// cargo refuses), LockfileFor names the lockfile beside the nearest manifest:
// a file a member's tree does not hold, so the pool is one tree, never trees
// that could resolve different versions.
func TestRustLockfileWithoutCargosAnswerStaysBesideTheManifest(t *testing.T) {
	rust, _ := ByName("rust")
	dir := t.TempDir()
	member := filepath.Join(dir, "m")
	if err := os.MkdirAll(member, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte("[workspace]\nmembers = [\"m\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(member, "Cargo.toml"), []byte("[package]\nname = \"m\"\nversion = \"0.1.0\"\n[dependencies]\nitoa = \"1\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", t.TempDir())
	if rel, ok := LockfileFor(rust, member, ""); !ok || rel != "Cargo.lock" {
		t.Errorf("got %q, %v; want Cargo.lock beside the member's manifest", rel, ok)
	}
}

// A Cargo.toml pins something when it declares any dependency table
// ([dependencies], [dev-dependencies], [build-dependencies], a target's,
// a dotted key) or is a workspace, whose members are not read here. A plain
// package with none has nothing for Cargo.lock to pin, so its trees agree
// without one. What cannot be read counts as pinning.
func TestRustManifestPins(t *testing.T) {
	rust, _ := ByName("rust")
	l := rust.(Lockfiler)
	const head = "[package]\nname = \"a\"\nversion = \"0.1.0\"\n"
	for _, c := range []struct {
		name, manifest string
		want           bool
	}{
		{"no dependency", head, false},
		{"no dependency, other tables", head + "[lib]\npath = \"src/lib.rs\"\n[features]\ndefault = []\n", false},
		{"dependencies", head + "[dependencies]\nitoa = \"1\"\n", true},
		{"dev-dependencies", head + "[dev-dependencies]\nitoa = \"1\"\n", true},
		{"build-dependencies", head + "[build-dependencies]\ncc = \"1\"\n", true},
		{"a target's dependencies", head + "[target.'cfg(unix)'.dependencies]\nlibc = \"0.2\"\n", true},
		{"one dependency as a table", head + "[dependencies.itoa]\nversion = \"1\"\n", true},
		{"a dotted key", head + "dependencies.itoa = \"1\"\n", true},
		{"an inline table under target", head + "target = { 'cfg(unix)' = { dependencies = { libc = \"0.2\" } } }\n", true},
		{"a byte-order mark before [dependencies]", "\ufeff[dependencies]\nitoa = \"1\"\n[package]\nname = \"a\"\n", true},
		{"a byte-order mark before [workspace]", "\ufeff[workspace]\nmembers = []\n", true},
		{"a workspace", "[workspace]\nmembers = [\"m\"]\n", true},
		{"a package that is also a workspace", head + "[workspace]\n", true},
		{"no manifest", "", true},
	} {
		dir := t.TempDir()
		if c.manifest != "" {
			if err := os.WriteFile(filepath.Join(dir, "Cargo.toml"), []byte(c.manifest), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if got := l.ManifestPins(dir); got != c.want {
			t.Errorf("%s: ManifestPins = %v, want %v", c.name, got, c.want)
		}
	}
}
