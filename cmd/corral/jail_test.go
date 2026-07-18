// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"errors"
	"runtime"
	"strings"
	"testing"
)

// resolveLocalJail must NEVER return a "none"/unsafe backend, even if the
// caller explicitly asks for it via --jail none. --local always sandboxes;
// the test suite's separate AGENT_EXEC_UNSAFE_HOST path is not reachable here.
func TestResolveLocalJail_NoneAlwaysRejected(t *testing.T) {
	iso, err := resolveLocalJail("none")
	if err == nil {
		t.Fatalf("resolveLocalJail(\"none\") = %v, nil; want an error", iso)
	}
	if iso != nil {
		t.Fatalf("resolveLocalJail(\"none\") returned a non-nil isolator %v; must never return one for \"none\"", iso)
	}
	if !strings.Contains(err.Error(), "--local") {
		t.Errorf("error %q should explain that --local always sandboxes", err.Error())
	}
}

// bwrapUnavailableError is the testable seam: the actionable-error formatter,
// exercised directly so the test doesn't need a real (degraded) bwrap on the
// host to trigger the failure path.
func TestBwrapUnavailableError_NamesTheFixAndTheAlternative(t *testing.T) {
	cause := errors.New("bwrap cannot create a sandbox (user namespaces disabled?): exit status 1")
	err := bwrapUnavailableError(cause)
	if err == nil {
		t.Fatal("bwrapUnavailableError(cause) = nil; want a non-nil error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "apparmor") {
		t.Errorf("error message %q must name the apparmor fix", msg)
	}
	if !strings.Contains(msg, "--jail container") {
		t.Errorf("error message %q must suggest --jail container", msg)
	}
	if !strings.Contains(msg, "/etc/apparmor.d/bwrap") {
		t.Errorf("error message %q should point at the surgical apparmor profile path", msg)
	}
	if !strings.Contains(msg, cause.Error()) {
		t.Errorf("error message %q should preserve the underlying cause for debugging", msg)
	}
}

// resolveLocalJail("container") must never silently succeed to an unsandboxed
// backend when no container runtime/image is configured — it should surface
// the underlying sandbox.Resolve error.
func TestResolveLocalJail_ContainerSurfacesUnderlyingError(t *testing.T) {
	t.Setenv("CORRALAI_EXEC_RUNTIME", "")
	t.Setenv("CORRALAI_EXEC_IMAGE", "")
	// Only meaningful when docker/podman really are absent from this host's
	// PATH; skip rather than flake on a host that happens to have one.
	if hasContainerRuntime() {
		t.Skip("a container runtime is present on this host; nothing to assert here")
	}
	iso, err := resolveLocalJail("container")
	if err == nil {
		t.Fatalf("resolveLocalJail(\"container\") = %v, nil; want an error with no runtime configured", iso)
	}
	if iso != nil {
		t.Fatalf("resolveLocalJail(\"container\") returned a non-nil isolator %v on error", iso)
	}
}

// A degraded/unavailable auto-detected default backend must fail closed with
// an actionable message rather than panic or silently return an unsandboxed
// isolator. This does not require a real bwrap; on this dev host bwrap is
// known-degraded so the auto path is expected to fail here too — the
// assertion is only that it fails CLOSED with useful text, not on Linux
// specifically succeeding.
func TestResolveLocalJail_AutoNeverReturnsUnsafe(t *testing.T) {
	iso, err := resolveLocalJail("")
	if err != nil {
		if iso != nil {
			t.Fatalf("resolveLocalJail(\"\") returned both a non-nil isolator and an error")
		}
		if runtime.GOOS == "linux" && strings.Contains(err.Error(), "bwrap") {
			if !strings.Contains(err.Error(), "--jail container") {
				t.Errorf("bwrap auto-detect failure %q should suggest --jail container", err.Error())
			}
		}
		return
	}
	if iso == nil {
		t.Fatal("resolveLocalJail(\"\") returned nil, nil")
	}
	if iso.Name() == "none" {
		t.Fatalf("resolveLocalJail(\"\") auto-detected the \"none\" backend; must never do this")
	}
}
