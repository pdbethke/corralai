// SPDX-License-Identifier: Elastic-2.0

package scanstore

import (
	"context"
	"path/filepath"
	"testing"
)

func openTempMutantStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "scans.duckdb"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestRecordMutantsRoundTrips(t *testing.T) {
	s := openTempMutantStore(t)
	ctx := context.Background()
	id, err := s.Record(ctx, Scan{Owner: "acme", Repo: "r"}, []File{{
		Path: "a.go", Disposition: "audited", Gradable: true,
	}})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	in := []Mutant{
		{ScanID: id, Path: "a.go", MutantID: "m1", Outcome: "killed", ParentSHA256: "abc", Proven: false},
		{ScanID: id, Path: "a.go", MutantID: "m2", Outcome: "survived", ParentSHA256: "abc", Proven: true},
	}
	if err := s.RecordMutants(ctx, in); err != nil {
		t.Fatalf("RecordMutants: %v", err)
	}
	got, err := s.MutantsForScan(ctx, id)
	if err != nil {
		t.Fatalf("MutantsForScan: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d mutants, want 2", len(got))
	}
	var proven int
	for _, m := range got {
		if m.Proven {
			proven++
		}
		if m.ParentSHA256 != "abc" {
			t.Fatalf("ParentSHA256 did not round-trip: %+v", m)
		}
	}
	if proven != 1 {
		t.Fatalf("proven count = %d, want 1", proven)
	}
}

// A survivor is not the same claim as a PROVEN survivor: one is disclosed and
// unadjudicated, the other was killed by an authored test. A table that cannot
// tell them apart cannot grade anything.
func TestRecordMutantsRejectsAnUnknownOutcome(t *testing.T) {
	s := openTempMutantStore(t)
	err := s.RecordMutants(context.Background(), []Mutant{
		{ScanID: 1, Path: "a.go", MutantID: "m1", Outcome: "probably-fine"},
	})
	if err == nil {
		t.Fatal("an unknown outcome was accepted — this table is queried by exact string")
	}
}

func TestRecordMutantsEmptyIsNotAnError(t *testing.T) {
	if err := openTempMutantStore(t).RecordMutants(context.Background(), nil); err != nil {
		t.Fatalf("empty RecordMutants: %v", err)
	}
}
