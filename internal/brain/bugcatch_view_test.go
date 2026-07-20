// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/advpool"
	"github.com/pdbethke/corralai/internal/bugcatch"
	"github.com/pdbethke/corralai/internal/criticscore"
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
	sc, err := BuildBugCatchScorecard(store, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(sc.Cells) != 1 || !sc.Cells[0].Provisional {
		t.Fatalf("2-run cell must be provisional: %+v", sc.Cells)
	}
}

func TestBuildBugCatchScorecardNilStore(t *testing.T) {
	sc, err := BuildBugCatchScorecard(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(sc.Cells) != 0 {
		t.Fatalf("nil store should yield empty scorecard, got %+v", sc)
	}
}

// TestBuildBugCatchScorecardJoinsCriticPrecision seeds bugcatch runs for two
// test-critic models (3 converged runs each, so the base Runs-based
// Provisional flag is false for both) and a criticscore.Store with
// adjudicated findings: haiku gets 2 confirmed + 1 refuted (denom 3, NOT
// below provisionalBelow=3 -> not provisional on the critic axis either),
// opus gets 1 confirmed + 0 refuted (denom 1, below 3 -> provisional). Proves
// the join populates CriticConfirmed/CriticRefuted/CriticPrecision only for
// the test-critic role, with exact numbers, and that a thin adjudication
// sample still marks a cell provisional even when it has plenty of runs.
func TestBuildBugCatchScorecardJoinsCriticPrecision(t *testing.T) {
	if provisionalBelow != 3 {
		t.Fatalf("test assumes provisionalBelow==3, got %d", provisionalBelow)
	}
	store, err := bugcatch.Open(t.TempDir() + "/bc.duckdb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	for _, model := range []string{"haiku", "opus"} {
		for i := 0; i < 3; i++ {
			if err := store.Record(ctx, []bugcatch.Observation{{
				TS: time.Unix(int64(i), 0).UTC(), RecordID: int64(i + 1), Model: model, Role: advpool.RoleTestCritic,
				Source: "pool", CriticFlags: 1,
			}}); err != nil {
				t.Fatal(err)
			}
		}
	}

	cs, err := criticscore.Open(t.TempDir() + "/cs.duckdb")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	if err := cs.Record(ctx, []criticscore.Finding{
		{ID: "1:1", RecordID: 1, Model: "haiku", Scope: "whole-test", Adjudication: "confirmed", Source: "human"},
		{ID: "2:1", RecordID: 2, Model: "haiku", Scope: "whole-test", Adjudication: "confirmed", Source: "human"},
		{ID: "3:1", RecordID: 3, Model: "haiku", Scope: "whole-test", Adjudication: "refuted", Source: "human"},
		{ID: "1:2", RecordID: 1, Model: "opus", Scope: "whole-test", Adjudication: "confirmed", Source: "human"},
	}); err != nil {
		t.Fatal(err)
	}

	sc, err := BuildBugCatchScorecard(store, cs)
	if err != nil {
		t.Fatal(err)
	}
	byModel := map[string]ScorecardCell{}
	for _, c := range sc.Cells {
		byModel[c.Model] = c
	}
	haiku := byModel["haiku"]
	if haiku.CriticConfirmed != 2 || haiku.CriticRefuted != 1 || haiku.CriticUnadjudicated != 0 {
		t.Fatalf("haiku critic counts wrong: %+v", haiku)
	}
	if haiku.CriticPrecision == nil || *haiku.CriticPrecision != 2.0/3.0 {
		t.Fatalf("haiku critic precision wrong: %+v", haiku)
	}
	if haiku.Provisional {
		t.Fatalf("haiku has 3 runs and 3 adjudications, should not be provisional: %+v", haiku)
	}

	opus := byModel["opus"]
	if opus.CriticConfirmed != 1 || opus.CriticRefuted != 0 || opus.CriticUnadjudicated != 0 {
		t.Fatalf("opus critic counts wrong: %+v", opus)
	}
	if opus.CriticPrecision == nil || *opus.CriticPrecision != 1.0 {
		t.Fatalf("opus critic precision wrong: %+v", opus)
	}
	if !opus.Provisional {
		t.Fatalf("opus has only 1 adjudication (<3), should be provisional despite 3 runs: %+v", opus)
	}
}
