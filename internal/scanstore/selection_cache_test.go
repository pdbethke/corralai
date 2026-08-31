// SPDX-License-Identifier: Elastic-2.0

package scanstore

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"
)

// TestSelectionCachePutGetRoundTrip is the ledger-side headline case: raw
// evidence Put under one key comes back byte-for-byte identical, with the
// scan id that earned it.
func TestSelectionCachePutGetRoundTrip(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "scans.duckdb")
	st, err := Open(dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	// A 1 MB raw payload — BLOB fidelity has to hold at the size real
	// coverage-context evidence actually reaches (up to ~1.3 MB on a large
	// repo), not just on a two-byte fixture that would round-trip through
	// almost any encoding by accident.
	raw := bytes.Repeat([]byte("selection-evidence-byte."), 1<<15+7)

	if err := st.SelectionCachePut(ctx, "tree1", "cmd1", "python", "workspace", raw, "", 42); err != nil {
		t.Fatalf("SelectionCachePut: %v", err)
	}
	got, scanID, ok, err := st.SelectionCacheGet(ctx, "tree1", "cmd1", "python", "workspace")
	if err != nil {
		t.Fatalf("SelectionCacheGet: %v", err)
	}
	if !ok {
		t.Fatal("SelectionCacheGet: no hit for a key just Put")
	}
	if scanID != 42 {
		t.Errorf("scanID = %d, want 42 (the creating scan)", scanID)
	}
	if !bytes.Equal(got, raw) {
		t.Errorf("raw evidence did not round-trip byte-for-byte: got %d bytes, want %d bytes", len(got), len(raw))
	}
}

// TestSelectionCacheGetMissesOnAnyKeyChange: tree_digest, cmd_digest,
// plugin and substrate are all part of the key — a change to any one of
// them is a genuinely different question and must never be served from
// another row. substrate matters most: a jail run's degraded-but-Ran=true
// evidence must never be served to a workspace run over the identical
// tree, or the reverse (the #110 class).
func TestSelectionCacheGetMissesOnAnyKeyChange(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "scans.duckdb")
	st, err := Open(dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	if err := st.SelectionCachePut(ctx, "tree1", "cmd1", "python", "workspace", []byte("evidence"), "", 1); err != nil {
		t.Fatalf("SelectionCachePut: %v", err)
	}

	cases := []struct {
		name                                     string
		treeDigest, cmdDigest, plugin, substrate string
	}{
		{"different tree", "tree2", "cmd1", "python", "workspace"},
		{"different cmd", "tree1", "cmd2", "python", "workspace"},
		{"different plugin", "tree1", "cmd1", "go", "workspace"},
		{"different substrate", "tree1", "cmd1", "python", "jail"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, ok, err := st.SelectionCacheGet(ctx, tc.treeDigest, tc.cmdDigest, tc.plugin, tc.substrate)
			if err != nil {
				t.Fatalf("SelectionCacheGet: %v", err)
			}
			if ok {
				t.Errorf("expected a miss on %s, got a hit", tc.name)
			}
		})
	}
}

// TestSelectionCacheTableMigratesOntoAnExistingLedger: a store opened
// against a DSN that already holds a scans/scan_files ledger from before
// selection_cache existed must gain the table on reopen, not fail or
// silently skip it — the same contract goal_cache proved for itself.
func TestSelectionCacheTableMigratesOntoAnExistingLedger(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "scans.duckdb")

	st1, err := Open(dsn)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := st1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	st2, err := Open(dsn)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer st2.Close()
	ctx := context.Background()
	if err := st2.SelectionCachePut(ctx, "t", "c", "go", "workspace", []byte("x"), "", 1); err != nil {
		t.Fatalf("SelectionCachePut after reopen: %v", err)
	}
	if _, _, ok, err := st2.SelectionCacheGet(ctx, "t", "c", "go", "workspace"); err != nil || !ok {
		t.Fatalf("SelectionCacheGet after reopen: ok=%v err=%v", ok, err)
	}
}

// TestScanSelectionReusedFromRoundTrips proves the scans-grain column
// exists, is nullable, and round-trips through Record/ScanByID like every
// other pointer column on this table — the ONLY signal that distinguishes
// "this scan reused a prior scan's coverage evidence" from "this scan ran
// no selection pass at all", since both leave SelectionMillis nil.
func TestScanSelectionReusedFromRoundTrips(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "scans.duckdb")
	st, err := Open(dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	reusedFrom := int64(7)
	id, err := st.Record(ctx, Scan{SelectionReusedFrom: &reusedFrom}, nil)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	row, ok, err := st.ScanByID(ctx, id)
	if err != nil || !ok {
		t.Fatalf("ScanByID: ok=%v err=%v", ok, err)
	}
	if row.SelectionMillis != nil {
		t.Errorf("SelectionMillis = %v, want nil — the reused scan ran no selection pass of its own", *row.SelectionMillis)
	}
	if row.SelectionReusedFrom == nil || *row.SelectionReusedFrom != 7 {
		t.Errorf("SelectionReusedFrom = %v, want *7", row.SelectionReusedFrom)
	}

	// A scan that neither ran nor reused a selection pass must read back
	// with SelectionReusedFrom nil, never a fabricated 0 (scan id 0 is not
	// a real scan).
	id2, err := st.Record(ctx, Scan{}, nil)
	if err != nil {
		t.Fatalf("Record (no selection): %v", err)
	}
	row2, ok2, err := st.ScanByID(ctx, id2)
	if err != nil || !ok2 {
		t.Fatalf("ScanByID (no selection): ok=%v err=%v", ok2, err)
	}
	if row2.SelectionReusedFrom != nil {
		t.Errorf("SelectionReusedFrom = %v, want nil for a scan that neither ran nor reused a selection pass", *row2.SelectionReusedFrom)
	}
}
