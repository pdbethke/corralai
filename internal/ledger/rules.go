// SPDX-License-Identifier: Elastic-2.0

package ledger

// The rules for what a chain of entries MEANS: which finding verdict stands,
// which entries a retraction voids, which entries a reader should see. They
// are pure functions over entries, so the CLI and a browser read the record
// the same way.

// Adjudications returns, for every finding ref, the NEWEST adjudication in
// entries — a later verdict by anyone supersedes an earlier one, and both
// stay in the chain.
func Adjudications(entries []Entry) map[string]Adjudication {
	out := map[string]Adjudication{}
	for _, e := range entries {
		if e.Kind == KindAdjudication && e.Adjudication != nil {
			out[e.Adjudication.Adjudicates] = *e.Adjudication
		}
	}
	return out
}

// ChainHasSigners reports whether the chain registers signers at all: a
// signer entry, or a leading checkpoint that carries a set.
func ChainHasSigners(entries []Entry) bool {
	for i, e := range entries {
		if e.Kind == KindSigner {
			return true
		}
		if i == 0 && e.Kind == KindCheckpoint && e.Checkpoint != nil && len(e.Checkpoint.Signers) > 0 {
			return true
		}
	}
	return false
}

// Retracted returns the hashes of every entry a KindRetract entry in
// entries names.
func Retracted(entries []Entry) map[string]Entry {
	// A retraction that has ITSELF been retracted is not in force, so the
	// entry it named comes back — retract, undo, redo, and the record
	// alternates. Resolved in ONE REVERSE PASS: a retraction can only name
	// an EARLIER entry, so walking from the newest backwards means every
	// retraction that could void this one has already been decided.
	//
	// The first fix for this grew a monotone "void" set forward, which is
	// wrong because voiding is not monotone — once a retraction is voided,
	// everything it voided comes back — and it reported the original entry
	// as NOT retracted at odd depth >= 3. Caught by a second cold review of
	// this package, 2026-09-08, on the fix for the first one.
	targeted := map[string]bool{} // named by an in-force retraction seen later in the chain
	inForce := make([]Entry, 0, len(entries))
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.Kind != KindRetract {
			continue
		}
		if targeted[e.Hash] {
			continue // a later, in-force retraction retracted this one
		}
		inForce = append(inForce, e)
		targeted[e.Retracts] = true
	}
	out := map[string]Entry{}
	for _, e := range inForce {
		out[e.Retracts] = e
	}
	return out
}

// ScanEntries is the record as a reader should see it: the scan entries,
// in chain order, with retracted ones left out. Every reader of the record
// — the view, the prior, the verdict cache — goes through this, so a
// retraction takes effect everywhere at once.
func ScanEntries(entries []Entry) []Entry {
	var out []Entry
	for _, e := range LiveEntries(entries) {
		if e.IsScan() {
			out = append(out, e)
		}
	}
	return out
}

// LiveEntries is every entry of every kind that stands: retracted entries
// left out, and an adjudication of a retracted review left out with it.
// A retraction used to reach scan entries only, so a retracted review's
// rows and findings still loaded into the view and pushed to a warehouse
// (ed079ca08965#R3); the view and the push go through this.
func LiveEntries(entries []Entry) []Entry {
	retracted := Retracted(entries)
	var out []Entry
	for _, e := range entries {
		if _, gone := retracted[e.Hash]; gone {
			continue
		}
		if e.Kind == KindAdjudication && e.Adjudication != nil {
			if h, _, ok := CutRef(e.Adjudication.Adjudicates); ok {
				if _, gone := retracted[h]; gone {
					continue
				}
			}
		}
		out = append(out, e)
	}
	return out
}
