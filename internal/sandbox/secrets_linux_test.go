// SPDX-License-Identifier: Elastic-2.0

//go:build linux

package sandbox

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBwrapMasksSystemCargoSecrets: the bwrap jail applies the masks after
// every bind, so a cargo home's credentials and checkouts read as nothing
// while its bin/ stays usable. Run for real: the stand-in for /usr is bound
// read-only exactly as /usr is.
func TestBwrapMasksSystemCargoSecrets(t *testing.T) {
	iso, err := Resolve(Config{Backend: "bwrap"})
	if err != nil {
		t.Skipf("SKIP: bwrap unavailable: %v", err)
	}
	usr, ch := fakeSystemCargoHome(t)
	t.Setenv("CARGO_HOME", ch)
	old := wholeBoundTrees
	wholeBoundTrees = []string{usr}
	t.Cleanup(func() { wholeBoundTrees = old })

	script := "cat " + filepath.Join(ch, "credentials.toml") + "; cat " + filepath.Join(ch, "config.toml") +
		"; ls -A " + filepath.Join(ch, "git") + "; echo BIN=$(head -c 9 " + filepath.Join(ch, "bin", "cargo") + ")"
	res := Run(context.Background(), script, Options{
		Workspace:     t.TempDir(),
		Backend:       iso,
		ReadOnlyBinds: []Bind{{Host: usr, Target: usr}},
	})
	if strings.Contains(res.Output, "secret") || strings.Contains(res.Output, "checkouts") {
		t.Errorf("a masked secret was readable in the jail:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "BIN=#!/bin/sh") {
		t.Errorf("bin/ must stay readable:\n%s", res.Output)
	}
}

// TestBwrapStartsWithAPrefixCargoUnderUsr (R1 negative control, real bwrap):
// a standalone cargo in the stand-in /usr/local, with lib/, go/ and a
// man -> share/man symlink, masks nothing: the jail starts and go/ is intact.
func TestBwrapStartsWithAPrefixCargoUnderUsr(t *testing.T) {
	iso, err := Resolve(Config{Backend: "bwrap"})
	if err != nil {
		t.Skipf("SKIP: bwrap unavailable: %v", err)
	}
	usr := t.TempDir()
	local := filepath.Join(usr, "local")
	for _, d := range []string{"bin", "lib", "go/bin", "share/man"} {
		if err := os.MkdirAll(filepath.Join(local, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"bin/cargo", "go/bin/go"} {
		if err := os.WriteFile(filepath.Join(local, f), []byte("#!/bin/sh\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("share/man", filepath.Join(local, "man")); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CARGO_HOME", "")
	t.Setenv("PATH", filepath.Join(local, "bin")+":/usr/bin:/bin")
	old := wholeBoundTrees
	wholeBoundTrees = []string{usr}
	t.Cleanup(func() { wholeBoundTrees = old })

	res := Run(context.Background(), "head -c 9 "+filepath.Join(local, "go", "bin", "go")+"; echo; echo STARTED", Options{
		Workspace: t.TempDir(), Backend: iso, ReadOnlyBinds: []Bind{{Host: usr, Target: usr}},
	})
	if res.ExitCode != 0 || !strings.Contains(res.Output, "#!/bin/sh") || !strings.Contains(res.Output, "STARTED") {
		t.Errorf("the jail must start with /usr/local/go intact; exit=%d out=%q", res.ExitCode, res.Output)
	}
}

// TestBwrapMasksFollowSymlinkedEntries (R4, real bwrap): in a cargo home
// under /usr, an entry that is a symlink is classified by its TARGET — a link
// to a directory inside the bound tree masks that directory; a link to a
// target outside every whole-bound tree, or a dangling one, is skipped (it is
// unreachable through the tree anyway). The jail starts either way.
func TestBwrapMasksFollowSymlinkedEntries(t *testing.T) {
	iso, err := Resolve(Config{Backend: "bwrap"})
	if err != nil {
		t.Skipf("SKIP: bwrap unavailable: %v", err)
	}
	usr, ch := fakeSystemCargoHome(t)
	cache := filepath.Join(usr, "cache", "registry")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cache, "index"), []byte("private-index\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(ch, "registry")); err != nil {
		t.Fatal(err)
	}
	for name, target := range map[string]string{"registry": cache, "outside": t.TempDir(), "dangling": filepath.Join(usr, "nope")} {
		if err := os.Symlink(target, filepath.Join(ch, name)); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CARGO_HOME", ch)
	old := wholeBoundTrees
	wholeBoundTrees = []string{usr}
	t.Cleanup(func() { wholeBoundTrees = old })

	res := Run(context.Background(), "cat "+filepath.Join(cache, "index")+"; cat "+filepath.Join(ch, "credentials.toml")+"; echo STARTED", Options{
		Workspace: t.TempDir(), Backend: iso, ReadOnlyBinds: []Bind{{Host: usr, Target: usr}},
	})
	if !strings.Contains(res.Output, "STARTED") {
		t.Fatalf("the jail must start; exit=%d out=%q", res.ExitCode, res.Output)
	}
	if strings.Contains(res.Output, "private-index") || strings.Contains(res.Output, "secret") {
		t.Errorf("a masked entry was readable:\n%s", res.Output)
	}
}

// TestBwrapStartsWithADistroCargoInUsrBin (S1, real bwrap): a regular
// /usr/bin/cargo beside /usr/bin/env masks nothing — the jail starts and runs
// the other programs in /usr/bin.
func TestBwrapStartsWithADistroCargoInUsrBin(t *testing.T) {
	iso, err := Resolve(Config{Backend: "bwrap"})
	if err != nil {
		t.Skipf("SKIP: bwrap unavailable: %v", err)
	}
	usr := standInUsr(t)
	t.Setenv("CARGO_HOME", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("PATH", filepath.Join(usr, "bin")+":/usr/bin:/bin")
	old := wholeBoundTrees
	wholeBoundTrees = []string{usr}
	t.Cleanup(func() { wholeBoundTrees = old })

	res := Run(context.Background(), "sh "+filepath.Join(usr, "bin", "ls")+"; echo STARTED", Options{
		Workspace: t.TempDir(), Backend: iso, ReadOnlyBinds: []Bind{{Host: usr, Target: usr}},
	})
	if res.ExitCode != 0 || !strings.Contains(res.Output, "LS-RUNS") || !strings.Contains(res.Output, "STARTED") {
		t.Errorf("the jail must start and run /usr/bin programs; exit=%d out=%q", res.ExitCode, res.Output)
	}
}
