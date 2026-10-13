// SPDX-License-Identifier: Elastic-2.0

//go:build darwin

package sandbox

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestJailCanReadAnotherProcessesStartupEnvironment PINS A KNOWN LEAK: code
// inside the macOS jail can read the exec-time environment and arguments of
// any non-Apple process the same user owns (sysctl KERN_PROCARGS2) —
// corral's own process included, so a model API key in corral's
// environment is readable by the code it audits. Measured on macOS 27.0.1:
// neither (deny sysctl-read), a sysctl-name deny nor (process-info*
// (target self)) stops it; Apple platform binaries are protected,
// user-built ones are not. bwrap's --unshare-pid prevents it on Linux.
//
// This documents an OS limit (spec named gap C1); if it starts failing,
// macOS closed the hole — update README/doctor.
func TestJailCanReadAnotherProcessesStartupEnvironment(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on PATH: cannot build the non-Apple helper process")
	}
	// Built OUTSIDE the jail, into the jail's workspace, which the jail can
	// read and execute. A USER-BUILT binary on purpose: Apple platform
	// binaries (/bin/sleep, and /bin/ps, which the jail cannot even exec,
	// being setuid) are protected from this read, and corral is not one.
	ws := t.TempDir()
	helper := filepath.Join(ws, "procargs")
	build := exec.Command(goBin, "build", "-o", helper, "./testdata/procargs") // #nosec G204 -- test-built helper
	build.Env = append(os.Environ(), "GOTOOLCHAIN=local", "GOFLAGS=")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the helper: %v\n%s", err, out)
	}

	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	marker := "corral-c1-" + hex.EncodeToString(b[:])
	proc := exec.Command(helper, "sleep") // #nosec G204 -- test-built helper
	proc.Env = append(os.Environ(), "CORRAL_C1_MARKER="+marker)
	if err := proc.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = proc.Process.Kill(); _ = proc.Wait() })

	// The marker is NOT in the jail's own environment (env -i, no
	// forwarding), so seeing it means the jail read the helper's.
	res := runJailed(t, "./procargs read "+strconv.Itoa(proc.Process.Pid), Options{Workspace: ws})
	if !strings.Contains(res.Output, marker) {
		t.Fatalf("the jail could NOT read another process's startup environment (exit=%d output=%q).\n"+
			"macOS appears to have closed named gap C1: update the README Platforms gaps, the spec, and doctor's darwin gap list.",
			res.ExitCode, res.Output)
	}
}
