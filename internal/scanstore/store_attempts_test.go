// SPDX-License-Identifier: Elastic-2.0

package scanstore

import (
	"context"
	"testing"
)

func TestRecordMutantAttemptsRoundTrips(t *testing.T) {
	s := openTempMutantStore(t)
	ctx := context.Background()
	id, err := s.Record(ctx, Scan{Owner: "acme", Repo: "r"}, []File{{
		Path: "a.go", Disposition: "audited", Gradable: true,
	}})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	in := []MutantAttempt{
		{ScanID: id, Path: "a.go", MutantID: "m1", Model: "qwen3.5:9b", Role: "test-writer", Shadow: false, Outcome: "killed"},
		{ScanID: id, Path: "a.go", MutantID: "m1", Model: "gemma4", Role: "test-writer-shadow", Shadow: true, Outcome: "survived"},
	}
	if err := s.RecordMutantAttempts(ctx, in); err != nil {
		t.Fatalf("RecordMutantAttempts: %v", err)
	}
	got, err := s.AttemptsForScan(ctx, id)
	if err != nil {
		t.Fatalf("AttemptsForScan: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d attempts, want 2 — both seats' vectors must be readable", len(got))
	}
	// Same mutant, two models, two outcomes: that is the whole point.
	if got[0].MutantID != got[1].MutantID {
		t.Errorf("attempts are for different mutants (%q, %q) — a pair must share the mutant id", got[0].MutantID, got[1].MutantID)
	}
	if got[0].Model == got[1].Model {
		t.Errorf("both attempts recorded model %q — a pair must be two distinct seats", got[0].Model)
	}
}

func TestRecordMutantAttemptsRejectsAnUnknownOutcome(t *testing.T) {
	s := openTempMutantStore(t)
	err := s.RecordMutantAttempts(context.Background(), []MutantAttempt{
		{ScanID: 1, Path: "a.go", MutantID: "m1", Model: "x", Role: "test-writer", Outcome: "probably-fine"},
	})
	if err == nil {
		t.Fatal("an unknown outcome was accepted — this table is queried by exact string")
	}
}

// GLOBAL CONSTRAINT: the gating table is untouched. Writing attempts must not
// add, remove or alter a single scan_mutants row.
func TestRecordMutantAttemptsLeavesScanMutantsUntouched(t *testing.T) {
	s := openTempMutantStore(t)
	ctx := context.Background()
	id, err := s.Record(ctx, Scan{Owner: "acme", Repo: "r"}, []File{{
		Path: "a.go", Disposition: "audited", Gradable: true,
	}})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := s.RecordMutants(ctx, []Mutant{
		{ScanID: id, Path: "a.go", MutantID: "m1", Outcome: "killed", ParentSHA256: "abc"},
	}); err != nil {
		t.Fatalf("RecordMutants: %v", err)
	}
	before, err := s.MutantsForScan(ctx, id)
	if err != nil {
		t.Fatalf("MutantsForScan before: %v", err)
	}
	if err := s.RecordMutantAttempts(ctx, []MutantAttempt{
		{ScanID: id, Path: "a.go", MutantID: "m1", Model: "a", Role: "test-writer", Outcome: "killed"},
		{ScanID: id, Path: "a.go", MutantID: "m1", Model: "b", Role: "test-writer-shadow", Shadow: true, Outcome: "survived"},
	}); err != nil {
		t.Fatalf("RecordMutantAttempts: %v", err)
	}
	after, err := s.MutantsForScan(ctx, id)
	if err != nil {
		t.Fatalf("MutantsForScan after: %v", err)
	}
	if len(before) != len(after) {
		t.Fatalf("scan_mutants row count changed %d -> %d — a measurement table leaked into the gating table", len(before), len(after))
	}
	for i := range before {
		if before[i] != after[i] {
			t.Fatalf("scan_mutants row %d changed: %+v -> %+v", i, before[i], after[i])
		}
	}
}
