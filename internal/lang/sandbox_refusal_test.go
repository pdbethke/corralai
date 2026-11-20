// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// Swift's JailPreflight refuses every jailed run whatever the host holds, and
// says so in a way a door can read without matching the word "swift": the
// refusal is ErrSandboxUnsupported. RefusesEverySandbox hands back exactly
// that refusal, text unchanged, so a door that prints it prints the one
// wording Swift's JailPreflight already uses.
func TestSwiftRefusesEverySandboxOutright(t *testing.T) {
	p, ok := ByName("swift")
	if !ok {
		t.Fatal("no swift plugin")
	}
	jp := JailPreflight(p, []string{"swift", "test"})
	if !errors.Is(jp, ErrSandboxUnsupported) {
		t.Fatalf("swift's JailPreflight = %v, want it to be ErrSandboxUnsupported", jp)
	}
	got := RefusesEverySandbox(p, []string{"swift", "test"})
	if got == nil || got.Error() != jp.Error() {
		t.Fatalf("RefusesEverySandbox = %v, want swift's JailPreflight refusal verbatim (%v)", got, jp)
	}
}

// A language whose jail needs nothing of its own is never reported as
// refusing the sandbox: the answer is keyed on JailPreflight, not on a name.
// (Rust and PHP are left out: their JailPreflight reads the host, and their
// conditional refusals are not ErrSandboxUnsupported, which
// TestAConditionalJailRefusalIsNotOutright pins.)
func TestLanguagesWithoutAJailRefusalDoNotRefuseEverySandbox(t *testing.T) {
	for _, n := range []string{"go", "python", "javascript", "typescript", "ruby"} {
		p, ok := ByName(n)
		if !ok {
			t.Fatalf("no %s plugin", n)
		}
		if err := RefusesEverySandbox(p, p.TestCmd()); err != nil {
			t.Errorf("%s: RefusesEverySandbox = %v, want nil", n, err)
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
	} else if errors.Is(err, ErrSandboxUnsupported) {
		t.Fatalf("rust's conditional refusal is marked outright: %v", err)
	}
	if err := RefusesEverySandbox(p, []string{"cargo", "test"}); err != nil {
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
