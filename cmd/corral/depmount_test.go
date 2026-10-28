// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/sandbox"
)

// The deps line says how each directory is mounted, and each wording is
// true: a backend that does not overlay binds it read-only; a bwrap that
// overlays it makes it copy-on-write, writable in the run only.
func TestDepMountWordsSaysHowEachDirIsMounted(t *testing.T) {
	if got := depMountWords(nil, t.TempDir()); got != "read-only" {
		t.Errorf("no overlaying backend: %q, want read-only", got)
	}
	iso, err := sandbox.Resolve(sandbox.Config{Backend: "bwrap"})
	if err != nil {
		t.Skipf("bwrap unavailable: %v", err)
	}
	dir := t.TempDir()
	if !sandbox.OverlaysDir(iso, dir) {
		out, _ := exec.Command("bwrap", "--version").Output()
		t.Skipf("%s cannot overlay; overlay path unexercised", strings.TrimSpace(string(out)))
	}
	if got := depMountWords(iso, dir); got != "copy-on-write, writable in the run only" {
		t.Errorf("an overlaying bwrap: %q", got)
	}
}

// A dependency directory that cannot be listed is mounted by no backend
// (per-entry binds expand to nothing, and bwrap does not overlay what it
// cannot list for its cache masks), and the deps line says so rather than
// naming a mount that never happens.
func TestDepMountWordsUnreadableDirIsNotMounted(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads a directory whatever its mode")
	}
	dir := filepath.Join(t.TempDir(), "node_modules")
	if err := os.Mkdir(dir, 0o300); err != nil { // #nosec G301 -- deliberately unreadable
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) }) // #nosec G302 -- let TempDir clean up
	want := "not mounted: it could not be read"
	if got := depMountWords(nil, dir); got != want {
		t.Errorf("no overlaying backend: %q, want %q", got, want)
	}
	if iso, err := sandbox.Resolve(sandbox.Config{Backend: "bwrap"}); err == nil {
		if got := depMountWords(iso, dir); got != want {
			t.Errorf("bwrap: %q, want %q", got, want)
		}
	}
}
