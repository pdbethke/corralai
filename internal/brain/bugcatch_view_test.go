// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/bugcatch"
)

func TestBuildScorecardFlagsThinCellsProvisional(t *testing.T) {
	store, err := bugcatch.Open(t.TempDir() + "/bc.duckdb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	// 2 runs for claude test-writer (< 3 ⇒ provisional).
	for i := 0; i < 2; i++ {
		store.Record(ctx, []bugcatch.Observation{{
			TS: time.Unix(int64(i), 0).UTC(), Model: "claude-sonnet-5", Role: "test-writer",
			Source: "pool", Catches: 1, Opportunities: 1, SoundTests: 1, AuthoredTests: 1,
		}})
	}
	sc, err := BuildBugCatchScorecard(store)
	if err != nil {
		t.Fatal(err)
	}
	if len(sc.Cells) != 1 || !sc.Cells[0].Provisional {
		t.Fatalf("2-run cell must be provisional: %+v", sc.Cells)
	}
}

func TestBuildBugCatchScorecardNilStore(t *testing.T) {
	sc, err := BuildBugCatchScorecard(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(sc.Cells) != 0 {
		t.Fatalf("nil store should yield empty scorecard, got %+v", sc)
	}
}
