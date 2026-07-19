package bugcatch

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(t.TempDir() + "/bc.duckdb")
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestScorecardRecallPrecisionAndMootRuns(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ts := time.Date(2026, 7, 17, 0, 0, 0, 0, time.UTC)
	// Run A (needs-review): test-writer caught 1 of 2 gaps; authored a sound test.
	must(t, s.Record(ctx, []Observation{{
		TS: ts, RecordID: 10, Model: "claude-sonnet-5", Role: "test-writer", Source: "pool",
		Catches: 1, Opportunities: 2, SoundTests: 1, AuthoredTests: 1,
	}}))
	// Run B (certified, moot): 0 gaps → contributes soundness but NO recall opportunity.
	must(t, s.Record(ctx, []Observation{{
		TS: ts, RecordID: 11, Model: "claude-sonnet-5", Role: "test-writer", Source: "pool",
		Catches: 0, Opportunities: 0, SoundTests: 1, AuthoredTests: 1,
	}}))
	cells, err := s.Scorecard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var tw *Cell
	for i := range cells {
		if cells[i].Role == "test-writer" {
			tw = &cells[i]
		}
	}
	if tw == nil {
		t.Fatal("no test-writer cell")
	}
	if tw.Catches != 1 || tw.Opportunities != 2 {
		t.Fatalf("catches/opps = %d/%d, want 1/2", tw.Catches, tw.Opportunities)
	}
	if tw.Recall == nil || *tw.Recall != 0.5 {
		t.Fatalf("recall = %v, want 0.5 (moot run adds no opportunity)", tw.Recall)
	}
	if tw.Precision == nil || *tw.Precision != 1.0 {
		t.Fatalf("precision = %v, want 1.0 over 2 sound/2 authored", tw.Precision)
	}
	if tw.Runs != 2 {
		t.Fatalf("runs = %d, want 2", tw.Runs)
	}
}

func TestScorecardNilWhenNoDenominator(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	// A mutant-generator seat: no test-writer opportunities/authored → recall+precision nil.
	must(t, s.Record(ctx, []Observation{{
		TS: time.Unix(1, 0).UTC(), RecordID: 12, Model: "claude-sonnet-5", Role: "mutant-generator",
		Source: "pool", MutantsPlanted: 5, MutantsSurvived: 1,
	}}))
	cells, _ := s.Scorecard(ctx)
	if len(cells) != 1 || cells[0].Recall != nil || cells[0].Precision != nil {
		t.Fatalf("mutant-generator cell must have nil recall/precision: %+v", cells)
	}
	if cells[0].MutantsPlanted != 5 || cells[0].MutantsSurvived != 1 {
		t.Fatalf("adversary line = %d/%d, want 5/1", cells[0].MutantsPlanted, cells[0].MutantsSurvived)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// TestScorecardRunsCountsDistinctRunsNotRows is the regression test for
// IMPORTANT 2: swarm slice 2 fans the mutant-generator role out into one row
// PER SHARD (up to DefaultMaxShards=8), while every other role still writes
// exactly one row per run. Runs must count converged RUNS (COUNT(DISTINCT
// record_id)), never observation rows (COUNT(*)) — the latter would report a
// single 8-shard run as RUNS=8 for mutant-generator while test-writer
// correctly reports RUNS=1 for the SAME run, silently defeating the
// provisionalBelow=3 "not yet a signal" gate after one real run.
func TestScorecardRunsCountsDistinctRunsNotRows(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	ts := time.Unix(1, 0).UTC()

	// Run 100: 8 mutant-generator shard rows (same record_id, same model —
	// exactly what one sharded run's driver output looks like) plus the
	// SAME run's single test-writer row.
	for shard := 0; shard < 8; shard++ {
		must(t, s.Record(ctx, []Observation{{
			TS: ts, RecordID: 100, Model: "m", Role: "mutant-generator", Source: "pool",
			Shard: shard, MutantsPlanted: 1,
		}}))
	}
	must(t, s.Record(ctx, []Observation{{
		TS: ts, RecordID: 100, Model: "m", Role: "test-writer", Source: "pool",
		Catches: 1, Opportunities: 1, AuthoredTests: 1, SoundTests: 1,
	}}))

	// A second, independent run (record_id 101) contributes one more row to
	// each role, so both cells have exactly 2 real runs behind them.
	must(t, s.Record(ctx, []Observation{{
		TS: ts, RecordID: 101, Model: "m", Role: "mutant-generator", Source: "pool",
		Shard: 0, MutantsPlanted: 1,
	}}))
	must(t, s.Record(ctx, []Observation{{
		TS: ts, RecordID: 101, Model: "m", Role: "test-writer", Source: "pool",
		Catches: 1, Opportunities: 1, AuthoredTests: 1, SoundTests: 1,
	}}))

	cells, err := s.Scorecard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var gen, tw *Cell
	for i := range cells {
		switch cells[i].Role {
		case "mutant-generator":
			gen = &cells[i]
		case "test-writer":
			tw = &cells[i]
		}
	}
	if gen == nil || tw == nil {
		t.Fatalf("missing cells: %+v", cells)
	}
	if gen.Runs != 2 {
		t.Fatalf("mutant-generator Runs = %d, want 2 (2 runs, 9 rows) — Runs is counting rows, not runs", gen.Runs)
	}
	if tw.Runs != 2 {
		t.Fatalf("test-writer Runs = %d, want 2", tw.Runs)
	}
}

// TestMigrationIdempotent proves opening the same ledger file twice adds no
// duplicate columns and errors on neither open — the property the prior
// round's scratch test verified and then deleted.
func TestMigrationIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bc.duckdb")

	s1, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	cols1 := countColumns(t, s1)
	if err := s1.Close(); err != nil {
		t.Fatalf("close first: %v", err)
	}

	s2, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer s2.Close()
	cols2 := countColumns(t, s2)

	if len(cols2) != len(cols1) {
		t.Fatalf("column count changed across a re-open with no schema change: %d -> %d (duplicate ALTER?)", len(cols1), len(cols2))
	}
	for name := range cols2 {
		if cols2[name] != 1 {
			t.Errorf("column %q appears %d times, want exactly 1", name, cols2[name])
		}
	}
}

// countColumns returns, for the live bugcatch_observations table, how many
// times each column name appears in information_schema.columns — more than
// once for any name would mean a migration ALTER ran again on an
// already-migrated table.
func countColumns(t *testing.T, s *Store) map[string]int {
	t.Helper()
	rows, err := s.db.Query(`SELECT column_name FROM information_schema.columns WHERE table_name = ?`, "bugcatch_observations")
	if err != nil {
		t.Fatalf("probe columns: %v", err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan column: %v", err)
		}
		out[name]++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate columns: %v", err)
	}
	return out
}

// TestMigrationUpgradesPreExistingDatabase proves a ledger created before
// swarm slice 2 (the bare pre-migration CREATE TABLE, missing every column in
// bugcatchObservationsMigrationCols) gets those columns added on Open, and
// can then round-trip a full Record() — every field, including the per-shard
// ones the pre-migration schema never had.
func TestMigrationUpgradesPreExistingDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.duckdb")

	// Stand up the ORIGINAL pre-migration schema directly (bypassing Open,
	// which would immediately migrate it) — the exact CREATE TABLE this
	// package used before the shard/region/etc. columns existed.
	raw, err := sql.Open("duckdb", path)
	if err != nil {
		t.Fatalf("raw open: %v", err)
	}
	if _, err := raw.Exec(`CREATE TABLE bugcatch_observations (
		ts TIMESTAMP, record_id BIGINT, record_head VARCHAR, mission_id BIGINT,
		repo VARCHAR, commit VARCHAR, model VARCHAR, role VARCHAR, source VARCHAR,
		catches INTEGER, opportunities INTEGER, sound_tests INTEGER, authored_tests INTEGER,
		critic_flags INTEGER, mutants_planted INTEGER, mutants_survived INTEGER
	)`); err != nil {
		t.Fatalf("create legacy table: %v", err)
	}
	// Insert a row through the LEGACY (pre-slice-2) schema, before Open/migrate
	// ever runs — this is what every row in a production ledger predating
	// swarm slice 2 actually looks like: NULL in all eight additive columns.
	// This is the assertion the prior scratch version of this test had and
	// lost when promoted: without it, nothing exercises Observations() against
	// a real legacy row, which is exactly the row that made it scan-fail on
	// `shard`/`region_complexity`/.../`shadow` (all NULL, scanned into
	// int/bool destinations) the first time this method met a real ledger.
	legacyTS := time.Unix(1000, 0).UTC()
	if _, err := raw.Exec(`INSERT INTO bugcatch_observations (
		ts, record_id, record_head, mission_id, repo, commit, model, role, source,
		catches, opportunities, sound_tests, authored_tests,
		critic_flags, mutants_planted, mutants_survived
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		legacyTS, int64(199), "head199", int64(1), "r", "c", "m", "test-writer", "pool",
		1, 2, 1, 1, 0, 0, 0); err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	if err := raw.Close(); err != nil {
		t.Fatalf("close raw: %v", err)
	}

	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open on pre-existing database: %v", err)
	}
	defer s.Close()

	cols := countColumns(t, s)
	for _, c := range bugcatchObservationsMigrationCols {
		if cols[c.name] != 1 {
			t.Fatalf("migrated column %q not added exactly once (count=%d)", c.name, cols[c.name])
		}
	}

	ctx := context.Background()
	ts := time.Unix(2, 0).UTC()
	want := Observation{
		TS: ts, RecordID: 200, RecordHead: "head200", MissionID: 9,
		Repo: "r", Commit: "c", Model: "m", Role: "mutant-generator", Source: "pool",
		Catches: 1, Opportunities: 2, SoundTests: 3, AuthoredTests: 4,
		CriticFlags: 5, MutantsPlanted: 6, MutantsSurvived: 7,
		Shard: 1, Region: "A, B", RegionComplexity: 8, RegionLines: 9,
		TestComplexity: 10, ParseRetries: 1, Dropped: true, Shadow: false,
	}
	if err := s.Record(ctx, []Observation{want}); err != nil {
		t.Fatalf("Record on upgraded database: %v", err)
	}
	got, err := s.Observations(ctx)
	if err != nil {
		t.Fatalf("Observations: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 round-tripped rows (1 legacy + 1 post-migration), got %d: %+v", len(got), got)
	}
	var newRow, legacyRow *Observation
	for i := range got {
		switch got[i].RecordID {
		case 200:
			newRow = &got[i]
		case 199:
			legacyRow = &got[i]
		}
	}
	if newRow == nil || legacyRow == nil {
		t.Fatalf("expected rows for record_id 199 and 200, got %+v", got)
	}
	if *newRow != want {
		t.Fatalf("round-tripped observation = %+v, want %+v", *newRow, want)
	}

	// The legacy row predates every additive column: it must come back with
	// their zero values (via COALESCE), not fail to scan or return NULL.
	wantLegacy := Observation{
		TS: legacyTS, RecordID: 199, RecordHead: "head199", MissionID: 1,
		Repo: "r", Commit: "c", Model: "m", Role: "test-writer", Source: "pool",
		Catches: 1, Opportunities: 2, SoundTests: 1, AuthoredTests: 1,
		// Shard, Region, RegionComplexity, RegionLines, TestComplexity,
		// ParseRetries, Dropped, Shadow all left at zero value: never written
		// by the legacy schema, so this ledger has no way to know otherwise.
	}
	if *legacyRow != wantLegacy {
		t.Fatalf("legacy round-tripped observation = %+v, want %+v", *legacyRow, wantLegacy)
	}
}
