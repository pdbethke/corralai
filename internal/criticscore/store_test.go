// SPDX-License-Identifier: Elastic-2.0

package criticscore_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/criticscore"
)

func openTmp(t *testing.T) *criticscore.Store {
	t.Helper()
	s, err := criticscore.Open(filepath.Join(t.TempDir(), "cs.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestRecordAndPrecision(t *testing.T) {
	s := openTmp(t)
	ctx := context.Background()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Record(ctx, []criticscore.Finding{
		{ID: "1:10", RecordID: 1, Model: "haiku", Scope: "whole-test", Adjudication: "refuted", Source: "auto"},
		{ID: "1:11", RecordID: 1, Model: "haiku", Scope: "dead-check", Adjudication: "unadjudicated", Source: "auto"},
		{ID: "2:12", RecordID: 2, Model: "gemini-pro", Scope: "dead-check", Adjudication: "confirmed", Source: "human"},
	}))
	// human-confirm the pending haiku one
	ok, err := s.Adjudicate(ctx, "1:11", "confirmed", "alice")
	must(err)
	if !ok {
		t.Fatal("adjudicate should report changed")
	}
	cells, err := s.Precision(ctx)
	must(err)
	byModel := map[string]criticscore.CriticCell{}
	for _, c := range cells {
		byModel[c.Model] = c
	}
	h := byModel["haiku"]
	if h.Confirmed != 1 || h.Refuted != 1 || h.Precision == nil || *h.Precision != 0.5 {
		t.Fatalf("haiku precision wrong: %+v", h)
	}
	// Record must NOT downgrade the human 'confirmed' back to auto/unadjudicated
	must(s.Record(ctx, []criticscore.Finding{{ID: "1:11", RecordID: 1, Model: "haiku", Scope: "dead-check", Adjudication: "unadjudicated", Source: "auto"}}))
	f, ok, err := s.Get(ctx, "1:11")
	must(err)
	if !ok || f.Adjudication != "confirmed" || f.Source != "human" {
		t.Fatalf("human adjudication was clobbered by Record: %+v", f)
	}
	pend, err := s.ListPending(ctx)
	must(err)
	if len(pend) != 0 {
		t.Fatalf("no pending expected, got %d", len(pend))
	}
}

// TestPrecisionNilWhenNoAdjudicated pins the documented reason Precision returns
// a *float64: a model with zero confirmed+refuted (only unadjudicated findings)
// gets a nil Precision, not a misleading 0.0.
func TestPrecisionNilWhenNoAdjudicated(t *testing.T) {
	s := openTmp(t)
	ctx := context.Background()
	if err := s.Record(ctx, []criticscore.Finding{
		{ID: "1:1", RecordID: 1, Model: "fresh", Scope: "whole-test", Adjudication: "unadjudicated", Source: "auto"},
	}); err != nil {
		t.Fatal(err)
	}
	cells, err := s.Precision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, c := range cells {
		if c.Model == "fresh" {
			found = true
			if c.Precision != nil {
				t.Fatalf("Precision must be nil with 0 adjudicated, got %v", *c.Precision)
			}
			if c.Unadjudicated != 1 || c.Confirmed != 0 || c.Refuted != 0 {
				t.Fatalf("counts wrong: %+v", c)
			}
		}
	}
	if !found {
		t.Fatal("model 'fresh' missing from Precision cells")
	}
}
