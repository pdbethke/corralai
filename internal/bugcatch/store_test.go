package bugcatch

import (
	"context"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/bc.duckdb")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestScorecardRecallPrecisionAndMootRuns(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ts := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	// Run A (needs-review): test-writer caught 1 of 2 gaps; authored a sound test.
	must(t, s.Record(ctx, []Observation{{
		TS: ts, RecordID: 10, Model: "claude-sonnet-5", Role: "test-writer", Source: "pool",
		Catches: 1, Opportunities: 2, SoundTests: 1, AuthoredTests: 1,
	}}))
	// Run B (certified, moot): 0 gaps → contributes soundness but NO recall opportunity.
	must(t, s.Record(ctx, []Observation{{
		TS: ts, RecordID: 11, Model: "claude-sonnet-5", Role: "test-writer", Source: "pool",
		Catches: 0, Opportunities: 0, SoundTests: 1, AuthoredTests: 1,
	}}))
	cells, err := s.Scorecard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var tw *Cell
	for i := range cells {
		if cells[i].Role == "test-writer" {
			tw = &cells[i]
		}
	}
	if tw == nil {
		t.Fatal("no test-writer cell")
	}
	if tw.Catches != 1 || tw.Opportunities != 2 {
		t.Fatalf("catches/opps = %d/%d, want 1/2", tw.Catches, tw.Opportunities)
	}
	if tw.Recall == nil || *tw.Recall != 0.5 {
		t.Fatalf("recall = %v, want 0.5 (moot run adds no opportunity)", tw.Recall)
	}
	if tw.Precision == nil || *tw.Precision != 1.0 {
		t.Fatalf("precision = %v, want 1.0 over 2 sound/2 authored", tw.Precision)
	}
	if tw.Runs != 2 {
		t.Fatalf("runs = %d, want 2", tw.Runs)
	}
}

func TestScorecardNilWhenNoDenominator(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	// A mutant-generator seat: no test-writer opportunities/authored → recall+precision nil.
	must(t, s.Record(ctx, []Observation{{
		TS: time.Unix(1, 0).UTC(), RecordID: 12, Model: "claude-sonnet-5", Role: "mutant-generator",
		Source: "pool", MutantsPlanted: 5, MutantsSurvived: 1,
	}}))
	cells, _ := s.Scorecard(ctx)
	if len(cells) != 1 || cells[0].Recall != nil || cells[0].Precision != nil {
		t.Fatalf("mutant-generator cell must have nil recall/precision: %+v", cells)
	}
	if cells[0].MutantsPlanted != 5 || cells[0].MutantsSurvived != 1 {
		t.Fatalf("adversary line = %d/%d, want 5/1", cells[0].MutantsPlanted, cells[0].MutantsSurvived)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
