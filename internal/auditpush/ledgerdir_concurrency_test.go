// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

// concurrentAppendsEnv names the directory a re-exec'd child of this test
// binary appends to; concurrentAppendsCountEnv how many entries it writes.
// The usual Go pattern for a multi-process test: the child is this same
// binary, run with -test.run naming the helper below.
const (
	concurrentAppendsEnv      = "CORRAL_TEST_LEDGER_APPEND_DIR"
	concurrentAppendsCountEnv = "CORRAL_TEST_LEDGER_APPEND_N"
)

// requireChainOf asserts dir holds exactly n entries and that the chain
// they form is intact — the property two unlocked writers break for good
// (both read one head, both link to it, and every later append is refused).
func requireChainOf(t *testing.T, dir string, n int) {
	t.Helper()
	if err := RequireIntactChain(dir); err != nil {
		t.Fatalf("the chain is broken after concurrent appends: %v", err)
	}
	got, err := ReadLedgerDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != n {
		t.Fatalf("want %d entries, one per append, got %d", n, len(got))
	}
	// And the chain is still extendable: the next append is accepted.
	if _, err := PushBundle(dir, Bundle{Scan: ScanRow{Repo: "r", Commit: "after", ScanID: 1}}); err != nil {
		t.Fatalf("an append after the concurrent ones was refused: %v", err)
	}
}

// Goroutines of ONE process appending at once. Before the directory lock,
// 8 concurrent PushBundle calls broke the chain in 3 of 3 runs (final
// whole-branch review, 2026-10-20).
func TestConcurrentAppendsInOneProcessKeepTheChainIntact(t *testing.T) {
	dir := t.TempDir()
	const n = 8
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := PushBundle(dir, Bundle{Scan: ScanRow{Repo: "r", Commit: fmt.Sprintf("g%03d", i), ScanID: 1}})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("a concurrent append failed: %v", err)
		}
	}
	requireChainOf(t, dir, n)
}

// Two PROCESSES appending at once — the machine ledger's real shape: two
// certify runs on one machine. Each child is this test binary re-run on the
// helper below.
func TestConcurrentAppendsFromTwoProcessesKeepTheChainIntact(t *testing.T) {
	dir := t.TempDir()
	const procs, each = 2, 6
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmds := make([]*exec.Cmd, procs)
	outs := make([]*lockedBuffer, procs)
	for p := range procs {
		cmd := exec.Command(exe, "-test.run=^TestHelperAppendsToALedgerDir$", "-test.count=1") // #nosec G204 -- this test binary re-run on its own helper
		outs[p] = &lockedBuffer{}
		cmd.Stdout, cmd.Stderr = outs[p], outs[p]
		cmd.Env = append(os.Environ(), concurrentAppendsEnv+"="+dir, concurrentAppendsCountEnv+"="+strconv.Itoa(each), "CORRAL_TEST_LEDGER_APPEND_TAG=p"+strconv.Itoa(p))
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		cmds[p] = cmd
	}
	for p, cmd := range cmds {
		if err := cmd.Wait(); err != nil {
			t.Fatalf("child %d failed: %v\n%s", p, err, outs[p].String())
		}
	}
	requireChainOf(t, dir, procs*each)
}

// TestHelperAppendsToALedgerDir is the child of the two-process test, and a
// no-op anywhere else. It appends from several goroutines too, so each
// process contends with itself as well as with its sibling.
func TestHelperAppendsToALedgerDir(t *testing.T) {
	dir := os.Getenv(concurrentAppendsEnv)
	if dir == "" {
		t.Skip("the child half of TestConcurrentAppendsFromTwoProcessesKeepTheChainIntact; runs only when re-exec'd by it")
	}
	n, err := strconv.Atoi(os.Getenv(concurrentAppendsCountEnv))
	if err != nil {
		t.Fatal(err)
	}
	tag := os.Getenv("CORRAL_TEST_LEDGER_APPEND_TAG")
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := PushBundle(dir, Bundle{Scan: ScanRow{Repo: "r", Commit: fmt.Sprintf("%s-%03d", tag, i), ScanID: 1}}); err != nil {
				t.Errorf("append %s/%d: %v", tag, i, err)
			}
		}()
	}
	wg.Wait()
}

// The lock creates nothing beside the entries: a ledger directory is a git
// worktree published with `git add -A`, and every reader (ReadLedgerDir,
// the site renderer) walks it.
func TestAppendingLeavesNoLockFileInTheLedgerDir(t *testing.T) {
	dir := t.TempDir()
	if _, err := PushBundle(dir, Bundle{Scan: ScanRow{Repo: "r", Commit: "aaa1", ScanID: 1}}); err != nil {
		t.Fatal(err)
	}
	var stray []string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() && !isLedgerEntryName(d.Name()) {
			stray = append(stray, p)
		}
		return nil
	})
	if len(stray) != 0 {
		t.Fatalf("an append left files that are not entries: %v", stray)
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.b = append(l.b, p...)
	return len(p), nil
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return string(l.b)
}
