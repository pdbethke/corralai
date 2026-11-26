// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/lang"
)

// The options a Swift file's workspace pool is built with carry the lockfile
// Swift declares, so a SwiftPM checkout with a remote dependency whose
// Package.resolved git ignores has it copied into each tree, said with the
// file, and one whose Package.resolved is missing gets one tree, said with
// the file; a package with nothing to pin keeps its trees and says nothing,
// as does a Go checkout (Go declares no lockfile). Built with the real
// pool, on a real git checkout, so the option's effect is what is checked,
// not its presence.
func TestWorkspaceRunnerOptsCarryTheLanguagesLockfile(t *testing.T) {
	remote := "// swift-tools-version:5.9\nimport PackageDescription\nlet package = Package(name: \"Calc\", dependencies: [.package(url: \"https://example.com/dep.git\", from: \"1.0.0\")])\n"
	none := "// swift-tools-version:5.9\nimport PackageDescription\nlet package = Package(name: \"Calc\")\n"
	swift, _ := lang.ByName("swift")
	golang, _ := lang.ByName("go")
	for _, c := range []struct {
		name     string
		plug     lang.Plugin
		manifest string
		missing  bool // no Package.resolved in the checkout at all
		trees    int
		named    bool // the note names Package.resolved
	}{
		{"swift, remote dependency", swift, remote, false, 2, true},
		{"swift, remote dependency, no Package.resolved", swift, remote, true, 1, true},
		{"swift, nothing to pin", swift, none, false, 2, false},
		{"go", golang, remote, false, 2, false},
	} {
		root := swiftCheckout(t, c.manifest)
		if c.missing {
			if err := os.Remove(filepath.Join(root, "Package.resolved")); err != nil {
				t.Fatal(err)
			}
		}
		p, d, err := adequacy.NewWorkspacePool(context.Background(), root, 2, time.Minute, workspaceRunnerOpts(c.plug, 2, root, "")...)
		if err != nil {
			t.Fatal(err)
		}
		p.Close()
		if d.Trees != c.trees || c.named != strings.Contains(d.Note, "Package.resolved") {
			t.Errorf("%s, ignored Package.resolved: want %d tree(s), note naming it %v, got %+v", c.name, c.trees, c.named, d)
		}
	}
}

// swiftCheckout is a committed SwiftPM checkout with manifest, an ignored
// .build and an ignored Package.resolved.
func swiftCheckout(t *testing.T, manifest string) string {
	return committedCheckout(t, map[string]string{
		"Package.swift":           manifest,
		"Sources/Calc/Calc.swift": "public func f() {}\n",
		"go.mod":                  "module x\n",
		".gitignore":              ".build/\nPackage.resolved\n",
		"Package.resolved":        "{}\n",
	})
}

// committedCheckout is a git checkout of files, every one added and
// committed (git leaves out what a .gitignore among them ignores).
func committedCheckout(t *testing.T, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "init"}} {
		cmd := exec.Command("git", args...) // #nosec G204 -- test fixture, literal args
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return root
}

// The same for Rust: a crate or workspace with a dependency whose Cargo.lock
// git ignores (most Rust libraries) has the checkout's copied into each
// tree, said with the file, and gets one tree, said with the file, when the
// checkout has none; a committed Cargo.lock, or a crate with nothing to pin,
// keeps its trees and says nothing. A virtual workspace root counts as
// pinning (its members are not read), and its Cargo.lock is the one beside
// it.
func TestWorkspaceRunnerOptsCarryRustsLockfile(t *testing.T) {
	rust, _ := lang.ByName("rust")
	const dep = "[package]\nname = \"a\"\nversion = \"0.1.0\"\n[dependencies]\nitoa = \"1\"\n"
	const noDep = "[package]\nname = \"a\"\nversion = \"0.1.0\"\n"
	const ws = "[workspace]\nmembers = [\"crates/*\"]\n"
	for _, c := range []struct {
		name  string
		files map[string]string
		trees int
		named bool // the note names Cargo.lock
	}{
		{"a crate, Cargo.lock ignored", map[string]string{"Cargo.toml": dep, "src/lib.rs": "", "Cargo.lock": "", ".gitignore": "target/\nCargo.lock\n"}, 2, true},
		{"a crate, Cargo.lock ignored and missing", map[string]string{"Cargo.toml": dep, "src/lib.rs": "", ".gitignore": "target/\nCargo.lock\n"}, 1, true},
		{"a crate, Cargo.lock committed", map[string]string{"Cargo.toml": dep, "src/lib.rs": "", "Cargo.lock": "", ".gitignore": "target/\n"}, 2, false},
		{"a crate with nothing to pin, Cargo.lock ignored and missing", map[string]string{"Cargo.toml": noDep, "src/lib.rs": "", ".gitignore": "target/\nCargo.lock\n"}, 2, false},
		{"a workspace, Cargo.lock ignored and missing", map[string]string{"Cargo.toml": ws, "crates/a/Cargo.toml": dep, "crates/a/src/lib.rs": "", ".gitignore": "target/\nCargo.lock\n"}, 1, true},
		{"a workspace, Cargo.lock committed", map[string]string{"Cargo.toml": ws, "crates/a/Cargo.toml": dep, "crates/a/src/lib.rs": "", "Cargo.lock": "", ".gitignore": "target/\n"}, 2, false},
	} {
		root := committedCheckout(t, c.files)
		p, d, err := adequacy.NewWorkspacePool(context.Background(), root, 2, time.Minute, workspaceRunnerOpts(rust, 2, root, "")...)
		if err != nil {
			t.Fatal(err)
		}
		p.Close()
		if d.Trees != c.trees || c.named != strings.Contains(d.Note, "Cargo.lock") {
			t.Errorf("%s: want %d tree(s), note naming Cargo.lock %v, got %+v", c.name, c.trees, c.named, d)
		}
	}
}

// A Python file in a repository that also builds a Rust extension (maturin's
// layout: Cargo.toml and a target/ git does not ignore) keeps its trees and
// is told nothing about target/, because its test command does not build
// it; a Rust file in the same checkout gets one tree, the note naming
// target. The rule and the note are the audited language's.
func TestWorkspaceRunnerOptsScopeBuildDirectoriesToTheAuditedLanguage(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	for rel, body := range map[string]string{
		"Cargo.toml":             "[package]\nname = \"ext\"\n",
		"src/lib.rs":             "pub fn f() {}\n",
		"pkg/mod.py":             "x = 1\n",
		"tests/test_mod.py":      "def test(): pass\n",
		"target/debug/libext.so": "o\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "Cargo.toml", "src", "pkg", "tests"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "init"}} {
		cmd := exec.Command("git", args...) // #nosec G204 -- test fixture, literal args
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	py, _ := lang.ByName("python")
	p, d, err := adequacy.NewWorkspacePool(context.Background(), root, 2, time.Minute, workspaceRunnerOpts(py, 2, root, "")...)
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
	if d.Trees != 2 || strings.Contains(d.Note, "target") {
		t.Errorf("python file: want two trees and nothing about target, got %+v", d)
	}
	rust, _ := lang.ByName("rust")
	p, d, err = adequacy.NewWorkspacePool(context.Background(), root, 2, time.Minute, workspaceRunnerOpts(rust, 2, root, "")...)
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
	if d.Trees != 1 || !strings.Contains(d.Note, "target") {
		t.Errorf("rust file: want one tree naming target, got %+v", d)
	}
}
