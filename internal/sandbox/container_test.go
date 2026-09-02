// SPDX-License-Identifier: Elastic-2.0

package sandbox

import (
	"strings"
	"testing"
)

// TestContainerTmpfsAllowsExec pins the fix for a bug that made this backend
// unable to run Go at all.
//
// Docker mounts --tmpfs noexec by DEFAULT. A Go toolchain compiles its test
// binary into $TMPDIR (/tmp/go-build*/…) and then execs it, so every `go test`
// inside the container jail died with
//
//	fork/exec /tmp/go-build…/ctest.test: permission denied
//
// which reads to an operator as a broken project rather than a broken jail —
// and README advertises this backend as the macOS/Windows path. Every
// compile-then-run toolchain has the same shape (cc into a temp a.out, cargo,
// node-gyp), so it was never a Go quirk.
//
// The rest of the mount options are asserted too: exec was added because the
// workspace bind is already writable AND executable, so noexec on /tmp bought
// nothing — but nosuid and nodev cost nothing and stay.
func TestContainerTmpfsAllowsExec(t *testing.T) {
	argv, err := containerIsolator{runtime: "docker", image: "golang:1.24"}.Wrap(
		"go test ./...", Options{Workspace: "/w"}, nil)
	if err != nil {
		t.Fatalf("Wrap: %v", err)
	}
	joined := strings.Join(argv, " ")

	for _, mount := range []string{"/tmp", "/home/agent"} {
		var opts string
		for i, a := range argv {
			if a == "--tmpfs" && i+1 < len(argv) && strings.HasPrefix(argv[i+1], mount+":") {
				opts = argv[i+1]
			}
		}
		if opts == "" {
			t.Errorf("no --tmpfs for %s with explicit options — docker then applies its noexec default, and a compiled test binary cannot run", mount)
			continue
		}
		if !strings.Contains(opts, "exec") || strings.Contains(opts, "noexec") {
			t.Errorf("--tmpfs %s lacks exec: a toolchain that compiles into it and runs the result fails with 'permission denied', which reads as a broken project", opts)
		}
		for _, keep := range []string{"nosuid", "nodev"} {
			if !strings.Contains(opts, keep) {
				t.Errorf("--tmpfs %s dropped %s — exec was needed, loosening the rest was not", opts, keep)
			}
		}
	}

	// The boundary this backend actually rests on is untouched.
	for _, must := range []string{"--network=none", "--read-only", "--cap-drop=ALL"} {
		if !strings.Contains(joined, must) {
			t.Errorf("argv lost %s — the isolation this backend rests on", must)
		}
	}
}
