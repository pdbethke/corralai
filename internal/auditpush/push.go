// SPDX-License-Identifier: Elastic-2.0

// Package auditpush appends a scan's verdict to a DuckDB the OPERATOR owns —
// a local file, or their MotherDuck database.
//
// It is deliberately not a service. corral has no hosted tier and collects no
// telemetry: the key is theirs, the runner is theirs, and the warehouse is
// theirs too. That is also why the target is any DuckDB rather than MotherDuck
// specifically — `md:<db>` is one destination, a path on disk is another, and
// neither is a lock-in.
//
// What it exists for is the question a single pull request cannot answer.
// One kill rate is a sample; we have watched the same unchanged diff score 0.85
// and then 0.90. Forty of them are a distribution, and "this file has drifted
// from 0.9 to 0.6 over two months" is a claim no individual run can support.
//
// Two rules the schema enforces rather than documents:
//
//   - APPEND ONLY. A verified receipt that can be UPDATEd is not a receipt.
//     Rows are inserted, never modified, and — whenever --attest ran too —
//     each carries the sha256 of the signed statement it came from, so a row
//     traces back to something a third party can verify. Without --attest
//     there is no statement to point to, and a row's statement_sha256 is
//     honestly empty rather than fabricated. scan_id was the retired local
//     DuckDB record's row id and is 0 on every current row; the identity
//     that joins the grains is scan_uid.
//   - THE QUALIFIERS TRAVEL WITH THE NUMBERS. proven_missed = 0 means "nothing
//     was proven" rather than "the suite is clean" whenever the writer failed
//     or its test never graded. Aggregation is exactly where that distinction
//     gets dropped and a zero silently becomes good news, so the columns sit
//     beside the number and every documented query carries them.
package auditpush

import (
	"fmt"
	"strings"

	"github.com/pdbethke/corralai/internal/ledger"
)

// Row, Link and TestsPerMutantSpread live in internal/ledger (see Bundle).
type (
	Row                  = ledger.Row
	Link                 = ledger.Link
	TestsPerMutantSpread = ledger.TestsPerMutantSpread
)

// Push appends file rows to target, creating the tables if they are not
// there.
//
// DEPRECATED, and kept because it is the shape every pre-bundle caller was
// written against: it pushes ONE of the five grains. New callers build a
// Bundle and call PushBundle, which is what this now delegates to (with an
// empty scan row and no mutants, calls or events).
//
// target is a DuckDB path or `md:<db>`. For MotherDuck the caller must have set
// motherduck_token in the environment — the same contract fleet sync uses, and
// the reason this takes no credential of its own: corral never holds one.
//
// link is variadic so Push(target, rows) keeps working for every caller that
// has no statement to link rows to — the test-only/legacy path, and any
// caller pushing rows without --attest. MORE THAN ONE link is an error, not
// a silent "first one wins": the dropped link is precisely the traceability
// a row would then claim and not have.
func Push(target string, rows []Row, link ...Link) (int, error) {
	if len(link) > 1 {
		return 0, fmt.Errorf("auditpush: Push takes at most one Link, got %d", len(link))
	}
	if len(rows) == 0 {
		if strings.TrimSpace(target) == "" {
			return 0, fmt.Errorf("auditpush: no target")
		}
		return 0, nil
	}
	var lk Link
	if len(link) == 1 {
		lk = link[0]
	}
	c, err := PushBundle(target, Bundle{Files: rows, Link: lk})
	return c.Files, err
}
