// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/lang"
)

// The ledger entry's mutant rows carry their hunks: the always-on recorder
// hears every file's graded mutants from the driver's sink and stamps each
// row's `code` with the edit, matched by (path, id). Without it every ledger
// row was bare, so a later run's prior could not tell two runs' different
// edits at one place apart (a Cursor read of #286, confirmed). The custody
// rule is unchanged — `code` is in BlankUnpushedSource's set, so a --push
// without --push-source still withholds it.
func TestLedgerMutantRowsCarryTheirHunks(t *testing.T) {
	r := newMutantSetRecorder()
	sha := shaOf("package pkg\n\nfunc A() int { return 1 }\n")
	r.sink("pkg/a.go", []adequacy.Mutant{
		{ID: "s0/m1", ParentSHA256: sha, Span: lang.LineRange{Start: 3, End: 3}, Search: "return 1", Replace: "return 0"},
		{ID: "s0/m2", ParentSHA256: sha, Span: lang.LineRange{Start: 3, End: 3}, Search: "return 1", Replace: "return -1"},
	})
	rows := []auditpush.MutantRow{
		{Path: "pkg/a.go", MutantID: "s0/m2", Outcome: "survived"},
		{Path: "pkg/a.go", MutantID: "s0/m1", Outcome: "killed"},
		{Path: "pkg/b.go", MutantID: "s0/m1", Outcome: "killed"}, // a cache hit: no dev pass, no hunk
	}
	r.stampHunks(rows)
	for i, want := range []string{adequacy.EncodeHunk("return 1", "return -1"), adequacy.EncodeHunk("return 1", "return 0"), ""} {
		if rows[i].Code != want {
			t.Errorf("row %d code = %q, want %q", i, rows[i].Code, want)
		}
	}
	if s, rp, ok := adequacy.DecodeHunk(rows[0].Code); !ok || s != "return 1" || rp != "return -1" {
		t.Errorf("the hunk does not round-trip: %q %q %v", s, rp, ok)
	}
	b := auditpush.Bundle{Mutants: rows}
	auditpush.BlankUnpushedSource(&b)
	if b.Mutants[0].Code != "" {
		t.Error("a push without --push-source must withhold the hunk: it is the audited code")
	}
}
