// SPDX-License-Identifier: Elastic-2.0

package recordview

import "github.com/pdbethke/corralai/internal/auditpush"

// PairChecks pairs each entry with the check VerifyLedgerDir made of it,
// by hash. The two come from two reads of the directory (verification reads
// it itself). Pairing them by position let a concurrent change attach one
// entry's signature check to another entry's bytes, or render an entry
// appended between the reads as "unsigned" with no problem counted (review
// 786e8a0c675e#R1). An entry no check names was not verified, and says so.
func PairChecks(entries []auditpush.LedgerEntry, checks []auditpush.ChainCheck) []auditpush.ChainCheck {
	byHash := make(map[string]auditpush.ChainCheck, len(checks))
	for _, c := range checks {
		byHash[c.Hash] = c
	}
	out := make([]auditpush.ChainCheck, len(entries))
	for i, e := range entries {
		c, ok := byHash[e.Hash]
		if !ok {
			c = auditpush.ChainCheck{Hash: e.Hash, Problem: "not verified: the ledger changed while it was read — reload"}
		}
		out[i] = c
	}
	return out
}
