// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/learn"
	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/telemetry"
)

func TestMatchLearnedSignatures(t *testing.T) {
	findings := []queue.Finding{
		{Type: "missing-req", Target: "go.mod"},
		{Type: "bug", Target: "once.sh"},
	}
	approved := []learn.Proposal{
		{Signature: "missing-req|go.mod", Status: learn.StatusApproved},
		{Signature: "vuln|creds.go", Status: learn.StatusApproved},
		{Signature: "bug|once.sh", Status: learn.StatusPending}, // not approved — must not match
	}
	got := matchLearnedSignatures(findings, approved)
	if len(got) != 1 || got[0] != "missing-req|go.mod" {
		t.Fatalf("got %v, want exactly [missing-req|go.mod]", got)
	}
}

func TestMissionHistoryListSkipsRunningAndOrdersNewestFirst(t *testing.T) {
	dir := t.TempDir()
	q, _ := queue.Open(filepath.Join(dir, "q.sqlite3"))
	defer q.Close()
	m, _ := mission.Open(filepath.Join(dir, "m.sqlite3"))
	defer m.Close()

	mid1, _ := mission.CreateMission(m, q, "first", []mission.PhaseSpec{{Name: "build", Instruction: "x"}}, false)
	_ = m.SetMissionStatus(mid1, "done")
	mid2, _ := mission.CreateMission(m, q, "second", []mission.PhaseSpec{{Name: "build", Instruction: "x"}}, false)
	_ = m.SetMissionStatus(mid2, "done")
	_, _ = mission.CreateMission(m, q, "still running", nil, false) // stays "running" — must be excluded

	got, err := MissionHistoryList(m, q, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 non-running missions, got %d", len(got))
	}
	if got[0].ID != mid2 || got[1].ID != mid1 {
		t.Fatalf("expected newest first [%d,%d], got [%d,%d]", mid2, mid1, got[0].ID, got[1].ID)
	}
}

func TestMissionHistoryDurationPrefersMissionCompletedEvent(t *testing.T) {
	dir := t.TempDir()
	q, _ := queue.Open(filepath.Join(dir, "q.sqlite3"))
	defer q.Close()
	m, _ := mission.Open(filepath.Join(dir, "m.sqlite3"))
	defer m.Close()
	tel, _ := telemetry.Open(filepath.Join(dir, "t.duckdb"))
	defer tel.Close()

	mid, _ := mission.CreateMission(m, q, "x", []mission.PhaseSpec{{Name: "build", Instruction: "x"}}, false)
	_ = m.SetMissionStatus(mid, "done")
	_ = tel.Record(telemetry.Event{MissionID: mid, Kind: "mission_completed"})

	got, err := MissionHistoryDetail(m, q, tel, nil, mid)
	if err != nil || got == nil {
		t.Fatalf("detail: %v err=%v", got, err)
	}
	if got.DurationSeconds < 0 {
		t.Fatalf("duration must be non-negative, got %v", got.DurationSeconds)
	}
}

func TestMissionHistoryDetailUnknownMission(t *testing.T) {
	dir := t.TempDir()
	q, _ := queue.Open(filepath.Join(dir, "q.sqlite3"))
	defer q.Close()
	m, _ := mission.Open(filepath.Join(dir, "m.sqlite3"))
	defer m.Close()
	got, err := MissionHistoryDetail(m, q, nil, nil, 999)
	if err != nil || got != nil {
		t.Fatalf("expected (nil,nil) for unknown mission, got %v err=%v", got, err)
	}
}
