// SPDX-License-Identifier: Elastic-2.0

//go:build linux

package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func bwrapOrSkip(t *testing.T) Isolator {
	t.Helper()
	b := bwrapIsolator{}
	if err := b.Preflight(); err != nil {
		t.Skipf("bwrap unavailable: %v", err)
	}
	return b
}

func TestBwrapConfinesWritesAndReads(t *testing.T) {
	iso := bwrapOrSkip(t)
	ws := t.TempDir()
	// A write INSIDE the workspace persists to the host.
	r := Run(context.Background(), "echo hi > made.txt && cat made.txt", Options{Workspace: ws, Backend: iso})
	if r.ExitCode != 0 || !strings.Contains(r.Output, "hi") {
		t.Fatalf("in-workspace write should work: %+v", r)
	}
	if _, err := os.ReadFile(filepath.Join(ws, "made.txt")); err != nil {
		t.Fatalf("in-workspace write should persist to the host: %v", err)
	}
	// A write to an absolute HOST path OUTSIDE the workspace must NOT reach the
	// host: that path isn't bound into the jail, so the write either fails or lands
	// in the jail's throwaway tmpfs — the host file must never appear. (If the jail
	// leaked, i.e. the host root were bound writable, this host file WOULD appear.)
	outside := t.TempDir() // a host dir deliberately NOT bound into the jail
	escape := filepath.Join(outside, "pwned")
	Run(context.Background(), "echo pwned > "+escape, Options{Workspace: ws, Backend: iso})
	if _, err := os.Stat(escape); err == nil {
		t.Fatalf("write escaped the jail onto the host at %s", escape)
	}
	// Host files outside the minimal read-only root are not even visible.
	r = Run(context.Background(), "cat /etc/hostname", Options{Workspace: ws, Backend: iso})
	if r.ExitCode == 0 {
		t.Fatalf("host /etc/hostname must be invisible in the jail: %+v", r)
	}
}

func TestBwrapCwdAndSecretFree(t *testing.T) {
	iso := bwrapOrSkip(t)
	t.Setenv("CORRAL_TOKEN", "super-secret")
	ws := t.TempDir()
	r := Run(context.Background(), "echo TOKEN=[$CORRAL_TOKEN]", Options{Workspace: ws, Backend: iso})
	if !strings.Contains(r.Output, "TOKEN=[]") {
		t.Fatalf("the command's env must be secret-free, got %q", r.Output)
	}
}

func TestBwrapRlimitLiveEchoOk(t *testing.T) {
	iso := bwrapOrSkip(t)
	r := Run(context.Background(), "echo ok", Options{Workspace: t.TempDir(), Backend: iso})
	if r.ExitCode != 0 {
		t.Fatalf("echo ok with rlimit prelude should exit 0: %+v", r)
	}
	if !strings.Contains(r.Output, "ok") {
		t.Fatalf("expected 'ok' in output, got %q", r.Output)
	}
}

func TestBwrapHomeIsWritable(t *testing.T) {
	iso := bwrapOrSkip(t)
	r := Run(context.Background(), `echo "HOME=$HOME"; echo probe > "$HOME/probe" && cat "$HOME/probe"`, Options{Workspace: t.TempDir(), Backend: iso})
	if r.ExitCode != 0 {
		t.Fatalf("writing to $HOME in the jail should work: %+v", r)
	}
	if !strings.Contains(r.Output, "HOME=/home/agent") || !strings.Contains(r.Output, "probe") {
		t.Fatalf("HOME should be /home/agent and writable, got %q", r.Output)
	}
}
