// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func writeManifest(t *testing.T, repo, rel, text string) {
	t.Helper()
	p := filepath.Join(repo, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

const (
	pkgToml     = "[package]\nname = \"leaf_crate\"\nversion = \"0.1.0\"\n"
	virtualToml = "[workspace]\nmembers = [\"crates/*\"]\n"
)

// The scoped argv, golden, for both runner shapes. Both must come from the
// same code path, so each shape is asserted against the same crate.
func TestScopeTestCmdAddsThePackageForBothRunners(t *testing.T) {
	repo := t.TempDir()
	writeManifest(t, repo, "Cargo.toml", virtualToml)
	writeManifest(t, repo, "crates/leaf/Cargo.toml", pkgToml)
	writeManifest(t, repo, "crates/leaf/src/lib.rs", "")
	code := "crates/leaf/src/lib.rs"

	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"libtest", []string{"cargo", "test"}, []string{"cargo", "test", "-p", "leaf_crate"}},
		{"nextest", []string{"cargo", "nextest", "run"}, []string{"cargo", "nextest", "run", "-p", "leaf_crate"}},
		{"nextest with a filter", []string{"cargo", "nextest", "run", "-E", "test(foo)"}, []string{"cargo", "nextest", "run", "-E", "test(foo)", "-p", "leaf_crate"}},
		{"libtest args after -- stay after it", []string{"cargo", "test", "--", "--nocapture"}, []string{"cargo", "test", "-p", "leaf_crate", "--", "--nocapture"}},
		{"a toolchain selector", []string{"cargo", "+1.92.0", "test"}, []string{"cargo", "+1.92.0", "test", "-p", "leaf_crate"}},
	}
	for _, c := range cases {
		got, err := ScopeTestCmd(rustPlugin{}, repo, code, c.in)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

// The operator who already chose packages is not second-guessed, and does not
// need a resolvable manifest to say so.
func TestScopeTestCmdLeavesAnOperatorSelectionAlone(t *testing.T) {
	repo := t.TempDir() // no Cargo.toml at all: nothing would be resolvable
	for _, cmd := range [][]string{
		{"cargo", "test", "-p", "other"},
		{"cargo", "test", "-pother"},
		{"cargo", "test", "--package", "other"},
		{"cargo", "test", "--package=other"},
		{"cargo", "test", "--workspace"},
		{"cargo", "test", "--all"},
		{"cargo", "nextest", "run", "--manifest-path", "x/Cargo.toml"},
		{"cargo", "nextest", "run", "--manifest-path=x/Cargo.toml"},
	} {
		got, err := ScopeTestCmd(rustPlugin{}, repo, "src/lib.rs", cmd)
		if err != nil || !reflect.DeepEqual(got, cmd) {
			t.Errorf("%q: got (%q, %v), want it unchanged", cmd, got, err)
		}
	}
}

// A -p after the -- belongs to the test binary, not to cargo: it is not the
// operator choosing a package.
func TestScopeTestCmdIgnoresFlagsAfterTheSeparator(t *testing.T) {
	repo := t.TempDir()
	writeManifest(t, repo, "Cargo.toml", pkgToml)
	got, err := ScopeTestCmd(rustPlugin{}, repo, "src/lib.rs", []string{"cargo", "test", "--", "-p", "x"})
	want := []string{"cargo", "test", "-p", "leaf_crate", "--", "-p", "x"}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("got (%q, %v), want %q", got, err, want)
	}
}

func TestScopeTestCmdOwningCrateIsTheNearestPackage(t *testing.T) {
	repo := t.TempDir()
	// A manifest with BOTH tables is a package.
	writeManifest(t, repo, "Cargo.toml", "[workspace]\nmembers=[\"inner\"]\n[package]\nname = \"root_pkg\"\n")
	writeManifest(t, repo, "inner/Cargo.toml", "[package]\nname = \"inner_pkg\"\n")
	writeManifest(t, repo, "inner/deep/src/x.rs", "")
	writeManifest(t, repo, "top.rs", "")
	// A virtual manifest reached first is passed over when a package sits above.
	writeManifest(t, repo, "v/Cargo.toml", virtualToml)
	writeManifest(t, repo, "v/src/y.rs", "")

	for file, want := range map[string]string{
		"inner/deep/src/x.rs": "inner_pkg",
		"top.rs":              "root_pkg",
		"v/src/y.rs":          "root_pkg",
	} {
		got, err := ScopeTestCmd(rustPlugin{}, repo, file, []string{"cargo", "test"})
		if err != nil || got[len(got)-1] != want {
			t.Errorf("%s: got (%q, %v), want crate %s", file, got, err, want)
		}
	}
}

func TestScopeTestCmdRefusals(t *testing.T) {
	cargoTest := []string{"cargo", "test"}

	t.Run("virtual manifest with no package above", func(t *testing.T) {
		repo := t.TempDir()
		writeManifest(t, repo, "Cargo.toml", virtualToml)
		_, err := ScopeTestCmd(rustPlugin{}, repo, "tools/x.rs", cargoTest)
		if err == nil {
			t.Fatal("a virtual manifest was guessed at")
		}
		for _, frag := range []string{"tools/x.rs", filepath.Join(repo, "Cargo.toml"), "virtual manifest", "-p <crate>"} {
			if !strings.Contains(err.Error(), frag) {
				t.Errorf("refusal %q lacks %q", err, frag)
			}
		}
	})
	t.Run("no Cargo.toml at all", func(t *testing.T) {
		repo := t.TempDir()
		_, err := ScopeTestCmd(rustPlugin{}, repo, "src/lib.rs", cargoTest)
		if err == nil || !strings.Contains(err.Error(), "no Cargo.toml") || !strings.Contains(err.Error(), "-p <crate>") {
			t.Errorf("got %v, want a refusal naming the missing Cargo.toml", err)
		}
	})
	t.Run("the walk stops at the repo dir", func(t *testing.T) {
		outer := t.TempDir()
		writeManifest(t, outer, "Cargo.toml", pkgToml) // ABOVE the repo: must not be found
		repo := filepath.Join(outer, "repo")
		if err := os.MkdirAll(filepath.Join(repo, "src"), 0o750); err != nil {
			t.Fatal(err)
		}
		_, err := ScopeTestCmd(rustPlugin{}, repo, "src/lib.rs", cargoTest)
		if err == nil || !strings.Contains(err.Error(), "no Cargo.toml") {
			t.Errorf("got %v, want a refusal: the manifest above the repo is not the repo's", err)
		}
	})
	t.Run("a file outside the repo", func(t *testing.T) {
		repo := t.TempDir()
		writeManifest(t, repo, "Cargo.toml", pkgToml)
		if _, err := ScopeTestCmd(rustPlugin{}, repo, "../elsewhere/x.rs", cargoTest); err == nil {
			t.Error("a path outside the repo was scoped")
		}
	})
	t.Run("no checkout to read", func(t *testing.T) {
		if _, err := ScopeTestCmd(rustPlugin{}, "", "src/lib.rs", cargoTest); err == nil {
			t.Error("scoped with no repo dir")
		}
	})
	t.Run("a crate name that is not a crate name", func(t *testing.T) {
		repo := t.TempDir()
		writeManifest(t, repo, "Cargo.toml", "[package]\nname = \"--manifest-path=/etc\"\n")
		if _, err := ScopeTestCmd(rustPlugin{}, repo, "src/lib.rs", cargoTest); err == nil {
			t.Error("a manifest-supplied flag reached argv")
		}
	})
	t.Run("a shell-wrapped command cannot be scoped", func(t *testing.T) {
		repo := t.TempDir()
		writeManifest(t, repo, "Cargo.toml", pkgToml)
		if _, err := ScopeTestCmd(rustPlugin{}, repo, "src/lib.rs", []string{"sh", "-c", "cargo test"}); err == nil {
			t.Error("a shell-wrapped command was scoped by appending to sh")
		}
	})
}

// A plugin that does not implement CommandScoper is unchanged: every other
// language's command reaches the run exactly as before.
func TestScopeTestCmdIsIdentityForOtherLanguages(t *testing.T) {
	for _, name := range Names() {
		if _, scopes := registry[name].(CommandScoper); scopes {
			continue
		}
		cmd := []string{"x", "y"}
		got, err := ScopeTestCmd(registry[name], t.TempDir(), "a/b", cmd)
		if err != nil || !reflect.DeepEqual(got, cmd) {
			t.Errorf("%s: got (%q, %v)", name, got, err)
		}
	}
}

// Only cargo takes -p. A wrapper (make, just, a script) would receive it as its
// own argument, so the crate must be named by the operator instead.
func TestScopeTestCmdOnlyScopesCargoRunners(t *testing.T) {
	repo := t.TempDir()
	writeManifest(t, repo, "Cargo.toml", pkgToml)

	for _, cmd := range [][]string{{"make", "test"}, {"just", "test"}, {"./ci.sh"}, {"make", "nextest"}} {
		_, err := ScopeTestCmd(rustPlugin{}, repo, "src/lib.rs", cmd)
		if err == nil || !strings.Contains(err.Error(), "-p <crate>") {
			t.Errorf("%q: got %v, want the name-the-crate refusal", cmd, err)
		}
	}
	// ...unless the operator's own selection is present.
	if got, err := ScopeTestCmd(rustPlugin{}, repo, "src/lib.rs", []string{"make", "test", "-p", "x"}); err != nil || len(got) != 4 {
		t.Errorf("operator selection on a wrapper: got (%q, %v)", got, err)
	}
	for _, c := range []struct{ in, want []string }{
		{[]string{"/home/u/.cargo/bin/cargo", "test"}, []string{"/home/u/.cargo/bin/cargo", "test", "-p", "leaf_crate"}},
		{[]string{"cargo-nextest", "nextest", "run"}, []string{"cargo-nextest", "nextest", "run", "-p", "leaf_crate"}},
		{[]string{"RUST_LOG=1", "cargo", "test"}, []string{"RUST_LOG=1", "cargo", "test", "-p", "leaf_crate"}},
	} {
		got, err := ScopeTestCmd(rustPlugin{}, repo, "src/lib.rs", c.in)
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("%q: got (%q, %v), want %q", c.in, got, err, c.want)
		}
	}
}

// A command that names no program at all (only a VAR=value prefix) used to be
// refused as running ".": filepath.Base of an empty program. It says there is
// no program instead (final review m3).
func TestScopeTestCmdRefusesACommandWithNoProgram(t *testing.T) {
	repo := t.TempDir()
	writeManifest(t, repo, "Cargo.toml", pkgToml)
	_, err := ScopeTestCmd(rustPlugin{}, repo, "src/lib.rs", []string{"RUST_LOG=1"})
	if err == nil || strings.Contains(err.Error(), `"."`) || !strings.Contains(err.Error(), "no program") || !strings.Contains(err.Error(), "-p <crate>") {
		t.Errorf("got %v, want a refusal saying the command names no program", err)
	}
}

// A manifest with no [package] below a package root does not end the walk.
func TestScopeTestCmdContinuesPastAPackagelessManifest(t *testing.T) {
	repo := t.TempDir()
	writeManifest(t, repo, "Cargo.toml", pkgToml)
	writeManifest(t, repo, "a/b/Cargo.toml", virtualToml)
	writeManifest(t, repo, "a/Cargo.toml", "[dependencies]\nx = \"1\"\n")
	got, err := ScopeTestCmd(rustPlugin{}, repo, "a/b/c/src/lib.rs", []string{"cargo", "test"})
	if err != nil || got[len(got)-1] != "leaf_crate" {
		t.Errorf("got (%q, %v), want the package at the root", got, err)
	}
}
