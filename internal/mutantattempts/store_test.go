// SPDX-License-Identifier: Elastic-2.0

package mutantattempts

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openTemp(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "attempts.duckdb"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestRecordRoundTrips(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	ts := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	in := []Attempt{
		{TS: ts, RecordID: 7, RecordHead: "sha256:abc", MissionID: 1, Repo: "acme/r", Commit: "c0ffee",
			Path: "a.go", MutantID: "m1", Model: "qwen3.5:9b", Role: "test-writer", Shadow: false, Outcome: "killed"},
		{TS: ts, RecordID: 7, RecordHead: "sha256:abc", MissionID: 1, Repo: "acme/r", Commit: "c0ffee",
			Path: "a.go", MutantID: "m1", Model: "gemma4", Role: "test-writer-shadow", Shadow: true, Outcome: "survived"},
	}
	if err := s.Record(ctx, in); err != nil {
		t.Fatalf("Record: %v", err)
	}
	got, err := s.AttemptsForRecord(ctx, 7)
	if err != nil {
		t.Fatalf("AttemptsForRecord: %v", err)
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
	var shadowSeen bool
	for _, a := range got {
		if a.Shadow {
			shadowSeen = true
			if a.Role != "test-writer-shadow" {
				t.Errorf("shadow row has role %q", a.Role)
			}
		}
		if a.RecordHead != "sha256:abc" || a.Repo != "acme/r" || a.Commit != "c0ffee" {
			t.Errorf("run context did not round-trip: %+v", a)
		}
	}
	if !shadowSeen {
		t.Error("no challenger row round-tripped — the pair is unjoinable")
	}
}

// record_id is the RUN discriminator, and that is why it is the key: two
// audits of the SAME path must stay separate, or the correlation read at
// report time pools across runs with different mutant sets.
func TestAttemptsForRecordSeparatesRuns(t *testing.T) {
	s := openTemp(t)
	ctx := context.Background()
	rows := []Attempt{
		{RecordID: 1, Path: "a.go", MutantID: "m1", Model: "A", Role: "test-writer", Outcome: "killed"},
		{RecordID: 1, Path: "a.go", MutantID: "m1", Model: "B", Role: "test-writer-shadow", Shadow: true, Outcome: "survived"},
		{RecordID: 2, Path: "a.go", MutantID: "m1", Model: "A", Role: "test-writer", Outcome: "survived"},
		{RecordID: 2, Path: "a.go", MutantID: "m1", Model: "B", Role: "test-writer-shadow", Shadow: true, Outcome: "survived"},
	}
	if err := s.Record(ctx, rows); err != nil {
		t.Fatalf("Record: %v", err)
	}
	for _, rec := range []int64{1, 2} {
		got, err := s.AttemptsForRecord(ctx, rec)
		if err != nil {
			t.Fatalf("AttemptsForRecord(%d): %v", rec, err)
		}
		if len(got) != 2 {
			t.Fatalf("record %d returned %d rows, want 2 — one run's rows leaked into another's", rec, len(got))
		}
	}
	all, err := s.Attempts(ctx)
	if err != nil {
		t.Fatalf("Attempts: %v", err)
	}
	if len(all) != 4 {
		t.Fatalf("Attempts returned %d rows, want 4 — a same-path re-audit must APPEND, never overwrite", len(all))
	}
}

func TestRecordRejectsAnUnknownOutcome(t *testing.T) {
	err := openTemp(t).Record(context.Background(), []Attempt{
		{RecordID: 1, Path: "a.go", MutantID: "m1", Model: "x", Role: "test-writer", Outcome: "probably-fine"},
	})
	if err == nil {
		t.Fatal("an unknown outcome was accepted — this table is queried by exact string")
	}
}

func TestRecordEmptyIsNotAnError(t *testing.T) {
	if err := openTemp(t).Record(context.Background(), nil); err != nil {
		t.Fatalf("empty Record: %v", err)
	}
}
