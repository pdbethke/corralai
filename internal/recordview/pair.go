// SPDX-License-Identifier: Elastic-2.0

package recordview

import "github.com/pdbethke/corralai/internal/auditpush"

// PairedCheck is one entry's chain check, and whether there was one.
// Verified is false only when the verifying pass never saw the entry: its
// HashOK, LinkOK and Signed are then zero because nothing measured them,
// not because anything failed, and no page may render them as outcomes.
type PairedCheck struct {
	auditpush.ChainCheck
	Verified bool
}

// PairChecks pairs each entry with the check VerifyLedgerDir made of it,
// by hash. The two come from two reads of the directory (verification reads
// it itself). Pairing them by position let a concurrent change attach one
// entry's signature check to another entry's bytes, or render an entry
// appended between the reads as "unsigned" with no problem counted (review
// 786e8a0c675e#R1). An entry no check names was not verified, and says so:
// as not verified, never as a failed hash, link or signature, which would
// be a could-not-measure rendered as a measurement (final review of
// feat/recordview, 2026-10-18).
func PairChecks(entries []auditpush.LedgerEntry, checks []auditpush.ChainCheck) []PairedCheck {
	byHash := make(map[string]auditpush.ChainCheck, len(checks))
	for _, c := range checks {
		byHash[c.Hash] = c
	}
	out := make([]PairedCheck, len(entries))
	for i, e := range entries {
		c, ok := byHash[e.Hash]
		if !ok {
			c = auditpush.ChainCheck{Hash: e.Hash}
		}
		out[i] = PairedCheck{ChainCheck: c, Verified: ok}
	}
	return out
}
