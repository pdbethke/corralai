// SPDX-License-Identifier: Elastic-2.0

package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeSystemCargoHome lays out a cargo home the way the official rust image
// does (CARGO_HOME=/usr/local/cargo), under a stand-in for /usr.
func fakeSystemCargoHome(t *testing.T) (usr, cargoHome string) {
	t.Helper()
	usr = t.TempDir()
	cargoHome = filepath.Join(usr, "local", "cargo")
	for rel, body := range map[string]string{
		"bin/cargo":                 "#!/bin/sh\n",
		"bin/rustup":                "#!/bin/sh\n",
		"credentials.toml":          "[registry]\ntoken = \"secret\"\n",
		"config.toml":               "[registries.priv]\ntoken = \"secret\"\n",
		"git/checkouts/priv/lib.rs": "// private\n",
		"registry/cache/x.crate":    "",
	} {
		p := filepath.Join(cargoHome, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return usr, cargoHome
}

// TestSystemSecretMasksHideACargoHomeButItsBin: a cargo home inside a tree
// the jail binds whole (/usr) is exposed through that bind, so when it is the
// operator's CARGO_HOME (the official rust image sets
// CARGO_HOME=/usr/local/cargo) everything in it but bin/ is masked — judged
// by the same predicate that narrows a toolchain bind (SecretEntries).
func TestSystemSecretMasksHideACargoHomeButItsBin(t *testing.T) {
	usr, ch := fakeSystemCargoHome(t)
	t.Setenv("CARGO_HOME", ch)
	t.Setenv("PATH", "/usr/bin:/bin")
	var got []string
	for _, m := range secretMasks([]string{usr}) {
		got = append(got, m.Path)
	}
	slices.Sort(got)
	want := []string{
		filepath.Join(ch, "config.toml"),
		filepath.Join(ch, "credentials.toml"),
		filepath.Join(ch, "git"),
		filepath.Join(ch, "registry"),
	}
	if !slices.Equal(got, want) {
		t.Errorf("masks = %v, want %v", got, want)
	}
}

// TestACargoHomeNotNamedByCargoHomeIsNotOne: the rule's stated cost. A
// cargo-home-shaped directory that CARGO_HOME does not name — here the
// image's /usr/local/cargo with CARGO_HOME unset and its cargo on PATH — is
// an ordinary toolchain directory: nothing in it is masked.
func TestACargoHomeNotNamedByCargoHomeIsNotOne(t *testing.T) {
	usr, ch := fakeSystemCargoHome(t)
	t.Setenv("CARGO_HOME", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", filepath.Join(ch, "bin")+":/usr/bin:/bin")
	if IsCargoHome(ch) {
		t.Error("only the operator's CARGO_HOME is a cargo home")
	}
	if got := secretMasks([]string{usr}); len(got) != 0 {
		t.Errorf("masks = %+v, want none", got)
	}
}

// TestSecretMasksIgnoreACargoHomeOutsideTheTree: a cargo home that is not
// inside a whole-bound tree is not reachable through it, so it is not this
// rule's to mask (a toolchain bind narrows it instead).
func TestSecretMasksIgnoreACargoHomeOutsideTheTree(t *testing.T) {
	_, ch := fakeSystemCargoHome(t)
	t.Setenv("CARGO_HOME", ch)
	if got := secretMasks([]string{t.TempDir()}); len(got) != 0 {
		t.Errorf("masks = %+v, want none", got)
	}
}

// TestAnInstallPrefixIsNeverACargoHome (R1): /usr/local with a standalone
// cargo in bin/, plus lib/, go/ and Debian's man -> share/man symlink, is not
// the operator's CARGO_HOME (nor ~/.cargo), so it is not a cargo home — a
// cargo home is exactly what cargo defines. Nothing in it is masked.
func TestAnInstallPrefixIsNeverACargoHome(t *testing.T) {
	usr := t.TempDir()
	local := filepath.Join(usr, "local")
	for _, d := range []string{"bin", "lib/python3/dist-packages", "go/bin", "share/man"} {
		if err := os.MkdirAll(filepath.Join(local, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(local, "bin", "cargo"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("share/man", filepath.Join(local, "man")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CARGO_HOME", "")
	t.Setenv("PATH", filepath.Join(local, "bin")+":/usr/bin:/bin")
	if IsCargoHome(local) {
		t.Error("a directory that is neither CARGO_HOME nor ~/.cargo is never a cargo home")
	}
	if got := secretMasks([]string{usr}); len(got) != 0 {
		t.Errorf("masks = %+v, want none", got)
	}
}

// TestOnlyTheOperatorsCargoHomeIsOne: a cargo home is exactly what cargo
// defines — $CARGO_HOME, else ~/.cargo. No file inside a directory makes one:
// not cargo's install ledgers (`cargo install --root` writes them into any
// root, Homebrew's kegs included), not registry/, not bin/rustup, not an
// `env` script.
func TestOnlyTheOperatorsCargoHomeIsOne(t *testing.T) {
	t.Setenv("CARGO_HOME", "")
	t.Setenv("HOME", t.TempDir())
	for _, marker := range []string{".crates.toml", ".crates2.json", ".package-cache", "registry/", "bin/rustup", "env"} {
		d := t.TempDir()
		p := filepath.Join(d, marker)
		if err := os.MkdirAll(filepath.Join(d, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(marker, "/") {
			if err := os.MkdirAll(p, 0o755); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(p, nil, 0o755); err != nil {
			t.Fatal(err)
		}
		if IsCargoHome(d) {
			t.Errorf("%s does not make a cargo home", marker)
		}
	}
	op := t.TempDir()
	t.Setenv("CARGO_HOME", op)
	if !IsCargoHome(op) {
		t.Error("the operator's CARGO_HOME is a cargo home by definition")
	}
}

// TestOperatorCargoHomeIsComparedResolved (M1): a symlinked CARGO_HOME is the
// same cargo home seen through its target, which is the path a resolved
// binary walks up through.
func TestOperatorCargoHomeIsComparedResolved(t *testing.T) {
	real := filepath.Join(t.TempDir(), "cargo")
	if err := os.MkdirAll(filepath.Join(real, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	if err := os.Symlink(real, filepath.Join(home, ".cargo")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CARGO_HOME", "")
	t.Setenv("HOME", home)
	if !IsCargoHome(real) {
		t.Error("the target of a symlinked ~/.cargo is the operator's cargo home")
	}
	if got := CargoHomeOf(filepath.Join(real, "bin", "cargo")); got != real {
		t.Errorf("CargoHomeOf = %q, want %q", got, real)
	}
}

// TestDebianAndInstallRootsAreNotCargoHomes (M2, M3): Debian's
// /usr/share/cargo (bin/cargo wrapper, registry/ of packaged crates) and a
// /usr/local that `cargo install --root /usr/local` wrote .crates.toml into
// are toolchain directories, not cargo homes — neither is CARGO_HOME or
// ~/.cargo, and no marker makes one: nothing in either is masked.
func TestDebianAndInstallRootsAreNotCargoHomes(t *testing.T) {
	usr := t.TempDir()
	for _, f := range []string{"share/cargo/bin/cargo", "share/cargo/registry/serde-1.0/lib.rs", "local/bin/cargo", "local/.crates.toml", "local/share/ca/cert.pem"} {
		p := filepath.Join(usr, f)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CARGO_HOME", "")
	t.Setenv("HOME", t.TempDir())
	for _, bin := range []string{"share/cargo/bin", "local/bin"} {
		t.Setenv("PATH", filepath.Join(usr, bin)+":/usr/bin:/bin")
		if got := secretMasks([]string{usr}); len(got) != 0 {
			t.Errorf("cargo in %s: masks = %+v, want none", bin, got)
		}
	}
}

// standInUsr lays out a stand-in for /usr the way a distribution with a
// packaged cargo does: /usr/bin/cargo is a regular file beside env (which
// every Linux /usr/bin has), sh-runnable programs, and lib/.
func standInUsr(t *testing.T) string {
	t.Helper()
	usr := t.TempDir()
	if err := os.MkdirAll(filepath.Join(usr, "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, body := range map[string]string{
		"bin/cargo": "#!/bin/sh\n",
		"bin/env":   "#!/bin/sh\n",
		"bin/ls":    "echo LS-RUNS\n",
	} {
		p := filepath.Join(usr, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return usr
}

// TestUsrBinIsNeverACargoHome (S1): a distribution's /usr/bin holds cargo and
// env. Neither it nor /usr is CARGO_HOME or ~/.cargo, so neither is a cargo
// home: nothing in it, or in /usr, is masked.
func TestUsrBinIsNeverACargoHome(t *testing.T) {
	usr := standInUsr(t)
	t.Setenv("CARGO_HOME", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", filepath.Join(usr, "bin")+":/usr/bin:/bin")
	if IsCargoHome(filepath.Join(usr, "bin")) || IsCargoHome(usr) {
		t.Error("/usr/bin and /usr are never cargo homes")
	}
	if got := CargoHomeOf(filepath.Join(usr, "bin", "cargo")); got != "" {
		t.Errorf("CargoHomeOf(/usr/bin/cargo) = %q, want none", got)
	}
	if got := secretMasks([]string{usr}); len(got) != 0 {
		t.Errorf("masks = %+v, want none", got)
	}
}

// TestOperatorCargoHomeIsOneWhateverItHolds (S5): the operator's own
// CARGO_HOME is a cargo home by definition, a stray lib/ included — nothing
// it holds can make it something else.
func TestOperatorCargoHomeIsOneWhateverItHolds(t *testing.T) {
	op := t.TempDir()
	for _, d := range []string{"bin", "lib"} {
		if err := os.MkdirAll(filepath.Join(op, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CARGO_HOME", op)
	if !IsCargoHome(op) {
		t.Error("the operator's CARGO_HOME is a cargo home whatever else it holds")
	}
}

// TestSecretMasksReadCargoHomeThroughOperatorCargoHome (S4): a relative
// CARGO_HOME is resolved the one way OperatorCargoHome resolves it, so the
// mask walk finds it.
func TestSecretMasksReadCargoHomeThroughOperatorCargoHome(t *testing.T) {
	usr, ch := fakeSystemCargoHome(t)
	t.Chdir(filepath.Dir(ch))
	t.Setenv("CARGO_HOME", filepath.Base(ch))
	t.Setenv("PATH", "/nonexistent")
	if got := secretMasks([]string{usr}); len(got) == 0 {
		t.Error("a relative CARGO_HOME inside /usr was not masked")
	}
}
