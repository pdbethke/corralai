// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"errors"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/sandbox"
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
	err := bwrapUnavailableError(cause, true)
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

// `corral certify --repo` exposes no --jail flag, so its bwrap failure must not
// advise `--jail container`: an escape hatch unreachable from the command that
// printed the message is worse than no advice at all.
func TestBwrapUnavailableError_OmitsTheContainerHatchWhenUnreachable(t *testing.T) {
	cause := errors.New("bwrap cannot create a sandbox (user namespaces disabled?): exit status 1")
	msg := bwrapUnavailableError(cause, false).Error()
	if strings.Contains(msg, "--jail") {
		t.Errorf("message %q advises a --jail flag the calling command does not expose", msg)
	}
	// The actionable half must survive: the operator can still fix the host.
	if !strings.Contains(msg, "/etc/apparmor.d/bwrap") {
		t.Errorf("message %q dropped the apparmor fix", msg)
	}
	if !strings.Contains(msg, cause.Error()) {
		t.Errorf("message %q dropped the underlying cause", msg)
	}
}

// TestNewRunJailIsTheOnlyJailBuilder pins the structural parity guarantee by
// construction: doctor's preflight probe and certify --local's actual
// scoring/enumeration jails must go through exactly ONE function
// (newRunJail/newRunEnumerator), never a second, independent call to
// adequacy.NewJail/NewEnumerator — that duplication is exactly how doctor
// and the run it preflights came to probe two different sandbox realities.
// Checked by reading this package's own non-test source rather than by
// comparing behavior, because the whole point is that there is nowhere left
// for a divergent SECOND builder to hide.
func TestNewRunJailIsTheOnlyJailBuilder(t *testing.T) {
	src := packageSourceExcludingTests(t)

	if n := strings.Count(src, "func newRunJail("); n != 1 {
		t.Fatalf("newRunJail must have exactly one definition in this package, found %d", n)
	}
	if n := strings.Count(src, "func newRunEnumerator("); n != 1 {
		t.Fatalf("newRunEnumerator must have exactly one definition in this package, found %d", n)
	}

	// jail.go itself holds the ONE direct call to each lower-level adequacy
	// constructor, inside newRunJail/newRunEnumerator — excepted below by
	// name, not skipped by exempting the whole file, since a SECOND direct
	// call landing anywhere else in jail.go would be exactly the kind of
	// divergence this test exists to catch. certify_repo.go's (--repo, not
	// --local) coverage-preflight enumerator is a genuinely different,
	// deliberately one-off shape (see its own comment: "built SPECIFICALLY
	// for this one call") and is excepted too — but every OTHER file in this
	// package, not just the two doctor.go/certify_local.go this bug was
	// first found in, must go through the shared builder. A third call site
	// added anywhere else in the package — a new command, a new scan mode —
	// would be exactly this bug happening again in a file nobody thought to
	// name here, so this scans the WHOLE package rather than an enumerated
	// allowlist of files.
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		if name == "jail.go" || name == "certify_repo.go" {
			continue
		}
		s := sourceOf(t, name)
		if strings.Contains(s, "adequacy.NewJail(") {
			t.Errorf("%s calls adequacy.NewJail directly — it must go through newRunJail, the shared builder, or it can silently diverge from the other call site again", name)
		}
		if strings.Contains(s, "adequacy.NewEnumerator(") {
			t.Errorf("%s calls adequacy.NewEnumerator directly — it must go through newRunEnumerator", name)
		}
	}
	for _, name := range []string{"doctor.go", "certify_local.go"} {
		s := sourceOf(t, name)
		if !strings.Contains(s, "newRunJail(") {
			t.Errorf("%s must build its jail via newRunJail, the shared builder", name)
		}
	}
}

// TestNewRunJailBuildsIdenticalConfigForIdenticalInputs pins parity at the
// value level, not just the source level: the same iso/timeout/depBinds
// handed to newRunJail twice must build two jails with an identical
// configuration — nothing about doctor's own call is special-cased inside
// the builder.
func TestNewRunJailBuildsIdenticalConfigForIdenticalInputs(t *testing.T) {
	iso, err := sandbox.Resolve(sandbox.Config{})
	if err != nil {
		t.Skipf("no working sandbox backend on this host: %v", err)
	}
	binds := []adequacy.DepBind{{Host: "/tmp/does-not-need-to-exist", Rel: "node_modules"}}

	a := newRunJail(iso, 45*time.Second, binds)
	b := newRunJail(iso, 45*time.Second, binds)
	if !reflect.DeepEqual(a, b) {
		t.Fatalf("newRunJail built two different configurations from identical inputs:\n%#v\n%#v", a, b)
	}

	ae := newRunEnumerator(iso, 45*time.Second, binds)
	be := newRunEnumerator(iso, 45*time.Second, binds)
	if !reflect.DeepEqual(ae, be) {
		t.Fatalf("newRunEnumerator built two different configurations from identical inputs:\n%#v\n%#v", ae, be)
	}
}

// packageSourceExcludingTests concatenates every non-test .go file in this
// package, for the structural (source-text) assertions above.
func packageSourceExcludingTests(t *testing.T) string {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b.WriteString(sourceOf(t, name))
		b.WriteString("\n")
	}
	return b.String()
}

func sourceOf(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
