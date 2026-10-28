// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"os/exec"
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
