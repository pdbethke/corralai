// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/modelcorr"
	"github.com/pdbethke/corralai/internal/mutantattempts"
)

// THE MISSING ADAPTER. advpool defines MutantAttemptSink and deliberately
// imports no store; without a composition root attaching one, d.MutantAttempts
// stayed nil on every path and a measured challenger wrote zero rows — the
// feature was inert end to end. This pins the wire from both directions: the
// driver field is set, and what the driver feeds it lands in the store in a
// shape modelcorr can pair.
func TestWireLocalMutantAttemptsPersistsBothSeats(t *testing.T) {
	path := filepath.Join(t.TempDir(), "attempts.duckdb")
	var d advpool.Driver
	closer, opened, rows := wireLocalMutantAttempts(&d, path, "acme/r", "c0ffee", nil)
	defer closer()
	if !opened || rows == nil {
		t.Fatal("the correlation store did not open")
	}
	if d.MutantAttempts == nil {
		t.Fatal("d.MutantAttempts is nil after wiring — this is exactly the dangling wire that made the feature inert")
	}

	var fed []advpool.MutantAttempt
	for i := 1; i <= 12; i++ {
		mid := "m" + string(rune('a'+i-1))
		primary, shadow := "survived", "survived"
		if i <= 2 {
			primary = "killed"
		}
		fed = append(fed,
			advpool.MutantAttempt{Path: "a.go", MutantID: mid, Model: "primary", Role: advpool.RoleTestWriter, Outcome: primary},
			advpool.MutantAttempt{Path: "a.go", MutantID: mid, Model: "challenger", Role: advpool.RoleTestWriterShadow, Shadow: true, Outcome: shadow},
		)
	}
	d.MutantAttempts.Record(42, "sha256:deadbeef", fed)

	if n := atomic.LoadInt64(rows); n != int64(len(fed)) {
		t.Fatalf("counter says %d rows written, want %d", n, len(fed))
	}

	st, err := mutantattempts.Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer func() { _ = st.Close() }()
	got, err := st.AttemptsForRecord(context.Background(), 42)
	if err != nil {
		t.Fatalf("AttemptsForRecord: %v", err)
	}
	if len(got) != len(fed) {
		t.Fatalf("read back %d rows, want %d", len(got), len(fed))
	}
	for _, a := range got {
		if a.RecordHead != "sha256:deadbeef" || a.Repo != "acme/r" || a.Commit != "c0ffee" {
			t.Fatalf("the sink did not stamp the run context the pure driver cannot carry: %+v", a)
		}
	}

	// And the rows are actually pairable — the only reason they are stored.
	pairs, err := modelcorr.FromAttempts(got)
	if err != nil {
		t.Fatalf("FromAttempts: %v", err)
	}
	if len(pairs) != 1 {
		t.Fatalf("got %d pairs from a stored head-to-head, want 1", len(pairs))
	}
	if !pairs[0].Sufficient {
		t.Errorf("the stored pair reported insufficient data over a union of %d survivors", pairs[0].UnionSurvivors)
	}
}
