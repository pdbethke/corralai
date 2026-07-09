// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/repo"
	"github.com/pdbethke/corralai/internal/repoindex"
	"github.com/pdbethke/corralai/internal/telemetry"
)

// errorRepoIndex is a fake RepoIndex whose IndexPaths always returns an error —
// used to assert that an index failure does not abort create_mission.
type errorRepoIndex struct{}

func (e *errorRepoIndex) IndexPaths(_ int64, _ string, _ []string) error {
	return fmt.Errorf("index deliberately broken")
}
func (e *errorRepoIndex) Search(_ int64, _ string, _ int) ([]repoindex.Hit, error) {
	return nil, fmt.Errorf("search not available")
}

// TestCreateMissionIndexFailureDoesNotAbortProvisioning verifies Fix 5b:
// a failing IndexPaths (injected via the RepoIndex interface) must not prevent
// create_mission from returning a valid MissionView. Search is an aid, not a gate.
func TestCreateMissionIndexFailureDoesNotAbortProvisioning(t *testing.T) {
	root := t.TempDir()

	// Build a local git repo to clone from so Clone/Checkout succeed.
	srcDir := filepath.Join(root, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "main.go"), []byte("package main\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"git", "-C", srcDir, "init", "-b", "main"},
		{"git", "-C", srcDir, "config", "user.email", "test@example.com"},
		{"git", "-C", srcDir, "config", "user.name", "Test"},
		{"git", "-C", srcDir, "add", "."},
		{"git", "-C", srcDir, "commit", "-m", "init"},
	} {
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("git setup %v: %v\n%s", args, err, out)
		}
	}

	cstore, err := coord.Open(filepath.Join(root, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cstore.Close() })

	q, err := queue.Open(filepath.Join(root, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })

	mstore, err := mission.Open(filepath.Join(root, "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mstore.Close() })

	ws := filepath.Join(root, "ws")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}

	eng := repo.New("", "")

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() {
		_ = NewServer(cstore, nil, Options{
			Queue:     q,
			Missions:  mstore,
			Repo:      eng,
			Workspace: ws,
			Index:     &errorRepoIndex{}, // IndexPaths always errors
		}).Run(ctx, serverT)
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{
		Name: "create_mission",
		Arguments: map[string]any{
			"directive": "do something useful",
			"repo":      srcDir,
		},
	})
	if err != nil {
		t.Fatalf("create_mission: %v", err)
	}
	// A failing IndexPaths (in the background goroutine) must NOT cause create_mission
	// to return a tool error — the mission provisions successfully regardless.
	if res.IsError {
		t.Fatalf("create_mission with failing index must NOT return a tool error, got: %q", toolErrText(res))
	}
	var mv mission.MissionView
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &mv); err != nil {
		t.Fatalf("decode MissionView: %v", err)
	}
	if mv.ID == 0 {
		t.Fatal("expected a valid mission ID in the returned MissionView")
	}
}

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
