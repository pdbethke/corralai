// SPDX-License-Identifier: Elastic-2.0

// Package dirlock is an exclusive, inter-process lock on a directory: the
// one mutual exclusion a ledger directory's writers share. A chain is one
// writer at a time, and "one writer" has to hold across PROCESSES — two
// certify runs on one machine append to the same machine ledger — and across
// goroutines in one process, so an in-process mutex would not do and a
// lockfile that outlives a crashed writer would wedge every later run.
//
// The lock is advisory and is released by the kernel when its holder exits,
// however it exits, so a kill -9 mid-append never leaves the directory
// locked. It is per OPEN, not per process: two Lock calls in one process
// exclude each other exactly as two processes do.
//
// Where the lock lives is platform-shaped, and that is deliberate: on unix
// it is flock(2) on the directory itself, so NOTHING is created inside the
// directory — a ledger directory is often a git worktree committed with
// `git add -A` (docs/corral/github-action.md), and a lockfile there would be
// committed to the record branch. Windows cannot lock a directory handle, so
// there the lock is a file under the user's temp directory named by the
// directory's absolute path (lock_windows.go).
package dirlock

import (
	"fmt"
	"os"
)

// Lock blocks until it holds dir's exclusive lock and returns the function
// that releases it. dir must already exist: creating it is the caller's
// decision, never a side effect of asking for a lock.
func Lock(dir string) (unlock func() error, err error) {
	st, err := os.Stat(dir)
	if err != nil {
		return nil, fmt.Errorf("dirlock: %w", err)
	}
	if !st.IsDir() {
		return nil, fmt.Errorf("dirlock: %s is not a directory", dir)
	}
	return lock(dir)
}
