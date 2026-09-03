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
		// --memory alone leaves Docker's default swap allowance, which is the
		// SAME size again — so `--memory=2g` actually permitted 2 GiB of
		// memory plus 2 GiB of swap (measured: memory.swap.max=2147483648).
		// Pinning them equal makes the flag mean what it reads as.
		"--memory-swap=2g",
		// A dropped capability set is not the same as a promise that none can
		// be regained: without this, a setuid binary inside the image can
		// still raise privileges during exec. Measured before adding it:
		// NoNewPrivs: 0.
		"--security-opt=no-new-privileges",
		// exec IS REQUIRED, and its absence made this backend unable to run Go
		// at all. Docker mounts --tmpfs noexec by default, and a Go toolchain
		// compiles its test binary into $TMPDIR (/tmp/go-build*/…) and then
		// EXECS it — so `go test` died with
		//     fork/exec /tmp/go-build…/ctest.test: permission denied
		// which reads as a broken project rather than a broken jail. Every
		// compile-then-run toolchain has this shape (cc into a temp a.out,
		// cargo, node-gyp), so it is not a Go quirk to special-case.
		//
		// WHAT THIS COSTS, stated correctly. An earlier version of this comment
		// argued it "costs nothing real" because the workspace bind is already
		// a writable+executable location. THAT IS FALSE on this backend: the
		// workspace is 0755/0644 owned by the HOST uid (adequacy/jail.go),
		// the container runs as root with --cap-drop=ALL, and root without
		// CAP_DAC_OVERRIDE cannot write a directory it does not own. Measured
		// inside the real jail:
		//
		//   touch <workspace>/x        Permission denied
		//   python -m compileall .     PermissionError on ./__pycache__
		//   exec from /tmp             works
		//
		// So /tmp is the ONLY writable location, and making it executable is a
		// real widening, not a no-op. It is accepted deliberately: without it
		// every compile-then-run toolchain fails as a broken project rather
		// than a broken jail, which is worse — a jail that silently cannot
		// grade is how this backend produced vacuous passes. The boundary that
		// actually contains an escape is untouched: --network=none,
		// --read-only rootfs, --cap-drop=ALL, --security-opt=no-new-privileges,
		// --pids-limit, --memory. nosuid and nodev are kept on both tmpfs.
		"--tmpfs", "/tmp:rw,exec,nosuid,nodev",
		"--tmpfs", "/home/agent:rw,exec,nosuid,nodev",
		"-e", "HOME=/home/agent",
		// Offline jail: pin GOTOOLCHAIN=local so `go` never tries to download a
		// go.mod-pinned toolchain (mirrors the bwrap backend). See isolator_linux.go.
		"-e", "GOTOOLCHAIN=local",
	}
	for _, kv := range env {
		if i := strings.IndexByte(kv, '='); i > 0 {
			// PATH IS DROPPED, NOT FORWARDED. MinimalEnv always carries the
			// host's PATH, and forwarding it REPLACES the image's — measured
			// against golang:1.26.6, `go` disappears entirely:
			//
			//   with the host PATH:  GO NOT FOUND
			//   with the image's:    /usr/local/go/bin/go
			//
			// It survived only because python:3.12-slim keeps python3 in
			// /usr/bin, which the host PATH also lists — so this failed
			// silently and image-dependently, on exactly the toolchains that
			// install somewhere unusual. It also leaked the operator's home
			// directory layout into an untrusted container for no benefit.
			// The image knows where its own tools are.
			if kv[:i] == "HOME" || kv[:i] == "GOTOOLCHAIN" || kv[:i] == "PATH" {
				continue // already pinned above, or owned by the image
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
