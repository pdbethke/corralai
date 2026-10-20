// SPDX-License-Identifier: Elastic-2.0

//go:build windows

package dirlock

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows"
)

// lock takes LockFileEx on a file under the user's temp directory named by
// the directory's absolute path. Windows cannot byte-range lock a directory
// handle, and a lockfile INSIDE the directory would be committed by the
// `git add -A` that publishes a ledger branch. The path is lower-cased
// because Windows paths are case-insensitive: two spellings of one directory
// must name one lock. The cost, stated: two USERS writing one shared
// directory have different temp directories and so do not exclude each
// other; a ledger directory is one operator's.
func lock(dir string) (func() error, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("dirlock: %w", err)
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(abs))))
	path := filepath.Join(os.TempDir(), "corral-dirlock-"+hex.EncodeToString(sum[:8])+".lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) // #nosec G304 -- a name derived from a hash, under the user's temp dir
	if err != nil {
		return nil, fmt.Errorf("dirlock: %w", err)
	}
	h := windows.Handle(f.Fd())
	ol := new(windows.Overlapped)
	if err := windows.LockFileEx(h, windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, ol); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("dirlock: lock %s: %w", dir, err)
	}
	return func() error {
		uerr := windows.UnlockFileEx(h, 0, 1, 0, ol)
		cerr := f.Close()
		return errors.Join(uerr, cerr)
	}, nil
}
