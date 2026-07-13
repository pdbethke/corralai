// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/queue"
)

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
