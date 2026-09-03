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
	if other, ok := c.imageIsOnlyInTheOtherRuntime(); ok {
		return fmt.Errorf("container backend: image %q is not in %s's local store, but it IS in %s's — "+
			"corral chose %s (podman is preferred over docker; set CORRALAI_EXEC_RUNTIME to override). "+
			"Either build the image with %s, or run with CORRALAI_EXEC_RUNTIME=%s",
			c.image, c.runtime, other, c.runtime, c.runtime, other)
	}
	return nil
}

// imageIsOnlyInTheOtherRuntime reports the OTHER installed runtime when the
// image is absent from the chosen runtime's store and present in that one.
//
// THIS EXACT CONFUSION COST FOUR CI ROUND TRIPS. A machine with both runtimes
// installed — ubuntu-latest is one, and so is any developer box with Docker
// Desktop and podman — silently prefers podman. An image built with
// `docker build` is then invisible, and the failure surfaces as podman trying
// to PULL it from docker.io and being denied, which reads like a registry
// auth problem and says nothing about the runtime corral picked or the store
// the image is actually sitting in.
//
// It is deliberately narrow: it fires ONLY when the image is demonstrably
// present in the other runtime. A plain "not found locally" must NOT fail
// preflight — pulling an image from a registry is an ordinary, supported
// setup, and refusing it would break every operator who names a public image
// they have not pulled yet.
func (c containerIsolator) imageIsOnlyInTheOtherRuntime() (string, bool) {
	if c.image == "" {
		return "", false
	}
	other := "docker"
	if c.runtime == "docker" {
		other = "podman"
	}
	if _, err := exec.LookPath(other); err != nil {
		return "", false
	}
	if hasImageLocally(c.runtime, c.image) {
		return "", false
	}
	if !hasImageLocally(other, c.image) {
		return "", false
	}
	return other, true
}

// hasImageLocally reports whether runtime's LOCAL store holds image. Any error
// (runtime missing, daemon down, unexpected output) answers false, so this can
// only ever add information — never turn a working setup into a failure.
func hasImageLocally(runtime, image string) bool {
	var cmd *exec.Cmd
	switch runtime {
	case "podman":
		// #nosec G204 -- runtime is podman/docker (this switch, or the operator's
		// own CORRALAI_EXEC_RUNTIME) and image is the operator's own
		// CORRALAI_EXEC_IMAGE: local configuration, not remote input. Both are
		// separate argv elements with no shell, so neither can inject a command;
		// this is the same trust boundary Wrap already runs the audit itself
		// under, and it only ever READS the local image store.
		cmd = exec.Command(runtime, "image", "exists", image)
	default:
		// #nosec G204 -- see the podman branch above: operator-supplied runtime
		// and image, passed as argv, read-only query.
		cmd = exec.Command(runtime, "image", "inspect", image)
	}
	cmd.Stdout, cmd.Stderr = nil, nil
	return cmd.Run() == nil
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
