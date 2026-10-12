// SPDX-License-Identifier: Elastic-2.0

package sandbox

import (
	"os"
	"path/filepath"
	"slices"
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
// the jail binds whole (/usr) is exposed through that bind, so everything in
// it but bin/ is masked — found from CARGO_HOME or from the cargo on PATH,
// and judged by the same predicate that narrows a toolchain bind
// (SecretEntries).
func TestSystemSecretMasksHideACargoHomeButItsBin(t *testing.T) {
	for _, via := range []string{"CARGO_HOME", "PATH"} {
		usr, ch := fakeSystemCargoHome(t)
		t.Setenv("CARGO_HOME", "")
		t.Setenv("PATH", "/usr/bin:/bin")
		if via == "CARGO_HOME" {
			t.Setenv("CARGO_HOME", ch)
		} else {
			t.Setenv("PATH", filepath.Join(ch, "bin")+":/usr/bin:/bin")
		}
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
			t.Errorf("via %s: masks = %v, want %v", via, got, want)
		}
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
// cargo in bin/, plus lib/, go/ and Debian's man -> share/man symlink, is an
// install prefix. Nothing in it is masked.
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
		t.Error("a directory with lib/ is an install prefix, never a cargo home")
	}
	if got := secretMasks([]string{usr}); len(got) != 0 {
		t.Errorf("masks = %+v, want none", got)
	}
}

// TestCargoHomeMarkers: outside the operator's own CARGO_HOME, a directory is
// a cargo home only when it carries one of cargo's (or rustup's) own markers
// — never by bin/cargo alone.
func TestCargoHomeMarkers(t *testing.T) {
	t.Setenv("CARGO_HOME", "")
	t.Setenv("HOME", t.TempDir())
	for marker, isDir := range map[string]bool{".crates.toml": false, ".crates2.json": false, ".package-cache": false, "registry": true, "env": false} {
		d := t.TempDir()
		if err := os.MkdirAll(filepath.Join(d, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if isDir {
			if err := os.Mkdir(filepath.Join(d, marker), 0o755); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(filepath.Join(d, marker), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if !IsCargoHome(d) {
			t.Errorf("%s marks a cargo home", marker)
		}
	}
	bare := t.TempDir()
	if err := os.MkdirAll(filepath.Join(bare, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bare, "bin", "cargo"), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	if IsCargoHome(bare) {
		t.Error("bin/cargo alone does not make a cargo home")
	}
	op := t.TempDir()
	t.Setenv("CARGO_HOME", op)
	if !IsCargoHome(op) {
		t.Error("the operator's CARGO_HOME is a cargo home by definition")
	}
}
