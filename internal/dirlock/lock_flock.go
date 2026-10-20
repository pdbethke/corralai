// SPDX-License-Identifier: Elastic-2.0

//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package dirlock

import (
	"errors"
	"fmt"
	"os"
	"syscall"
)

// lock is flock(2) on the directory's own descriptor. flock locks belong to
// the open file description, so two opens in one process conflict, and the
// kernel drops the lock when the descriptor closes — including when the
// process dies.
func lock(dir string) (func() error, error) {
	f, err := os.Open(dir) // #nosec G304 -- the ledger directory the caller is about to write
	if err != nil {
		return nil, fmt.Errorf("dirlock: %w", err)
	}
	fd := int(f.Fd()) // #nosec G115 -- a descriptor the kernel just handed out fits an int
	for {
		err = syscall.Flock(fd, syscall.LOCK_EX)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("dirlock: lock %s: %w", dir, err)
	}
	return func() error {
		uerr := syscall.Flock(fd, syscall.LOCK_UN)
		cerr := f.Close()
		return errors.Join(uerr, cerr)
	}, nil
}
