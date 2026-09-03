// SPDX-License-Identifier: Elastic-2.0

package sandbox

import (
	"os"
	"os/exec"
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

// TestPreflightDoesNotRejectAnImageTheChosenRuntimeHas pins the NARROWNESS of
// the runtime-mismatch check, which is the half that could break working
// setups.
//
// The check exists because a machine with both runtimes silently prefers
// podman (isolator.go), so an image built with `docker build` is invisible and
// the failure arrives as a registry PULL denial — which reads like an auth
// problem and names neither the runtime corral picked nor the store the image
// is in. That cost four CI round trips.
//
// But it must fire ONLY when the image is demonstrably in the OTHER runtime.
// "Not found locally" on its own is an ordinary, supported setup — naming a
// public image you have not pulled yet — and failing preflight for it would
// break every operator who does that. This asserts the common case stays
// clean: an image the chosen runtime actually has must preflight without
// complaint.
func TestPreflightDoesNotRejectAnImageTheChosenRuntimeHas(t *testing.T) {
	image := os.Getenv("CORRALAI_EXEC_IMAGE")
	if image == "" {
		t.Skip("CORRALAI_EXEC_IMAGE not set — needs a real image in a real runtime's store")
	}
	for _, rt := range []string{"docker", "podman"} {
		if _, err := exec.LookPath(rt); err != nil {
			continue
		}
		if !hasImageLocally(rt, image) {
			continue
		}
		c := containerIsolator{runtime: rt, image: image}
		if err := c.Preflight(); err != nil {
			t.Errorf("%s has image %q locally, so preflight must pass, got: %v", rt, image, err)
		}
	}
}
