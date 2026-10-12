// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/sandbox"
)

// fakeHomeWithCredentials builds a $HOME holding the two toolchain layouts
// whose install root ALSO holds a credentials store: rustup's ~/.cargo (the
// `cargo login` token in credentials.toml, and the legacy extensionless
// credentials) and RubyGems' ~/.gem (the gem push key in credentials). It
// points HOME and PATH at it and returns the credential files.
func fakeHomeWithCredentials(t *testing.T) (home string, creds []string) {
	t.Helper()
	home = t.TempDir()
	write := func(rel, body string, mode os.FileMode) string {
		p := filepath.Join(home, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write(".cargo/bin/rustup", "#!/bin/sh\n", 0o755)
	if err := os.Symlink("rustup", filepath.Join(home, ".cargo", "bin", "cargo")); err != nil {
		t.Fatal(err)
	}
	write(".rustup/settings.toml", "", 0o644)
	write(".gem/ruby/3.2.0/bin/rspec", "#!/bin/sh\n", 0o755)
	creds = []string{
		write(".cargo/credentials.toml", "[registry]\ntoken = \"secret\"\n", 0o600),
		write(".cargo/credentials", "[registry]\ntoken = \"secret\"\n", 0o600),
		write(".gem/credentials", ":rubygems_api_key: secret\n", 0o600),
	}
	t.Setenv("HOME", home)
	t.Setenv("RUSTUP_HOME", "")
	t.Setenv("PATH", filepath.Join(home, ".cargo", "bin")+":"+filepath.Join(home, ".gem", "ruby", "3.2.0", "bin")+":/usr/bin:/bin")
	return home, creds
}

func coveringBind(binds []sandbox.Bind, file string) (sandbox.Bind, bool) {
	for _, b := range binds {
		if file == b.Host || strings.HasPrefix(file, b.Host+string(filepath.Separator)) {
			return b, true
		}
	}
	return sandbox.Bind{}, false
}

func hasBind(binds []sandbox.Bind, host string) bool {
	for _, b := range binds {
		if b.Host == host {
			return true
		}
	}
	return false
}

// TestNoJailBindCoversACredentialsStore: neither the graded jail nor the
// networked fetch jail may bind a directory that holds a credentials store.
// A graded run is offline, but what it prints reaches the ledger, which can be
// public; the fetch run has the network and runs repository-controlled
// programs. The toolchain is still reachable: the directory the binary lives
// in, and rustup's home for a rustup proxy.
func TestNoJailBindCoversACredentialsStore(t *testing.T) {
	home, creds := fakeHomeWithCredentials(t)
	cmds := [][]string{{"cargo", "test"}, {"rspec"}}

	for _, cmd := range cmds {
		var graded, fetch sandbox.Options
		gi := &captureIsolator{name: "bwrap", onWrap: func(o sandbox.Options) { graded = o }}
		if _, err := NewJail(gi, time.Second).RunTest(context.Background(), map[string]string{"a": "1"}, cmd); err != nil {
			t.Fatalf("%v: RunTest: %v", cmd, err)
		}
		fi := &captureIsolator{name: "bwrap", onWrap: func(o sandbox.Options) { fetch = o }}
		if _, _, err := RunFetchStep(context.Background(), fi, t.TempDir(), cmd, time.Second); err != nil {
			t.Fatalf("%v: RunFetchStep: %v", cmd, err)
		}
		for jail, o := range map[string]sandbox.Options{"graded": graded, "fetch": fetch} {
			for _, c := range creds {
				if b, ok := coveringBind(o.ReadOnlyBinds, c); ok {
					t.Errorf("%v, %s jail: bind %s exposes %s", cmd, jail, b.Host, c)
				}
			}
		}
	}

	var o sandbox.Options
	ci := &captureIsolator{name: "bwrap", onWrap: func(got sandbox.Options) { o = got }}
	if _, err := NewJail(ci, time.Second).RunTest(context.Background(), map[string]string{"a": "1"}, []string{"cargo", "test"}); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{filepath.Join(home, ".cargo", "bin"), filepath.Join(home, ".rustup")} {
		if !hasBind(o.ReadOnlyBinds, want) {
			t.Errorf("a rustup cargo needs %s bound; binds = %+v", want, o.ReadOnlyBinds)
		}
	}
}

// TestRustupLayoutNeverBindsCargoHome: for a rustup proxy the jail binds
// ~/.cargo/bin and the rustup home and never ~/.cargo itself, even before any
// credentials file exists there — a later `cargo login` would add one, and
// ~/.cargo/git/checkouts already holds private git dependencies' sources.
func TestRustupLayoutNeverBindsCargoHome(t *testing.T) {
	home, creds := fakeHomeWithCredentials(t)
	for _, c := range creds {
		if err := os.Remove(c); err != nil {
			t.Fatal(err)
		}
	}
	binds, err := toolchainBindsFor("cargo")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{filepath.Join(home, ".cargo", "bin"), filepath.Join(home, ".rustup")}
	var got []string
	for _, b := range binds {
		got = append(got, b.Host)
	}
	if !slices.Equal(got, want) {
		t.Errorf("rustup cargo binds = %v, want %v", got, want)
	}
}
