// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"

	"github.com/pdbethke/corralai/internal/sandbox"
)

// resolveLocalJail resolves the sandbox for a --local run from the --jail flag
// (or auto), returning an actionable error and NEVER a weaker/unsandboxed
// backend. Fail closed: corral does not run untrusted test+code unsandboxed.
//
// jailFlag empty or "auto" tries the platform default backend (bwrap on
// Linux, sandbox-exec on macOS) via sandbox.Resolve. A bwrap userns/preflight
// failure — the Ubuntu 24.04 apparmor default that disables unprivileged user
// namespaces — is turned into an actionable error via bwrapUnavailableError
// instead of the raw plumbing error. "container" resolves the docker/podman
// backend and surfaces sandbox.Resolve's own clear error when no runtime/
// image is configured. "none" (and any UnsafeHost path) is refused here
// unconditionally: --local always sandboxes, regardless of what the
// separate, env-gated test-suite unsafe path allows internally.
func resolveLocalJail(jailFlag string) (sandbox.Isolator, error) {
	flag := strings.TrimSpace(jailFlag)

	if flag == "none" {
		return nil, errors.New(
			"corral certify --local: --jail none is not supported — --local always sandboxes the code and tests it runs; " +
				"the unsandboxed AGENT_EXEC_UNSAFE_HOST path is an internal test-only escape hatch, not a product option")
	}

	backend := flag
	if backend == "auto" {
		backend = ""
	}

	iso, err := sandbox.Resolve(sandbox.Config{Backend: backend})
	if err == nil {
		// Defense in depth: Resolve should never hand back "none" unless
		// UnsafeHost was set (which we never set above), but refuse to
		// propagate it regardless of how it got here.
		if iso.Name() == "none" {
			return nil, errors.New("corral certify --local: refusing to run unsandboxed (\"none\" backend); this should be unreachable")
		}
		return iso, nil
	}

	// Only bwrap gets the apparmor-specific actionable rewrite; an explicit
	// --jail container failure should surface sandbox.Resolve's own message
	// (already clear: install docker/podman or set CORRALAI_EXEC_IMAGE).
	if backend == "" || backend == "bwrap" {
		return nil, bwrapUnavailableError(err)
	}
	return nil, err
}

// bwrapUnavailableError turns a raw bwrap Preflight/Resolve failure into a
// concise, copy-pasteable actionable error: it names the Ubuntu 24.04 apparmor
// cause (unprivileged userns disabled by the distro default), the surgical
// fix, and the --jail container fallback. It is a pure formatter — no bwrap
// invocation — so it is unit-testable without a working (or degraded) bwrap
// on the host.
func bwrapUnavailableError(cause error) error {
	return fmt.Errorf(
		`no working bwrap sandbox: %w

corral never runs untrusted test/mutant code unsandboxed. On Ubuntu 24.04+
apparmor disables unprivileged user namespaces by default, which is the usual
cause. Fix it with a surgical profile that allows only bwrap's own binary:

  printf 'abi <abi/4.0>,\ninclude <tunables/global>\n\n/usr/bin/bwrap flags=(unconfined) {\n  userns,\n  include if exists <local/bwrap>\n}\n' | sudo tee /etc/apparmor.d/bwrap
  sudo systemctl reload apparmor

Or skip bwrap entirely: --jail container (needs docker or podman, plus
CORRALAI_EXEC_IMAGE set to an image with your toolchain).`, cause)
}

// hasContainerRuntime reports whether docker or podman is on PATH, so tests
// that expect the "no runtime found" error can skip cleanly on a host that
// happens to have one installed rather than flake.
func hasContainerRuntime() bool {
	if _, err := exec.LookPath("podman"); err == nil {
		return true
	}
	if _, err := exec.LookPath("docker"); err == nil {
		return true
	}
	return false
}
