// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestEnsureGoVendoredNoOps pins the three cases that must NOT stage a copy or
// run `go mod vendor`: non-Go code, a Go dir that isn't a module, and a repo
// that already carries vendor/ (which the jail bind-mounts as-is).
func TestEnsureGoVendoredNoOps(t *testing.T) {
	dir := t.TempDir()

	got, cleanup, err := ensureGoVendored(dir, "python", io.Discard)
	if err != nil || got != dir {
		t.Fatalf("non-Go must be a no-op: got=%s err=%v", got, err)
	}
	cleanup()

	got, cleanup, err = ensureGoVendored(dir, "go", io.Discard)
	if err != nil || got != dir {
		t.Fatalf("Go without go.mod must be a no-op: got=%s err=%v", got, err)
	}
	cleanup()

	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\ngo 1.26\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "vendor"), 0o750); err != nil {
		t.Fatal(err)
	}
	got, cleanup, err = ensureGoVendored(dir, "go", io.Discard)
	if err != nil || got != dir {
		t.Fatalf("already-vendored repo must be a no-op: got=%s err=%v", got, err)
	}
	cleanup()
}

// TestCopyTreeSkipGit proves the staging copy carries the source tree but never
// the .git dir.
func TestCopyTreeSkipGit(t *testing.T) {
	src := t.TempDir()
	mustMkdir(t, filepath.Join(src, ".git"))
	mustWrite(t, filepath.Join(src, ".git", "HEAD"), "ref: refs/heads/main")
	mustMkdir(t, filepath.Join(src, "pkg"))
	mustWrite(t, filepath.Join(src, "go.mod"), "module x\n")
	mustWrite(t, filepath.Join(src, "pkg", "a.go"), "package pkg\n")

	dst := t.TempDir()
	if err := copyTreeSkipGit(src, dst); err != nil {
		t.Fatalf("copyTreeSkipGit: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "go.mod")); err != nil {
		t.Error("go.mod was not copied")
	}
	if _, err := os.Stat(filepath.Join(dst, "pkg", "a.go")); err != nil {
		t.Error("pkg/a.go was not copied")
	}
	if _, err := os.Stat(filepath.Join(dst, ".git")); !os.IsNotExist(err) {
		t.Error(".git must be skipped, but it was copied")
	}
}

func TestBuildRepoSeedLoadsTreeAndAlwaysReturnsCleanup(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.py"), []byte("x = 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	seed, err := buildRepoSeed(root, "python", "bwrap", nil, false, io.Discard)
	if err != nil {
		t.Fatalf("buildRepoSeed: %v", err)
	}
	if seed.cleanup == nil {
		t.Fatal("cleanup must always be non-nil — callers defer it unconditionally")
	}
	defer seed.cleanup()

	if got := seed.files["a.py"]; got != "x = 1\n" {
		t.Errorf("files[a.py] = %q, want the file's contents", got)
	}
	// Non-Go repos stage nothing: the seed dir IS the repo dir.
	if seed.seedDir != root {
		t.Errorf("seedDir = %q, want %q for a non-Go repo", seed.seedDir, root)
	}
}

func TestBuildRepoSeedPropagatesLoadError(t *testing.T) {
	_, err := buildRepoSeed(filepath.Join(t.TempDir(), "does-not-exist"), "python", "bwrap", nil, false, io.Discard)
	if err == nil {
		t.Fatal("a missing repo dir must be an error, not an empty seed")
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o750); err != nil {
		t.Fatal(err)
	}
}

// mustWrite writes s to p, creating p's parent directories as needed.
func mustWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}
