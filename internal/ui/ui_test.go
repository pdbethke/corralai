// SPDX-License-Identifier: Elastic-2.0

package ui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/artifacts"
	"github.com/pdbethke/corralai/internal/brain"
	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/learn"
	"github.com/pdbethke/corralai/internal/memory"
	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/telemetry"
)

// TestAgentWindowsStructure asserts that the served HTML contains all the
// structural markers required by the draggable multi-agent windows design:
//   - the #windows overlay layer
//   - openAgentWindow (opens/raises a window; called by selectAgent and canvas click)
//   - aw-ask-input (the persistent ask input created ONCE per window, outside .aw-body)
//   - renderAgentWindowBody (the tick-safe body updater — ask footer never touched here)
//   - askAgentWindow (per-window POST /api/ask)
//   - aw-badge (model badge in the title bar)
//
// These are structural/grep checks; real focus/drag acceptance is manual in-browser.
func TestAgentWindowsStructure(t *testing.T) {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(raw)
	markers := []string{
		`id="windows"`,
		`openAgentWindow`,
		`aw-ask-input`,
		`renderAgentWindowBody`,
		`askAgentWindow`,
		`aw-badge`,
	}
	for _, m := range markers {
		if !strings.Contains(html, m) {
			t.Errorf("index.html missing required marker: %q", m)
		}
	}
	// Verify the ask input is NOT inside the tick-updated body function.
	// renderAgentWindowBody must not contain 'aw-ask-input' (it only sets bodyEl.innerHTML).
	const fnStart = "function renderAgentWindowBody("
	const fnEnd = "function askAgentWindow("
	si := strings.Index(html, fnStart)
	ei := strings.Index(html, fnEnd)
	if si < 0 || ei < 0 || si >= ei {
		t.Fatalf("could not locate renderAgentWindowBody..askAgentWindow range in index.html")
	}
	bodyFn := html[si:ei]
	if strings.Contains(bodyFn, "aw-ask-input") {
		t.Error("renderAgentWindowBody must NOT reference aw-ask-input (the ask input must be outside the tick-updated body)")
	}
}

// The live execution feed reads recent_executions from /api/state, so the
// snapshot must carry the brain's execution ring (newest-first, with ok/exit).
func TestStateEndpointCarriesExecutions(t *testing.T) {
	cs, err := coord.Open(filepath.Join(t.TempDir(), "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	ring := brain.NewExecRing()
	ring.Add(brain.Execution{Agent: "Ada", Role: "builder", Command: "go build ./...", ExitCode: 0, Ok: true, Summary: "ok", TS: 1000})
	ring.Add(brain.Execution{Agent: "Boa", Role: "tester", Command: "go test ./...", ExitCode: 1, Ok: false, Summary: "FAIL", TS: 1001})

	h := Handler(Deps{Coord: cs, MemOwners: map[string]bool{}, Executions: ring})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var payload struct {
		Executions []struct {
			Agent    string `json:"agent"`
			Role     string `json:"role"`
			Command  string `json:"command"`
			ExitCode int    `json:"exit_code"`
			Ok       bool   `json:"ok"`
			Summary  string `json:"summary"`
		} `json:"recent_executions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Executions) != 2 {
		t.Fatalf("want 2 executions in /api/state, got %d", len(payload.Executions))
	}
	// newest-first: Boa's failing test run is first
	if e := payload.Executions[0]; e.Agent != "Boa" || e.Role != "tester" || e.Ok || e.ExitCode != 1 {
		t.Fatalf("newest execution not rendered with role/exit/ok: %+v", e)
	}
	if e := payload.Executions[1]; e.Agent != "Ada" || !e.Ok || e.Command != "go build ./..." {
		t.Fatalf("second execution not rendered: %+v", e)
	}
}

// The frontend reads server_now, parked_grace_seconds, and per-agent status from
// /api/state. This pins that JSON contract.
func TestStateEndpointCarriesParkedFields(t *testing.T) {
	cs, err := coord.Open(filepath.Join(t.TempDir(), "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	if _, err := cs.BootstrapSession("alice", "", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := cs.SetStatus("alice", "awaiting_approval"); err != nil {
		t.Fatal(err)
	}
	h := Handler(Deps{Coord: cs, MemOwners: map[string]bool{}})
	req := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var payload struct {
		ServerNow          float64 `json:"server_now"`
		ParkedGraceSeconds float64 `json:"parked_grace_seconds"`
		ActiveAgents       []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"active_agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.ServerNow <= 0 || payload.ParkedGraceSeconds <= 0 {
		t.Fatalf("server_now/parked_grace_seconds missing: %+v", payload)
	}
	var alice string
	for _, a := range payload.ActiveAgents {
		if a.Name == "alice" {
			alice = a.Status
		}
	}
	if alice != "awaiting_approval" {
		t.Fatalf("agent status not in /api/state, got %q", alice)
	}
}

// The UI renders the live task list + which bee owns each task, so /api/state
// must carry the queue's tasks with status + claimed_by.
func TestStateEndpointCarriesTasks(t *testing.T) {
	dir := t.TempDir()
	cs, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if err := q.Enqueue(1, []queue.TaskSpec{{Key: "build#1", Role: "builder", Title: "build", Instruction: "x"}}); err != nil {
		t.Fatal(err)
	}
	q.PromoteReady(1)
	if _, err := q.ClaimNext("Ada", []string{"builder"}, 300); err != nil {
		t.Fatal(err)
	}

	h := Handler(Deps{Coord: cs, MemOwners: map[string]bool{}, Queue: q})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var payload struct {
		Tasks []struct {
			Title     string `json:"title"`
			Status    string `json:"status"`
			ClaimedBy string `json:"claimed_by"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Tasks) != 1 {
		t.Fatalf("want 1 task in /api/state, got %d", len(payload.Tasks))
	}
	if tk := payload.Tasks[0]; tk.Status != "claimed" || tk.ClaimedBy != "Ada" {
		t.Fatalf("task not rendered with assignment: %+v", tk)
	}
}

// The UI shows findings (high-severity prominent), so /api/state must carry them
// with severity + reporter.
func TestStateEndpointCarriesFindings(t *testing.T) {
	dir := t.TempDir()
	cs, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if _, err := q.AddFinding(queue.Finding{MissionID: 1, Reporter: "Hawk", Type: "vuln", Severity: "high", Target: "api"}); err != nil {
		t.Fatal(err)
	}

	h := Handler(Deps{Coord: cs, MemOwners: map[string]bool{}, Queue: q})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var payload struct {
		Findings []struct {
			Severity string `json:"severity"`
			Reporter string `json:"reporter"`
			Type     string `json:"type"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Findings) != 1 {
		t.Fatalf("want 1 finding in /api/state, got %d", len(payload.Findings))
	}
	if f := payload.Findings[0]; f.Severity != "high" || f.Reporter != "Hawk" || f.Type != "vuln" {
		t.Fatalf("finding not rendered with severity/reporter: %+v", f)
	}
}

// The UI shows re-architecture, so /api/state tasks must carry status +
// supersedes lineage for cancelled/superseded tasks.
func TestStateEndpointCarriesLineage(t *testing.T) {
	dir := t.TempDir()
	cs, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	q.Enqueue(1, []queue.TaskSpec{{Key: "build", Role: "builder", Title: "build", Instruction: "old"}})
	q.PromoteReady(1)
	ts, _ := q.List(1)
	if _, err := q.SupersedeTask(ts[0].ID, queue.TaskSpec{Key: "build-v2", Role: "builder", Title: "build", Instruction: "rebuilt"}); err != nil {
		t.Fatal(err)
	}

	h := Handler(Deps{Coord: cs, MemOwners: map[string]bool{}, Queue: q})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	var payload struct {
		Tasks []struct {
			Key        string `json:"key"`
			Status     string `json:"status"`
			Supersedes int64  `json:"supersedes"`
		} `json:"tasks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	var oldSuperseded, newLineage bool
	for _, tk := range payload.Tasks {
		if tk.Key == "build" && tk.Status == "superseded" {
			oldSuperseded = true
		}
		if tk.Key == "build-v2" && tk.Supersedes != 0 {
			newLineage = true
		}
	}
	if !oldSuperseded || !newLineage {
		t.Fatalf("lineage not in /api/state: superseded=%v lineage=%v (%+v)", oldSuperseded, newLineage, payload.Tasks)
	}
}

// The Progress tab renders each mission's plan, so /api/state must carry the
// missions (id/directive/status).
func TestStateEndpointCarriesMissions(t *testing.T) {
	dir := t.TempDir()
	cs, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
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
	if _, err := mission.CreateMission(ms, q, "build me a world cup dashboard", nil, false); err != nil {
		t.Fatal(err)
	}

	h := Handler(Deps{Coord: cs, MemOwners: map[string]bool{}, Queue: q, Missions: ms})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	var payload struct {
		Missions []struct {
			ID        int64  `json:"id"`
			Directive string `json:"directive"`
			Status    string `json:"status"`
		} `json:"missions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Missions) != 1 {
		t.Fatalf("want 1 mission in /api/state, got %d", len(payload.Missions))
	}
	if m := payload.Missions[0]; m.Directive != "build me a world cup dashboard" || m.Status != "running" {
		t.Fatalf("mission not rendered: %+v", m)
	}
}

func TestStateEndpointCarriesModelComparison(t *testing.T) {
	dir := t.TempDir()
	cs, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	tel, err := telemetry.Open(filepath.Join(dir, "tel.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer tel.Close()
	tel.Record(telemetry.Event{Kind: "finding_reported", Model: "gpt-x", Detail: map[string]any{"severity": "high", "finding_id": "f1"}})
	tel.Record(telemetry.Event{Kind: "finding_resolved", Model: "gpt-x", Detail: map[string]any{"outcome": "addressed", "finding_id": "f1"}})

	h := Handler(Deps{Coord: cs, MemOwners: map[string]bool{}, Telemetry: tel})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var payload struct {
		ModelComparison struct {
			Columns []string `json:"columns"`
			Rows    [][]any  `json:"rows"`
		} `json:"model_comparison"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.ModelComparison.Rows) != 1 {
		t.Fatalf("want 1 model_comparison row in /api/state, got %d", len(payload.ModelComparison.Rows))
	}
	if got := payload.ModelComparison.Rows[0][0]; got != "gpt-x" {
		t.Fatalf("model = %v, want gpt-x", got)
	}
}

// The proposals card reads its data from /api/state, so the snapshot must
// carry pending learning-loop proposals with the fields the card renders
// (signature, count badge, guidance, skill-name chip, status). Only pending
// proposals are surfaced — approved/rejected proposals aren't awaiting an
// operator decision anymore.
func TestStateEndpointCarriesProposals(t *testing.T) {
	dir := t.TempDir()
	cs, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	ls, err := learn.Open(filepath.Join(dir, "l.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer ls.Close()

	p, _, err := ls.Upsert("missing-req|go.mod", "finding", "builder", []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ls.SetDraft(p.ID, "run go mod init first", "init-go-workspace", "# init-go-workspace\nsteps"); err != nil {
		t.Fatal(err)
	}

	h := Handler(Deps{Coord: cs, MemOwners: map[string]bool{}, Learn: ls})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var payload struct {
		Proposals []struct {
			ID        int64  `json:"id"`
			Signature string `json:"signature"`
			Count     int    `json:"count"`
			Guidance  string `json:"guidance"`
			SkillName string `json:"skill_name"`
			Status    string `json:"status"`
		} `json:"proposals"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Proposals) != 1 {
		t.Fatalf("want 1 pending proposal in /api/state, got %d: %+v", len(payload.Proposals), payload.Proposals)
	}
	pp := payload.Proposals[0]
	if pp.ID != p.ID || pp.Signature != "missing-req|go.mod" || pp.Count != 2 ||
		pp.Guidance != "run go mod init first" || pp.SkillName != "init-go-workspace" || pp.Status != "pending" {
		t.Fatalf("proposal not rendered with expected fields: %+v", pp)
	}
}

// POST /api/proposal/approve calls the Promote callback wired in Deps — here
// wired to the real brain.ApproveProposal over temp stores, proving the UI
// endpoint drives the same fan-out the MCP tool does. Only pending proposals
// remain in /api/state afterward.
func TestProposalApproveEndpointPromotesViaCallback(t *testing.T) {
	dir := t.TempDir()
	// brain.ApproveProposal fans guidance/skill into memory via
	// mem.Add(targetDir=""), which resolves through CORRALAI_MEMORY_DIR.
	// Give this test its own default-memory dir (package TestMain already
	// keeps it off the real ~/.claude directory, but this also isolates it
	// from any other test in this package that hits the same path).
	t.Setenv("CORRALAI_MEMORY_DIR", filepath.Join(dir, "default-mem"))
	cs, err := coord.Open(filepath.Join(dir, "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	ls, err := learn.Open(filepath.Join(dir, "l.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer ls.Close()
	mstore, err := memory.Open(filepath.Join(dir, "m.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer mstore.Close()
	astore, err := artifacts.Open(filepath.Join(dir, "a.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer astore.Close()

	p, _, err := ls.Upsert("missing-req|go.mod", "finding", "builder", []string{"a"})
	if err != nil {
		t.Fatal(err)
	}
	if err := ls.SetDraft(p.ID, "run go mod init first", "init-go-workspace", "# init-go-workspace\nsteps"); err != nil {
		t.Fatal(err)
	}

	promote := func(id int64, actor string) error {
		_, err := brain.ApproveProposal(ls, mstore, astore, nil, id, actor, false, false)
		return err
	}
	reject := func(id int64, reason string) error { return ls.Reject(id, reason) }

	h := Handler(Deps{Coord: cs, MemOwners: map[string]bool{}, Learn: ls, Promote: promote, Reject: reject})

	// Non-POST is rejected.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/proposal/approve", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /api/proposal/approve status = %d, want 405", rec.Code)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/proposal/reject", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET /api/proposal/reject status = %d, want 405", rec.Code)
	}

	body, _ := json.Marshal(map[string]any{"id": p.ID})
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/proposal/approve", bytes.NewReader(body)))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST /api/proposal/approve status = %d body=%s", rec.Code, rec.Body.String())
	}
	got, err := ls.ByID(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != learn.StatusApproved {
		t.Fatalf("proposal status after approve = %q, want approved", got.Status)
	}

	// The approved proposal no longer appears in /api/state's pending-only list.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/state", nil))
	var payload struct {
		Proposals []struct {
			ID int64 `json:"id"`
		} `json:"proposals"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Proposals) != 0 {
		t.Fatalf("approved proposal still listed as pending: %+v", payload.Proposals)
	}
}

// TestHistoryEndpoints covers the Completed tab's read surface: /api/history
// (list) and /api/history/{id} (drill-down), including the 404 for an
// unknown mission id.
func TestHistoryEndpoints(t *testing.T) {
	deps := Deps{
		History: func() ([]brain.MissionSummary, error) {
			return []brain.MissionSummary{{ID: 1, Directive: "ship it", Status: "done"}}, nil
		},
		HistoryDetail: func(id int64) (*brain.MissionDetail, error) {
			if id != 1 {
				return nil, nil
			}
			return &brain.MissionDetail{MissionSummary: brain.MissionSummary{ID: 1, Directive: "ship it", Status: "done"}}, nil
		},
	}
	srv := httptest.NewServer(Handler(deps))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/api/history")
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("GET /api/history: %v status=%v", err, res.StatusCode)
	}
	var listOut struct {
		Missions []brain.MissionSummary `json:"missions"`
	}
	if err := json.NewDecoder(res.Body).Decode(&listOut); err != nil || len(listOut.Missions) != 1 {
		t.Fatalf("decode: %v missions=%v", err, listOut.Missions)
	}

	res2, err := http.Get(srv.URL + "/api/history/1")
	if err != nil || res2.StatusCode != 200 {
		t.Fatalf("GET /api/history/1: %v status=%v", err, res2.StatusCode)
	}
	res3, _ := http.Get(srv.URL + "/api/history/999")
	if res3.StatusCode != 404 {
		t.Fatalf("unknown mission should 404, got %d", res3.StatusCode)
	}
}

// TestHistoryDetailExecutionsSnakeCase pins /api/history/{id}'s executions to
// snake_case keys — the API is snake_case everywhere else, and index.html's
// history/replay JS reads e.ok / e.ts / e.mission_id, not the Go struct's
// PascalCase field names.
func TestHistoryDetailExecutionsSnakeCase(t *testing.T) {
	deps := Deps{
		History: func() ([]brain.MissionSummary, error) { return nil, nil },
		HistoryDetail: func(id int64) (*brain.MissionDetail, error) {
			return &brain.MissionDetail{
				MissionSummary: brain.MissionSummary{ID: id, Directive: "ship it", Status: "done"},
				Executions: []queue.Execution{
					{MissionID: id, Agent: "bob", Role: "builder", Command: "go test ./...", ExitCode: 0, OK: true, TS: 1234},
				},
			}, nil
		},
	}
	srv := httptest.NewServer(Handler(deps))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/api/history/1")
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("GET /api/history/1: %v status=%v", err, res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)

	var payload struct {
		Mission struct {
			Executions []struct {
				MissionID int64  `json:"mission_id"`
				Agent     string `json:"agent"`
				Role      string `json:"role"`
				Command   string `json:"command"`
				ExitCode  int    `json:"exit_code"`
				OK        bool   `json:"ok"`
				TS        int64  `json:"ts"`
			} `json:"executions"`
		} `json:"mission"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode: %v body=%s", err, body)
	}
	if len(payload.Mission.Executions) != 1 {
		t.Fatalf("expected 1 execution, got %d body=%s", len(payload.Mission.Executions), body)
	}
	e := payload.Mission.Executions[0]
	if e.MissionID != 1 || e.Agent != "bob" || e.Role != "builder" || e.Command != "go test ./..." || e.ExitCode != 0 || !e.OK || e.TS != 1234 {
		t.Fatalf("execution decoded wrong via snake_case tags: %+v", e)
	}

	// Belt-and-suspenders: the raw JSON must not carry PascalCase keys, which
	// would mean the struct's json tags regressed.
	raw := string(body)
	for _, bad := range []string{`"MissionID"`, `"Agent"`, `"OK"`, `"TS"`, `"ExitCode"`, `"Command"`, `"Role"`} {
		if strings.Contains(raw, bad) {
			t.Fatalf("raw response still contains PascalCase key %s: %s", bad, raw)
		}
	}
}

// TestCompletedDetailGroupsTasksByPhaseName pins the Completed tab's
// phase/task grouping to the codebase's own precedent: mission.Store.View
// groups tasks by t.Title against p.Name (planToTasks: "Title carries the
// phase name so the View can group tasks back by phase"). Grouping by role
// double-counts whenever two phases share a role — which DefaultPlan does
// today (build-core and build are both "builder"). Structural check, same
// style as TestAgentWindowsStructure; the DOM-level proof is the seeded
// Playwright run in the task report.
func TestCompletedDetailGroupsTasksByPhaseName(t *testing.T) {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	html := string(raw)
	const fnStart = "function openMissionDetail("
	const fnEnd = "function closeMissionDetail("
	si := strings.Index(html, fnStart)
	ei := strings.Index(html, fnEnd)
	if si < 0 || ei < 0 || si >= ei {
		t.Fatalf("could not locate openMissionDetail..closeMissionDetail range in index.html")
	}
	fn := html[si:ei]
	if !strings.Contains(fn, "tasksByPhase[t.title]") {
		t.Error("openMissionDetail must group tasks by t.title (the phase name planToTasks stamps on every task)")
	}
	if !strings.Contains(fn, "tasksByPhase[p.name]") {
		t.Error("openMissionDetail must look up a phase's tasks by p.name (mirrors mission.Store.View)")
	}
	if strings.Contains(fn, "tasksByPhase[t.role]") || strings.Contains(fn, "tasksByPhase[p.role]") {
		t.Error("openMissionDetail must NOT group by role — DefaultPlan reuses \"builder\" across build-core and build, so role-keyed grouping double-counts")
	}
}

// TestReplayPlayerStructure is a structural/grep check (mirrors
// TestAgentWindowsStructure) over the served replay-player.js (and, for the
// pieces that stayed in index.html — the <script src> wiring, the live-SSE
// bootstrap, and setView — over index.html) for the required client
// globals/functions, the embed-friendly startReplay(streamOrUrl) indirection
// (per the binding design constraint: the player's data source is injected,
// not hard-coupled to a brain/SSE endpoint), and that none of the read-only
// replay functions issue a POST/mutating fetch.
func TestReplayPlayerStructure(t *testing.T) {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		t.Fatal(err)
	}
	rawPlayer, err := fs.ReadFile(sub, "replay-player.js")
	if err != nil {
		t.Fatal(err)
	}
	player := string(rawPlayer)
	rawIndex, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	indexHTML := string(rawIndex)

	playerMarkers := []string{
		`let replayEvents = [], replayIdx = 0, replayPlaying = false, replaySpeed = 1`,
		`function startReplay(streamOrUrl)`,
		`function openReplay(missionId)`,
		`function closeReplay()`,
		`function replayStep()`,
		`function applyReplayEvent(ev)`,
		`function seekReplay(target)`,
	}
	for _, m := range playerMarkers {
		if !strings.Contains(player, m) {
			t.Errorf("replay-player.js missing required replay marker: %q", m)
		}
	}
	indexMarkers := []string{`id="replay"`, `id="replay-scrub"`, `<script src="/replay-player.js"></script>`}
	for _, m := range indexMarkers {
		if !strings.Contains(indexHTML, m) {
			t.Errorf("index.html missing required marker: %q", m)
		}
	}
	// The extraction must not silently start a live connection from a
	// brain-less file, and the product page must still start one.
	if strings.Contains(player, "let es = connectSSE()") {
		t.Error("replay-player.js must not auto-invoke connectSSE at load — a static embed has no brain to connect to; the embedding page opts in via `es = connectSSE()`")
	}
	if !strings.Contains(indexHTML, "es = connectSSE();") {
		t.Error("index.html must still start live SSE (`es = connectSSE();`) now that connectSSE moved to replay-player.js")
	}

	// Embed-friendliness: startReplay must accept a URL OR an already-resolved
	// events object/array — that's what lets a static embed hand it a baked
	// JSON file instead of a live /api/replay URL.
	const fnStart = "function startReplay(streamOrUrl){"
	const fnEnd = "// openReplay:"
	si := strings.Index(player, fnStart)
	ei := strings.Index(player, fnEnd)
	if si < 0 || ei < 0 || si >= ei {
		t.Fatalf("could not locate startReplay..openReplay range in replay-player.js")
	}
	startFn := player[si:ei]
	if !strings.Contains(startFn, "typeof streamOrUrl === 'string'") {
		t.Error("startReplay must branch on typeof streamOrUrl — a URL is fetched, a plain object/array is used as-is (no hard-coupling to /api/replay)")
	}
	if strings.Contains(startFn, "/api/replay") {
		t.Error("startReplay itself must not hard-code /api/replay — that URL belongs only in openReplay's wrapper call")
	}

	// Read-only by construction: none of the replay functions may call a
	// mutating endpoint. In replay-player.js the block runs to EOF (it was
	// the last thing extracted).
	const rStart = "// ---- replay player ----"
	rsi := strings.Index(player, rStart)
	if rsi < 0 {
		t.Fatalf("could not locate the replay player block in replay-player.js")
	}
	replayBlock := player[rsi:]
	if strings.Contains(replayBlock, "method:'POST'") || strings.Contains(replayBlock, `method: 'POST'`) {
		t.Error("replay player block must be read-only — no POST/mutating fetch")
	}

	// The "#empty" caption ("no agents yet") is normally refreshed only inside
	// apply() (SSE-driven, stayed in index.html). During replay — and in the
	// static-embed path where apply() never runs at all — the replay pipeline
	// must refresh it itself, null-guarded for embeds that don't render the
	// element.
	const scrubStart = "function renderReplayScrub(){"
	ssi := strings.Index(player, scrubStart)
	if ssi < 0 {
		t.Fatalf("could not locate renderReplayScrub in replay-player.js")
	}
	sei := strings.Index(player[ssi:], "\n}")
	if sei < 0 {
		t.Fatalf("could not locate the end of renderReplayScrub in replay-player.js")
	}
	scrubFn := player[ssi : ssi+sei]
	if !strings.Contains(scrubFn, "getElementById('empty')") || !strings.Contains(scrubFn, "nodes.size") {
		t.Error("renderReplayScrub must refresh #empty's display from nodes.size — apply() never runs during replay/static-embed, so the stale caption would sit over replayed agents")
	}

	// Leaving replay via any tab must not orphan the session: setView tears
	// the session down whenever the target view isn't replay, via the
	// idempotent stopReplaySession(). stopReplaySession moved to
	// replay-player.js; setView (a full multi-tab dispatcher) stayed in
	// index.html — both are checked in their new homes.
	if !strings.Contains(player, "function stopReplaySession()") {
		t.Error("replay-player.js missing stopReplaySession() — the idempotent replay teardown (stop timer, resume SSE)")
	}
	const svStart = "function setView(v){"
	vsi := strings.Index(indexHTML, svStart)
	if vsi < 0 {
		t.Fatalf("could not locate setView in index.html")
	}
	vei := strings.Index(indexHTML[vsi:], "\n}")
	if vei < 0 {
		t.Fatalf("could not locate the end of setView in index.html")
	}
	setViewFn := indexHTML[vsi : vsi+vei]
	if !strings.Contains(setViewFn, "stopReplaySession()") {
		t.Error("setView must call stopReplaySession() when navigating to a non-replay view — otherwise switching tabs mid-replay leaves the timer running and SSE closed with no visible control")
	}
}

// TestReplayEndpoint asserts the read-only /api/replay endpoint: a valid
// mission returns its reconstructed beat stream as JSON, a store error
// surfaces as 500, and a missing/non-numeric mission param 400s — exactly
// the same request-shape as /api/history/{id}, just fed by Deps.Replay
// (wired to brain.BuildReplayStream in cmd/corral/main.go).
func TestReplayEndpoint(t *testing.T) {
	deps := Deps{
		Replay: func(missionID int64) ([]brain.ReplayEvent, error) {
			if missionID != 5 {
				return nil, fmt.Errorf("no such mission")
			}
			return []brain.ReplayEvent{{TS: 1, Kind: "task_claimed", Subject: "build"}}, nil
		},
	}
	srv := httptest.NewServer(Handler(deps))
	defer srv.Close()

	res, err := http.Get(srv.URL + "/api/replay?mission=5")
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("GET /api/replay?mission=5: %v status=%v", err, res.StatusCode)
	}
	var out struct {
		Events []brain.ReplayEvent `json:"events"`
	}
	if err := json.NewDecoder(res.Body).Decode(&out); err != nil || len(out.Events) != 1 {
		t.Fatalf("decode: %v events=%v", err, out.Events)
	}

	if res2, _ := http.Get(srv.URL + "/api/replay?mission=999"); res2.StatusCode != 500 {
		t.Fatalf("store error should surface as 500, got %d", res2.StatusCode)
	}
	if res3, _ := http.Get(srv.URL + "/api/replay"); res3.StatusCode != 400 {
		t.Fatalf("missing mission param should 400, got %d", res3.StatusCode)
	}
}
