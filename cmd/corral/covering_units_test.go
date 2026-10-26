// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"database/sql"
	"io"
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// The value survives the boundary (AGENTS.md, "the bug shape that keeps
// recurring"): a unit count set on a Candidate reaches the job, the record
// row, the signed bundle's ledger.Row and the warehouse's covering_units
// column, for a graded file and an ungradable one alike, and never lands
// in CoveringTests on the way.
func TestCoveringUnitsSurvivesCandidateToLedgerRow(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "src", "a.ts"), "export function a(): number { return 1; }\n")
	three := 3
	jobs := emitTestJobs(t, []reposcan.Candidate{{Path: "src/a.ts", Lang: "typescript", CoveringTestPath: "test/a.test.ts", CoveringUnits: &three}}, root)
	if len(jobs) != 1 || jobs[0].CoveringUnits == nil || *jobs[0].CoveringUnits != 3 {
		t.Fatalf("job dropped CoveringUnits: %+v", jobs)
	}

	for _, gradable := range []bool{true, false} {
		res := reposcan.FileResult{Job: jobs[0], Gradable: gradable}
		if !gradable {
			res.Reason = reposcan.ReasonExecutorError
		}
		files := buildScanFileRows([]reposcan.FileResult{res}, nil, reposcan.CoverageMap{}, "", root, io.Discard)
		if len(files) != 1 || files[0].CoveringUnits == nil || *files[0].CoveringUnits != 3 {
			t.Fatalf("gradable=%v: record row dropped CoveringUnits: %+v", gradable, files)
		}
		b := buildBundle(scanstore.Scan{Owner: "o", Repo: "r", Commit: "c"}, 1, files, nil, nil, nil, auditpush.Link{}, false, "o/r", "c", "", bundleMeta{})
		if len(b.Files) != 1 || b.Files[0].CoveringUnits == nil || *b.Files[0].CoveringUnits != 3 {
			t.Fatalf("gradable=%v: ledger row dropped CoveringUnits: %+v", gradable, b.Files)
		}
		if b.Files[0].CoveringTests != nil {
			t.Errorf("gradable=%v: CoveringTests = %d, set from units", gradable, *b.Files[0].CoveringTests)
		}

		target := filepath.Join(t.TempDir(), "w.duckdb")
		if _, err := auditpush.PushBundle(target, b); err != nil {
			t.Fatal(err)
		}
		db, err := sql.Open("duckdb", target)
		if err != nil {
			t.Fatal(err)
		}
		var units, tests sql.NullInt64
		err = db.QueryRow(`SELECT covering_units, covering_tests FROM corral_audits WHERE path = 'src/a.ts'`).Scan(&units, &tests)
		db.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !units.Valid || units.Int64 != 3 || tests.Valid {
			t.Errorf("gradable=%v: covering_units = %v (valid %v), covering_tests valid %v; want 3 and NULL", gradable, units.Int64, units.Valid, tests.Valid)
		}
	}
}
