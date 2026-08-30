// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A private tree is a COPY of the checkout, and an editable install's .pth
// still points at the ORIGINAL. Without the tree's own root on PYTHONPATH the
// suite imports unmutated source, every mutant survives, and corral signs the
// false accusation this codebase has already killed five times. The env is the
// whole defence, so it is pinned here.
func TestPythonTreeEnvPutsTheTreeFirstOnPYTHONPATH(t *testing.T) {
	p, ok := ByName("python")
	if !ok {
		t.Fatal("no python plugin")
	}
	te, ok := p.(TreeEnver)
	if !ok {
		t.Fatal("python plugin does not implement lang.TreeEnver — the pool can never give its trees an import path")
	}

	// Explicit, so the assertion means the same thing on a box whose shell
	// happens to export one.
	t.Setenv("PYTHONPATH", "")

	flat := t.TempDir()
	got := te.TreeEnv(flat, 4)
	if len(got) != 1 || got[0] != "PYTHONPATH="+flat {
		t.Fatalf("TreeEnv(%q) = %v, want [PYTHONPATH=%s]", flat, got, flat)
	}

	srcy := t.TempDir()
	if err := os.MkdirAll(filepath.Join(srcy, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	got = te.TreeEnv(srcy, 4)
	want := "PYTHONPATH=" + filepath.Join(srcy, "src") + string(os.PathListSeparator) + srcy
	if len(got) != 1 || got[0] != want {
		t.Fatalf("src-layout TreeEnv(%q) = %v, want [%s]", srcy, got, want)
	}
}

// An operator's own PYTHONPATH must survive — appended AFTER the tree, never
// dropped and never in front of it (in front, the original checkout wins the
// import again and the canary is the only thing standing between that and a
// signed zero).
func TestPythonTreeEnvKeepsTheOperatorsPYTHONPATHBehindTheTree(t *testing.T) {
	p, _ := ByName("python")
	te := p.(TreeEnver)
	tree := t.TempDir()
	t.Setenv("PYTHONPATH", "/opt/mine")

	got := te.TreeEnv(tree, 1)
	want := "PYTHONPATH=" + tree + string(os.PathListSeparator) + "/opt/mine"
	if len(got) != 1 || got[0] != want {
		t.Fatalf("TreeEnv = %v, want [%s]", got, want)
	}
}

// CPU is DIVIDED, not multiplied: N trees each assuming the whole box thrash
// it and turn the concurrency probe into a false negative. Go is the one
// language whose toolchain fans out on its own, so it is the one that has to
// be told.
func TestGoTreeEnvDividesTheCPU(t *testing.T) {
	p, ok := ByName("go")
	if !ok {
		t.Fatal("no go plugin")
	}
	te, ok := p.(TreeEnver)
	if !ok {
		t.Fatal("go plugin does not implement lang.TreeEnver — six trees would each try to use all 24 cores")
	}
	t.Setenv("GOFLAGS", "")

	// 24 cores over 6 trees = 4 apiece.
	got := te.TreeEnv(t.TempDir(), 4)
	want := map[string]bool{"GOMAXPROCS=4": true, "GOFLAGS=-trimpath -p=4": true}
	if len(got) != len(want) {
		t.Fatalf("TreeEnv = %v, want %v", got, want)
	}
	for _, kv := range got {
		if !want[kv] {
			t.Fatalf("TreeEnv = %v, unexpected %q (want %v)", got, kv, want)
		}
	}
	// The operator's own GOFLAGS survive: assigning GOFLAGS outright would
	// silently drop a -mod=vendor the project's suite needs, and grade the
	// mutant with a build the operator never runs.
	t.Setenv("GOFLAGS", "-mod=vendor")
	got = te.TreeEnv(t.TempDir(), 4)
	var flags string
	for _, kv := range got {
		if strings.HasPrefix(kv, "GOFLAGS=") {
			flags = kv
		}
	}
	if flags != "GOFLAGS=-mod=vendor -trimpath -p=4" {
		t.Fatalf("GOFLAGS = %q, want the operator's own flags kept with -trimpath and -p appended", flags)
	}

	// Fails closed: a degenerate share is one core, never zero (GOMAXPROCS=0
	// is rejected by the runtime) and never unbounded.
	for _, cores := range []int{0, -1} {
		got := te.TreeEnv(t.TempDir(), cores)
		for _, kv := range got {
			if kv != "GOMAXPROCS=1" && !strings.HasSuffix(kv, "-p=1") {
				t.Fatalf("TreeEnv(cores=%d) = %v, want a share of 1", cores, got)
			}
		}
	}
}
