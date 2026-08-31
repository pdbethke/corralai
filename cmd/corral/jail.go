// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// newRunJail is the ONE place in this package that turns a resolved sandbox
// backend into the adequacy.Jail an actual run scores mutants with. Every
// caller — the real `--local` scoring/validation/enumeration jails AND
// doctor's own preflight probe — MUST go through this function, never call
// adequacy.NewJail directly, so the two can never quietly diverge.
//
// This exists because they DID diverge once, silently: doctor built its own
// jail with no dependency binds while a `--repo-dir` run built one with
// depBinds (auto-detected .venv/node_modules/vendor dirs) via a second,
// separate call to adequacy.NewJail. The preflight and the run were probing
// two different sandbox realities, and doctor had no way to notice — the
// rehearsal that motivated this: doctor reported the toolchain reachable,
// the real run then died with `.venv/bin/python: not found`, 27k tokens
// spent on mutants that were never graded. depBinds nil/empty behaves
// identically to omitting the option — WithReadOnlyBinds(nil) leaves the
// jail's binds unset — so this is always safe to call unconditionally,
// bare mode included.
func newRunJail(iso sandbox.Isolator, timeout time.Duration, depBinds []adequacy.DepBind) adequacy.Jail {
	return adequacy.NewJail(iso, timeout, adequacy.WithReadOnlyBinds(depBinds))
}

// newRunEnumerator is newRunJail's Enumerator-returning sibling — same
// backend/timeout/binds contract, same single-definition requirement. Wired
// wherever an Enumerator is built alongside a Jail off the same inputs.
func newRunEnumerator(iso sandbox.Isolator, timeout time.Duration, depBinds []adequacy.DepBind) adequacy.Enumerator {
	return adequacy.NewEnumerator(iso, timeout, adequacy.WithReadOnlyBinds(depBinds))
}

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
	return resolveJailFn(jailFlag, true)
}

// resolveJailFn is the single seam EVERY sandbox resolution in this command
// goes through. A package var solely so a test can COUNT resolutions: "the
// repo scan resolves the sandbox once for the whole scan and hands it to
// every job" is a claim about how many times the backend probe runs, and it
// cannot be checked by comparing the isolators themselves — bwrapIsolator is
// an empty struct, so two INDEPENDENT resolutions compare equal and an
// identity assertion can never fail. Never reassigned in production.
var resolveJailFn = resolveJail

// resolveScanJail resolves the sandbox for `corral certify --repo`. Same
// fail-closed rules as resolveLocalJail, but the scan exposes no --jail flag,
// so a bwrap failure must not advise `--jail container`: an operator who never
// ran --local cannot reach that escape hatch from the command that printed the
// message.
func resolveScanJail() (sandbox.Isolator, error) {
	return resolveJailFn("", false)
}

// resolveJail is the shared implementation. containerFallback says whether the
// calling command actually exposes --jail, and therefore whether the bwrap
// advice may point at `--jail container`.
func resolveJail(jailFlag string, containerFallback bool) (sandbox.Isolator, error) {
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
		return nil, bwrapUnavailableError(err, containerFallback)
	}
	return nil, err
}

// bwrapUnavailableError turns a raw bwrap Preflight/Resolve failure into a
// concise, copy-pasteable actionable error: it names the Ubuntu 24.04 apparmor
// cause (unprivileged userns disabled by the distro default), the surgical
// fix, and — only for commands that actually expose --jail — the container
// fallback. It is a pure formatter — no bwrap invocation — so it is
// unit-testable without a working (or degraded) bwrap on the host.
func bwrapUnavailableError(cause error, containerFallback bool) error {
	tail := `Otherwise run on a host where bwrap can isolate: this command has no
sandbox-backend override.`
	if containerFallback {
		tail = `Or skip bwrap entirely: --jail container (needs docker or podman, plus
CORRALAI_EXEC_IMAGE set to an image with your toolchain, e.g.
CORRALAI_EXEC_IMAGE=python:3.12-bookworm).`
	}
	return fmt.Errorf(
		`no working bwrap sandbox: %w

corral never runs untrusted test/mutant code unsandboxed. On Ubuntu 24.04+
apparmor disables unprivileged user namespaces by default, which is the usual
cause. Fix it with a surgical profile that allows only bwrap's own binary:

  printf 'abi <abi/4.0>,\ninclude <tunables/global>\n\n/usr/bin/bwrap flags=(unconfined) {\n  userns,\n  include if exists <local/bwrap>\n}\n' | sudo tee /etc/apparmor.d/bwrap
  sudo systemctl reload apparmor

%s`, cause, tail)
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
