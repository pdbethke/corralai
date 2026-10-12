// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"os"
	"path/filepath"
	"testing"
)

// layout writes files (and, for a value starting with "->", symlinks) under
// root.
func layout(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if len(body) > 2 && body[:2] == "->" {
			if err := os.Symlink(body[2:], p); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// TestDistroCargoInstalledToolsNarrowToBin (R2): cargo from the distribution
// (/usr/bin/cargo) plus `cargo install`ed tools in ~/.cargo — no bin/cargo,
// no bin/rustup there. ~/.cargo is the operator's CARGO_HOME by definition,
// so it is still bound as bin/ alone: its config.toml registry token and its
// git checkouts stay out.
func TestDistroCargoInstalledToolsNarrowToBin(t *testing.T) {
	home := t.TempDir()
	layout(t, home, map[string]string{
		".cargo/bin/cargo-nextest":         "#!/bin/sh\n",
		".cargo/.crates.toml":              "[v1]\n",
		".cargo/config.toml":               "[registries.priv]\ntoken = \"secret\"\n",
		".cargo/git/checkouts/priv/lib.rs": "// private\n",
	})
	t.Setenv("HOME", home)
	t.Setenv("CARGO_HOME", "")
	t.Setenv("PATH", filepath.Join(home, ".cargo", "bin")+":/usr/bin:/bin")

	binds, err := toolchainBindsFor("cargo-nextest")
	if err != nil {
		t.Fatal(err)
	}
	if len(binds) != 1 || binds[0].Host != filepath.Join(home, ".cargo", "bin") {
		t.Errorf("binds = %+v, want exactly ~/.cargo/bin", binds)
	}
}

// TestBrewPrefixWithRustStillBindsItsRunners (R3): a Homebrew-shaped prefix
// (~/.linuxbrew here; /opt/homebrew on macOS) that has rust installed is an
// install prefix — it has lib/ — never a cargo home. Its other runners
// (node, resolving into Cellar/) and its cargo still bind the prefix.
func TestBrewPrefixWithRustStillBindsItsRunners(t *testing.T) {
	home := t.TempDir()
	brew := filepath.Join(home, ".linuxbrew")
	layout(t, home, map[string]string{
		".linuxbrew/Cellar/rust/1.90.0/bin/cargo":              "#!/bin/sh\n",
		".linuxbrew/Cellar/rust/1.90.0/lib/librustc_driver.so": "",
		".linuxbrew/Cellar/node/22.0.0/bin/node":               "#!/bin/sh\n",
		".linuxbrew/lib/libz.so":                               "",
		".linuxbrew/bin/cargo":                                 "->../Cellar/rust/1.90.0/bin/cargo",
		".linuxbrew/bin/node":                                  "->../Cellar/node/22.0.0/bin/node",
	})
	t.Setenv("HOME", home)
	t.Setenv("CARGO_HOME", "")
	t.Setenv("PATH", filepath.Join(brew, "bin")+":/usr/bin:/bin")
	for _, cmd := range []string{"node", "cargo"} {
		binds, err := toolchainBindsFor(cmd)
		if err != nil || !hasBind(binds, brew) {
			t.Errorf("%s: binds=%+v err=%v, want the brew prefix bound", cmd, binds, err)
		}
	}
}

// TestStandaloneRustPrefixKeepsItsLib (R5): a standalone Rust install
// (bin/cargo, bin/rustc, lib/librustc_driver) is a prefix, not a cargo home;
// rustc's lib/ must stay reachable.
func TestStandaloneRustPrefixKeepsItsLib(t *testing.T) {
	// ~/sdk is a toolchain directory directly under $HOME, so the prefix IS
	// the toolchain root (as /opt/rust is) rather than a child of it.
	home := t.TempDir()
	t.Setenv("HOME", home)
	prefix := filepath.Join(home, "sdk")
	layout(t, prefix, map[string]string{
		"bin/cargo":                       "#!/bin/sh\n",
		"bin/rustc":                       "#!/bin/sh\n",
		"lib/librustc_driver.so":          "",
		"lib/rustlib/x86_64/lib/std.rlib": "",
	})
	t.Setenv("CARGO_HOME", "")
	t.Setenv("PATH", filepath.Join(prefix, "bin")+":/usr/bin:/bin")
	binds, err := toolchainBindsFor("rustc")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := coveringBind(binds, filepath.Join(prefix, "lib", "librustc_driver.so")); !ok {
		t.Errorf("binds = %+v: rustc's lib/ is not reachable", binds)
	}
}
