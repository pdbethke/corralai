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
