// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"os"
	"path/filepath"
	"testing"
)

// resolvedTempDir is t.TempDir() with its symlinks resolved. The code under
// test resolves every binary it finds on PATH, and on macOS t.TempDir() is
// /var/folders/..., a symlink to /private/var/folders/...: a test that
// expects an unresolved path compares two spellings of one directory.
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	d, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return d
}

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
	home := resolvedTempDir(t)
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
// (~/.linuxbrew here; /opt/homebrew on macOS) that has rust installed is not
// CARGO_HOME or ~/.cargo, so it is never a cargo home. Its other runners
// (node, resolving into Cellar/) and its cargo still bind the prefix.
func TestBrewPrefixWithRustStillBindsItsRunners(t *testing.T) {
	home := resolvedTempDir(t)
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
	home := resolvedTempDir(t)
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

// TestUvLocalBinIsNotACargoHome (S2): uv's standalone installer writes
// ~/.local/bin/env beside uv. ~/.local/bin is a directory of programs, not a
// cargo home, so uv and a pip --user pytest bind it as before.
func TestUvLocalBinIsNotACargoHome(t *testing.T) {
	home := resolvedTempDir(t)
	layout(t, home, map[string]string{
		".local/bin/uv":     "#!/bin/sh\n",
		".local/bin/pytest": "#!/bin/sh\n",
		".local/bin/env":    "#!/bin/sh\n",
	})
	t.Setenv("HOME", home)
	t.Setenv("CARGO_HOME", "")
	t.Setenv("PATH", filepath.Join(home, ".local", "bin")+":/usr/bin:/bin")
	for _, cmd := range []string{"uv", "pytest"} {
		binds, err := toolchainBindsFor(cmd)
		if err != nil || !hasBind(binds, filepath.Join(home, ".local", "bin")) {
			t.Errorf("%s: binds=%+v err=%v, want ~/.local/bin bound", cmd, binds, err)
		}
	}
}

// TestOperatorCargoHomeWithStrayLibNarrowsToBin (S5): ~/.cargo is the
// operator's cargo home by definition, whatever it holds: a stray lib/ does
// not make it something bound whole with its registry token and checkouts.
func TestOperatorCargoHomeWithStrayLibNarrowsToBin(t *testing.T) {
	home := resolvedTempDir(t)
	layout(t, home, map[string]string{
		".cargo/bin/cargo-nextest":         "#!/bin/sh\n",
		".cargo/lib/stray.so":              "",
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

// TestRustupHomeRefusesACargoHome (S6): RUSTUP_HOME pointed at a cargo home
// would bind it whole, exposing everything the cargo-home rule hides. It is
// refused, even when that cargo home holds nothing but bin/ today.
func TestRustupHomeRefusesACargoHome(t *testing.T) {
	home := resolvedTempDir(t)
	layout(t, home, map[string]string{".cargo/bin/rustup": "#!/bin/sh\n"})
	t.Setenv("HOME", home)
	t.Setenv("CARGO_HOME", "")
	t.Setenv("RUSTUP_HOME", filepath.Join(home, ".cargo"))
	if rh, err := rustupHome("cargo"); err == nil {
		t.Errorf("RUSTUP_HOME=~/.cargo -> %q, want refused", rh)
	}
}

// TestLinuxbrewRustupKegBindsThePrefix (I1): a linuxbrew rustup keg (wrappers
// in bin/, real binaries in libexec/bin, .crates.toml at the keg root) is an
// install prefix, not a cargo home: cargo binds the brew prefix, which covers
// libexec/, and is not narrowed to the keg's bin/.
func TestLinuxbrewRustupKegBindsThePrefix(t *testing.T) {
	home := resolvedTempDir(t)
	brew := filepath.Join(home, ".linuxbrew")
	keg := filepath.Join(brew, "Cellar", "rustup", "1.28.2")
	layout(t, home, map[string]string{
		".linuxbrew/Cellar/rustup/1.28.2/.crates.toml":       "[v1]\n",
		".linuxbrew/Cellar/rustup/1.28.2/.crates2.json":      "{}\n",
		".linuxbrew/Cellar/rustup/1.28.2/bin/cargo":          "#!/bin/sh\n",
		".linuxbrew/Cellar/rustup/1.28.2/bin/rustup":         "#!/bin/sh\n",
		".linuxbrew/Cellar/rustup/1.28.2/libexec/bin/cargo":  "#!/bin/sh\n",
		".linuxbrew/Cellar/rustup/1.28.2/libexec/bin/rustup": "#!/bin/sh\n",
		".linuxbrew/lib/libz.so":                             "",
		".linuxbrew/opt/rustup":                              "->../Cellar/rustup/1.28.2",
	})
	t.Setenv("HOME", home)
	t.Setenv("CARGO_HOME", "")
	t.Setenv("RUSTUP_HOME", "")
	t.Setenv("PATH", filepath.Join(brew, "opt", "rustup", "bin")+":/usr/bin:/bin")
	binds, err := toolchainBindsFor("cargo")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := coveringBind(binds, filepath.Join(keg, "libexec", "bin", "cargo")); !ok {
		t.Errorf("binds = %+v: the keg's libexec/ is not reachable", binds)
	}
}

// TestSymlinkedOperatorCargoHomeNarrowsToBin (M1): ~/.cargo as a symlink to
// another disk, with a stray lib/ and a registry token in config.toml, is
// still the operator's cargo home: only its bin/ is bound.
func TestSymlinkedOperatorCargoHomeNarrowsToBin(t *testing.T) {
	home := resolvedTempDir(t)
	data := filepath.Join(home, "sdk", "cargo")
	layout(t, home, map[string]string{
		"sdk/cargo/bin/cargo-nextest": "#!/bin/sh\n",
		"sdk/cargo/lib/stray.so":      "",
		"sdk/cargo/config.toml":       "[registries.priv]\ntoken = \"secret\"\n",
		".cargo":                      "->" + data,
	})
	t.Setenv("HOME", home)
	t.Setenv("CARGO_HOME", "")
	t.Setenv("PATH", filepath.Join(home, ".cargo", "bin")+":/usr/bin:/bin")
	binds, err := toolchainBindsFor("cargo-nextest")
	if err != nil {
		t.Fatal(err)
	}
	if len(binds) != 1 || binds[0].Host != filepath.Join(data, "bin") {
		t.Errorf("binds = %+v, want exactly the cargo home's bin/", binds)
	}
}
