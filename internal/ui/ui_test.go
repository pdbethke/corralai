// SPDX-License-Identifier: Elastic-2.0

package ui

import (
	"bytes"
	"encoding/json"
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
