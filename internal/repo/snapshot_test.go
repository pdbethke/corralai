// SPDX-License-Identifier: Elastic-2.0

package repo

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotRoundTripExcludesGit(t *testing.T) {
	src := t.TempDir()
	os.MkdirAll(filepath.Join(src, "pkg"), 0o755)
	os.MkdirAll(filepath.Join(src, ".git"), 0o755)
	os.MkdirAll(filepath.Join(src, "node_modules", "x"), 0o755)
	os.MkdirAll(filepath.Join(src, "vendor", "lib"), 0o755)
	os.WriteFile(filepath.Join(src, "pkg", "a.go"), []byte("package pkg\n"), 0o644)
	os.WriteFile(filepath.Join(src, "README.md"), []byte("hi\n"), 0o644)
	os.WriteFile(filepath.Join(src, ".git", "config"), []byte("[core]\n"), 0o644)
	os.WriteFile(filepath.Join(src, "node_modules", "x", "p.js"), []byte("x\n"), 0o644)
	os.WriteFile(filepath.Join(src, "vendor", "lib", "x.go"), []byte("package lib\n"), 0o644)

	e := New("", "")
	data, manifest, err := e.Snapshot(src)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := manifest["pkg/a.go"]; !ok {
		t.Fatalf("manifest missing pkg/a.go: %v", manifest)
	}
	for bad := range manifest {
		if strings.HasPrefix(bad, ".git/") || strings.HasPrefix(bad, "node_modules/") || strings.HasPrefix(bad, "vendor/") {
			t.Fatalf("snapshot leaked excluded path: %s", bad)
		}
	}
	// untar into a fresh dir via ApplyFiles-equivalent: use the engine's own untar test helper.
	dst := t.TempDir()
	if err := untarGz(dst, data); err != nil { // test helper below
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(dst, "pkg", "a.go"))
	if string(got) != "package pkg\n" {
		t.Fatalf("round-trip content wrong: %q", got)
	}
	if _, err := os.Stat(filepath.Join(dst, ".git")); !os.IsNotExist(err) {
		t.Fatalf(".git must not be in the snapshot")
	}
}

func TestApplyFilesEscapeSkipped(t *testing.T) {
	dir := t.TempDir()
	e := New("", "")
	applied, err := e.ApplyFiles(dir, []FileWrite{
		{Path: "ok.go", Content: "package x\n"},
		{Path: "../escape.go", Content: "nope\n"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(applied) != 1 || applied[0] != "ok.go" {
		t.Fatalf("expected only ok.go applied, got %v", applied)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.go")); !os.IsNotExist(err) {
		t.Fatal("escape file was written outside dir")
	}
	got, _ := os.ReadFile(filepath.Join(dir, "ok.go"))
	if string(got) != "package x\n" {
		t.Fatalf("ok.go content wrong: %q", got)
	}
}

// TestApplyFilesGitSkipped covers the credential-boundary fix: ApplyFiles must
// silently skip any path whose first segment is .git (or node_modules/vendor)
// and must never write those bytes to disk. The skipped path must be absent from
// the applied slice. A normal file in the same call must still be written.
func TestApplyFilesGitSkipped(t *testing.T) {
	dir := t.TempDir()
	e := New("", "")
	applied, err := e.ApplyFiles(dir, []FileWrite{
		{Path: ".git/config", Content: "[core]\n  fsmonitor = <shell>\n"},
		{Path: "ok.go", Content: "package ok\n"},
		{Path: "node_modules/evil.js", Content: "evil\n"},
		{Path: "vendor/lib.go", Content: "evil\n"},
	})
	if err != nil {
		t.Fatalf("ApplyFiles: unexpected error: %v", err)
	}
	if len(applied) != 1 || applied[0] != "ok.go" {
		t.Fatalf("expected only ok.go in applied, got %v", applied)
	}
	// .git/config must NOT be written
	if _, err := os.Stat(filepath.Join(dir, ".git", "config")); !os.IsNotExist(err) {
		t.Fatal("ApplyFiles wrote .git/config — credential-boundary violation")
	}
	// ok.go must have been written with the correct content
	if got, _ := os.ReadFile(filepath.Join(dir, "ok.go")); string(got) != "package ok\n" {
		t.Fatalf("ok.go wrong content: %q", got)
	}
}

func TestHeadSHA(t *testing.T) {
	bare := makeBareRepoWithCommit(t) // from repo_test.go (Task 1 of #15)
	dest := filepath.Join(t.TempDir(), "w")
	e := New("", "")
	if err := e.Clone(context.Background(), bare, "main", dest); err != nil {
		t.Fatal(err)
	}
	sha, err := e.HeadSHA(context.Background(), dest)
	if err != nil || len(sha) < 7 {
		t.Fatalf("HeadSHA = %q err=%v", sha, err)
	}
}

func TestHeadSHAUnbornBranch(t *testing.T) {
	dir := t.TempDir()
	e := New("", "")
	// git init with no commits → unborn HEAD
	if out, err := exec.Command("git", "-C", dir, "init").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	sha, err := e.HeadSHA(context.Background(), dir)
	if err != nil || sha != "" {
		t.Fatalf("unborn branch: want (\"\", nil), got (%q, %v)", sha, err)
	}
}

// untarGz is a test-only helper that expands a gzip'd tar into dir.
func untarGz(dir string, data []byte) error {
	return extractTarGz(dir, data) // exported-for-test? no — same package, use unexported helper
}
