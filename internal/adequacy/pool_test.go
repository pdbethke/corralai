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

// TestProbeDowngradesASuiteThatIsNotConcurrencySafe: a suite that takes an
// exclusive lock passes alone and fails when two trees run it at once — the
// fixed-port shape, without a port.
func TestProbeDowngradesASuiteThatIsNotConcurrencySafe(t *testing.T) {
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("flock not on PATH")
	}
	root := newGitRepo(t, map[string]string{"a.py": "x=0\n"}, nil)
	lock := filepath.Join(t.TempDir(), "lock")
	cmd := []string{"sh", "-c", fmt.Sprintf(`exec 9>%s; flock -n 9 || exit 1; sleep 0.3; [ "$(cat a.py)" != "!!!corral canary!!!" ]`, lock)}
	p, _, _ := NewWorkspacePool(context.Background(), root, 3, time.Minute)
	defer p.Close()
	q, d := p.Probe(context.Background(), nil, "a.py", "x=0\n", cmd)
	defer q.Close()
	if d.Trees != 1 || !strings.HasPrefix(d.Note, "suite is not concurrency-safe: baseline failed under 3") {
		t.Errorf("got %+v", d)
	}
	if q.Trees() != 1 || q.treeRoots()[0] != root {
		t.Errorf("downgrade must score on the checkout itself")
	}
}

// TestProbePassesAConcurrencySafeSuite: the healthy path returns the pool the
// caller already has, untouched.
func TestProbePassesAConcurrencySafeSuite(t *testing.T) {
	root := newGitRepo(t, map[string]string{"a.py": "x=0\n"}, nil)
	cmd := []string{"sh", "-c", `[ "$(cat a.py)" = "x=0" ]`} // fails on the canary, passes on compliant
	p, _, _ := NewWorkspacePool(context.Background(), root, 3, time.Minute)
	defer p.Close()
	q, d := p.Probe(context.Background(), nil, "a.py", "x=0\n", cmd)
	if d.Trees != 3 || d.Note != "" || q != p {
		t.Errorf("got %+v", d)
	}
}

// TestProbeCatchesATreeThatImportsTheOriginal: a tree that reads the ORIGINAL
// checkout (an editable install's .pth) sees compliant code even when its own
// file is the canary — the probe must catch that, because otherwise every
// mutant would survive.
func TestProbeCatchesATreeThatImportsTheOriginal(t *testing.T) {
	root := newGitRepo(t, map[string]string{"a.py": "x=0\n"}, nil)
	cmd := []string{"sh", "-c", fmt.Sprintf(`[ "$(cat %s/a.py)" = "x=0" ]`, root)} // reads root, not $PWD
	p, _, _ := NewWorkspacePool(context.Background(), root, 2, time.Minute)
	defer p.Close()
	q, d := p.Probe(context.Background(), nil, "a.py", "x=0\n", cmd)
	defer q.Close()
	if d.Trees != 1 || !strings.Contains(d.Note, "imports the original") {
		t.Errorf("got %+v", d)
	}
}

// TestDowngradedPoolCarriesNoTreeEnv is the other half of "a pool of one IS
// the checkout, byte for byte".
//
// WithTreeEnv describes what a COPY needs to behave like the checkout: its own
// root on PYTHONPATH (so it cannot import the original through an editable
// install's .pth) and its own SHARE of the box (GOMAXPROCS, -p, because it is
// one of N). On the checkout itself both are actively harmful — the run would
// be pinned to cores/N with no sibling tree to yield to, i.e. SLOWER than if
// concurrency had never been attempted, and a suite that never asked for a
// PYTHONPATH would get one.
//
// Every downgrade path lands here: n <= 1, a checkout that is not a git work
// tree, a universe over the cap, and Probe's own downgrade (which rebuilds
// through NewWorkspacePool with the SAME option list, tree env included —
// which is exactly how this got shipped wrong once).
func TestDowngradedPoolCarriesNoTreeEnv(t *testing.T) {
	treeEnv := WithTreeEnv(func(tree string) []string { return []string{"CORRAL_TREE=" + tree} })
	// The run passes only if the tree env is ABSENT.
	clean := []string{"sh", "-c", `[ -z "$CORRAL_TREE" ]`}

	t.Run("n=1", func(t *testing.T) {
		root := newGitRepo(t, map[string]string{"a.py": "x=0\n"}, nil)
		p, _, _ := NewWorkspacePool(context.Background(), root, 1, time.Minute, treeEnv)
		defer p.Close()
		if ok, err := p.RunTest(context.Background(), nil, clean); err != nil || !ok {
			t.Errorf("a one-tree pool injected the per-tree env into the operator's checkout: ok=%v err=%v", ok, err)
		}
	})

	t.Run("not a git work tree", func(t *testing.T) {
		root := t.TempDir()
		writeFile(t, filepath.Join(root, "a.py"), "x=0\n")
		p, d, _ := NewWorkspacePool(context.Background(), root, 3, time.Minute, treeEnv)
		defer p.Close()
		if d.Trees != 1 {
			t.Fatalf("expected a downgrade, got %+v", d)
		}
		if ok, err := p.RunTest(context.Background(), nil, clean); err != nil || !ok {
			t.Errorf("a downgraded pool injected the per-tree env into the operator's checkout: ok=%v err=%v", ok, err)
		}
	})

	t.Run("probe downgrade", func(t *testing.T) {
		root := newGitRepo(t, map[string]string{"a.py": "x=0\n"}, nil)
		// Passes only in the checkout itself, so the probe must downgrade.
		cmd := []string{"sh", "-c", `[ "$(pwd -P)" = "` + root + `" ] && [ "$(cat a.py)" = "x=0" ]`}
		p, _, _ := NewWorkspacePool(context.Background(), root, 3, time.Minute, treeEnv)
		q, d := p.Probe(context.Background(), nil, "a.py", "x=0\n", cmd)
		defer q.Close()
		if d.Trees != 1 {
			t.Fatalf("expected a downgrade, got %+v", d)
		}
		if ok, err := q.RunTest(context.Background(), nil, clean); err != nil || !ok {
			t.Errorf("the pool the probe fell back to carries the per-tree env, on the operator's own checkout: ok=%v err=%v", ok, err)
		}
	})
}

// Found by the first real run: `corral certify --repo .` hands the pool a
// RELATIVE root, and a symlink whose target is the relative ".venv" points at
// ITSELF from inside the tree — every run failed with "too many levels of
// symbolic links" and the probe (correctly) downgraded to one tree. The
// checkout's dep dirs must be linked by absolute path, whatever root the
// operator typed.
func TestPoolLinksDepDirsByAbsolutePathFromARelativeRoot(t *testing.T) {
	root := newGitRepo(t, map[string]string{"a.py": "x=1\n"}, nil)
	writeFile(t, filepath.Join(root, ".venv", "bin", "marker"), "here\n")
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chdir(wd) }()
	p, d, err := NewWorkspacePool(context.Background(), ".", 2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if d.Trees != 2 {
		t.Fatalf("disclosure = %+v", d)
	}
	for _, tree := range p.treeRoots() {
		target, err := os.Readlink(filepath.Join(tree, ".venv"))
		if err != nil {
			t.Fatal(err)
		}
		if !filepath.IsAbs(target) {
			t.Errorf("%s: .venv links to %q — a relative target resolves inside the tree, i.e. to itself", tree, target)
		}
		// Reading THROUGH the link is the real test: a self-loop fails here.
		mustExist(t, filepath.Join(tree, ".venv", "bin", "marker"))
	}
}

// TestToxIsNeverSharedBetweenTrees pins the one dep dir that must NOT be
// symlinked. tox WRITES into .tox — it builds and reuses its envs there — so
// N trees sharing one is cross-tree interference on a directory the run is
// mutating, AND a write through the link into the operator's real checkout.
// Every other entry in symlinkedDepDirs is a read-only build product.
func TestToxIsNeverSharedBetweenTrees(t *testing.T) {
	root := newGitRepo(t, map[string]string{"a.py": "x=1\n"}, []string{".venv/", ".tox/"})
	for _, d := range []string{".venv", ".tox"} {
		if err := os.MkdirAll(filepath.Join(root, d, "lib"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p, d, err := NewWorkspacePool(context.Background(), root, 2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if !reflect.DeepEqual(d.Shared, []string{".venv"}) {
		t.Errorf("Shared = %q, want only [.venv]: .tox is written to, so it is never shared", d.Shared)
	}
	for _, tree := range p.treeRoots() {
		mustNotExist(t, filepath.Join(tree, ".tox"))
	}
}

// Found on requests: tests/certs/valid/ca is a TRACKED SYMLINK (-> ../expired/ca).
// The copy kept regular files only, so every tree lacked the CA the TLS test
// verifies against, the baseline failed under any N > 1, and the probe blamed
// the SUITE ("not concurrency-safe") for what was an incomplete copy. At N=1
// nothing is copied, so the downgrade "fixed" it — three runs in a row. A
// tree must carry the checkout's symlinks as symlinks, target verbatim.
func TestPoolCopiesSymlinksAsSymlinks(t *testing.T) {
	root := newGitRepo(t, map[string]string{"a.py": "x=1\n", "certs/expired/ca.crt": "CA\n"}, nil)
	if err := os.Symlink("expired", filepath.Join(root, "certs", "valid")); err != nil {
		t.Fatal(err)
	}
	// Untracked-not-ignored is in the universe too, so no commit is needed;
	// requests' link is tracked, and the copy must not care which.
	p, d, err := NewWorkspacePool(context.Background(), root, 2, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if d.Trees != 2 {
		t.Fatalf("disclosure = %+v", d)
	}
	for _, tree := range p.treeRoots() {
		target, err := os.Readlink(filepath.Join(tree, "certs", "valid"))
		if err != nil {
			t.Fatalf("%s: certs/valid must be a symlink like the checkout's: %v", tree, err)
		}
		if target != "expired" {
			t.Errorf("%s: certs/valid -> %q, want the checkout's target %q verbatim", tree, target, "expired")
		}
		mustExist(t, filepath.Join(tree, "certs", "valid", "ca.crt"))
	}
}
