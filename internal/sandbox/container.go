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
		return errors.New("container backend: set CORRALAI_EXEC_IMAGE to the image to run commands in")
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
		"--tmpfs", "/tmp",
		"--tmpfs", "/home/agent",
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
