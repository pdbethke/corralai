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

	if err := st.SelectionCachePut(ctx, "tree1", "cmd1", "python", "workspace", raw, ""); err != nil {
		t.Fatalf("SelectionCachePut: %v", err)
	}
	got, ok, err := st.SelectionCacheGet(ctx, "tree1", "cmd1", "python", "workspace")
	if err != nil {
		t.Fatalf("SelectionCacheGet: %v", err)
	}
	if !ok {
		t.Fatal("SelectionCacheGet: no hit for a key just Put")
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

	if err := st.SelectionCachePut(ctx, "tree1", "cmd1", "python", "workspace", []byte("evidence"), ""); err != nil {
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
			_, ok, err := st.SelectionCacheGet(ctx, tc.treeDigest, tc.cmdDigest, tc.plugin, tc.substrate)
			if err != nil {
				t.Fatalf("SelectionCacheGet: %v", err)
			}
			if ok {
				t.Errorf("expected a miss on %s, got a hit", tc.name)
			}
		})
	}
}

// TestSelectionCacheGetTreatsEmptyRawAsAMiss heals a ledger that already
// holds a poisoned row: before the write-side fix, a failed instrumented
// run (empty stdout, nil error — a non-zero exit is a RESULT there, not an
// error) was recorded as though it were real, Ran:true evidence. That row
// can already exist in a real database; this ledger must never keep
// serving it as a hit — every empty/whitespace-only raw is an honest MISS,
// so the caller re-runs the instrumented pass instead of being stuck
// forever on evidence that measured nothing.
func TestSelectionCacheGetTreatsEmptyRawAsAMiss(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "scans.duckdb")
	st, err := Open(dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	for _, raw := range [][]byte{{}, []byte("   "), []byte("\n\t \n")} {
		if err := st.SelectionCachePut(ctx, "tree1", "cmd1", "python", "workspace", raw, ""); err != nil {
			t.Fatalf("SelectionCachePut(%q): %v", raw, err)
		}
		got, ok, err := st.SelectionCacheGet(ctx, "tree1", "cmd1", "python", "workspace")
		if err != nil {
			t.Fatalf("SelectionCacheGet after Put(%q): %v", raw, err)
		}
		if ok {
			t.Errorf("Put(%q): got a hit, want a miss — empty raw must never be served", raw)
		}
		if got != nil {
			t.Errorf("Put(%q): got raw=%q on a miss, want nil", raw, got)
		}
	}

	// A genuine, non-empty Put under the SAME key is still served normally
	// — the empty-raw rule does not turn this cache into a permanent miss.
	if err := st.SelectionCachePut(ctx, "tree1", "cmd1", "python", "workspace", []byte("real evidence"), ""); err != nil {
		t.Fatalf("SelectionCachePut (healing): %v", err)
	}
	got, ok, err := st.SelectionCacheGet(ctx, "tree1", "cmd1", "python", "workspace")
	if err != nil {
		t.Fatalf("SelectionCacheGet after healing Put: %v", err)
	}
	if !ok || string(got) != "real evidence" {
		t.Errorf("got raw=%q ok=%v, want a hit on the healed row", got, ok)
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
	if err := st2.SelectionCachePut(ctx, "t", "c", "go", "workspace", []byte("x"), ""); err != nil {
		t.Fatalf("SelectionCachePut after reopen: %v", err)
	}
	if _, ok, err := st2.SelectionCacheGet(ctx, "t", "c", "go", "workspace"); err != nil || !ok {
		t.Fatalf("SelectionCacheGet after reopen: ok=%v err=%v", ok, err)
	}
}
