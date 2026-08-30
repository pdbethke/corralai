// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// twoFileLedgerRows builds the ledger rows for a scan that audited one file
// and refused another — the shape the warehouse used to lose. repoDir holds
// the audited file so ParentSHA256 is a real hash of real bytes.
func twoFileLedgerRows(t *testing.T) (string, []scanstore.File) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.go"), []byte("package p\n\nfunc F() int { return 1 }\n"), 0o600); err != nil {
		t.Fatalf("write the audited file: %v", err)
	}
	results := []reposcan.FileResult{
		{
			Job:      reposcan.Job{Path: "a.go", Lang: "go"},
			Gradable: true,
			Verdict: advpool.Verdict{
				DevKillRate: 0.5, Survivors: 2, ProvenMissed: 1,
				MutantsTotal: 8, MutantsInvalid: 1,
				RegionsTotal: 2, RegionsProbed: 2,
				Status: "needs-review", AuthoredTest: "func TestX(t *testing.T) {}",
				ProvenMutantIDs: []string{"m2"},
				TestSelection: advpool.TestSelection{
					Method: "coverage-lines", Selected: 4, Of: 41, PerMutant: true,
					TestsPerMutant: &advpool.TestsPerMutantSpread{Min: 3, Median: 4, Max: 9},
				},
				Concurrency: advpool.Concurrency{Trees: 6, Note: "probe passed"},
			},
		},
		{
			Job:      reposcan.Job{Path: "b.go", Lang: "go"},
			Gradable: false,
			Reason:   reposcan.ReasonBaselineFailed,
			Detail:   "the suite did not pass on unmutated code",
		},
	}
	return dir, buildScanFileRows(results, nil, reposcan.CoverageMap{}, "", dir, io.Discard)
}

// TestBundleIsTheLedgerRowForRow is the whole point of having ONE mapping:
// the pushed rows are the ledger's rows, not a second derivation of the
// report. Two properties, and the first is a regression the report path
// could not satisfy at all —
//
//   - the file corral REFUSED is in the bundle, with its disposition and its
//     reason. The old report-derived path pushed only r.Weakest, so every
//     rejected file was simply absent from the warehouse, and "is this repo
//     covered?" could not be asked of it;
//   - no field is dropped on the way. Walked by REFLECTION over the field
//     names rather than asserted one at a time, because a hand-written list
//     of assertions is exactly what stops covering the struct the day
//     somebody adds a field to it.
func TestBundleIsTheLedgerRowForRow(t *testing.T) {
	_, files := twoFileLedgerRows(t)
	if len(files) != 2 {
		t.Fatalf("expected two ledger rows, got %d", len(files))
	}

	scan := scanstore.Scan{Repo: "o/r", Commit: "deadbeef", Audited: 1, Candidates: 2}
	b := buildBundle(scan, 11, files, nil, nil, nil, auditpush.Link{}, false,
		"o/r", "deadbeef", "", bundleMeta{ModelsByRole: `{"writer":"m"}`, Passed: false})

	if len(b.Files) != 2 {
		t.Fatalf("bundle carries %d file row(s), want both dispositions", len(b.Files))
	}
	var rejected *auditpush.Row
	for i := range b.Files {
		if b.Files[i].Path == "b.go" {
			rejected = &b.Files[i]
		}
	}
	if rejected == nil {
		t.Fatal("the REJECTED file is missing from the bundle — the warehouse would show a repo as clean where corral never graded it")
	}
	if rejected.Disposition != "rejected" || rejected.Reason != reposcan.ReasonBaselineFailed {
		t.Errorf("rejected row = %+v, want disposition/reason to survive the mapping", *rejected)
	}
	if rejected.KillRate != nil {
		t.Errorf("a rejected file must carry no kill rate, got %v", *rejected.KillRate)
	}

	// deliberatelyUnmapped names every scanstore.File field that has an
	// identically-named auditpush.Row field this test does NOT compare, with
	// the reason. Anything not listed here must match exactly.
	deliberatelyUnmapped := map[string]string{
		// The ledger column predates the NULL-not-zero rule and is a plain
		// int64; the warehouse column is nullable. Converted on purpose by
		// nilIfZeroMillis — see buildAuditRows.
		"SuiteBaselineMillis": "int64 in the ledger, *int64 (NULL when unmeasured) in the warehouse",
		// The ledger keeps three nullable ints; the warehouse keeps one
		// pointer-to-struct, so an unmeasured spread is absent rather than
		// three zeros.
		"TestsPerMutantMin":    "folded into Row.TestsPerMutant",
		"TestsPerMutantMedian": "folded into Row.TestsPerMutant",
		"TestsPerMutantMax":    "folded into Row.TestsPerMutant",
		// The ledger's canonical per-file roster; the warehouse column holds
		// the scan-wide roster JSON, which is a different (and older) claim.
		"ModelsByRole": "Row.ModelsByRole is the scan-wide roster JSON, not the per-file KV",
		// Renamed at the boundary, and compared explicitly below.
		"MutantsTotal": "lands in Row.MutantsPlanted",
		// Local-only bookkeeping with no warehouse column at all.
		"Gradable":    "implied by Disposition",
		"ComputedAt":  "no warehouse column",
		"MutantsFrom": "no warehouse column",
	}

	for _, f := range files {
		var row auditpush.Row
		for _, r := range b.Files {
			if r.Path == f.Path {
				row = r
			}
		}
		lv, rv := reflect.ValueOf(f), reflect.ValueOf(row)
		lt := lv.Type()
		for i := 0; i < lt.NumField(); i++ {
			name := lt.Field(i).Name
			if _, skip := deliberatelyUnmapped[name]; skip {
				continue
			}
			rf := rv.FieldByName(name)
			if !rf.IsValid() {
				continue // no warehouse column of that name
			}
			if rf.Type() != lv.Field(i).Type() {
				t.Errorf("%s: field %s is %s in the ledger and %s in the warehouse, and is not on the deliberately-unmapped list",
					f.Path, name, lv.Field(i).Type(), rf.Type())
				continue
			}
			if !reflect.DeepEqual(lv.Field(i).Interface(), rf.Interface()) {
				t.Errorf("%s: field %s was dropped or changed on the way to the warehouse: ledger %v, row %v",
					f.Path, name, lv.Field(i).Interface(), rf.Interface())
			}
		}
		if row.MutantsPlanted != f.MutantsTotal {
			t.Errorf("%s: mutants_planted = %d, want the ledger's mutants_total %d", f.Path, row.MutantsPlanted, f.MutantsTotal)
		}
	}

	// The audited row's validity key is a real hash of the real bytes.
	for _, r := range b.Files {
		if r.Path == "a.go" && len(r.ParentSHA256) != 64 {
			t.Errorf("the audited row's parent_sha256 = %q, want a sha256 of the audited bytes", r.ParentSHA256)
		}
	}
}

// TestWarehouseRowsSHA256IsDeterministic is the property the signed
// statement rests on: warehouseRowsSha256 is only verifiable if a third
// party rebuilding the rows from the same scan gets the same hash. Nothing
// in the mapping may depend on map iteration order, a clock or a pointer
// address.
func TestWarehouseRowsSHA256IsDeterministic(t *testing.T) {
	_, files := twoFileLedgerRows(t)
	scan := scanstore.Scan{Repo: "o/r", Commit: "deadbeef", Audited: 1, Candidates: 2}
	meta := bundleMeta{ModelsByRole: `{"writer":"m"}`, Passed: true}

	first := buildBundle(scan, 11, files, nil, nil, nil, auditpush.Link{}, false, "o/r", "deadbeef", "", meta)
	second := buildBundle(scan, 11, files, nil, nil, nil, auditpush.Link{}, false, "o/r", "deadbeef", "", meta)

	a, err := warehouseRowsSHA256(first)
	if err != nil {
		t.Fatalf("hash the first bundle: %v", err)
	}
	b, err := warehouseRowsSHA256(second)
	if err != nil {
		t.Fatalf("hash the second bundle: %v", err)
	}
	if a != b {
		t.Errorf("warehouseRowsSha256 is not deterministic: %s != %s", a, b)
	}

	// And the statement's hash must not depend on the statement's own hash:
	// stamping a Link changes nothing.
	withLink := first
	withLink.Link = auditpush.Link{ScanID: 11, StatementSHA256: "deadbeef"}
	c, err := warehouseRowsSHA256(withLink)
	if err != nil {
		t.Fatalf("hash the linked bundle: %v", err)
	}
	if c != a {
		t.Error("warehouseRowsSha256 changed when a statement hash was attached — the statement's hash would then depend on itself")
	}
}
