// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/sandbox"
)

const systemCargoBin = "/usr/local/cargo/bin"

// rustJailOrSkip returns the real bwrap jail, or skips naming the reason. The
// message starts with SKIP: because CI greps the -v output for it: a jail test
// that skips silently proves nothing about the jail.
func rustJailOrSkip(t *testing.T) VerboseJail {
	t.Helper()
	backend, err := sandbox.Resolve(sandbox.Config{Backend: "bwrap"})
	if err != nil {
		t.Skipf("SKIP: no bwrap here: %v", err)
	}
	if _, err := os.Stat("/usr/local/rustup/toolchains"); err != nil {
		t.Skipf("SKIP: no system-wide rustup at /usr/local/rustup (scripts/ci-provision-test-toolchains.sh installs it): %v", err)
	}
	if _, err := os.Stat(systemCargoBin + "/cargo-nextest"); err != nil {
		t.Skipf("SKIP: no cargo-nextest in %s: %v", systemCargoBin, err)
	}
	vj, ok := NewJail(backend, 120*time.Second).(VerboseJail)
	if !ok {
		t.Fatal("jail is not verbose")
	}
	return vj
}

// TestJailRunsTheSystemWideRustToolchain: cargo, rustc and cargo-nextest all
// run inside corral's own jail path (NewJail, toolchain binds, pinned env)
// from the system-wide layout, with the system toolchain first on PATH.
// Nothing here is a hand-built bwrap line.
func TestJailRunsTheSystemWideRustToolchain(t *testing.T) {
	vj := rustJailOrSkip(t)
	t.Setenv("PATH", systemCargoBin+":"+os.Getenv("PATH"))
	ok, out, err := vj.RunTestVerbose(context.Background(), map[string]string{"x": "x"},
		[]string{"sh", "-c", "cargo --version && rustc --version && cargo nextest --version"})
	if err != nil || !ok {
		t.Fatalf("the system-wide Rust toolchain did not run in the jail (ok=%v err=%v):\n%s", ok, err, out)
	}
	for _, want := range []string{"cargo 1.", "rustc 1.", "cargo-nextest "} {
		if !strings.Contains(out, want) {
			t.Errorf("jail output lacks %q:\n%s", want, out)
		}
	}
}

// TestJailRustupProxyDispatchesToTheSystemToolchain: when `cargo` resolves on
// the HOST to the operator's own rustup proxy (~/.cargo/bin/cargo), the jail
// binds only that bin/ (never the rest of the cargo home) and the pinned
// RUSTUP_HOME makes the proxy run the SYSTEM toolchain, not the operator's
// own. The version the jail reports must be the system one.
func TestJailRustupProxyDispatchesToTheSystemToolchain(t *testing.T) {
	vj := rustJailOrSkip(t)
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("SKIP: no home: %v", err)
	}
	proxy := home + "/.cargo/bin/cargo"
	if _, err := os.Stat(proxy); err != nil {
		t.Skipf("SKIP: no user-home rustup proxy at %s: %v", proxy, err)
	}
	t.Setenv("CARGO_HOME", home+"/.cargo")
	t.Setenv("PATH", home+"/.cargo/bin:"+os.Getenv("PATH"))
	if p, _ := exec.LookPath("cargo"); p != proxy {
		t.Skipf("SKIP: cargo resolves to %s, not the proxy", p)
	}
	sys := exec.Command(systemCargoBin+"/cargo", "--version")
	sys.Env = append(os.Environ(), "RUSTUP_HOME=/usr/local/rustup")
	want, err := sys.Output()
	if err != nil {
		t.Skipf("SKIP: system cargo does not run on the host: %v", err)
	}
	ok, out, err := vj.RunTestVerbose(context.Background(), map[string]string{"x": "x"},
		[]string{"sh", "-c", "cargo --version"})
	if err != nil || !ok {
		t.Fatalf("the user-home proxy did not run in the jail (ok=%v err=%v):\n%s", ok, err, out)
	}
	if strings.TrimSpace(out) != strings.TrimSpace(string(want)) {
		t.Errorf("the jail ran %q, want the system toolchain %q", strings.TrimSpace(out), strings.TrimSpace(string(want)))
	}
}
