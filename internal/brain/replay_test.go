// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/telemetry"
)

// kindSubject is the golden file's shape: ORDER and (kind,subject) pairs only
// — never literal timestamps, since neither internal/queue nor
// internal/telemetry expose a test-overridable clock seam across package
// boundaries. Monotonic-ts is asserted separately, in Go, not the fixture.
type kindSubject struct {
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
}

func seedReplayMission(t *testing.T) (*queue.Store, *telemetry.Store, int64) {
	t.Helper()
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })
	m, err := mission.Open(filepath.Join(dir, "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	tel, err := telemetry.Open(filepath.Join(dir, "t.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { tel.Close() })

	mid, err := mission.CreateMission(m, q, "build a tool", []mission.PhaseSpec{{Name: "build", Instruction: "build it"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.PromoteReady(mid); err != nil {
		t.Fatal(err)
	}
	tk, err := q.ClaimNext("bee1", nil, 3600)
	if err != nil || tk == nil {
		t.Fatalf("claim: %v", err)
	}
	if _, err := q.Complete(tk.ID, "bee1", "built"); err != nil {
		t.Fatal(err)
	}
	fid, err := q.AddFinding(queue.Finding{MissionID: mid, TaskID: tk.ID, Reporter: "bee1", Type: "bug", Severity: "low", Target: "x.go"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.SetFindingStatus(fid, queue.FindingAddressed); err != nil {
		t.Fatal(err)
	}
	if err := q.RecordExecution(queue.Execution{MissionID: mid, Agent: "bee1", Command: "go build ./...", OK: true}); err != nil {
		t.Fatal(err)
	}
	if err := tel.Record(telemetry.Event{MissionID: mid, Kind: "mission_completed", Actor: "engine"}); err != nil {
		t.Fatal(err)
	}
	return q, tel, mid
}

func TestBuildReplayStreamGoldenOrder(t *testing.T) {
	q, tel, mid := seedReplayMission(t)
	events, err := BuildReplayStream(q, tel, mid)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("expected a non-empty stream")
	}
	for i := 1; i < len(events); i++ {
		if events[i].TS < events[i-1].TS {
			t.Fatalf("stream must be time-ordered: event %d (ts=%v) precedes event %d (ts=%v)", i, events[i].TS, i-1, events[i-1].TS)
		}
	}
	got := make([]kindSubject, len(events))
	for i, e := range events {
		got[i] = kindSubject{Kind: e.Kind, Subject: e.Subject}
	}
	gotJSON, _ := json.MarshalIndent(got, "", "  ")

	goldenPath := filepath.Join("testdata", "replay_golden.json")
	if os.Getenv("UPDATE_GOLDEN") != "" {
		if err := os.WriteFile(goldenPath, gotJSON, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotJSON) != string(want) {
		t.Fatalf("replay stream shape drifted from golden.\ngot:\n%s\nwant:\n%s", gotJSON, want)
	}
}

func TestBuildReplayStreamDegradesGracefullyWithNoTelemetry(t *testing.T) {
	dir := t.TempDir()
	q, _ := queue.Open(filepath.Join(dir, "q.sqlite3"))
	defer q.Close()
	m, _ := mission.Open(filepath.Join(dir, "m.sqlite3"))
	defer m.Close()
	mid, _ := mission.CreateMission(m, q, "no ambience", []mission.PhaseSpec{{Name: "build", Instruction: "x"}}, false)
	_, _ = q.PromoteReady(mid)

	events, err := BuildReplayStream(q, nil, mid) // tel == nil: no ambience at all
	if err != nil {
		t.Fatal(err)
	}
	if len(events) == 0 {
		t.Fatal("a mission with only task rows must still yield a playable (non-empty) stream")
	}
}

// TestBuildReplayStreamCarriesModel: model identity must ride through the
// stream from BOTH sources that know it — the queue's findings table
// (reporter_model/reporter_backend) and the telemetry event log's model
// column — so an exported recording is self-describing about what models
// built it (site Part D derives meta.models and per-model analytics from
// the committed stream alone; it never sees the brain's telemetry DB).
func TestBuildReplayStreamCarriesModel(t *testing.T) {
	q, tel, mid := seedReplayMission(t)
	if _, err := q.AddFinding(queue.Finding{MissionID: mid, Reporter: "bee2", Type: "bug",
		Severity: "high", Target: "y.go", ReporterModel: "qwen2.5-coder:7b", ReporterBackend: "ollama"}); err != nil {
		t.Fatal(err)
	}
	if err := tel.Record(telemetry.Event{MissionID: mid, Kind: "finding_reported",
		Actor: "bee2", Subject: "y.go", Model: "qwen2.5-coder:7b"}); err != nil {
		t.Fatal(err)
	}
	events, err := BuildReplayStream(q, tel, mid)
	if err != nil {
		t.Fatal(err)
	}
	var queueSide, telSide bool
	for _, ev := range events {
		if ev.Kind != "finding_reported" || ev.Subject != "y.go" || ev.Model != "qwen2.5-coder:7b" {
			continue
		}
		if b, _ := ev.Detail["backend"].(string); b == "ollama" {
			queueSide = true // queue-derived: has reporter_backend in detail
		} else {
			telSide = true // telemetry-derived: model column only
		}
	}
	if !queueSide {
		t.Error("queue-derived finding_reported must carry Model + detail.backend from reporter_model/reporter_backend")
	}
	if !telSide {
		t.Error("telemetry-derived finding_reported must carry Model from the telemetry model column")
	}
}
