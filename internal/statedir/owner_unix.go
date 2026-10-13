// SPDX-License-Identifier: Elastic-2.0

//go:build unix

package statedir

import (
	"os"
	"syscall"
)

// ownershipChecked says whether ownerUID can answer on this platform.
const ownershipChecked = true

// ownerUID is the uid that owns fi.
func ownerUID(fi os.FileInfo) (int, bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, false
	}
	return int(st.Uid), true
}
