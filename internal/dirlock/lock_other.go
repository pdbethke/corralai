// SPDX-License-Identifier: Elastic-2.0

//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly || windows)

package dirlock

import (
	"fmt"
	"runtime"
)

// lock refuses on a platform with no lock implementation. Writing a ledger
// unlocked is how two writers break a chain for good, so "no lock" is an
// error the caller reports, never a silent success.
func lock(dir string) (func() error, error) {
	return nil, fmt.Errorf("dirlock: no directory lock on %s, so %s cannot be written safely", runtime.GOOS, dir)
}
