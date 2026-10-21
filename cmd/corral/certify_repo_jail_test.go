// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/sandbox"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// certify --repo had no --jail, so the scan always took the platform default
// (bwrap on Linux) while certify --local and doctor could run their jail in a
// container image that carries the project's toolchain. The flag must reach
// the one resolver every sandbox goes through, and with the --jail advice on
// failure, since this command now offers it.
func TestNewLocalExecutorWithJailHandsTheFlagToTheResolver(t *testing.T) {
	var gotFlag string
	var gotFallback bool
	orig := resolveJailFn
	resolveJailFn = func(flag string, fallback bool) (sandbox.Isolator, error) {
		gotFlag, gotFallback = flag, fallback
		return nil, errors.New("no container runtime in this test")
	}
	t.Cleanup(func() { resolveJailFn = orig })

	ex := newLocalExecutorWithJail(t.TempDir(), nil, substrateJail, "container", 0, io.Discard)
	defer ex.Close()
	if gotFlag != "container" {
		t.Fatalf("resolver got --jail %q, want %q", gotFlag, "container")
	}
	if !gotFallback {
		t.Error("certify --repo now offers --jail, so a bwrap failure may advise `--jail container`")
	}
	if err := ex.preflight(); err == nil || !strings.Contains(err.Error(), "no container runtime") {
		t.Errorf("preflight = %v, want the resolver's own error", err)
	}
}

// The workspace substrate runs no sandbox, so a --jail beside it would be
// recorded as chosen and never used: a usage error, before any work.
func TestCertifyRepoRefusesJailWithWorkspaceSubstrate(t *testing.T) {
	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{"--repo", t.TempDir(), "--substrate", substrateWorkspace, "--jail", "container",
		"--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--dry-run"}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit %d, want 2; stderr=%s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "--jail") || !strings.Contains(errb.String(), substrateWorkspace) {
		t.Errorf("stderr must name --jail and the workspace substrate: %s", errb.String())
	}
}

// An unknown backend is a usage error naming what is accepted, not a silent
// fall-through to the default sandbox.
func TestCertifyRepoRefusesUnknownJail(t *testing.T) {
	var out, errb bytes.Buffer
	code := runCertifyRepo([]string{"--repo", t.TempDir(), "--jail", "chroot",
		"--writer-model", testHerdWriter, "--mutant-model", testHerdMutant, "--critic-model", "off", "--dry-run"}, &out, &errb)
	if code != 2 {
		t.Fatalf("exit %d, want 2; stderr=%s", code, errb.String())
	}
	if !strings.Contains(errb.String(), `"chroot"`) || !strings.Contains(errb.String(), "container") {
		t.Errorf("stderr must name the bad value and the accepted ones: %s", errb.String())
	}
}

// The jail the executor resolved is the one the record names: a test isolator
// stands in for bwrap, and its name must be what sandboxIdentity reports.
// The workspace substrate resolves none and names none; a dry run has no
// executor at all.
func TestTheExecutorNamesTheJailItResolved(t *testing.T) {
	orig := resolveJailFn
	resolveJailFn = func(string, bool) (sandbox.Isolator, error) { return namedJail("bwrap"), nil }
	t.Cleanup(func() { resolveJailFn = orig })

	ex := newLocalExecutorWithJail(t.TempDir(), nil, substrateJail, "", 0, io.Discard)
	defer ex.Close()
	if got := ex.sandboxIdentity(); got != (sandbox.Identity{Backend: "bwrap"}) {
		t.Errorf("jail substrate identity = %+v, want bwrap", got)
	}
	ws := newLocalExecutorWithJail(t.TempDir(), nil, substrateWorkspace, "", 0, io.Discard)
	defer ws.Close()
	if got := ws.sandboxIdentity(); got != (sandbox.Identity{}) {
		t.Errorf("workspace substrate identity = %+v, want none", got)
	}
	var none *localExecutor
	if got := none.sandboxIdentity(); got != (sandbox.Identity{}) {
		t.Errorf("dry run identity = %+v, want none", got)
	}
}

// bundleMeta's sandbox must survive into the signed scan row: buildBundle is
// a field-by-field copy, the shape that drops whatever nobody remembered.
func TestBuildBundleCarriesTheSandbox(t *testing.T) {
	sb := sandbox.Identity{Backend: "container", Image: "py:1@sha256:11855f76"}
	b := buildBundle(scanstore.Scan{Repo: "o/r", Commit: "c", Substrate: substrateJail}, 1, nil, nil, nil, nil,
		auditpush.Link{}, false, "o/r", "c", "", bundleMeta{Sandbox: sb})
	if b.Scan.SandboxBackend != sb.Backend || b.Scan.SandboxImage != sb.Image {
		t.Errorf("scan row sandbox = %q %q, want %+v", b.Scan.SandboxBackend, b.Scan.SandboxImage, sb)
	}
}

type namedJail string

func (n namedJail) Name() string                                           { return string(n) }
func (namedJail) Preflight() error                                         { return nil }
func (namedJail) Wrap(string, sandbox.Options, []string) ([]string, error) { return nil, nil }
