// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/telemetry"
)

// TestMissionFootprintOfAndAll covers the DB relief valve's (#66) FOOTPRINT
// view across both stores: per-mission counts and the fleet-wide list.
func TestMissionFootprintOfAndAll(t *testing.T) {
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	m, err := mission.Open(filepath.Join(dir, "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	tel, err := telemetry.Open(filepath.Join(dir, "t.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer tel.Close()

	mid, err := mission.CreateMission(m, q, "ship it", []mission.PhaseSpec{{Name: "build", Instruction: "x"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.AddFinding(queue.Finding{MissionID: mid, Reporter: "tester", Type: "bug", Severity: "low"}); err != nil {
		t.Fatal(err)
	}
	if err := tel.Record(telemetry.Event{MissionID: mid, Kind: "mission_created", Actor: "engine"}); err != nil {
		t.Fatal(err)
	}

	fp, err := MissionFootprintOf(q, tel, mid)
	if err != nil {
		t.Fatal(err)
	}
	if fp.Tasks != 1 || fp.Findings != 1 || fp.TelemetryEvents != 1 || fp.TotalRows != 3 {
		t.Fatalf("footprint = %+v, want tasks=1 findings=1 telemetry=1 total=3", fp)
	}

	// nil telemetry degrades TelemetryEvents to 0, never errors.
	fpNoTel, err := MissionFootprintOf(q, nil, mid)
	if err != nil {
		t.Fatal(err)
	}
	if fpNoTel.TelemetryEvents != 0 || fpNoTel.TotalRows != 2 {
		t.Fatalf("nil-telemetry footprint = %+v, want telemetry_events=0 total=2", fpNoTel)
	}

	all, err := MissionFootprintAll(m, q, tel)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || all[0].MissionID != mid || all[0].TotalRows != 3 {
		t.Fatalf("MissionFootprintAll = %+v, want one entry for mission %d with total_rows=3", all, mid)
	}
}

// TestPruneMissionClearsBothStoresAndReturnsPreDeleteFootprint proves
// brain.PruneMission (the shared orchestration behind both the HTTP endpoint
// and any future MCP tool) deletes a mission's rows from BOTH the
// coordination store and telemetry, and that it hands back the footprint as
// it stood BEFORE deletion (so a caller can log/echo what was reclaimed).
func TestPruneMissionClearsBothStoresAndReturnsPreDeleteFootprint(t *testing.T) {
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	m, err := mission.Open(filepath.Join(dir, "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	tel, err := telemetry.Open(filepath.Join(dir, "t.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer tel.Close()

	mid, err := mission.CreateMission(m, q, "ship it", []mission.PhaseSpec{{Name: "build", Instruction: "x"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := q.RecordExecution(queue.Execution{MissionID: mid, Agent: "bob", Command: "go build", ExitCode: 0, OK: true, TS: 1}); err != nil {
		t.Fatal(err)
	}
	if err := tel.Record(telemetry.Event{MissionID: mid, Kind: "mission_created", Actor: "engine"}); err != nil {
		t.Fatal(err)
	}

	before, err := PruneMission(q, tel, mid)
	if err != nil {
		t.Fatal(err)
	}
	if before.Tasks != 1 || before.Executions != 1 || before.TelemetryEvents != 1 {
		t.Fatalf("PruneMission returned footprint %+v, want the pre-delete counts (1 task/execution/event)", before)
	}

	after, err := MissionFootprintOf(q, tel, mid)
	if err != nil {
		t.Fatal(err)
	}
	if after.TotalRows != 0 {
		t.Fatalf("mission %d not fully pruned from both stores: %+v", mid, after)
	}
}

// TestBuildReplayStreamNonEmptyBeforePrune proves the EXPORT mechanism (the
// same stream /api/replay serves) yields a non-empty tape for a mission that
// still has records — the durable dump an operator must archive to a static
// file BEFORE calling PruneMission (export -> prune -> reclaim).
func TestBuildReplayStreamNonEmptyBeforePrune(t *testing.T) {
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	m, err := mission.Open(filepath.Join(dir, "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	tel, err := telemetry.Open(filepath.Join(dir, "t.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer tel.Close()

	mid, err := mission.CreateMission(m, q, "ship it", []mission.PhaseSpec{{Name: "build", Instruction: "x"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := tel.Record(telemetry.Event{MissionID: mid, Kind: "mission_created", Actor: "engine"}); err != nil {
		t.Fatal(err)
	}

	tape, err := BuildReplayStream(q, tel, mid)
	if err != nil {
		t.Fatal(err)
	}
	if len(tape) == 0 {
		t.Fatalf("BuildReplayStream returned an empty tape for a mission with records")
	}

	// After prune, the SAME mission's tape is empty — the exported tape (taken
	// beforehand) is the only surviving record of the mission's story.
	if _, err := PruneMission(q, tel, mid); err != nil {
		t.Fatal(err)
	}
	tapeAfter, err := BuildReplayStream(q, tel, mid)
	if err != nil {
		t.Fatal(err)
	}
	if len(tapeAfter) != 0 {
		t.Fatalf("expected an empty tape after prune, got %d events", len(tapeAfter))
	}
}
