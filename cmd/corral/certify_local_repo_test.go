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

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o750); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
		t.Fatal(err)
	}
}
