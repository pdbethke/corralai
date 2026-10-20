// SPDX-License-Identifier: Elastic-2.0

package dirlock

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Two Locks in ONE process must exclude each other: the ledger's writers
// include goroutines of one process, and an fcntl-style per-process lock
// would let them both in.
func TestLockExcludesASecondLockInTheSameProcess(t *testing.T) {
	dir := t.TempDir()
	unlock, err := Lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan struct{})
	go func() {
		u2, err := Lock(dir)
		if err != nil {
			t.Error(err)
			close(got)
			return
		}
		close(got)
		_ = u2()
	}()
	select {
	case <-got:
		t.Fatal("a second Lock was granted while the first was held")
	case <-time.After(200 * time.Millisecond):
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("the second Lock was never granted after the first was released")
	}
}

// The lock creates nothing inside the directory: a ledger directory is
// committed with `git add -A`, and a lockfile would be committed with it.
func TestLockLeavesTheDirectoryEmpty(t *testing.T) {
	dir := t.TempDir()
	unlock, err := Lock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ents) != 0 {
		t.Fatalf("the lock left %d file(s) in the directory: %v", len(ents), ents[0].Name())
	}
}

// A missing directory is refused rather than created: making the directory
// is the caller's decision.
func TestLockRefusesAMissingDirectory(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope")
	if _, err := Lock(missing); err == nil {
		t.Fatal("Lock on a missing directory succeeded")
	}
	if _, err := os.Stat(missing); !os.IsNotExist(err) {
		t.Fatalf("Lock created the directory it was asked to lock: %v", err)
	}
}
