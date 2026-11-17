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

// overlayingBwrapOrSkip is the real bwrap isolator as Resolve builds it, with
// its construction-time probes run, skipped (and so printed by the CI census)
// when bwrap is unusable or cannot overlay at all.
func overlayingBwrapOrSkip(t *testing.T) Isolator {
	t.Helper()
	iso, err := newBwrapIsolator()
	if err != nil {
		t.Skipf("bwrap unavailable: %v", err)
	}
	if !Overlays(iso) {
		t.Skip("this bwrap cannot overlay (older than 0.10, or overlayfs refused in a user namespace); seeded path unexercised")
	}
	return iso
}

// Review Focus 2: a seed overlaid at a target that is not its own path. The
// run reads the seed at /corral-work, its writes (a new file and a change to
// a seed file) land in its private upper layer, the seed on the host is
// unchanged, and a second run of the same seed sees none of the first's
// writes. A bwrap that overlays at a directory's own path but not at a target
// must say so (PlacesWorkspace false) and refuse, never bind the seed
// writable.
func TestBwrapSeedOverlaidAtTargetKeepsTheSeedUnchanged(t *testing.T) {
	iso := overlayingBwrapOrSkip(t)
	seed := t.TempDir()
	if err := os.WriteFile(filepath.Join(seed, "seedfile"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := Options{Workspace: seed, SeedLower: seed, Target: WorkTarget, Backend: iso}
	if !PlacesWorkspace(iso) {
		r := Run(context.Background(), "echo x > leaked", opts)
		if r.Err == "" || r.ExitCode != -1 {
			t.Errorf("a bwrap that cannot overlay at a target ran a seeded run there: %+v", r)
		}
		assertSeedUnchanged(t, seed)
		t.Skip("this bwrap overlays only at a directory's own path; the refusal is checked, the seeded-at-target path unexercised")
	}

	r := Run(context.Background(), "pwd; cat seedfile; echo one > written; echo changed > seedfile; cat written seedfile", opts)
	if r.ExitCode != 0 || r.Err != "" {
		t.Fatalf("first seeded run failed: %+v", r)
	}
	for _, want := range []string{WorkTarget + "\n", "hello\n", "one\nchanged"} {
		if !strings.Contains(r.Output+"\n", want) {
			t.Errorf("first run output %q lacks %q", r.Output, want)
		}
	}
	assertSeedUnchanged(t, seed)

	r = Run(context.Background(), "pwd; cat seedfile; test -e written && echo LEAKED || echo clean", opts)
	if r.ExitCode != 0 || !strings.Contains(r.Output, "hello") || !strings.Contains(r.Output, "clean") || strings.Contains(r.Output, "LEAKED") {
		t.Errorf("second run saw the first run's writes or lost the seed: %+v", r)
	}
	assertSeedUnchanged(t, seed)
}

// assertSeedUnchanged: the seed holds exactly seedfile, with "hello\n".
func assertSeedUnchanged(t *testing.T, seed string) {
	t.Helper()
	ents, err := os.ReadDir(seed)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 1 || ents[0].Name() != "seedfile" {
		names := []string{}
		for _, e := range ents {
			names = append(names, e.Name())
		}
		t.Errorf("the seed on the host changed: it holds %v", names)
	}
	if b, err := os.ReadFile(filepath.Join(seed, "seedfile")); err != nil || string(b) != "hello\n" {
		t.Errorf("the seed's file changed on the host: %q, %v", b, err)
	}
}

// A run placed at /corral-work sees its workspace there: pwd says so, and a
// write lands in the HOST Workspace directory, which is also the directory
// the host process (bwrap itself) was started in.
func TestBwrapRunAtTargetWritesThroughToTheHostDir(t *testing.T) {
	iso := bwrapOrSkip(t)
	ws := t.TempDir()
	r := Run(context.Background(), "pwd; echo hi > made.txt", Options{Workspace: ws, Target: WorkTarget, Backend: iso})
	if r.ExitCode != 0 || r.Err != "" {
		t.Fatalf("run at the target failed: %+v", r)
	}
	if strings.TrimSpace(r.Output) != WorkTarget {
		t.Errorf("pwd in the jail = %q, want %s", r.Output, WorkTarget)
	}
	if b, err := os.ReadFile(filepath.Join(ws, "made.txt")); err != nil || string(b) != "hi\n" {
		t.Errorf("the write did not reach the host workspace: %q, %v", b, err)
	}
	if _, err := os.Stat(WorkTarget); err == nil {
		t.Errorf("%s exists on the host; the target must exist only in the jail", WorkTarget)
	}

	// The host process runs in the HOST workspace (cmd.Dir), never at the
	// in-jail target: an isolator that runs its argv on the host as is
	// prints the directory it was started in.
	r = Run(context.Background(), "pwd", Options{Workspace: ws, Target: WorkTarget, Backend: hostPwdIsolator{}})
	if r.ExitCode != 0 || strings.TrimSpace(r.Output) != ws {
		t.Errorf("the host process ran in %q, want the host workspace %s (%+v)", r.Output, ws, r)
	}
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

// TestBwrapRustPinsGradedVersusNetworked runs the real jail: a graded run
// (no network) is told CARGO_NET_OFFLINE and has a writable CARGO_HOME; the
// networked dependency fetch is not told CARGO_NET_OFFLINE even when the host
// is, and still has the same writable CARGO_HOME for its download cache.
func TestBwrapRustPinsGradedVersusNetworked(t *testing.T) {
	iso := bwrapOrSkip(t)
	t.Setenv("CARGO_NET_OFFLINE", "false")
	ws := t.TempDir()
	probe := `echo "offline=${CARGO_NET_OFFLINE-unset} rustup=$RUSTUP_HOME"; mkdir -p "$CARGO_HOME/registry" && echo cargohome-writable`
	env := append(MinimalEnv(), "CARGO_NET_OFFLINE=false")

	graded := Run(context.Background(), probe, Options{Workspace: ws, Backend: iso, Env: env})
	if graded.ExitCode != 0 || !strings.Contains(graded.Output, "offline=true rustup=/usr/local/rustup") || !strings.Contains(graded.Output, "cargohome-writable") {
		t.Errorf("graded run: want offline=true and a writable CARGO_HOME, got %+v", graded)
	}
	fetch := Run(context.Background(), probe, Options{Workspace: ws, Backend: iso, Network: true, Env: env})
	if fetch.ExitCode != 0 || !strings.Contains(fetch.Output, "offline=unset") || !strings.Contains(fetch.Output, "cargohome-writable") {
		t.Errorf("networked fetch: want CARGO_NET_OFFLINE unset and a writable CARGO_HOME, got %+v", fetch)
	}
}
