// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/principals"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/telemetry"
)

// steerTestBrain boots a brain with real mission/queue/telemetry stores (and,
// when roles != nil, a Principals store so isHumanAdmin is a real gate rather
// than dev mode's permissive nil-Roles fallback) — the shared setup for #58's
// pause_mission/resume_mission/cancel_mission tests.
func steerTestBrain(t *testing.T, roles *principals.Store) (*mcp.ClientSession, *mission.Store, *queue.Store, *telemetry.Store) {
	t.Helper()
	dir := t.TempDir()
	cstore, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cstore.Close() })
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

	ctx := context.Background()
	clientT, serverT := mcp.NewInMemoryTransports()
	go func() {
		_ = NewServer(cstore, nil, Options{Queue: q, Missions: m, Telemetry: tel, Principals: roles}).Run(ctx, serverT)
	}()
	client := mcp.NewClient(&mcp.Implementation{Name: "steer-test", Version: "0"}, nil)
	sess, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess, m, q, tel
}

func steerCall(t *testing.T, sess *mcp.ClientSession, tool string, id int64) *mcp.CallToolResult {
	t.Helper()
	res, err := sess.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: map[string]any{"id": id}})
	if err != nil {
		t.Fatalf("%s call: %v", tool, err)
	}
	return res
}

// TestPauseResumeCancelMissionEndToEnd drives #58's three steering verbs over
// MCP (dev mode: nil Principals => isHumanAdmin is permissive), asserting the
// mission's stored status transitions AND that each action is attributed in
// telemetry (so it surfaces in history/replay via BuildReplayStream).
func TestPauseResumeCancelMissionEndToEnd(t *testing.T) {
	sess, m, q, tel := steerTestBrain(t, nil)

	mid, err := mission.CreateMission(m, q, "steer via mcp", []mission.PhaseSpec{{Name: "build", Role: "builder", Instruction: "build it"}}, false)
	if err != nil {
		t.Fatal(err)
	}

	if res := steerCall(t, sess, "pause_mission", mid); res.IsError {
		t.Fatalf("pause_mission errored: %s", contentText(res))
	}
	mi, err := m.Mission(mid)
	if err != nil || mi == nil || mi.Status != "paused" {
		t.Fatalf("after pause_mission: mission=%+v err=%v, want status=paused", mi, err)
	}
	if reason, err := q.MissionHaltReason(mid); err != nil || reason != "paused" {
		t.Fatalf("MissionHaltReason after pause_mission = %q, %v; want paused, nil", reason, err)
	}

	if res := steerCall(t, sess, "resume_mission", mid); res.IsError {
		t.Fatalf("resume_mission errored: %s", contentText(res))
	}
	mi, err = m.Mission(mid)
	if err != nil || mi == nil || mi.Status != "running" {
		t.Fatalf("after resume_mission: mission=%+v err=%v, want status=running", mi, err)
	}

	if res := steerCall(t, sess, "cancel_mission", mid); res.IsError {
		t.Fatalf("cancel_mission errored: %s", contentText(res))
	}
	mi, err = m.Mission(mid)
	if err != nil || mi == nil || mi.Status != "cancelled" {
		t.Fatalf("after cancel_mission: mission=%+v err=%v, want status=cancelled", mi, err)
	}

	// Every action must be attributed in telemetry so it shows in history/replay.
	events, err := tel.EventsForMission(mid)
	if err != nil {
		t.Fatal(err)
	}
	wantKinds := map[string]bool{"mission_pause": false, "mission_resume": false, "mission_cancel": false}
	for _, e := range events {
		if _, ok := wantKinds[e.Kind]; ok {
			wantKinds[e.Kind] = true
		}
	}
	for kind, seen := range wantKinds {
		if !seen {
			t.Fatalf("telemetry missing a %q event for mission %d; got %+v", kind, mid, events)
		}
	}
}

// TestSteerMissionRequiresSuperuser proves the human gate is not decorative:
// with a real Principals store (auth on) and no verified superuser on the
// request, all three steering verbs are refused — mirroring
// TestApproveProposalRequiresSuperuser's pattern.
func TestSteerMissionRequiresSuperuser(t *testing.T) {
	dir := t.TempDir()
	roles, err := principals.Open(filepath.Join(dir, "p.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { roles.Close() })
	if err := roles.CreateSuperuser("real-admin@example.com", "test"); err != nil {
		t.Fatal(err)
	}

	sess, m, q, _ := steerTestBrain(t, roles)
	mid, err := mission.CreateMission(m, q, "refuse me", []mission.PhaseSpec{{Name: "build", Role: "builder", Instruction: "build it"}}, false)
	if err != nil {
		t.Fatal(err)
	}

	for _, tool := range []string{"pause_mission", "resume_mission", "cancel_mission"} {
		res := steerCall(t, sess, tool, mid)
		if !res.IsError {
			t.Fatalf("%s by a non-admin was accepted; want refusal", tool)
		}
	}

	// Nothing was touched: still running, no halt recorded.
	mi, err := m.Mission(mid)
	if err != nil || mi == nil || mi.Status != "running" {
		t.Fatalf("refused steering must not have changed mission status: mission=%+v err=%v", mi, err)
	}
	if reason, err := q.MissionHaltReason(mid); err != nil || reason != "" {
		t.Fatalf("refused steering must not have halted the claim path: reason=%q err=%v", reason, err)
	}
}
