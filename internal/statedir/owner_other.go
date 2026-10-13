// SPDX-License-Identifier: Elastic-2.0

//go:build !unix

package statedir

import "os"

// ownershipChecked says whether ownerUID can answer on this platform. Off
// unix there is no uid to compare; a per-user profile directory is the
// boundary instead.
const ownershipChecked = false

func ownerUID(os.FileInfo) (int, bool) { return 0, false }
