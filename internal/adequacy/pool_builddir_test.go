// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/lang"
)

// The audited language's declared build directory (lang.BuildDir, passed by
// WithBuildDirs) is never linked into a tree, and git keeps it out of the
// copy when it is ignored. Then every tree builds its own, and the pool says
// so on its disclosure, naming the directory (SwiftPM's .build, cargo's
// target). When git does NOT ignore it, its files are in the universe and
// would be copied into every tree, build output and all: the pool is one
// tree, the checkout, and the note names the directory.
func TestPoolNeverCopiesADeclaredBuildDirectory(t *testing.T) {
	for _, c := range []struct{ lang, marker, dir string }{
		{"swift", "Package.swift", ".build"},
		{"rust", "Cargo.toml", "target"},
	} {
		plug, _ := lang.ByName(c.lang)
		opt := WithBuildDirs(plug.(lang.BuildDirer).BuildDirs())
		files := map[string]string{c.marker: "x\n", "src/a.txt": "a\n"}

		ignored := newGitRepo(t, files, []string{c.dir + "/"})
		writeFile(t, filepath.Join(ignored, c.dir, "debug", "out.o"), "o\n")
		p, d, err := NewWorkspacePool(context.Background(), ignored, 3, time.Minute, opt)
		if err != nil {
			t.Fatal(err)
		}
		if d.Trees != 3 || !strings.Contains(d.Note, c.dir) {
			t.Errorf("%s, %s ignored: want three trees and a note naming it, got %+v", c.lang, c.dir, d)
		}
		for _, tree := range p.treeRoots() {
			mustNotExist(t, filepath.Join(tree, c.dir))
		}
		p.Close()

		copied := newGitRepo(t, files, nil)
		writeFile(t, filepath.Join(copied, c.dir, "debug", "out.o"), "o\n") // untracked, not ignored
		p, d, err = NewWorkspacePool(context.Background(), copied, 3, time.Minute, opt)
		if err != nil {
			t.Fatal(err)
		}
		if d.Trees != 1 || !strings.Contains(d.Note, c.dir) {
			t.Errorf("%s, %s not ignored: want one tree and a note naming it, got %+v", c.lang, c.dir, d)
		}
		if roots := p.treeRoots(); len(roots) != 1 || roots[0] != copied {
			t.Errorf("%s: the one tree must be the checkout itself, got %v", c.lang, roots)
		}
		p.Close()
	}
}

// A directory merely named like a build directory, with no marker beside it,
// is somebody's source: it is copied, and nothing is said about it.
func TestPoolCopiesADirectoryOnlyNamedLikeABuildDirectory(t *testing.T) {
	root := newGitRepo(t, map[string]string{"target/notes.txt": "n\n", "a.py": "x=1\n"}, nil)
	p, d, err := NewWorkspacePool(context.Background(), root, 2, time.Minute, WithBuildDirs([]lang.BuildDir{{Name: "target", Marker: "Cargo.toml"}}))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if d.Trees != 2 || d.Note != "" {
		t.Errorf("got %+v", d)
	}
	for _, tree := range p.treeRoots() {
		mustExist(t, filepath.Join(tree, "target", "notes.txt"))
	}
}

// The probe's answer for a healthy pool restates the build-directory note:
// a caller that records only the probe's disclosure must not lose it.
func TestProbeKeepsTheBuildDirectoryNote(t *testing.T) {
	root := newGitRepo(t, map[string]string{"Cargo.toml": "x\n", "a.py": "x=0\n"}, []string{"target/"})
	cmd := []string{"sh", "-c", `[ "$(cat a.py)" = "x=0" ]`}
	p, built, _ := NewWorkspacePool(context.Background(), root, 2, time.Minute, WithBuildDirs([]lang.BuildDir{{Name: "target", Marker: "Cargo.toml"}}))
	defer p.Close()
	q, d := p.Probe(context.Background(), nil, "a.py", "x=0\n", cmd)
	if q != p || d.Trees != 2 || d.Note == "" || d.Note != built.Note {
		t.Errorf("probe disclosure %+v, built %+v", d, built)
	}
}

// A pool told no build directories (no WithBuildDirs: the audited language
// declares none, as Python, Go, JavaScript, TypeScript, Ruby and PHP do)
// applies neither the one-tree rule nor the note, whatever another
// language's build output sits in the checkout: a cargo target/ git does not
// ignore beside a Cargo.toml is copied like any other file, as it was before
// the rule existed.
func TestPoolWithNoBuildDirectoriesIgnoresOtherLanguagesBuildOutput(t *testing.T) {
	root := newGitRepo(t, map[string]string{"Cargo.toml": "x\n", "a.py": "x=1\n"}, nil)
	writeFile(t, filepath.Join(root, "target", "debug", "out.o"), "o\n")
	p, d, err := NewWorkspacePool(context.Background(), root, 2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if d.Trees != 2 || d.Note != "" {
		t.Errorf("got %+v", d)
	}
}
