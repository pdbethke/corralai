// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
// confirmed by repeat, a timeout kill confirmed by its compliant re-probe,
// a survivor whose first-run failure did not repeat, and one such survivor
// whose first run named no test — recorded by its KillConfirmation alone.
func confirmationResult() reposcan.FileResult {
	return reposcan.FileResult{
		Job:      reposcan.Job{Path: "pkg/a.py", Lang: "python"},
		Gradable: true,
		Verdict: advpool.Verdict{
			DevKilledMutants: []advpool.MutantRef{
				{ID: "k1", ParentSHA256: "p", KilledBy: "a.py::test_x", KillConfirmation: adequacy.KillConfirmedAlone},
				{ID: "k2", ParentSHA256: "p", KillConfirmation: adequacy.KillConfirmedRepeated},
				{ID: "k3", ParentSHA256: "p", KillConfirmation: adequacy.KillConfirmedReprobed},
			},
			DevSurvivedMutants: []advpool.MutantRef{
				{ID: "s1", ParentSHA256: "p", UnrepeatedKilledBy: "a.py::test_x", KillConfirmation: adequacy.KillNotRepeated},
				{ID: "s2", ParentSHA256: "p", KillConfirmation: adequacy.KillNotRepeated},
			},
		},
	}
}

// checkConfirmationRows asserts the five rows' confirmation fields, at
// whatever grain they were read back.
func checkConfirmationRows(t *testing.T, where string, rows []scanstore.Mutant) {
	t.Helper()
	byID := map[string]scanstore.Mutant{}
	for _, r := range rows {
		byID[r.MutantID] = r
	}
	if len(byID) != 5 {
		t.Fatalf("%s: %d mutant row(s), want 5: %+v", where, len(byID), rows)
	}
	want := map[string][2]string{
		"k1": {adequacy.KillConfirmedAlone, ""},
		"k2": {adequacy.KillConfirmedRepeated, ""},
		"k3": {adequacy.KillConfirmedReprobed, ""},
		"s1": {adequacy.KillNotRepeated, "a.py::test_x"},
		"s2": {adequacy.KillNotRepeated, ""},
	}
	for id, w := range want {
		m := byID[id]
		if m.KillConfirmation != w[0] || m.UnrepeatedKilledBy != w[1] {
			t.Errorf("%s: row %s KillConfirmation %q UnrepeatedKilledBy %q, want %q / %q",
				where, id, m.KillConfirmation, m.UnrepeatedKilledBy, w[0], w[1])
		}
	}
	for _, id := range []string{"s1", "s2"} {
		if s := byID[id]; s.Outcome != "survived" || s.KilledBy != "" {
			t.Errorf("%s: the refuted kill %s must be an ordinary survivor with no KilledBy, got %+v", where, id, s)
		}
	}
	if k := byID["k3"]; k.Outcome != "killed" {
		t.Errorf("%s: the reprobed timeout kill must be killed, got %+v", where, k)
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
		(mutant_id = 'k3' AND kill_confirmation = 'reprobed') OR
		(mutant_id = 's1' AND kill_confirmation = 'not-repeated' AND unrepeated_killed_by = 'a.py::test_x') OR
		(mutant_id = 's2' AND kill_confirmation = 'not-repeated' AND unrepeated_killed_by IS NULL)`).Scan(&n); err != nil {
		t.Fatalf("querying the view by confirmation: %v", err)
	}
	if n != 5 {
		t.Errorf("the view holds %d row(s) with their confirmation, want 5", n)
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

// TestRepoFileBlockPrintsHowKillsWereConfirmed: `certify --repo` is the path
// real repositories run, and its per-file block did not print the
// confirmation line `certify --local` prints, so REA t15's 28 confirmed kills
// showed nowhere on screen. A reused (cached) verdict prints nothing, like the
// timing line beside it: those confirmations are the earning run's.
func TestRepoFileBlockPrintsHowKillsWereConfirmed(t *testing.T) {
	f := reposcan.WeakFile{
		Path: "a.ts", KillRate: 0.5, Survivors: 1,
		DevKilledMutants: []advpool.MutantRef{
			{ID: "m1", KillConfirmation: adequacy.KillConfirmedAlone},
			{ID: "m2", KillConfirmation: adequacy.KillConfirmedRepeated},
		},
		DevSurvivedMutants: []advpool.MutantRef{{ID: "m3", KillConfirmation: adequacy.KillNotRepeated}},
		ConfirmDuration:    90 * time.Second,
	}
	var b strings.Builder
	printWeakFile(&b, f)
	want := "   kills confirmed: 1 alone, 1 by repeating the run, 0 by timeout; 1 first-run failures did not repeat (now survivors) (confirmation took 1m30s)\n"
	if !strings.Contains(b.String(), want) {
		t.Errorf("--repo file block lacks the confirmation line %q:\n%s", want, b.String())
	}
	f.CacheHit = true
	b.Reset()
	printWeakFile(&b, f)
	if strings.Contains(b.String(), "kills confirmed") {
		t.Errorf("a reused verdict printed the earning run's confirmations:\n%s", b.String())
	}
}

// TestRepoFileBlockShowsProofsATimedOutRunKept: a timed-out fan-out now keeps
// the proofs its graded seats earned (PoolScored), so the file line must show
// the count rather than hide it behind the TIMED OUT marker.
func TestRepoFileBlockShowsProofsATimedOutRunKept(t *testing.T) {
	var b strings.Builder
	printWeakFile(&b, reposcan.WeakFile{Path: "a.ts", KillRate: 0.4, Survivors: 44, ProvenMissed: 30, TimedOut: true, PoolScored: true, WriterSeatsUngraded: 5})
	if !strings.Contains(b.String(), "(44 survivor(s), 30 proven missed)") {
		t.Errorf("timed-out file with kept proofs hid its count:\n%s", b.String())
	}
	b.Reset()
	printWeakFile(&b, reposcan.WeakFile{Path: "a.ts", KillRate: 0.4, Survivors: 44, TimedOut: true})
	if strings.Contains(b.String(), "proven missed") {
		t.Errorf("timed-out file with no pool score printed a proven count:\n%s", b.String())
	}
}
