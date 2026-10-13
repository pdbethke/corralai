// SPDX-License-Identifier: Elastic-2.0

//go:build unix

package statedir

import (
	"os"
	"syscall"
)

// lockedByAnother reports whether another process holds an fcntl lock on
// path. F_GETLK only asks; it never takes the lock. DuckDB holds one for as
// long as a store is open, and the daemon keeps several open, so a running
// daemon is always visible here (plan ruling R4).
func lockedByAnother(path string) (bool, error) {
	f, err := os.Open(path) // #nosec G304 -- a statedir table row under the operator's own legacy dir
	if err != nil {
		return false, err
	}
	defer f.Close()
	lk := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: 0, Start: 0, Len: 0}
	if err := syscall.FcntlFlock(f.Fd(), syscall.F_GETLK, &lk); err != nil {
		return false, err
	}
	return lk.Type != syscall.F_UNLCK, nil
}
