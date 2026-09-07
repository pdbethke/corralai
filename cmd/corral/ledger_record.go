// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"github.com/pdbethke/corralai/internal/auditpush"
)

// ledgerRecord is the record as a READER should see it, read once: every
// entry in chain order (All), the entries that stand (Live — retracted
// ones and the adjudications of retracted reviews left out, the rule
// auditpush.LiveEntries keeps for the view and the push), and the
// retractions by the hash they retract.
//
// One reader for every human-facing door — `corral ui`, `review show`,
// `review plan`, `brief` — because a retraction used to reach the view and
// the push and not the doors a person actually looks through: the UI
// rendered a retracted review as live, `show` printed it in full unmarked,
// and `plan` counted it as coverage (review 8377ae6320cc, R3 and R5, a
// Claude Code reviewer, Codex verifying). A door that reads the record
// goes through this, or it is the next one to be found.
type ledgerRecord struct {
	All       []auditpush.LedgerEntry
	Live      []auditpush.LedgerEntry
	Retracted map[string]auditpush.LedgerEntry
}

// readLedgerRecord reads dir once and splits it as above.
func readLedgerRecord(dir string) (ledgerRecord, error) {
	all, err := auditpush.ReadLedgerDir(dir)
	if err != nil {
		return ledgerRecord{}, err
	}
	return ledgerRecord{All: all, Live: auditpush.LiveEntries(all), Retracted: auditpush.Retracted(all)}, nil
}

// retractionOf is the retraction entry that withdrew e, if any.
func (r ledgerRecord) retractionOf(e auditpush.LedgerEntry) (auditpush.LedgerEntry, bool) {
	ret, ok := r.Retracted[e.Hash]
	return ret, ok
}

// liveAdjudications are the adjudications that stand, by "<hash>#<id>".
func (r ledgerRecord) liveAdjudications() map[string]auditpush.Adjudication {
	return auditpush.Adjudications(r.Live)
}
