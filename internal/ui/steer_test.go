// SPDX-License-Identifier: Elastic-2.0

package ui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/principals"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/telemetry"
)

func steerPost(t *testing.T, srv *httptest.Server, path string, id int64) *http.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"id": id})
	res, err := http.Post(srv.URL+path, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return res
}

// TestPauseResumeCancelEndpoints covers POST /api/mission/pause|resume|cancel
// end to end: each transitions the mission's stored status, pause halts the
// claim path (queue.Store.MissionHaltReason) and resume clears it, and GET is
// rejected (POST only) — mirroring the prune endpoint's contract.
func TestPauseResumeCancelEndpoints(t *testing.T) {
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

	mid, err := mission.CreateMission(m, q, "steer via http", []mission.PhaseSpec{{Name: "build", Role: "builder", Instruction: "x"}}, false)
	if err != nil {
		t.Fatal(err)
	}

	// Roles is nil (dev mode) => isSuperuser is permissive, same as every
	// other admin-write test in this package.
	srv := httptest.NewServer(Handler(Deps{Queue: q, Missions: m, Telemetry: tel}))
	defer srv.Close()

	if res, _ := http.Get(srv.URL + "/api/mission/pause"); res.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET /api/mission/pause status = %d, want 405", res.StatusCode)
	}

	if res := steerPost(t, srv, "/api/mission/pause", mid); res.StatusCode != 200 {
		t.Fatalf("POST pause: status=%d", res.StatusCode)
	}
	if mi, err := m.Mission(mid); err != nil || mi.Status != "paused" {
		t.Fatalf("after pause: mission=%+v err=%v, want status=paused", mi, err)
	}
	if reason, err := q.MissionHaltReason(mid); err != nil || reason != "paused" {
		t.Fatalf("MissionHaltReason after pause = %q, %v; want paused, nil", reason, err)
	}

	if res := steerPost(t, srv, "/api/mission/resume", mid); res.StatusCode != 200 {
		t.Fatalf("POST resume: status=%d", res.StatusCode)
	}
	if mi, err := m.Mission(mid); err != nil || mi.Status != "running" {
		t.Fatalf("after resume: mission=%+v err=%v, want status=running", mi, err)
	}
	if reason, err := q.MissionHaltReason(mid); err != nil || reason != "" {
		t.Fatalf("MissionHaltReason after resume = %q, %v; want empty, nil", reason, err)
	}

	if res := steerPost(t, srv, "/api/mission/cancel", mid); res.StatusCode != 200 {
		t.Fatalf("POST cancel: status=%d", res.StatusCode)
	}
	if mi, err := m.Mission(mid); err != nil || mi.Status != "cancelled" {
		t.Fatalf("after cancel: mission=%+v err=%v, want status=cancelled", mi, err)
	}

	// The steering actions are attributed in telemetry so they surface in
	// history/replay.
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

// TestSteerEndpointsRefuseWithoutOperatorAuth proves the human gate: with a
// real principals store configured (auth ON) and no verified superuser
// principal on the request, pause/resume/cancel are all refused — mirroring
// TestPruneEndpointRefusesWithoutOperatorAuth.
func TestSteerEndpointsRefuseWithoutOperatorAuth(t *testing.T) {
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
	roles, err := principals.Open(filepath.Join(dir, "p.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer roles.Close()
	if err := roles.CreateSuperuser("owner@example.com", "test"); err != nil {
		t.Fatal(err)
	}

	mid, err := mission.CreateMission(m, q, "refuse via http", []mission.PhaseSpec{{Name: "build", Role: "builder", Instruction: "x"}}, false)
	if err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(Handler(Deps{Queue: q, Missions: m, Roles: roles}))
	defer srv.Close()

	for _, path := range []string{"/api/mission/pause", "/api/mission/resume", "/api/mission/cancel"} {
		res := steerPost(t, srv, path, mid)
		if res.StatusCode != http.StatusForbidden {
			t.Fatalf("POST %s without a verified superuser: status = %d, want 403", path, res.StatusCode)
		}
	}

	// Nothing was touched: still running, no halt recorded.
	if mi, err := m.Mission(mid); err != nil || mi.Status != "running" {
		t.Fatalf("refused steering must not have changed mission status: mission=%+v err=%v", mi, err)
	}
	if reason, err := q.MissionHaltReason(mid); err != nil || reason != "" {
		t.Fatalf("refused steering must not have halted the claim path: reason=%q err=%v", reason, err)
	}
}
