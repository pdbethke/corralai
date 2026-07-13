// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/telemetry"
)

// TestReviewMissionAcceptEmitsMissionCompleted verifies that accepting a
// review (the non-engine completion path) also emits a mission_completed
// telemetry event, mirroring what the engine's auto-complete path does via
// OnMissionCompleted.
func TestReviewMissionAcceptEmitsMissionCompleted(t *testing.T) {
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	ms, err := mission.Open(filepath.Join(dir, "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()
	tel, err := telemetry.Open(filepath.Join(dir, "t.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer tel.Close()

	mid, err := mission.CreateMission(ms, q, "ship it", []mission.PhaseSpec{
		{Name: "build", Instruction: "build it"},
	}, true) // requires_review
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
	if _, err := q.Complete(tk.ID, "bee1", "done"); err != nil {
		t.Fatal(err)
	}
	if err := ms.SetMissionStatus(mid, "awaiting_review"); err != nil {
		t.Fatal(err)
	}

	mv, err := mission.SubmitReview(ms, q, mid, true, "", "client")
	if err != nil {
		t.Fatal(err)
	}
	rec(tel, mid, "review_accepted", "client", "", nil)
	if mv.Status == "done" {
		rounds := 0
		if full, ferr := ms.Mission(mid); ferr == nil && full != nil {
			rounds = full.ReviewRounds
		}
		rec(tel, mid, "mission_completed", "engine", "", map[string]any{"status": "done", "review_rounds": rounds})
	}

	rep, err := tel.RunReport("kinds")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rep.Rows {
		if row[0] == "mission_completed" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a mission_completed telemetry event after review accept")
	}
}

// The resolve_review MCP tool is the operator's entry point for a mission the
// findings gate parked at needs-review: it must refuse while a blocking finding
// is open and certify the mission done once the operator has cleared it.
func TestResolveReviewToolResolvesParkedMission(t *testing.T) {
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	ms, err := mission.Open(filepath.Join(dir, "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer ms.Close()

	mid, err := mission.CreateMission(ms, q, "ship it", []mission.PhaseSpec{{Name: "build", Instruction: "b"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	fid, err := q.AddFinding(queue.Finding{MissionID: mid, Reporter: "reviewer", Type: "design-flaw", Severity: "critical", Target: "arch", Evidence: "unsound"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ms.SetMissionStatus(mid, "needs-review"); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() { _ = NewServer(nil, nil, Options{Queue: q, Missions: ms}).Run(ctx, serverT) }()
	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	// Refused while the critical finding is open.
	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "resolve_review", Arguments: map[string]any{"id": mid}})
	if err != nil {
		t.Fatalf("resolve_review call: %v", err)
	}
	if !res.IsError {
		t.Fatal("resolve_review must refuse while a blocking finding is open")
	}

	// Operator dismisses the finding → resolve_review now certifies done.
	if _, err := q.SetFindingStatus(fid, queue.FindingDismissed); err != nil {
		t.Fatal(err)
	}
	res, err = sess.CallTool(ctx, &mcp.CallToolParams{Name: "resolve_review", Arguments: map[string]any{"id": mid}})
	if err != nil {
		t.Fatalf("resolve_review call: %v", err)
	}
	if res.IsError {
		t.Fatalf("resolve_review after clearing blockers must succeed, got: %q", toolErrText(res))
	}
	if mv, _ := ms.Mission(mid); mv == nil || mv.Status != "done" {
		t.Fatalf("mission should be done after resolve_review, got %v", mv)
	}
}
