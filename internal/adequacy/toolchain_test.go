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

// TestToolchainBindNeverMountsTheOperatorsCredentialDirectories pins the
// sixth review's H2: toolchainRoot walks up to the first child of a
// boundary, so a runner under ~/.local (pip --user, pipx), ~/.local/share
// (gem install --user-install), ~/.config (composer global) or ~/src (an
// absolute path into another project) resolved to that whole directory —
// and the jail bound it read-only, keyrings and gh/gcloud credentials
// included. A known toolchain directory still binds; ~/.local narrows to
// bin (+lib) or the one share subtree that is a toolchain; everything else
// under $HOME is refused with the way out.
func TestToolchainBindNeverMountsTheOperatorsCredentialDirectories(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	h := func(p string) string { return filepath.Join(home, p) }
	for _, tc := range []struct {
		bin, want string
		refuse    bool
	}{
		{h(".local/bin/pytest"), h(".local/bin"), false},
		{h(".local/share/gem/ruby/3.3.0/bin/rspec"), h(".local/share/gem"), false},
		{h(".local/share/pipx/venvs/x/bin/x"), h(".local/share/pipx"), false},
		{h(".local/share/claude/bin/claude"), "", true},
		{h(".config/composer/vendor/bin/phpunit"), "", true},
		{h("src/repo/node_modules/.bin/jest"), "", true},
		{h(".asdf/installs/golang/1.22/bin/go"), h(".asdf"), false},
		{h(".nvm/versions/node/v22/bin/node"), h(".nvm"), false},
	} {
		root := toolchainRoot(tc.bin)
		narrowed, refused := narrowHomeRoot(tc.bin, root)
		got := root
		if narrowed != "" {
			got = narrowed
		}
		switch {
		case tc.refuse && refused == "":
			t.Errorf("%s: bound %q — the jail mounted a credential directory", tc.bin, got)
		case !tc.refuse && refused != "":
			t.Errorf("%s: refused (%s), want %q bound", tc.bin, refused, tc.want)
		case !tc.refuse && got != tc.want:
			t.Errorf("%s: bound %q, want %q", tc.bin, got, tc.want)
		}
		if refused == "" && (got == h(".local") || got == h(".config") || got == home) {
			t.Errorf("%s: %q must never be bound", tc.bin, got)
		}
	}
}
