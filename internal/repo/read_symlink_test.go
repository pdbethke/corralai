// SPDX-License-Identifier: Elastic-2.0

package repo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestReadSurfaceRefusesSymlinkEscape proves the read surface (ReadFile/Grep/
// Snapshot) cannot be tricked by a symlink in a hostile cloned repo into reading
// host files outside the repo directory (the symlink-TOCTOU / path-escape class).
func TestReadSurfaceRefusesSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	repo := filepath.Join(base, "repo")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(repo, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o750); err != nil {
		t.Fatal(err)
	}
	const canary = "TOP-SECRET-CANARY-9f3b2a"
	secret := filepath.Join(outside, "secret.txt")
	if err := os.WriteFile(secret, []byte(canary), 0o600); err != nil {
		t.Fatal(err)
	}
	// a legitimate regular file in the repo (control)
	if err := os.WriteFile(filepath.Join(repo, "ok.txt"), []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	// a symlink inside the repo pointing at the outside secret
	if err := os.Symlink(secret, filepath.Join(repo, "escape.txt")); err != nil {
		t.Skipf("symlinks unsupported on this platform: %v", err)
	}

	e := &Engine{}

	// ReadFile must refuse to follow the symlink out of the repo.
	if got, err := e.ReadFile(repo, "escape.txt"); err == nil {
		t.Fatalf("ReadFile followed a symlink escape (should error); got %q", got)
	}

	// Grep must skip the symlink — the canary must never surface.
	hits, err := e.Grep(repo, canary, 50)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hits {
		if strings.Contains(h, canary) {
			t.Fatalf("Grep followed a symlink escape and leaked the canary: %q", h)
		}
	}

	// Snapshot must skip the symlink — the escape file must not be in the manifest,
	// and the legit regular file must still be captured.
	_, manifest, err := e.Snapshot(repo)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := manifest["escape.txt"]; ok {
		t.Fatalf("Snapshot captured the symlink escape.txt (should skip non-regular files)")
	}
	if _, ok := manifest["ok.txt"]; !ok {
		t.Fatalf("Snapshot dropped the legitimate regular file ok.txt")
	}
}
