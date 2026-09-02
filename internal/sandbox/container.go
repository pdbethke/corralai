// SPDX-License-Identifier: Elastic-2.0

package sandbox

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// containerIsolator runs commands inside a container via docker or podman.
// It is a thin wrapper: the container image is responsible for the actual
// toolchain; this type only enforces the isolation flags.
type containerIsolator struct {
	runtime string // "docker" or "podman" (or any OCI-compatible CLI)
	image   string // e.g. "ubuntu:24.04" or a project-specific agent image
}

func (c containerIsolator) Name() string { return "container" }

// Preflight verifies image is set and the container runtime is on PATH.
// It deliberately does NOT pull or start a container — startup latency and
// registry auth are the caller's concern.
func (c containerIsolator) Preflight() error {
	if c.image == "" {
		return errors.New("container backend: set CORRALAI_EXEC_IMAGE to the image to run commands in (e.g. CORRALAI_EXEC_IMAGE=python:3.12-bookworm)")
	}
	if _, err := exec.LookPath(c.runtime); err != nil {
		return fmt.Errorf("container backend: runtime %q not found on PATH: %w", c.runtime, err)
	}
	return nil
}

// Wrap builds the argv for running command inside the container. The container
// is always started read-only with all capabilities dropped; only --tmpfs mounts
// and the workspace bind are writable.
func (c containerIsolator) Wrap(command string, opts Options, env []string) ([]string, error) {
	if opts.Workspace == "" {
		return nil, errors.New("container: workspace required")
	}
	if c.image == "" {
		return nil, errors.New("container: image required")
	}

	network := "none"
	if opts.Network {
		network = "bridge"
	}

	argv := []string{c.runtime, "run", "--rm",
		"--network=" + network,
		"--read-only",
		"--cap-drop=ALL",
		"--pids-limit=512",
		"--memory=2g",
		// exec IS REQUIRED, and its absence made this backend unable to run Go
		// at all. Docker mounts --tmpfs noexec by default, and a Go toolchain
		// compiles its test binary into $TMPDIR (/tmp/go-build*/…) and then
		// EXECS it — so `go test` died with
		//     fork/exec /tmp/go-build…/ctest.test: permission denied
		// which reads as a broken project rather than a broken jail. Every
		// compile-then-run toolchain has this shape (cc into a temp a.out,
		// cargo, node-gyp), so it is not a Go quirk to special-case.
		//
		// It costs nothing real. noexec on /tmp is only a boundary if there is
		// no OTHER writable+executable location, and the workspace bind below
		// is exactly that — the mutant and its test are written there and run
		// from there, by design. The boundary this backend actually rests on
		// is untouched: --network=none, --read-only rootfs, --cap-drop=ALL,
		// --pids-limit, --memory. nosuid,nodev are kept.
		"--tmpfs", "/tmp:rw,exec,nosuid,nodev",
		"--tmpfs", "/home/agent:rw,exec,nosuid,nodev",
		"-e", "HOME=/home/agent",
		// Offline jail: pin GOTOOLCHAIN=local so `go` never tries to download a
		// go.mod-pinned toolchain (mirrors the bwrap backend). See isolator_linux.go.
		"-e", "GOTOOLCHAIN=local",
	}
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i > 0 {
			if kv[:i] == "HOME" || kv[:i] == "GOTOOLCHAIN" {
				continue // already pinned above
			}
			argv = append(argv, "-e", kv)
		}
	}
	argv = append(argv,
		"-v", opts.Workspace+":"+opts.Workspace,
		"-w", opts.Workspace,
	)
	for _, bnd := range opts.ReadOnlyBinds {
		argv = append(argv, "-v", bnd.Host+":"+bnd.Target+":ro")
	}
	argv = append(argv,
		c.image,
		"sh", "-c", command,
	)
	return argv, nil
}
