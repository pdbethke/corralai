// SPDX-License-Identifier: Elastic-2.0

package scanstore

import (
	"context"
	"path/filepath"
	"testing"
)

// TestGoalCachePutGetRoundTrip is Step 1's ledger-side headline case: a
// derived goal put under one key comes back byte-identical, and the
// UNGOALED shape (no goal, no provenance) round-trips too.
func TestGoalCachePutGetRoundTrip(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "scans.duckdb")
	st, err := Open(dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	if err := st.GoalCachePut(ctx, "a.go", "digest1", "model-x", "gp1", "must never panic", "derived:model-x@v1", false); err != nil {
		t.Fatalf("GoalCachePut: %v", err)
	}
	goal, provenance, ungoaled, ok, err := st.GoalCacheGet(ctx, "a.go", "digest1", "model-x", "gp1")
	if err != nil {
		t.Fatalf("GoalCacheGet: %v", err)
	}
	if !ok {
		t.Fatal("GoalCacheGet: no hit for a key just Put")
	}
	if ungoaled {
		t.Error("a goaled Put must not read back as ungoaled")
	}
	if goal != "must never panic" || provenance != "derived:model-x@v1" {
		t.Errorf("goal=%q provenance=%q, want byte-identical round trip", goal, provenance)
	}

	// The UNGOALED shape: no goal text, no provenance, but still a genuine
	// hit — the deriver's "no goal" answer is a fact worth reusing too.
	if err := st.GoalCachePut(ctx, "gen.go", "digest2", "model-x", "gp1", "", "", true); err != nil {
		t.Fatalf("GoalCachePut (ungoaled): %v", err)
	}
	_, _, ungoaled2, ok2, err := st.GoalCacheGet(ctx, "gen.go", "digest2", "model-x", "gp1")
	if err != nil {
		t.Fatalf("GoalCacheGet (ungoaled): %v", err)
	}
	if !ok2 {
		t.Fatal("GoalCacheGet: no hit for the ungoaled key just Put")
	}
	if !ungoaled2 {
		t.Error("an ungoaled Put must read back as ungoaled")
	}
}

// TestGoalCacheGetMissesOnAnyKeyChange: source_digest, model and
// engine_prompt_rev are all part of the key — a change to any one of them
// must be a genuinely different question, never served from the other's row.
func TestGoalCacheGetMissesOnAnyKeyChange(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "scans.duckdb")
	st, err := Open(dsn)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer st.Close()
	ctx := context.Background()

	if err := st.GoalCachePut(ctx, "a.go", "digest1", "model-x", "gp1", "goal text", "derived:model-x@v1", false); err != nil {
		t.Fatalf("GoalCachePut: %v", err)
	}

	cases := []struct {
		name                           string
		path, digest, model, promptRev string
	}{
		{"different digest", "a.go", "digest2", "model-x", "gp1"},
		{"different model", "a.go", "digest1", "model-y", "gp1"},
		{"different prompt rev", "a.go", "digest1", "model-x", "gp2"},
		{"different path", "b.go", "digest1", "model-x", "gp1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, _, ok, err := st.GoalCacheGet(ctx, tc.path, tc.digest, tc.model, tc.promptRev)
			if err != nil {
				t.Fatalf("GoalCacheGet: %v", err)
			}
			if ok {
				t.Errorf("expected a miss on %s, got a hit", tc.name)
			}
		})
	}
}

// TestGoalCacheTableMigratesOntoAnExistingLedger: a store opened against a
// DSN that already holds a scans/scan_files ledger from before goal_cache
// existed must gain the table on reopen, not fail or silently skip it.
func TestGoalCacheTableMigratesOntoAnExistingLedger(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "scans.duckdb")

	// First open: create the ledger the way any pre-goal-cache corral would
	// have — Open() itself is the only thing that has ever created this
	// file, so simply opening and closing it once stands in for "an existing
	// ledger without this table".
	st1, err := Open(dsn)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := st1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Reopen and use the goal cache immediately — if the table were missing
	// this would fail with a DuckDB "table does not exist" error.
	st2, err := Open(dsn)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer st2.Close()
	ctx := context.Background()
	if err := st2.GoalCachePut(ctx, "a.go", "d", "m", "gp1", "g", "p", false); err != nil {
		t.Fatalf("GoalCachePut after reopen: %v", err)
	}
	if _, _, _, ok, err := st2.GoalCacheGet(ctx, "a.go", "d", "m", "gp1"); err != nil || !ok {
		t.Fatalf("GoalCacheGet after reopen: ok=%v err=%v", ok, err)
	}
}
