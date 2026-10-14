// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/sandbox"
)

// stockRustup puts what `rustup-init` gives a user with no sudo on PATH: a
// ~/.cargo/bin holding cargo beside rustup, under a fresh HOME, ahead of the
// system directories (bwrap lives there; no cargo does).
func stockRustup(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	bin := filepath.Join(home, ".cargo", "bin")
	for _, n := range []string{"cargo", "rustup"} {
		mustWrite(t, filepath.Join(bin, n), "#!/bin/sh\n")
		if err := os.Chmod(filepath.Join(bin, n), 0o700); err != nil { // #nosec G302 -- test stand-in must be executable
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/usr/bin"+string(os.PathListSeparator)+"/bin")
}

// withSystemToolchain stands a temp dir in for the jail's system-wide rustup
// home — holding a toolchain or not.
func withSystemToolchain(t *testing.T, present bool) {
	t.Helper()
	dir := t.TempDir()
	if present {
		if err := os.MkdirAll(filepath.Join(dir, "toolchains"), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(lang.SwapSystemRustupHomeForTest(dir))
}

// TestDoctorRehearsesTheJailsRustToolchain: doctor's "toolchain reachable
// inside the sandbox" is a rehearsal of a jailed run, so when --code names
// the language it asks that language's jail-only preflight too — a stock
// rustup user is told, before any spend, what certify --local would refuse
// and why. Without --code doctor cannot know the language and asks nothing
// language-specific.
func TestDoctorRehearsesTheJailsRustToolchain(t *testing.T) {
	stockRustup(t)
	withSystemToolchain(t, false)
	rust, _ := lang.ByName("rust")
	got := checkToolchain(nil, []string{"cargo", "test"}, nil, rust)
	if got.ok || !strings.Contains(got.detail, sandbox.SystemRustupHome) || !strings.Contains(got.detail, "--substrate workspace") {
		t.Fatalf("doctor must fail the jail rehearsal for a ~/.cargo-only toolchain, naming the layout and the substrate that needs none: %+v", got)
	}

	if _, err := sandbox.Resolve(sandbox.Config{}); err != nil {
		t.Skipf("no working sandbox backend on this host for the end-to-end half: %v", err)
	}
	var out bytes.Buffer
	runDoctor([]string{"--repo", t.TempDir(), "--code", "src/foo.rs", "--", "cargo", "test"}, &out, &bytes.Buffer{})
	if !strings.Contains(out.String(), sandbox.SystemRustupHome) {
		t.Errorf("runDoctor --code src/foo.rs does not carry the jail's preflight:\n%s", out.String())
	}
}
