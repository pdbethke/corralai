// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestToolchainRootFindsTheInstallDirectory covers the shapes every popular
// version manager produces. The jail mounts /usr and nothing else, so a
// compiler installed by any of these was invisible and the run failed with
// "<tool>: not found" — blaming the operator's project for the sandbox's gap.
func TestToolchainRootFindsTheInstallDirectory(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	for _, tc := range []struct{ bin, want string }{
		{filepath.Join(home, ".asdf/installs/golang/1.22/bin/go"), filepath.Join(home, ".asdf")},
		{filepath.Join(home, ".nvm/versions/node/v22.0.0/bin/node"), filepath.Join(home, ".nvm")},
		{filepath.Join(home, ".cargo/bin/cargo"), filepath.Join(home, ".cargo")},
		{"/opt/homebrew/bin/node", "/opt/homebrew"},
		{"/home/linuxbrew/.linuxbrew/bin/ruby", "/home/linuxbrew"},
	} {
		if got := toolchainRoot(tc.bin); got != tc.want {
			t.Errorf("toolchainRoot(%q) = %q, want %q", tc.bin, got, tc.want)
		}
	}
}

// TestToolchainRootNeverReturnsABoundary is the safety property. Binding $HOME
// so a test can run would hand the sandboxed command every credential the
// operator owns — the exact opposite of what this package is for.
func TestToolchainRootNeverReturnsABoundary(t *testing.T) {
	home, _ := os.UserHomeDir()
	for _, bin := range []string{"/bin/sh", "/usr/bin/go", filepath.Join(home, "go")} {
		got := toolchainRoot(bin)
		if got == "/" || got == "/home" || got == "/usr" || (home != "" && got == home) {
			t.Fatalf("toolchainRoot(%q) returned a boundary %q — the jail must never bind that", bin, got)
		}
	}
}

// TestToolchainBindForSystemToolIsANoop: a binary already under /usr needs no
// extra mount, and adding one would widen the sandbox for nothing.
func TestToolchainBindForSystemToolIsANoop(t *testing.T) {
	b, err := toolchainBindFor("sh")
	if err != nil {
		t.Fatalf("resolving a system tool must not error: %v", err)
	}
	if b.Host != "" {
		t.Fatalf("a /usr binary needs no bind, got %+v", b)
	}
}

// TestToolchainBindForUnresolvableIsSilent: this function exists to REMOVE a
// misleading failure, not to add one. A command that cannot be resolved must
// fall through so the jail reports the runner's own message.
func TestToolchainBindForUnresolvableIsSilent(t *testing.T) {
	b, err := toolchainBindFor("definitely-not-a-real-binary-xyzzy")
	if err != nil || b.Host != "" {
		t.Fatalf("an unresolvable command must be silent, got bind=%+v err=%v", b, err)
	}
}

// TestSnapToolchainIsRefusedWithAReason: a snap re-execs through snapd over a
// unix socket a network-isolated jail cannot reach, so no amount of mounting
// fixes it. Saying that beats "go: not found" from deep inside the sandbox.
func TestSnapToolchainIsRefusedWithAReason(t *testing.T) {
	err := ErrSnapToolchain{Command: "go", Path: "/snap/bin/go"}
	var snap ErrSnapToolchain
	if !errors.As(error(err), &snap) {
		t.Fatal("callers must be able to recognize the snap case")
	}
	msg := err.Error()
	for _, want := range []string{"snap", "snapd", "cannot run inside the audit sandbox", "apt"} {
		if !contains(msg, want) {
			t.Fatalf("the message must explain the cause and a way out, missing %q: %s", want, msg)
		}
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
