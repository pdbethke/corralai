// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"testing"
)

// Swift declares that corral's sandbox never runs it (SandboxRefuser), and
// its JailPreflight refuses with that same declaration, so a door that
// prints RefusesEverySandbox's answer prints the one wording Swift's
// JailPreflight uses. The answer is read from the declaration, never by
// running a JailPreflight, so asking it starts no sandbox probe (PHP's).
func TestSwiftRefusesEverySandboxOutright(t *testing.T) {
	p, ok := ByName("swift")
	if !ok {
		t.Fatal("no swift plugin")
	}
	jp := JailPreflight(p, []string{"swift", "test"})
	got := RefusesEverySandbox(p)
	if got == nil || jp == nil || got.Error() != jp.Error() {
		t.Fatalf("RefusesEverySandbox = %v, want swift's JailPreflight refusal verbatim (%v)", got, jp)
	}
}

// No other language declares an outright refusal: Rust's and PHP's
// JailPreflight refusals depend on the host, and every other language's
// jail needs nothing of its own.
func TestOnlySwiftRefusesEverySandbox(t *testing.T) {
	for _, n := range Names() {
		p, _ := ByName(n)
		if err := RefusesEverySandbox(p); (err != nil) != (n == "swift") {
			t.Errorf("%s: RefusesEverySandbox = %v", n, err)
		}
	}
}

// Rust's JailPreflight refuses a user-home rustup with no system toolchain.
// That refusal can be fixed inside the sandbox, so it is not the outright
// kind, and a door must keep its sandbox advice for it.
func TestAConditionalJailRefusalIsNotOutright(t *testing.T) {
	p, _ := ByName("rust")
	restore := SwapSystemRustupHomeForTest(filepath.Join(t.TempDir(), "absent"))
	defer restore()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, bin := range []string{"cargo", "rustup"} {
		mustExec(t, filepath.Join(home, ".cargo", "bin", bin))
	}
	t.Setenv("PATH", filepath.Join(home, ".cargo", "bin"))
	if err := JailPreflight(p, []string{"cargo", "test"}); err == nil {
		t.Skip("rust's JailPreflight did not refuse here, so there is no conditional refusal to judge")
	}
	if err := RefusesEverySandbox(p); err != nil {
		t.Errorf("RefusesEverySandbox(rust) = %v, want nil: its refusal is conditional", err)
	}
}

func mustExec(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"), 0o700); err != nil { // #nosec G306 -- a test stub that must be executable
		t.Fatal(err)
	}
}
