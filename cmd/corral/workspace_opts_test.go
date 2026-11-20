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
// Package.resolved the trees would not hold gets one tree, said with the
// file; a package with nothing to pin keeps its trees, Package.resolved or
// not, as does a Go checkout (Go declares no lockfile). Built with the real
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
		trees    int
	}{
		{"swift, remote dependency", swift, remote, 1},
		{"swift, nothing to pin", swift, none, 2},
		{"go", golang, remote, 2},
	} {
		root := swiftCheckout(t, c.manifest)
		p, d, err := adequacy.NewWorkspacePool(context.Background(), root, 2, time.Minute, workspaceRunnerOpts(c.plug, 2, root)...)
		if err != nil {
			t.Fatal(err)
		}
		p.Close()
		if d.Trees != c.trees || (c.trees == 1) != strings.Contains(d.Note, "Package.resolved") {
			t.Errorf("%s, ignored Package.resolved: want %d tree(s), the note naming it only for one, got %+v", c.name, c.trees, d)
		}
	}
}

// swiftCheckout is a committed SwiftPM checkout with manifest, an ignored
// .build and an ignored Package.resolved.
func swiftCheckout(t *testing.T, manifest string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	for rel, body := range map[string]string{
		"Package.swift":           manifest,
		"Sources/Calc/Calc.swift": "public func f() {}\n",
		"go.mod":                  "module x\n",
		".gitignore":              ".build/\nPackage.resolved\n",
		"Package.resolved":        "{}\n",
	} {
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
	p, d, err := adequacy.NewWorkspacePool(context.Background(), root, 2, time.Minute, workspaceRunnerOpts(py, 2, root)...)
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
	if d.Trees != 2 || strings.Contains(d.Note, "target") {
		t.Errorf("python file: want two trees and nothing about target, got %+v", d)
	}
	rust, _ := lang.ByName("rust")
	p, d, err = adequacy.NewWorkspacePool(context.Background(), root, 2, time.Minute, workspaceRunnerOpts(rust, 2, root)...)
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
	if d.Trees != 1 || !strings.Contains(d.Note, "target") {
		t.Errorf("rust file: want one tree naming target, got %+v", d)
	}
}
