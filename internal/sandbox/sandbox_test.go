// SPDX-License-Identifier: Elastic-2.0

package sandbox

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRunCapturesOutputAndExit(t *testing.T) {
	r := Run(context.Background(), "echo hello world", Options{Workspace: t.TempDir(), Backend: noneIsolator{}})
	if r.ExitCode != 0 {
		t.Fatalf("exit = %d, want 0 (%s)", r.ExitCode, r.Err)
	}
	if !strings.Contains(r.Output, "hello world") {
		t.Fatalf("output = %q, want it to contain 'hello world'", r.Output)
	}
}

func TestRunNonzeroExit(t *testing.T) {
	r := Run(context.Background(), "exit 3", Options{Workspace: t.TempDir(), Backend: noneIsolator{}})
	if r.ExitCode != 3 {
		t.Fatalf("exit = %d, want 3", r.ExitCode)
	}
}

func TestRunTimesOut(t *testing.T) {
	start := time.Now()
	r := Run(context.Background(), "sleep 10", Options{Workspace: t.TempDir(), Timeout: 300 * time.Millisecond, Backend: noneIsolator{}})
	if !r.TimedOut {
		t.Fatalf("expected TimedOut, got %+v", r)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("timeout did not kill the process promptly")
	}
}

func TestRunCwdIsWorkspace(t *testing.T) {
	ws := t.TempDir()
	r := Run(context.Background(), "pwd", Options{Workspace: ws, Backend: noneIsolator{}})
	// macOS /tmp symlinks to /private/tmp; just require the basename matches.
	if !strings.Contains(r.Output, ws) && !strings.HasSuffix(strings.TrimSpace(r.Output), lastSeg(ws)) {
		t.Fatalf("pwd = %q, want the workspace %q", r.Output, ws)
	}
}

func TestRunEnvIsSecretFree(t *testing.T) {
	t.Setenv("CORRAL_TOKEN", "super-secret")
	// Default env is MinimalEnv(), which must not carry CORRAL_TOKEN.
	r := Run(context.Background(), "echo TOKEN=[$CORRAL_TOKEN]", Options{Workspace: t.TempDir(), Backend: noneIsolator{}})
	if strings.Contains(r.Output, "super-secret") {
		t.Fatalf("executed command saw a parent secret: %q", r.Output)
	}
	if !strings.Contains(r.Output, "TOKEN=[]") {
		t.Fatalf("expected the secret to be empty in the command env, got %q", r.Output)
	}
}

func TestRunOutputCap(t *testing.T) {
	r := Run(context.Background(), "yes x | head -c 100000", Options{Workspace: t.TempDir(), MaxOutput: 1000, Backend: noneIsolator{}})
	if len(r.Output) > 1200 { // cap + truncation note
		t.Fatalf("output not capped: %d bytes", len(r.Output))
	}
	if !strings.Contains(r.Output, "truncated") {
		t.Fatalf("expected a truncation note, got %d bytes", len(r.Output))
	}
}

func TestRunNilBackendDisabled(t *testing.T) {
	r := Run(context.Background(), "echo hi", Options{Workspace: t.TempDir()})
	if r.Err == "" || r.ExitCode != -1 {
		t.Fatalf("nil backend must be disabled, got %+v", r)
	}
}

func lastSeg(p string) string {
	parts := strings.Split(strings.TrimRight(p, "/"), "/")
	return parts[len(parts)-1]
}

// TestMinimalEnvHasNoGitToken is a credential-boundary guard: MinimalEnv() is the
// secret-free allowlist passed into sandboxed bee commands. It must never carry
// CORRALAI_GIT_TOKEN — the token is owned exclusively by cmd/corral and lives only
// in repo.Engine. This test passes immediately (MinimalEnv only allowlists
// PATH/HOME/LANG/LC_ALL/TMPDIR); it LOCKS the boundary so a future MinimalEnv
// change can't silently leak the token.
func TestMinimalEnvHasNoGitToken(t *testing.T) {
	t.Setenv("CORRALAI_GIT_TOKEN", "supersecret")
	for _, kv := range MinimalEnv() {
		if strings.Contains(kv, "CORRALAI_GIT_TOKEN") || strings.Contains(kv, "supersecret") {
			t.Fatalf("the secret-free jail env must never carry the git token: %q", kv)
		}
	}
}
