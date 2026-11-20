// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/lang"
)

// The options a Swift file's workspace pool is built with carry the lockfile
// Swift declares, so a SwiftPM checkout whose Package.resolved the trees would
// not hold gets one tree, said with the file; a Go checkout (Go declares no
// lockfile) keeps its trees. Built with the real pool, on a real git
// checkout, so the option's effect is what is checked, not its presence.
func TestWorkspaceRunnerOptsCarryTheLanguagesLockfile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	for rel, body := range map[string]string{
		"Package.swift":           "// swift-tools-version:5.9\n",
		"Sources/Calc/Calc.swift": "public func f() {}\n",
		"go.mod":                  "module x\n",
		".gitignore":              ".build/\nPackage.resolved\n",
		"Package.resolved":        "{}\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{{"init", "-q"}, {"add", "-A"}, {"-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "init"}} {
		cmd := exec.Command("git", args...) // #nosec G204 -- test fixture, literal args
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}

	swift, _ := lang.ByName("swift")
	p, d, err := adequacy.NewWorkspacePool(context.Background(), root, 2, time.Minute, workspaceRunnerOpts(swift, 2, root)...)
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
	if d.Trees != 1 || !strings.Contains(d.Note, "Package.resolved") {
		t.Errorf("swift, ignored Package.resolved: want one tree naming it, got %+v", d)
	}

	golang, _ := lang.ByName("go")
	p, d, err = adequacy.NewWorkspacePool(context.Background(), root, 2, time.Minute, workspaceRunnerOpts(golang, 2, root)...)
	if err != nil {
		t.Fatal(err)
	}
	p.Close()
	if d.Trees != 2 {
		t.Errorf("go declares no lockfile: want two trees, got %+v", d)
	}
}
