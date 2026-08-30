// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

// writeFile writes content at path, creating parent directories.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func mustExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Errorf("expected %s to exist: %v", path, err)
	}
}

func mustNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); err == nil {
		t.Errorf("expected %s NOT to exist", path)
	}
}

// newGitRepo creates a committed git checkout in a temp dir: files written,
// a .gitignore holding ignored, then `git add -A` + a commit.
func newGitRepo(t *testing.T, files map[string]string, ignored []string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	root := t.TempDir()
	for rel, content := range files {
		writeFile(t, filepath.Join(root, rel), content)
	}
	if len(ignored) > 0 {
		writeFile(t, filepath.Join(root, ".gitignore"), strings.Join(ignored, "\n")+"\n")
	}
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...) // #nosec G204 -- test fixture, literal args
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("add", "-A")
	run("-c", "user.email=t@t", "-c", "user.name=t", "commit", "-q", "-m", "init")
	return root
}

func TestPoolBuildsTreesFromTheGitUniverse(t *testing.T) {
	root := newGitRepo(t, map[string]string{"a.py": "x=1\n", "tests/test_a.py": "def test(): pass\n", "out/junk.py": "junk\n"}, []string{"out/"})
	writeFile(t, filepath.Join(root, "untracked.py"), "u=1\n") // untracked, NOT ignored
	if err := os.MkdirAll(filepath.Join(root, ".venv", "lib"), 0o755); err != nil {
		t.Fatal(err)
	}
	p, d, err := NewWorkspacePool(context.Background(), root, 3, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if d.Trees != 3 || d.Note != "" {
		t.Fatalf("disclosure = %+v", d)
	}
	// The shared dep dirs are the one thing the trees do NOT hold privately,
	// so the disclosure has to name them even on a healthy pool.
	if !reflect.DeepEqual(d.Shared, []string{".venv"}) {
		t.Errorf("disclosure Shared = %q, want [.venv]", d.Shared)
	}
	for _, tree := range p.treeRoots() {
		mustExist(t, filepath.Join(tree, "a.py"))
		mustExist(t, filepath.Join(tree, "untracked.py"))
		mustNotExist(t, filepath.Join(tree, "out", "junk.py"))
		if fi, err := os.Lstat(filepath.Join(tree, ".venv")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("%s: .venv must be a symlink to the checkout's, got %v %v", tree, fi, err)
		}
	}
}

func TestPoolOfOneIsTheCheckoutItself(t *testing.T) {
	root := newGitRepo(t, map[string]string{"a.py": "x=1\n"}, nil)
	p, d, _ := NewWorkspacePool(context.Background(), root, 1, time.Minute)
	if d.Trees != 1 || d.Note != "" || len(p.treeRoots()) != 1 || p.treeRoots()[0] != root {
		t.Errorf("n=1 must run on the checkout with no copy: %+v %v", d, p.treeRoots())
	}
	if d.Shared != nil {
		t.Errorf("a pool of one links nothing, so Shared must be nil: %q", d.Shared)
	}
}

func TestPoolDowngradesOutsideAGitWorkTree(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.py"), "x=1\n")
	_, d, err := NewWorkspacePool(context.Background(), root, 4, time.Minute)
	if err != nil || d.Trees != 1 || d.Note != "not a git work tree" {
		t.Errorf("got %+v %v", d, err)
	}
}

// The property the whole design exists for: two concurrent runs never see
// each other's mutant. Each run's command is a script that sleeps, then
// asserts the file content it was told to expect.
func TestPoolConcurrentRunsNeverObserveAnotherMutant(t *testing.T) {
	root := newGitRepo(t, map[string]string{"a.py": "x=0\n"}, nil)
	p, _, _ := NewWorkspacePool(context.Background(), root, 4, time.Minute)
	defer p.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 1; i <= 16; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			want := fmt.Sprintf("x=%d\n", i)
			// sh -c: sleep a little, then compare the file in the CURRENT tree to the expected mutant.
			ok, err := p.RunTest(context.Background(), map[string]string{"a.py": want},
				[]string{"sh", "-c", fmt.Sprintf(`sleep 0.05; [ "$(cat a.py)" = "x=%d" ]`, i)})
			if err != nil || !ok {
				errs <- fmt.Errorf("run %d: ok=%v err=%v", i, ok, err)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	// And every tree is restored afterwards.
	for _, tree := range p.treeRoots() {
		if b, _ := os.ReadFile(filepath.Join(tree, "a.py")); string(b) != "x=0\n" {
			t.Errorf("%s not restored: %q", tree, b)
		}
	}
}

// Enumerate goes through the SAME applyFiles/restore ledger a mutant run
// does, so it must borrow a tree too: an enumeration sharing tree 0 with an
// in-flight RunTest would interleave two ledgers on one checkout. Two trees,
// many of each call, so contention is guaranteed.
func TestPoolEnumerateNeverSharesATreeWithAConcurrentRun(t *testing.T) {
	root := newGitRepo(t, map[string]string{"a.py": "x=0\n"}, nil)
	p, d, err := NewWorkspacePool(context.Background(), root, 2, time.Minute)
	if err != nil || d.Trees != 2 {
		t.Fatalf("pool: %+v %v", d, err)
	}
	defer p.Close()
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 1; i <= 8; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			ok, rerr := p.RunTest(context.Background(), map[string]string{"a.py": fmt.Sprintf("x=%d\n", i)},
				[]string{"sh", "-c", fmt.Sprintf(`sleep 0.05; [ "$(cat a.py)" = "x=%d" ]`, i)})
			if rerr != nil || !ok {
				errs <- fmt.Errorf("run %d: ok=%v err=%v", i, ok, rerr)
			}
		}(i)
		go func(i int) {
			defer wg.Done()
			want := fmt.Sprintf("enum=%d\n", i)
			out, eerr := p.Enumerate(context.Background(), map[string]string{"a.py": want},
				[]string{"sh", "-c", "sleep 0.05; cat a.py"})
			if eerr != nil {
				errs <- fmt.Errorf("enumerate %d: %v", i, eerr)
			} else if out != want {
				errs <- fmt.Errorf("enumerate %d saw another run's file: %q, want %q", i, out, want)
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	for _, tree := range p.treeRoots() {
		if b, _ := os.ReadFile(filepath.Join(tree, "a.py")); string(b) != "x=0\n" {
			t.Errorf("%s not restored: %q", tree, b)
		}
	}
}

func TestPoolTreeEnvIsPerTree(t *testing.T) {
	root := newGitRepo(t, map[string]string{"a.py": "x=0\n"}, nil)
	p, _, _ := NewWorkspacePool(context.Background(), root, 2, time.Minute,
		WithTreeEnv(func(tree string) []string { return []string{"CORRAL_TREE=" + tree} }))
	defer p.Close()
	ok, err := p.RunTest(context.Background(), nil, []string{"sh", "-c", `[ "$CORRAL_TREE" = "$(pwd -P)" ]`})
	if err != nil || !ok {
		t.Errorf("the run must see ITS tree in the env: ok=%v err=%v", ok, err)
	}
}
