// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// The record half of kill confirmation (spec 2026-11-02 §6): a confirmed
// kill says how it was confirmed, and a survivor whose first grading run
// failed and whose repeat passed names the test that first run blamed. A
// confirmation nobody records is a measurement dropped on the floor.

// confirmationResult is one file with a kill confirmed alone, a kill
// confirmed by repeat, and a survivor whose first-run failure did not repeat.
func confirmationResult() reposcan.FileResult {
	return reposcan.FileResult{
		Job:      reposcan.Job{Path: "pkg/a.py", Lang: "python"},
		Gradable: true,
		Verdict: advpool.Verdict{
			DevKilledMutants: []advpool.MutantRef{
				{ID: "k1", ParentSHA256: "p", KilledBy: "a.py::test_x", KillConfirmation: adequacy.KillConfirmedAlone},
				{ID: "k2", ParentSHA256: "p", KillConfirmation: adequacy.KillConfirmedRepeated},
			},
			DevSurvivedMutants: []advpool.MutantRef{
				{ID: "s1", ParentSHA256: "p", UnrepeatedKilledBy: "a.py::test_x"},
			},
		},
	}
}

// checkConfirmationRows asserts the three rows' confirmation fields, at
// whatever grain they were read back.
func checkConfirmationRows(t *testing.T, where string, rows []scanstore.Mutant) {
	t.Helper()
	byID := map[string]scanstore.Mutant{}
	for _, r := range rows {
		byID[r.MutantID] = r
	}
	if len(byID) != 3 {
		t.Fatalf("%s: %d mutant row(s), want 3: %+v", where, len(byID), rows)
	}
	want := map[string][2]string{
		"k1": {adequacy.KillConfirmedAlone, ""},
		"k2": {adequacy.KillConfirmedRepeated, ""},
		"s1": {"", "a.py::test_x"},
	}
	for id, w := range want {
		m := byID[id]
		if m.KillConfirmation != w[0] || m.UnrepeatedKilledBy != w[1] {
			t.Errorf("%s: row %s KillConfirmation %q UnrepeatedKilledBy %q, want %q / %q",
				where, id, m.KillConfirmation, m.UnrepeatedKilledBy, w[0], w[1])
		}
	}
	if s := byID["s1"]; s.Outcome != "survived" || s.KilledBy != "" {
		t.Errorf("%s: the refuted kill must be an ordinary survivor with no KilledBy, got %+v", where, s)
	}
}

// TestKillConfirmationRoundTrips follows the two fields from the verdict to
// the ledger entry and back through every converter on the way — the
// scan_mutants row, the bundle row, the entry in a ledger directory, the
// `corral scans` reader, and the warehouse view — as
// TestToolMutantRowsNameTheirSource does for Source. A converter that forgets
// either field fails here.
func TestKillConfirmationRoundTrips(t *testing.T) {
	rows := buildScanMutantRows(1, []reposcan.FileResult{confirmationResult()})
	checkConfirmationRows(t, "scan rows", rows)

	b := buildBundle(scanstore.Scan{Repo: "o/r", Commit: "deadbeef"}, 1, nil, rows, nil, nil,
		auditpush.Link{}, true, "o/r", "deadbeef", "", bundleMeta{})
	var bRows []scanstore.Mutant
	for _, m := range b.Mutants {
		bRows = append(bRows, scanstore.Mutant{MutantID: m.MutantID, Outcome: m.Outcome, KilledBy: m.KilledBy,
			KillConfirmation: m.KillConfirmation, UnrepeatedKilledBy: m.UnrepeatedKilledBy})
	}
	checkConfirmationRows(t, "bundle rows", bRows)

	dir := filepath.Join(t.TempDir(), "ledger")
	if _, err := auditpush.PushBundle(dir+"/", b); err != nil {
		t.Fatalf("writing the entry: %v", err)
	}
	entries, err := auditpush.ReadLedgerDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("ReadLedgerDir: %d entries, %v", len(entries), err)
	}
	l := &ledgerScans{entries: entries}
	read, err := l.MutantsForScan(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	checkConfirmationRows(t, "ledger entry", read)

	db, err := auditpush.LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM corral_mutants WHERE
		(mutant_id = 'k1' AND kill_confirmation = 'alone' AND unrepeated_killed_by IS NULL) OR
		(mutant_id = 'k2' AND kill_confirmation = 'repeated') OR
		(mutant_id = 's1' AND kill_confirmation IS NULL AND unrepeated_killed_by = 'a.py::test_x')`).Scan(&n); err != nil {
		t.Fatalf("querying the view by confirmation: %v", err)
	}
	if n != 3 {
		t.Errorf("the view holds %d row(s) with their confirmation, want 3", n)
	}
	wb, err := auditpush.ReadBundleForScan(db, auditpush.ScanRow{Repo: "o/r", ScanID: 1, ScanUID: entries[0].ScanUID})
	if err != nil {
		t.Fatal(err)
	}
	var whRows []scanstore.Mutant
	for _, m := range wb.Mutants {
		whRows = append(whRows, scanstore.Mutant{MutantID: m.MutantID, Outcome: m.Outcome, KilledBy: m.KilledBy,
			KillConfirmation: m.KillConfirmation, UnrepeatedKilledBy: m.UnrepeatedKilledBy})
	}
	checkConfirmationRows(t, "warehouse read-back", whRows)
}
