// SPDX-License-Identifier: Elastic-2.0

// internal/repo/read_test.go
package repo

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadSurfaceAndEscape(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "pkg"), 0o755)
	os.WriteFile(filepath.Join(dir, "pkg", "a.go"), []byte("package pkg\nfunc Auth() {}\n"), 0o644)
	e := New("", "")
	if c, err := e.ReadFile(dir, "pkg/a.go"); err != nil || !contains(c, "func Auth") {
		t.Fatalf("read: %q err=%v", c, err)
	}
	if _, err := e.ReadFile(dir, "../secret"); err == nil {
		t.Fatal("escape via .. must be rejected")
	}
	// .git internals must not be readable through the read surface (matches the
	// Tree/Grep skip set).
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("[core]\n"), 0o644)
	if _, err := e.ReadFile(dir, ".git/config"); err == nil {
		t.Fatal("reading .git/config must be rejected")
	}
	tree, err := e.Tree(dir, "")
	if err != nil || !hasItem(tree, "pkg/a.go") {
		t.Fatalf("tree: %v %v", tree, err)
	}
	hits, err := e.Grep(dir, "Auth", 10)
	if err != nil || len(hits) == 0 || !contains(hits[0], "a.go") {
		t.Fatalf("grep: %v %v", hits, err)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || (len(sub) > 0 && stringIndex(s, sub) >= 0))
}
func stringIndex(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
func hasItem(xs []string, x string) bool {
	for _, v := range xs {
		if v == x {
			return true
		}
	}
	return false
}
