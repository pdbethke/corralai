// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/controlgate"
	"github.com/pdbethke/corralai/internal/controlspec"
	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/memory"
	"github.com/pdbethke/corralai/internal/principals"
)

func TestStageControl(t *testing.T) {
	store, err := controlspec.OpenStore(filepath.Join(t.TempDir(), "cs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Unix(1_700_000_000, 0).UTC()
	if err := store.SaveGoal(controlspec.Goal{ID: "asvs-v2.1.1", Owner: "o@x", Intent: "passwords >= 12 chars", CreatedTS: now}); err != nil {
		t.Fatal(err)
	}

	var gotReq controlgate.StageRequest
	stage := func(_ context.Context, req controlgate.StageRequest) (controlspec.GateTest, error) {
		gotReq = req
		return controlspec.GateTest{Owner: req.Owner, Goal: req.GoalID, Target: req.Target,
			Test: "package control\n// t", KillRate: 0.8, Survived: []string{"m2"},
			CodePath: req.CodePath, TestPath: req.TestPath, CreatedTS: req.Now}, nil
	}
	out, err := stageControl(context.Background(), store, stage, "o@x", "asvs-v2.1.1",
		"internal/auth/login.go", "package control\n// code", "go", "login.go", "login_control_test.go", 3, now)
	if err != nil {
		t.Fatal(err)
	}
	// The staged request carries the goal INTENT (not the id), the paths, and the go scaffold.
	if gotReq.Goal != "passwords >= 12 chars" || gotReq.CodePath != "login.go" || gotReq.TestPath != "login_control_test.go" {
		t.Fatalf("staged request wrong: %+v", gotReq.Request)
	}
	if gotReq.Base["go.mod"] == "" || len(gotReq.TestCmd) == 0 {
		t.Fatalf("go scaffold not applied: %+v", gotReq.Request)
	}
	if out.KillRate != 0.8 || out.Vetted {
		t.Fatalf("summary wrong: %+v", out)
	}
	// Missing goal → error, stage untouched.
	if _, err := stageControl(context.Background(), store, stage, "o@x", "nope", "t", "c", "go", "a.go", "a_test.go", 3, now); err == nil {
		t.Fatal("missing goal must error")
	}
}

func TestStageControl_ClampsMutantCeiling(t *testing.T) {
	store, _ := controlspec.OpenStore(filepath.Join(t.TempDir(), "cs.db"))
	defer store.Close()
	now := time.Unix(1_700_000_000, 0).UTC()
	_ = store.SaveGoal(controlspec.Goal{ID: "g1", Owner: "o@x", Intent: "x", CreatedTS: now})
	var gotReq controlgate.StageRequest
	stage := func(_ context.Context, req controlgate.StageRequest) (controlspec.GateTest, error) {
		gotReq = req
		return controlspec.GateTest{Owner: req.Owner, Goal: req.GoalID, Target: req.Target, CreatedTS: req.Now}, nil
	}
	if _, err := stageControl(context.Background(), store, stage, "o@x", "g1", "a.go", "c", "go", "a.go", "a_test.go", 10000, now); err != nil {
		t.Fatal(err)
	}
	if gotReq.NMutants != maxControlMutants {
		t.Fatalf("n_mutants=10000 must clamp to %d, got %d", maxControlMutants, gotReq.NMutants)
	}
}

func TestGetControl(t *testing.T) {
	store, err := controlspec.OpenStore(filepath.Join(t.TempDir(), "cs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Unix(1_700_000_000, 0).UTC()
	_ = store.SaveCandidate(controlspec.GateTest{Owner: "o@x", Goal: "g1", Target: "a.go", Test: "T1", KillRate: 1, CreatedTS: now})
	_ = store.SaveCandidate(controlspec.GateTest{Owner: "o@x", Goal: "g2", Target: "b.go", Test: "T2", KillRate: 1, CreatedTS: now})

	gt, err := getControl(store, "o@x", "g2", "b.go")
	if err != nil || gt.Test != "T2" {
		t.Fatalf("getControl should return the g2 candidate: %+v %v", gt, err)
	}
	if _, err := getControl(store, "o@x", "nope", "x"); err == nil {
		t.Fatal("absent candidate must error")
	}
}

// TestPromoteControlRequiresAdmin proves promote_control is admin-gated the
// same way promote_memory is (verbatim copy of that gate): an unauthenticated
// caller against a Principals store with a real superuser seeded is refused a
// tool error.
func TestPromoteControlRequiresAdmin(t *testing.T) {
	dir := t.TempDir()
	cstore, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cstore.Close() })
	mstore, err := memory.Open(filepath.Join(dir, "m.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { mstore.Close() })
	pstore, err := principals.Open(filepath.Join(dir, "p.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pstore.Close() })
	if err := pstore.CreateSuperuser("real-admin@example.com", "test"); err != nil {
		t.Fatal(err)
	}
	cspec, err := controlspec.OpenStore(filepath.Join(dir, "cs.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cspec.Close() })

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() {
		_ = NewServer(cstore, mstore, Options{Principals: pstore, ControlSpec: cspec}).Run(ctx, serverT)
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "t1", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("connect non-admin: %v", err)
	}
	defer sess.Close()

	res, err := sess.CallTool(ctx, &mcp.CallToolParams{Name: "promote_control", Arguments: map[string]any{
		"goal": "some-goal", "target": "some/target.go",
	}})
	if err != nil {
		t.Fatalf("promote_control non-admin call: %v", err)
	}
	if !res.IsError {
		t.Fatal("want tool error for non-admin promote_control, got success")
	}
}
