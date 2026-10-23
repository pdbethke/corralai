// SPDX-License-Identifier: Elastic-2.0

package ledger

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"
)

// ScanUID derives a scan's globally unique identity from the scan itself.
//
// DERIVED, NOT RANDOM, for the reason everything else about a ledger is: a
// value a reader cannot re-check is a value they have to trust. Re-running
// this over a pushed corral_scans row's own columns reproduces its scan_uid
// exactly, so the identity is auditable rather than asserted (the scan row
// only — see RecomputeScanUID for why the other grains cannot offer this).
//
// The inputs are what actually distinguishes two runs — the repository, the CI
// run if there was one, the box, the corral build, the audited commit, the
// substrate, the ledger scan id, and the start timestamp at nanosecond
// precision. Two runs that agree on every one of those are the same run. The
// timestamp is what makes this work where the legacy key fails: two local
// ledgers that both push "scan 1" for the same repo did not start at the same
// nanosecond.
//
// Fields are LENGTH-PREFIXED before hashing, the same guard
// reposcan.KeyInputs.CacheKey uses, so no combination of values can collide by
// concatenation — ("ab","c") and ("a","bc") must not produce one identity.
//
// 128 bits (32 hex chars) of a sha256: short enough to read in a query result,
// far past any collision that matters for a warehouse of scans.
func ScanUID(r ScanRow, ts time.Time) string {
	h := sha256.New()
	for _, f := range []string{
		r.Repo, r.RunURL, r.Host, r.CorralVersion, r.Commit, r.Substrate,
		strconv.FormatInt(r.ScanID, 10),
		ts.UTC().Format(time.RFC3339Nano),
	} {
		fmt.Fprintf(h, "%d:%s|", len(f), f)
	}
	return hex.EncodeToString(h.Sum(nil))[:32]
}
