// SPDX-License-Identifier: Elastic-2.0

package scanstore

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func openTempVerdictStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "scans.duckdb"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestRecordRoundTripsTheVerdictColumns(t *testing.T) {
	s := openTempVerdictStore(t)
	prior := int64(7)
	id, err := s.Record(context.Background(), Scan{Owner: "acme", Repo: "r"}, []File{{
		Path: "a.go", Lang: "go", Disposition: "audited", Gradable: true,
		CacheKey: "K", VerdictJSON: `{}`, ComputedAt: time.Now().UTC(),
		ModelsByRole:  "critic=claude-haiku-4-5,mutant-generator=claude-sonnet-5",
		MutantsTotal:  42,
		RegionsTotal:  8,
		RegionsProbed: 6,
		// Two seats abandoned after MaxShardRetries is a COVERAGE SHORTFALL:
		// the kill rate is over the mutants that exist, not the ones that
		// should have. A reader must be able to see that without re-running.
		DroppedRegions:           "handler.go:12,handler.go:44",
		VacuousFindings:          3,
		Status:                   "needs-review",
		AuthoredTestNotCollected: true,
		BaselineFailed:           false,
		CacheHit:                 true,
		ReusedFromScanID:         &prior,
	}})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}

	files, err := s.FilesForScan(context.Background(), id)
	if err != nil {
		t.Fatalf("FilesForScan: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("got %d files, want 1", len(files))
	}
	f := files[0]
	if f.MutantsTotal != 42 || f.RegionsTotal != 8 || f.RegionsProbed != 6 {
		t.Fatalf("mutant/region counts did not round-trip: %+v", f)
	}
	if f.ModelsByRole == "" || f.Status != "needs-review" || f.VacuousFindings != 3 {
		t.Fatalf("attribution did not round-trip: %+v", f)
	}
	if f.DroppedRegions == "" || !f.AuthoredTestNotCollected {
		t.Fatalf("shortfall flags did not round-trip: %+v", f)
	}
	if !f.CacheHit || f.ReusedFromScanID == nil || *f.ReusedFromScanID != 7 {
		t.Fatalf("reuse lineage did not round-trip: %+v", f)
	}
}

// The whole point of the lineage columns: an aggregate that grades models must
// be able to exclude rows that were REUSED, or one measurement is counted once
// per scan forever and whatever happened to be cached dominates the average.
func TestReusedRowsAreDistinguishableFromMeasuredOnes(t *testing.T) {
	s := openTempVerdictStore(t)
	ctx := context.Background()
	measured, err := s.Record(ctx, Scan{Owner: "acme", Repo: "r"}, []File{{
		Path: "a.go", Disposition: "audited", Gradable: true, CacheKey: "K",
		VerdictJSON: `{}`, MutantsTotal: 10, CacheHit: false,
	}})
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	if _, err := s.Record(ctx, Scan{Owner: "acme", Repo: "r"}, []File{{
		Path: "a.go", Disposition: "audited", Gradable: true, CacheKey: "K",
		VerdictJSON: `{}`, MutantsTotal: 10, CacheHit: true, ReusedFromScanID: &measured,
	}}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	var measuredRows int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM scan_files WHERE cache_key = ? AND NOT cache_hit`, "K",
	).Scan(&measuredRows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if measuredRows != 1 {
		t.Fatalf("got %d measured rows for one real measurement, want 1", measuredRows)
	}
}
