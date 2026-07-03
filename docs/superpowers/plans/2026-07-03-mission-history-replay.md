# Mission History + Replay Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every finished mission gets a Completed tab (list + detail, best-effort learned-linkage) and a read-only replay viewer that reconstructs the whole build from durable rows and plays it back on the corral canvas at up to 16×, plus richer telemetry recording so future missions replay with more ambience.

**Architecture:** Part C adds recording seams — a new `mission_completed` telemetry kind (engine + review-accept paths), a `findings.resolved_ts` column stamp, and new `agent_activity` / `claim_made` / `claim_released` / `host_seen` / `memory_written` kinds emitted at the spots that already feed the ephemeral rings — so the append-only telemetry event log becomes the one place ambience lives. Part A adds a read-only `internal/brain` surface (`mission_history` list + detail, mirroring `mission_analytics`'s shape) and a skin-aware Completed tab. Part B adds a merged, time-ordered replay reconstruction (`internal/brain/replay.go`) served over `/api/replay`, and a player mode in the existing canvas that pauses live SSE, replays through the same render path at variable speed, and recomputes positions client-side (never recorded).

**Tech Stack:** Go 1.26, modernc.org/sqlite (queue/mission), DuckDB (telemetry), existing `internal/ui` Deps-injection pattern, vanilla JS canvas renderer in `internal/ui/web/index.html`.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-07-03-mission-history-replay-design.md` — read it first.
- SPDX header `// SPDX-License-Identifier: Elastic-2.0` on every new file.
- `go vet ./...` and gosec clean (annotate deliberate patterns with `// #nosec Gxxx -- reason`); existing suites stay green throughout.
- Corral voice per-skin: never bee/hive language outside the `hive` skin; any new flavored string (tab labels, empty-states, replay copy) is a per-skin field in `SKINS`, never a bare literal.
- Every commit message ends with `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`.
- House test style: table-lite, `t.Fatalf` with got/want, clock seam via `var now = func() float64` where a package already has one (`internal/queue`, `internal/telemetry`) — don't add a new seam to a package that lacks one; work around it (Task 7's golden test asserts order/shape, not literal timestamps, for exactly this reason).
- TDD per task: failing test → minimal implementation → green → commit.
- Metadata-only telemetry discipline: no directive/instruction/body text in any `detail` payload beyond what existing kinds already carry; the fleet-sync column allowlist (`internal/fleet`) is untouched by this plan — every new kind still fits the existing `events(id,ts,mission_id,kind,actor,subject,model,detail)` shape.
- Spec-locked decisions (do not reopen): Part B works durable-only on already-recorded missions; graceful degradation when ambience kinds are absent; canvas positions are recomputed, never recorded; live SSE pauses during replay; replay is read-only by construction.

---

### Task 1: `mission_completed` telemetry — engine hook + review-accept emission

**Files:**
- Modify: `internal/mission/engine.go:75-125` (Engine struct — add `OnMissionCompleted` field), `internal/mission/engine.go:213-222` (Tick's auto-complete branch — fire the hook)
- Modify: `internal/brain/missions.go:269-282` (`review_mission` handler — emit `mission_completed` on the client-accept path, alongside the existing `review_accepted`/`review_changes` emission)
- Modify: `cmd/corral/main.go` (wire `engine.OnMissionCompleted` next to the existing `engine.OnFindingResolved` wiring at lines 621-629)
- Test: `internal/mission/engine_test.go` (new test), `internal/brain/missions_test.go` (new test)

**Interfaces:**
- Consumes: `rec(tel *telemetry.Store, missionID int64, kind, actor, subject string, detail map[string]any)` (`internal/brain/telemetry.go:16-23`, unchanged); `mission.Store.Mission(id int64) (*Mission, error)` (unchanged, returns `ReviewRounds`).
- Produces: `Engine.OnMissionCompleted func(missionID int64, status string, reviewRounds int)` (nil-safe: engine.go never calls it if nil); telemetry kind `mission_completed` with `Detail: map[string]any{"status": status, "review_rounds": reviewRounds}`, `Actor: "engine"`.

- [ ] **Step 1: Write the failing engine test**

```go
// in internal/mission/engine_test.go — add alongside TestMissionPipelinePull
func TestEngineFiresOnMissionCompleted(t *testing.T) {
	dir := t.TempDir()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	m, err := Open(filepath.Join(dir, "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	mid, err := CreateMission(m, q, "add a wishlist feature", nil, false) // default pipeline, no review
	if err != nil {
		t.Fatal(err)
	}
	e := NewEngine(m, q)
	var calls int
	var gotID int64
	var gotStatus string
	var gotRounds int
	e.OnMissionCompleted = func(missionID int64, status string, reviewRounds int) {
		calls++
		gotID, gotStatus, gotRounds = missionID, status, reviewRounds
	}

	done := false
	for i := 0; i < 60 && !done; i++ {
		drain(t, q)
		_ = e.Tick()
		if mv, _ := m.Mission(mid); mv != nil && mv.Status == "done" {
			done = true
		}
	}
	if !done {
		t.Fatal("mission did not converge")
	}
	if calls != 1 {
		t.Fatalf("OnMissionCompleted should fire exactly once, got %d calls", calls)
	}
	if gotID != mid || gotStatus != "done" {
		t.Fatalf("got id=%d status=%q, want id=%d status=done", gotID, gotStatus, mid)
	}
	if gotRounds != 0 {
		t.Fatalf("non-review mission should report review_rounds=0, got %d", gotRounds)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/mission/ -run TestEngineFiresOnMissionCompleted -count=1`
Expected: FAIL (`Engine` has no field `OnMissionCompleted`)

- [ ] **Step 3: Add the hook field and fire it**

In `internal/mission/engine.go`, add the field right after `OnFindingResolved` in the `Engine` struct (~line 108):

```go
	// OnMissionCompleted, when non-nil, fires whenever the ENGINE (not the
	// review-accept path — see mission.SubmitReview's caller) transitions a
	// mission to "done". The caller wires it to telemetry so an auto-completed
	// mission speaks mission_completed the same way a reviewed one does at its
	// own call site — mission.Store never imports telemetry, so this is the
	// engine's half of that split.
	OnMissionCompleted func(missionID int64, status string, reviewRounds int)
```

Then in `Tick()`, replace the auto-complete branch (~line 217-222):

```go
			} else {
				_ = e.m.SetMissionStatus(mi.ID, "done")
				if e.OnMissionCompleted != nil {
					e.OnMissionCompleted(mi.ID, "done", mi.ReviewRounds)
				}
				if e.Repo != nil {
					e.finishRepoMission(mi.ID) // fast path
				}
			}
```

- [ ] **Step 4: Run to verify green**

Run: `go test ./internal/mission/ -count=1`
Expected: PASS

- [ ] **Step 5: Write the failing brain test for the review-accept path**

```go
// in internal/brain/missions_test.go — add alongside existing tests
func TestReviewMissionAcceptEmitsMissionCompleted(t *testing.T) {
	c := newCohort(t, 1)
	// (newCohort's default NewServer(cs, nil, Options{}) has no Missions/Queue/Telemetry —
	// this test needs its own server; mirror missions_test.go's existing setup for a
	// review-enabled mission if one exists, or build one directly:)
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

	srv := brain.NewServer(nil, nil, brain.Options{Missions: ms, Queue: q, Telemetry: tel})
	// call review_mission{id, accept:true} over the harness, or directly via
	// mission.SubmitReview + the same telemetry emission this task adds:
	if _, err := mission.SubmitReview(ms, q, mid, true, "", "client"); err != nil {
		t.Fatal(err)
	}
	_ = srv // registration is exercised by the wire suite; here we assert the emission directly:
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
```

(This test calls `mission.SubmitReview` directly rather than through the MCP tool, so it fails until Step 6 wires the emission at the `review_mission` handler — the emission is the thing under test, and the handler is the only place that fires it, so run the assertion after invoking the handler's logic; if your harness makes the direct-store call insufficient, replace the last block with an MCP call through `newCohort`-style wiring using `brain.NewServer` + a real `mcp.Client`, matching `TestHarnessSmoke`.)

- [ ] **Step 6: Run to verify failure**

Run: `go test ./internal/brain/ -run TestReviewMissionAcceptEmitsMissionCompleted -count=1`
Expected: FAIL (no `mission_completed` row)

- [ ] **Step 7: Emit at the review-accept call site**

In `internal/brain/missions.go`, replace the `review_mission` handler body (lines 271-282):

```go
		func(_ context.Context, req *mcp.CallToolRequest, in reviewMissionIn) (*mcp.CallToolResult, mission.MissionView, error) {
			mv, err := mission.SubmitReview(store, q, in.ID, in.Accept, in.Feedback, identity(req, "client"))
			if err != nil {
				return nil, mission.MissionView{}, err
			}
			kind := "review_changes"
			if in.Accept {
				kind = "review_accepted"
			}
			rec(tel, in.ID, kind, identity(req, "client"), "", nil)
			if mv.Status == "done" {
				rounds := 0
				if full, ferr := store.Mission(in.ID); ferr == nil && full != nil {
					rounds = full.ReviewRounds
				}
				rec(tel, in.ID, "mission_completed", "engine", "", map[string]any{"status": "done", "review_rounds": rounds})
			}
			return nil, *mv, nil
		})
```

- [ ] **Step 8: Run to verify green**

Run: `go test ./internal/brain/ ./internal/mission/ -count=1`
Expected: PASS

- [ ] **Step 9: Wire the engine hook in main.go**

In `cmd/corral/main.go`, right after the existing `engine.OnFindingResolved = func(...) {...}` block (~line 629):

```go
	// mission_completed: the engine finally speaks telemetry on its own
	// auto-complete path, mirroring the review-accept emission in
	// internal/brain/missions.go so model_comparison/mission_history never
	// have to guess whether a mission finished.
	engine.OnMissionCompleted = func(missionID int64, status string, reviewRounds int) {
		if err := telStore.Record(telemetry.Event{
			MissionID: missionID, Kind: "mission_completed", Actor: "engine",
			Detail: map[string]any{"status": status, "review_rounds": reviewRounds},
		}); err != nil {
			log.Printf("telemetry mission_completed: %v", err)
		}
	}
```

- [ ] **Step 10: Build + full test**

Run: `go build ./... && go test ./internal/mission/ ./internal/brain/ -count=1`
Expected: PASS

- [ ] **Step 11: Commit**

```bash
git add internal/mission/engine.go internal/brain/missions.go internal/mission/engine_test.go internal/brain/missions_test.go cmd/corral/main.go && git commit -m "feat(telemetry): mission_completed — the engine finally speaks telemetry on both completion paths

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: `findings.resolved_ts` column stamp

**Files:**
- Modify: `internal/queue/store.go:141-150` (idempotent-migration list — add the new column)
- Modify: `internal/queue/findings.go` (`Finding` struct, `findingsSelect`, `queryFindings`, `SetFindingStatus`)
- Test: `internal/queue/findings_test.go` (new test)

**Interfaces:**
- Consumes: nothing new.
- Produces: `Finding.ResolvedTS float64 \`json:"resolved_ts,omitempty"\`` (0 = unresolved); `SetFindingStatus` unchanged signature `(id int64, status string) (bool, error)`, now stamps `resolved_ts=now()` when `status` is `addressed` or `dismissed`, and leaves it untouched (does not clear) on any other transition.

- [ ] **Step 1: Write the failing test**

```go
// in internal/queue/findings_test.go
func TestSetFindingStatusStampsResolvedTS(t *testing.T) {
	s := openStore(t) // existing test helper in this file; if named differently, use it
	id, err := s.AddFinding(Finding{MissionID: 1, Reporter: "bee1", Type: "bug", Severity: "high", Target: "x.go"})
	if err != nil {
		t.Fatal(err)
	}
	f, ok, _ := s.FindingByID(id)
	if !ok || f.ResolvedTS != 0 {
		t.Fatalf("freshly-opened finding must have resolved_ts=0, got %v", f.ResolvedTS)
	}
	if ok, err := s.SetFindingStatus(id, FindingAddressed); err != nil || !ok {
		t.Fatalf("SetFindingStatus: ok=%v err=%v", ok, err)
	}
	f2, _, _ := s.FindingByID(id)
	if f2.ResolvedTS == 0 {
		t.Fatal("resolved finding must have a non-zero resolved_ts")
	}
	if f2.ResolvedTS < f.CreatedTS {
		t.Fatalf("resolved_ts %v must be >= created_ts %v", f2.ResolvedTS, f.CreatedTS)
	}
}
```

(If `internal/queue/findings_test.go` uses a different store-opening helper than `openStore`, use whichever helper the file already defines — check the top of the file before writing this step.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/queue/ -run TestSetFindingStatusStampsResolvedTS -count=1`
Expected: FAIL (`Finding` has no field `ResolvedTS`)

- [ ] **Step 3: Migrate the schema**

In `internal/queue/store.go`, add to the idempotent-migration list (~line 147):

```go
		`ALTER TABLE findings ADD COLUMN resolved_ts REAL NOT NULL DEFAULT 0`,
```

- [ ] **Step 4: Add the field, projection, and stamp**

In `internal/queue/findings.go`:

```go
// Finding struct — add after CreatedTS:
	CreatedTS       float64 `json:"created_ts"`
	ResolvedTS      float64 `json:"resolved_ts,omitempty"` // set when status transitions to addressed|dismissed
```

```go
const findingsSelect = `SELECT id,mission_id,task_id,reporter,type,severity,target,evidence,suggested_action,status,recurring,created_ts,reporter_model,reporter_backend,resolved_ts FROM findings`
```

```go
func (s *Store) queryFindings(q string, args ...any) ([]Finding, error) {
	rows, err := s.db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Finding
	for rows.Next() {
		var f Finding
		var target, evidence, action sql.NullString
		var recurring int
		if err := rows.Scan(&f.ID, &f.MissionID, &f.TaskID, &f.Reporter, &f.Type, &f.Severity,
			&target, &evidence, &action, &f.Status, &recurring, &f.CreatedTS,
			&f.ReporterModel, &f.ReporterBackend, &f.ResolvedTS); err != nil {
			return nil, err
		}
		f.Target, f.Evidence, f.SuggestedAction = target.String, evidence.String, action.String
		f.Recurring = recurring == 1
		out = append(out, f)
	}
	return out, rows.Err()
}

// SetFindingStatus transitions a finding (open|addressed|dismissed). Returns
// false if no such finding. Validates the target status. Stamps resolved_ts
// the first time a finding leaves "open" — it is never cleared by a later
// transition, so it always reflects when the finding was FIRST resolved.
func (s *Store) SetFindingStatus(id int64, status string) (bool, error) {
	if !findingStatuses[status] {
		return false, fmt.Errorf("invalid status %q (want open|addressed|dismissed)", status)
	}
	var res sql.Result
	var err error
	if status == FindingOpen {
		res, err = s.db.Exec(`UPDATE findings SET status=? WHERE id=?`, status, id)
	} else {
		res, err = s.db.Exec(`UPDATE findings SET status=?, resolved_ts=CASE WHEN resolved_ts=0 THEN ? ELSE resolved_ts END WHERE id=?`,
			status, now(), id)
	}
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}
```

- [ ] **Step 5: Run to verify green**

Run: `go test ./internal/queue/ -count=1`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/queue/store.go internal/queue/findings.go internal/queue/findings_test.go && git commit -m "feat(queue): stamp findings.resolved_ts on first resolution — the row is no longer timeline-blind

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: `agent_activity` telemetry kind + per-mission cap

**Files:**
- Modify: `internal/brain/activity.go` (`registerActivity` — take `Options`, add `MissionID` to the tool input, emit telemetry with a cap)
- Modify: `internal/telemetry/store.go` (new `CountKind` accessor)
- Modify: `internal/brain/server.go:251` (call-site signature change)
- Test: `internal/telemetry/store_test.go` (new test), `internal/brain/activity_test.go` (new file)

**Interfaces:**
- Consumes: `rec(tel, missionID, kind, actor, subject, detail)` (unchanged).
- Produces: `func (s *telemetry.Store) CountKind(missionID int64, kind string) (int, error)`; `registerActivity(s *mcp.Server, ring *ActivityRing, opts Options)` (was `(s, ring)`); telemetry kind `agent_activity` with `Detail: map[string]any{"role": ..., "tool": ..., "detail": ...}` (the same one-line summary already carried by `Activity.Detail` — never a directive/instruction body); cap constant `const agentActivityCap = 2000`.

- [ ] **Step 1: Write the failing telemetry test**

```go
// in internal/telemetry/store_test.go
func TestCountKind(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "t.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for i := 0; i < 3; i++ {
		if err := s.Record(Event{MissionID: 7, Kind: "agent_activity", Actor: "bee1"}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.Record(Event{MissionID: 8, Kind: "agent_activity", Actor: "bee2"}); err != nil {
		t.Fatal(err)
	}
	n, err := s.CountKind(7, "agent_activity")
	if err != nil || n != 3 {
		t.Fatalf("CountKind(7): n=%d err=%v, want 3", n, err)
	}
	if n, _ := s.CountKind(8, "agent_activity"); n != 1 {
		t.Fatalf("CountKind(8): got %d, want 1", n)
	}
	if n, _ := s.CountKind(9, "agent_activity"); n != 0 {
		t.Fatalf("CountKind(9): got %d, want 0", n)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/telemetry/ -run TestCountKind -count=1`
Expected: FAIL (`CountKind` undefined)

- [ ] **Step 3: Implement `CountKind`**

In `internal/telemetry/store.go`, after `AgentTimeline`:

```go
// CountKind returns how many events of kind have been recorded for a mission
// — used to enforce per-mission volume guards (e.g. agent_activity's cap)
// without keeping an in-memory counter that would reset on restart.
func (s *Store) CountKind(missionID int64, kind string) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE mission_id=? AND kind=?`, missionID, kind).Scan(&n)
	return n, err
}
```

- [ ] **Step 4: Run to verify green**

Run: `go test ./internal/telemetry/ -count=1`
Expected: PASS

- [ ] **Step 5: Write the failing brain test for the cap**

```go
// in internal/brain/activity_test.go
package brain

import (
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/telemetry"
)

func TestReportActivityRecordsAndCaps(t *testing.T) {
	ring := NewActivityRing()
	tel, err := telemetry.Open(filepath.Join(t.TempDir(), "t.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer tel.Close()
	// Directly exercise the recording helper this task adds (not the MCP
	// round-trip — that's covered by the wire suite): recordActivity is the
	// function registerActivity's handler calls after ring.Add.
	for i := 0; i < agentActivityCap+5; i++ {
		recordActivity(tel, ring, 42, Activity{Agent: "bee1", Role: "builder", Tool: "run_command", Detail: "go build"})
	}
	n, err := tel.CountKind(42, "agent_activity")
	if err != nil {
		t.Fatal(err)
	}
	if n != agentActivityCap {
		t.Fatalf("agent_activity must be capped at %d, got %d", agentActivityCap, n)
	}
}
```

- [ ] **Step 6: Run to verify failure**

Run: `go test ./internal/brain/ -run TestReportActivityRecordsAndCaps -count=1`
Expected: FAIL (`recordActivity`/`agentActivityCap` undefined)

- [ ] **Step 7: Implement the capped emission**

In `internal/brain/activity.go`, add after `Add`/`Recent` and before `registerActivity`:

```go
// agentActivityCap bounds how many agent_activity events one mission can
// record — the cap protects the telemetry log, the log protects replay.
const agentActivityCap = 2000

// recordActivity emits an agent_activity telemetry event for a, gated by
// agentActivityCap. tel or missionID==0 is a no-op (activity reported outside
// a mission, or telemetry disabled, is observability-only and never durable).
// Crossing the cap logs loudly exactly once so an unbounded mission's silence
// is visible, not silent.
func recordActivity(tel *telemetry.Store, ring *ActivityRing, missionID int64, a Activity) {
	ring.Add(a)
	if tel == nil || missionID == 0 {
		return
	}
	n, err := tel.CountKind(missionID, "agent_activity")
	if err != nil {
		log.Printf("telemetry agent_activity: count: %v", err)
		return
	}
	if n >= agentActivityCap {
		if n == agentActivityCap {
			log.Printf("telemetry: agent_activity cap (%d) reached for mission %d — further activity for this mission will not be recorded", agentActivityCap, missionID)
		}
		return
	}
	if err := tel.Record(telemetry.Event{
		MissionID: missionID, Kind: "agent_activity", Actor: a.Agent,
		Detail: map[string]any{"role": a.Role, "tool": a.Tool, "detail": a.Detail},
	}); err != nil {
		log.Printf("telemetry agent_activity: %v", err)
	}
}
```

Add imports `"log"` and `"github.com/pdbethke/corralai/internal/telemetry"` to `internal/brain/activity.go`. Then update `registerActivity` to take `opts Options`, add `MissionID` to its input, and call the new helper:

```go
func registerActivity(s *mcp.Server, ring *ActivityRing, opts Options) {
	if ring == nil {
		return
	}
	type reportActivityIn struct {
		Name      string `json:"name"`
		Role      string `json:"role"`
		Tool      string `json:"tool"`
		Detail    string `json:"detail"`
		MissionID int64  `json:"mission_id,omitempty" jsonschema:"the mission this activity belongs to, when known — enables durable recording for replay"`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "report_activity",
		Description: "Report a tool-call you just made (observability only) so the swarm's live console shows what every bee is doing, in every phase. Pass mission_id when you have one so it's durably recorded for replay. Best-effort: never blocks or alters your work.",
	}, func(_ context.Context, req *mcp.CallToolRequest, in reportActivityIn) (*mcp.CallToolResult, okOut, error) {
		recordActivity(opts.Telemetry, ring, in.MissionID, Activity{
			Agent:  identity(req, in.Name),
			Role:   in.Role,
			Tool:   in.Tool,
			Detail: in.Detail,
			TS:     time.Now().Unix(),
		})
		return nil, okOut{OK: true}, nil
	})
}
```

- [ ] **Step 8: Fix the call site**

In `internal/brain/server.go:251`, change:

```go
	registerActivity(s, opts.ActivityRing, opts)
```

- [ ] **Step 9: Run to verify green**

Run: `go test ./internal/brain/ ./internal/telemetry/ -count=1`
Expected: PASS

- [ ] **Step 10: Commit**

```bash
git add internal/brain/activity.go internal/brain/server.go internal/brain/activity_test.go internal/telemetry/store.go internal/telemetry/store_test.go && git commit -m "feat(telemetry): agent_activity durable recording, capped at 2000/mission with a loud log at cap

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: `claim_made`/`claim_released`, `host_seen`, `memory_written` kinds

**Files:**
- Modify: `internal/brain/server.go:140-162` (`claim_paths`/`release_claims` handlers)
- Modify: `internal/brain/host.go:154-179` (`report_host` handler — material-change gate)
- Modify: `internal/brain/memory.go` (`add_memory` handler — call site around line 113)
- Test: `internal/brain/coordination_activity_test.go` (new file)

**Interfaces:**
- Consumes: `rec` helper (unchanged); `coord.ClaimResult{Granted []string; ...}` (unchanged); `HostBook.Get(agent string) (Host, bool)` (unchanged, already exists).
- Produces telemetry kinds (all `MissionID: 0` — claims and hosts are not mission-scoped today per the spec's data-survival research, so these are global ambience, not part of the Part B replay merge):
  - `claim_made` — one event per granted path, `Detail: map[string]any{"path": p, "exclusive": excl}`
  - `claim_released` — one event per requested path (or `"*"` when releasing all), `Detail: map[string]any{"path": p}`
  - `host_seen` — only on first sighting of an agent or a change to model/backend/jail, `Detail: map[string]any{"host": h.Host, "model": h.Model, "backend": h.Backend, "jail": h.Jail}`
  - `memory_written` — `Detail: map[string]any{"slug": slug, "type": typ, "shared": shared}` (author is the event's `Actor`) — **never** `name`/`body`/`description` (metadata-only discipline).

- [ ] **Step 1: Write the failing tests**

```go
// in internal/brain/coordination_activity_test.go
package brain

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/memory"
	"github.com/pdbethke/corralai/internal/telemetry"
)

func openTel(t *testing.T) *telemetry.Store {
	t.Helper()
	s, err := telemetry.Open(filepath.Join(t.TempDir(), "t.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func kindCount(t *testing.T, tel *telemetry.Store, kind string) int {
	t.Helper()
	rep, err := tel.RunReport("kinds")
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rep.Rows {
		if row[0] == kind {
			return int(row[1].(int64))
		}
	}
	return 0
}

func TestClaimAndReleaseEmitTelemetry(t *testing.T) {
	cs, err := coord.Open(filepath.Join(t.TempDir(), "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	tel := openTel(t)
	r, err := cs.ClaimPaths("bee1", []string{"a.go", "b.go"}, 3600, true, "build")
	if err != nil {
		t.Fatal(err)
	}
	recordClaimMade(tel, "bee1", r)
	if n := kindCount(t, tel, "claim_made"); n != len(r.Granted) {
		t.Fatalf("claim_made count = %d, want %d", n, len(r.Granted))
	}
	recordClaimReleased(tel, "bee1", []string{"a.go", "b.go"})
	if n := kindCount(t, tel, "claim_released"); n != 2 {
		t.Fatalf("claim_released count = %d, want 2", n)
	}
}

func TestHostSeenOnlyOnFirstOrMaterialChange(t *testing.T) {
	book := NewHostBook()
	tel := openTel(t)
	h1 := Host{Agent: "bee1", Model: "qwen2.5-coder:7b", Backend: "ollama", Jail: "bwrap"}
	recordHostSeen(tel, book, h1)
	if n := kindCount(t, tel, "host_seen"); n != 1 {
		t.Fatalf("first sighting: host_seen = %d, want 1", n)
	}
	recordHostSeen(tel, book, h1) // identical re-announce
	if n := kindCount(t, tel, "host_seen"); n != 1 {
		t.Fatalf("unchanged re-announce must not re-emit: host_seen = %d, want 1", n)
	}
	h2 := h1
	h2.Model = "qwen2.5-coder:14b"
	recordHostSeen(tel, book, h2)
	if n := kindCount(t, tel, "host_seen"); n != 2 {
		t.Fatalf("material change (model) must emit: host_seen = %d, want 2", n)
	}
}

func TestMemoryWrittenNeverCarriesBody(t *testing.T) {
	mem, err := memory.Open(filepath.Join(t.TempDir(), "mem.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer mem.Close()
	tel := openTel(t)
	slug, _, _, err := mem.Add("go-mod-init", "SECRET-LOOKING-BODY-TEXT run go mod init first", "how to init", "lesson", "default", "", true, "bee1")
	if err != nil {
		t.Fatal(err)
	}
	recordMemoryWritten(tel, "bee1", slug, "lesson", true)
	rep, err := tel.Query(`SELECT detail FROM events WHERE kind='memory_written'`)
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Rows) != 1 {
		t.Fatalf("expected 1 memory_written event, got %d", len(rep.Rows))
	}
	detail := rep.Rows[0][0].(string)
	if strings.Contains(detail, "SECRET-LOOKING-BODY-TEXT") {
		t.Fatalf("memory_written detail must never carry body text: %s", detail)
	}
}
```

(`memory.Open` here mirrors how other `internal/brain` tests construct a `*memory.Store` — check `internal/brain/memory_test.go` for the exact constructor if `Open` isn't it; adjust the two lines accordingly, the assertions are what matter.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/brain/ -run 'TestClaimAndReleaseEmitTelemetry|TestHostSeenOnlyOnFirstOrMaterialChange|TestMemoryWrittenNeverCarriesBody' -count=1`
Expected: FAIL (helpers undefined)

- [ ] **Step 3: Implement the three recording helpers + wire them at their call sites**

In `internal/brain/server.go`, add near the top (or a small new file `internal/brain/coordination_activity.go` — prefer the new file, since `server.go` is already large):

```go
// SPDX-License-Identifier: Elastic-2.0

package brain

import "github.com/pdbethke/corralai/internal/coord"

// recordClaimMade emits one claim_made event per granted path — claims are
// not mission-scoped (internal/coord's schema has no mission_id), so these
// are global ambience, not part of the Part B replay merge.
func recordClaimMade(tel *telemetry.Store, actor string, r *coord.ClaimResult) {
	for _, p := range r.Granted {
		rec(tel, 0, "claim_made", actor, p, map[string]any{"path": p, "exclusive": !r.Advisory})
	}
}

// recordClaimReleased emits one claim_released event per requested path. An
// empty paths list means "release everything mine" — recorded as a single
// wildcard event rather than guessing which paths were actually held.
func recordClaimReleased(tel *telemetry.Store, actor string, paths []string) {
	if len(paths) == 0 {
		rec(tel, 0, "claim_released", actor, "*", map[string]any{"path": "*"})
		return
	}
	for _, p := range paths {
		rec(tel, 0, "claim_released", actor, p, map[string]any{"path": p})
	}
}

// recordHostSeen emits host_seen only on an agent's first sighting or a
// material change to model/backend/jail — not every heartbeat, per the spec's
// volume discipline. prev is looked up from book BEFORE h is stored.
func recordHostSeen(tel *telemetry.Store, book *HostBook, h Host) {
	prev, existed := book.Get(h.Agent)
	book.Set(h)
	material := !existed || prev.Model != h.Model || prev.Backend != h.Backend || prev.Jail != h.Jail
	if !material {
		return
	}
	rec(tel, 0, "host_seen", h.Agent, h.Host, map[string]any{"host": h.Host, "model": h.Model, "backend": h.Backend, "jail": h.Jail})
}

// recordMemoryWritten emits metadata ONLY — slug, type, shared — never the
// name/body/description, per the fleet-sync metadata-only invariant.
func recordMemoryWritten(tel *telemetry.Store, actor, slug, typ string, shared bool) {
	rec(tel, 0, "memory_written", actor, slug, map[string]any{"slug": slug, "type": typ, "shared": shared})
}
```

Now wire the three call sites. In `internal/brain/server.go`, `claim_paths` handler (~line 151-156):

```go
			r, err := store.ClaimPaths(identity(req, in.Name), in.Paths, ttl, excl, in.Reason)
			if err != nil {
				return nil, coord.ClaimResult{}, err
			}
			recordClaimMade(opts.Telemetry, identity(req, in.Name), r)
			return nil, *r, nil
```

`release_claims` handler (~line 159-162):

```go
			n, err := store.ReleaseClaims(identity(req, in.Name), in.Paths)
			recordClaimReleased(opts.Telemetry, identity(req, in.Name), in.Paths)
			return nil, releaseOut{Released: n}, err
```

`internal/brain/host.go`'s `report_host` handler (~lines 169-179): replace `book.Set(Host{...})` with:

```go
		recordHostSeen(opts.Telemetry, book, Host{
			Agent: identity(req, in.Name), Role: in.Role, Host: in.Host,
			Model: in.Model, Backend: in.Backend, Jail: in.Jail, Net: in.Net,
			OS: in.OS, Pid: in.Pid, TS: time.Now().Unix(),
		})
```

`internal/brain/memory.go`'s `add_memory` handler (~line 113):

```go
		slug, path, status, err := mem.Add(in.Name, in.Body, in.Description, in.Type, in.Project, "", in.Shared, author)
		if err == nil {
			recordMemoryWritten(opts.Telemetry, author, slug, in.Type, in.Shared)
			auditKnowledge(opts, req, "add_memory",
				map[string]any{"slug": slug, "shared": in.Shared})
		}
```

(`opts.Telemetry` must already be in scope in each of these functions — `registerHost` already takes `opts Options`; if `add_memory`'s enclosing `register*` function doesn't currently take `opts`, add it and update its call site in `server.go`, following the same pattern as Task 3's `registerActivity` change.)

- [ ] **Step 4: Run to verify green**

Run: `go test ./internal/brain/ -count=1`
Expected: PASS

- [ ] **Step 5: gosec + vet**

Run: `go vet ./internal/brain/... && bash scripts/check-security.sh` (or the project's gosec invocation if that script doesn't exist — check `Makefile`/`docs/superpowers` for the exact command used elsewhere in this repo)
Expected: clean

- [ ] **Step 6: Commit**

```bash
git add internal/brain/coordination_activity.go internal/brain/server.go internal/brain/host.go internal/brain/memory.go internal/brain/coordination_activity_test.go && git commit -m "feat(telemetry): claim_made/claim_released, host_seen (material-change only), memory_written (metadata only)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5: `mission_history` read surface (brain-side)

**Files:**
- Create: `internal/brain/history.go`
- Modify: `internal/queue/executions.go` (add `ExecutionsByMission`)
- Modify: `internal/telemetry/store.go` (add `MissionCompletedAt`)
- Modify: `internal/brain/server.go` (register the new tool)
- Test: `internal/brain/history_test.go`, `internal/queue/executions_test.go`, `internal/telemetry/store_test.go`

**Interfaces:**
- Consumes: `mission.Store.ListMissions() ([]mission.Mission, error)`, `mission.Store.Mission(id) (*mission.Mission, error)`, `mission.Store.View(id, q) (*mission.MissionView, error)` (all unchanged); `queue.Store.List(missionID) ([]queue.Task, error)`, `queue.Store.FindingsFiltered(missionID, status, byModel) ([]queue.Finding, error)` (unchanged); `learn.Store.List(status string) ([]learn.Proposal, error)` (unchanged, Task-1-of-the-learning-loop plan).
- Produces (later tasks rely on these exact names):
  - `func (s *queue.Store) ExecutionsByMission(missionID int64) ([]Execution, error)` — newest-first, mission-scoped (the table is already indexed on `mission_id`).
  - `func (s *telemetry.Store) MissionCompletedAt(missionID int64) (ts float64, found bool, err error)` — latest `mission_completed` event's `ts` for the mission, if any.
  - `type brain.MissionSummary struct { ID int64; Directive, Status string; CreatedTS, UpdatedTS, DurationSeconds float64; TaskCount, DoneTaskCount, FindingCount int; PRURL string; LearnedSignatures []string }`
  - `type brain.MissionDetail struct { MissionSummary; Phases []mission.PhaseView; Tasks []queue.Task; Findings []queue.Finding; Executions []queue.Execution }`
  - `func brain.MissionHistoryList(m *mission.Store, q *queue.Store, tel *telemetry.Store, l *learn.Store) ([]MissionSummary, error)` — non-running missions, newest first.
  - `func brain.MissionHistoryDetail(m *mission.Store, q *queue.Store, tel *telemetry.Store, l *learn.Store, id int64) (*MissionDetail, error)` — nil, nil when no such mission.
  - `func brain.matchLearnedSignatures(findings []queue.Finding, approved []learn.Proposal) []string` — pure, unexported; the best-effort linkage heuristic.
  - MCP tool `mission_history {id?}` — omit `id` for the list, pass it for the detail (mirrors `mission_analytics`'s report/SQL branch style).

- [ ] **Step 1: Write the failing pure-function test (learned-linkage)**

```go
// in internal/brain/history_test.go
package brain

import (
	"testing"

	"github.com/pdbethke/corralai/internal/learn"
	"github.com/pdbethke/corralai/internal/queue"
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
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/brain/ -run TestMatchLearnedSignatures -count=1`
Expected: FAIL (`matchLearnedSignatures` undefined)

- [ ] **Step 3: Add `ExecutionsByMission` and `MissionCompletedAt` with their own tests**

```go
// in internal/queue/executions_test.go
func TestExecutionsByMission(t *testing.T) {
	s := openStore(t) // reuse this file's existing helper
	if err := s.RecordExecution(Execution{MissionID: 1, Agent: "bee1", Command: "go build", OK: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordExecution(Execution{MissionID: 2, Agent: "bee2", Command: "go test", OK: true}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ExecutionsByMission(1)
	if err != nil || len(got) != 1 || got[0].Command != "go build" {
		t.Fatalf("got %v err=%v", got, err)
	}
}
```

```go
// in internal/telemetry/store_test.go
func TestMissionCompletedAt(t *testing.T) {
	s, _ := Open(filepath.Join(t.TempDir(), "t.duckdb"))
	defer s.Close()
	if _, found, err := s.MissionCompletedAt(1); err != nil || found {
		t.Fatalf("no event yet: found=%v err=%v", found, err)
	}
	_ = s.Record(Event{MissionID: 1, Kind: "mission_completed"})
	ts, found, err := s.MissionCompletedAt(1)
	if err != nil || !found || ts <= 0 {
		t.Fatalf("ts=%v found=%v err=%v", ts, found, err)
	}
}
```

Run: `go test ./internal/queue/ ./internal/telemetry/ -run 'TestExecutionsByMission|TestMissionCompletedAt' -count=1` — FAIL as expected, then implement:

```go
// in internal/queue/executions.go, after ExecutionsByAgent
// ExecutionsByMission returns every recorded execution for a mission, newest
// first — the durable twin of what the live UI's ExecRing shows, and the
// source Part B's replay draws execution bursts from.
func (s *Store) ExecutionsByMission(missionID int64) ([]Execution, error) {
	rows, err := s.db.Query(
		`SELECT id,mission_id,agent,role,command,exit_code,ok,ts FROM executions WHERE mission_id=? ORDER BY ts DESC`,
		missionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Execution
	for rows.Next() {
		var e Execution
		var ok int
		if err := rows.Scan(&e.ID, &e.MissionID, &e.Agent, &e.Role, &e.Command, &e.ExitCode, &ok, &e.TS); err != nil {
			return nil, err
		}
		e.OK = ok == 1
		out = append(out, e)
	}
	return out, rows.Err()
}
```

```go
// in internal/telemetry/store.go, after AgentTimeline
// MissionCompletedAt returns the ts of the latest mission_completed event for
// missionID, if any — mission_history/replay use this to prefer event-based
// duration once mission_completed exists, falling back to task timestamps
// for missions recorded before this telemetry kind shipped.
func (s *Store) MissionCompletedAt(missionID int64) (float64, bool, error) {
	var ts float64
	err := s.db.QueryRow(
		`SELECT ts FROM events WHERE mission_id=? AND kind='mission_completed' ORDER BY ts DESC LIMIT 1`,
		missionID).Scan(&ts)
	if err == sql.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return ts, true, nil
}
```

Run: `go test ./internal/queue/ ./internal/telemetry/ -count=1` — PASS.

- [ ] **Step 4: Implement `matchLearnedSignatures` and the history surface**

```go
// internal/brain/history.go
// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"context"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/pdbethke/corralai/internal/learn"
	"github.com/pdbethke/corralai/internal/mission"
	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/telemetry"
)

// MissionSummary is one row of the Completed tab's list.
type MissionSummary struct {
	ID                int64    `json:"id"`
	Directive         string   `json:"directive"`
	Status            string   `json:"status"`
	CreatedTS         float64  `json:"created_ts"`
	UpdatedTS         float64  `json:"updated_ts"`
	DurationSeconds   float64  `json:"duration_seconds"`
	TaskCount         int      `json:"task_count"`
	DoneTaskCount     int      `json:"done_task_count"`
	FindingCount      int      `json:"finding_count"`
	PRURL             string   `json:"pr_url,omitempty"`
	LearnedSignatures []string `json:"learned_signatures,omitempty"`
}

// MissionDetail is the per-mission drill-down: phases/tasks/findings/executions.
type MissionDetail struct {
	MissionSummary
	Phases     []mission.PhaseView `json:"phases"`
	Tasks      []queue.Task        `json:"tasks"`
	Findings   []queue.Finding     `json:"findings"`
	Executions []queue.Execution   `json:"executions"`
}

// matchLearnedSignatures is the spec's best-effort linkage: promoted
// (approved) proposals whose signature matches one of this mission's
// findings. Heuristic until proposals carry mission provenance directly.
func matchLearnedSignatures(findings []queue.Finding, approved []learn.Proposal) []string {
	approvedSigs := map[string]bool{}
	for _, p := range approved {
		if p.Status == learn.StatusApproved {
			approvedSigs[p.Signature] = true
		}
	}
	seen := map[string]bool{}
	var out []string
	for _, f := range findings {
		sig := f.Type + "|" + f.Target
		if approvedSigs[sig] && !seen[sig] {
			seen[sig] = true
			out = append(out, sig)
		}
	}
	return out
}

// summarize builds one MissionSummary from a mission row + its queue/telemetry
// state. duration prefers mission_completed's event ts (once it exists) over
// the last task's done_ts, per the spec's "event-based once it exists" rule.
func summarize(mi mission.Mission, q *queue.Store, tel *telemetry.Store, approved []learn.Proposal) (MissionSummary, error) {
	tasks, err := q.List(mi.ID)
	if err != nil {
		return MissionSummary{}, err
	}
	findings, err := q.FindingsFiltered(mi.ID, "", "")
	if err != nil {
		return MissionSummary{}, err
	}
	done := 0
	var lastDone float64
	for _, t := range tasks {
		if t.Status == queue.StatusDone {
			done++
		}
		if t.DoneTS > lastDone {
			lastDone = t.DoneTS
		}
	}
	end := lastDone
	if tel != nil {
		if ts, found, err := tel.MissionCompletedAt(mi.ID); err == nil && found {
			end = ts
		}
	}
	dur := 0.0
	if end > mi.CreatedTS {
		dur = end - mi.CreatedTS
	}
	return MissionSummary{
		ID: mi.ID, Directive: mi.Directive, Status: mi.Status,
		CreatedTS: mi.CreatedTS, UpdatedTS: mi.UpdatedTS, DurationSeconds: dur,
		TaskCount: len(tasks), DoneTaskCount: done, FindingCount: len(findings),
		PRURL: mi.PRURL, LearnedSignatures: matchLearnedSignatures(findings, approved),
	}, nil
}

// MissionHistoryList returns every non-running mission, newest created first.
func MissionHistoryList(m *mission.Store, q *queue.Store, tel *telemetry.Store, l *learn.Store) ([]MissionSummary, error) {
	all, err := m.ListMissions()
	if err != nil {
		return nil, err
	}
	var approved []learn.Proposal
	if l != nil {
		approved, _ = l.List(learn.StatusApproved) // best-effort: linkage degrades to empty, never fails the list
	}
	out := make([]MissionSummary, 0, len(all))
	for _, mi := range all {
		if mi.Status == "running" {
			continue
		}
		sm, err := summarize(mi, q, tel, approved)
		if err != nil {
			return nil, err
		}
		out = append(out, sm)
	}
	return out, nil
}

// MissionHistoryDetail returns the full drill-down for one mission, or
// (nil, nil) when no such mission exists.
func MissionHistoryDetail(m *mission.Store, q *queue.Store, tel *telemetry.Store, l *learn.Store, id int64) (*MissionDetail, error) {
	mi, err := m.Mission(id)
	if err != nil || mi == nil {
		return nil, err
	}
	var approved []learn.Proposal
	if l != nil {
		approved, _ = l.List(learn.StatusApproved)
	}
	sm, err := summarize(*mi, q, tel, approved)
	if err != nil {
		return nil, err
	}
	mv, err := m.View(id, q)
	if err != nil {
		return nil, err
	}
	tasks, err := q.List(id)
	if err != nil {
		return nil, err
	}
	findings, err := q.FindingsFiltered(id, "", "")
	if err != nil {
		return nil, err
	}
	var execs []queue.Execution
	if q != nil {
		execs, err = q.ExecutionsByMission(id)
		if err != nil {
			return nil, err
		}
	}
	phases := []mission.PhaseView{}
	if mv != nil {
		phases = mv.Phases
	}
	return &MissionDetail{
		MissionSummary: sm, Phases: phases, Tasks: tasks, Findings: findings, Executions: execs,
	}, nil
}

type missionHistoryIn struct {
	ID int64 `json:"id,omitempty" jsonschema:"omit for the list of past missions; pass to drill into one mission's phases/tasks/findings/executions"`
}
type missionHistoryOut struct {
	Missions []MissionSummary `json:"missions,omitempty"`
	Mission  *MissionDetail   `json:"mission,omitempty"`
}

// registerHistory adds the mission_history read-only tool: past missions
// (list) or one mission's full drill-down (detail). Mirrors mission_analytics'
// report/ad-hoc branch shape. Registered only when Missions+Queue are set.
func registerHistory(s *mcp.Server, opts Options) {
	if opts.Missions == nil || opts.Queue == nil {
		return
	}
	mcp.AddTool(s, &mcp.Tool{Name: "mission_history",
		Description: "Past missions the herd already finished: list (directive, status, duration, task/finding counts, best-effort what-got-learned) or, given an id, the full drill-down — phases, tasks, findings, executions."},
		func(_ context.Context, _ *mcp.CallToolRequest, in missionHistoryIn) (*mcp.CallToolResult, missionHistoryOut, error) {
			if in.ID != 0 {
				d, err := MissionHistoryDetail(opts.Missions, opts.Queue, opts.Telemetry, opts.Learn, in.ID)
				if err != nil {
					return nil, missionHistoryOut{}, err
				}
				return nil, missionHistoryOut{Mission: d}, nil
			}
			ms, err := MissionHistoryList(opts.Missions, opts.Queue, opts.Telemetry, opts.Learn)
			if err != nil {
				return nil, missionHistoryOut{}, err
			}
			return nil, missionHistoryOut{Missions: ms}, nil
		})
}
```

Remove the now-unused `strings` import if the final file doesn't reference it (it isn't used above — drop it from the import block).

Register it in `internal/brain/server.go` alongside the other `register*` calls (~line 242, next to `registerAnalytics(s, opts)`):

```go
	registerHistory(s, opts)
```

- [ ] **Step 5: Write the list/detail/duration-fallback tests**

```go
// in internal/brain/history_test.go, continued
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
```

- [ ] **Step 6: Run everything to green**

Run: `go test ./internal/brain/ ./internal/queue/ ./internal/telemetry/ -count=1`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/brain/history.go internal/brain/history_test.go internal/brain/server.go internal/queue/executions.go internal/queue/executions_test.go internal/telemetry/store.go internal/telemetry/store_test.go && git commit -m "feat(brain): mission_history read surface — list + detail, duration fallback, best-effort learned-linkage

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 6: Completed tab UI

**Files:**
- Modify: `internal/ui/ui.go` (`Deps` gains `History func() ([]brain.MissionSummary, error)` and `HistoryDetail func(id int64) (*brain.MissionDetail, error)`; two new handlers)
- Modify: `internal/ui/web/index.html` (SKINS `completedTab` field, `#tab-completed`/`#completed` markup, `renderCompletedTab()`, `setView` wiring)
- Modify: `cmd/corral/main.go` (wire `History`/`HistoryDetail` into `ui.Deps`, next to `Promote`/`Reject`)
- Test: `internal/ui/ui_test.go` (new test)

**Interfaces:**
- Consumes: `brain.MissionHistoryList`/`MissionHistoryDetail` (Task 5).
- Produces: `GET /api/history` → `{"missions": [...MissionSummary]}`; `GET /api/history/{id}` → `{"mission": MissionDetail}` or 404; a `completed` view id alongside the existing `swarm|progress|topology|memory|proposals` views, driven by the same `setView()` toggle pattern.

- [ ] **Step 1: Write the failing UI test**

```go
// in internal/ui/ui_test.go — follow this file's existing httptest.NewServer(Handler(Deps{...})) pattern
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
```

(Match this file's actual constructor name if it isn't `Handler(deps)` — check the top of `internal/ui/ui_test.go` for the existing pattern other endpoint tests use, e.g. how the proposals endpoints are tested, and mirror it exactly.)

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/ui/ -run TestHistoryEndpoints -count=1`
Expected: FAIL (`Deps.History` undefined / 404 on `/api/history`)

- [ ] **Step 3: Add the Deps fields and handlers**

In `internal/ui/ui.go`, add to `Deps` (near `Promote`/`Reject`, ~line 103):

```go
	// History lists past (non-running) missions for the Completed tab. nil =>
	// the tab renders empty (feature disabled, never a 500).
	History func() ([]brain.MissionSummary, error)
	// HistoryDetail drills into one mission's phases/tasks/findings/executions.
	// Returns (nil, nil) for an unknown id — the handler turns that into 404.
	HistoryDetail func(id int64) (*brain.MissionDetail, error)
```

Add two handlers (near wherever `/api/proposal/approve` is registered) and their `mux.HandleFunc` registrations:

```go
func (s *Server) history(w http.ResponseWriter, r *http.Request) {
	if s.deps.History == nil {
		writeJSON(w, map[string]any{"missions": []any{}})
		return
	}
	ms, err := s.deps.History()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"missions": ms})
}

func (s *Server) historyDetail(w http.ResponseWriter, r *http.Request) {
	if s.deps.HistoryDetail == nil {
		http.NotFound(w, r)
		return
	}
	idStr := strings.TrimPrefix(r.URL.Path, "/api/history/")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "bad mission id", http.StatusBadRequest)
		return
	}
	d, err := s.deps.HistoryDetail(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if d == nil {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, map[string]any{"mission": d})
}
```

Register both next to the existing `mux.HandleFunc` block (adjust exact helper names — `writeJSON`, `strconv`/`strings` imports — to match whatever this file already uses for its other JSON handlers; the proposals endpoints are the closest existing example):

```go
	mux.HandleFunc("/api/history", s.history)
	mux.HandleFunc("/api/history/", s.historyDetail)
```

- [ ] **Step 4: Run to verify green**

Run: `go test ./internal/ui/ -count=1`
Expected: PASS

- [ ] **Step 5: Wire Deps in main.go**

In `cmd/corral/main.go`, extend the `ui.Deps{...}` literal (~line 953) with:

```go
		History: func() ([]brain.MissionSummary, error) {
			return brain.MissionHistoryList(missionStore, queueStore, telStore, learnStore)
		},
		HistoryDetail: func(id int64) (*brain.MissionDetail, error) {
			return brain.MissionHistoryDetail(missionStore, queueStore, telStore, learnStore, id)
		},
```

- [ ] **Step 6: Add the Completed tab to the canvas UI**

In `internal/ui/web/index.html`:

1. Add `completedTab` to each entry in `SKINS` (~lines 407-427), following the existing `proposalsTab` pattern — e.g. `ranch: ..., completedTab:'completed', ...`; `flock: ..., completedTab:'completed', ...`; `matrix: ..., completedTab:'archive', ...`; `hive: ..., completedTab:'completed', ...`.
2. Add a tab button next to `tab-proposals` in the nav markup: `<button id="tab-completed" onclick="setView('completed')">completed</button>` (its label is set from `skin().completedTab` in `setSkin()`, mirroring the existing `ptab.firstChild.textContent = skin().proposalsTab` line).
3. Add a `#completed` panel element (same family as `#proposals`) containing a list container and a detail pane: `<div id="completed" class="tab-panel"><div id="completed-list"></div><div id="completed-detail" style="display:none"></div></div>`.
4. Extend `setView(v)` (lines 1245-1263) with the `completed` case, following the exact existing pattern:

```js
  document.getElementById('tab-completed').classList.toggle('active', v==='completed');
  document.getElementById('completed').classList.toggle('show', v==='completed');
  ...
  if(v==='completed'){ renderCompletedTab(); }
```

5. Add `renderCompletedTab()` (mirror `renderProposalsTab`'s fetch-and-render shape at lines 839-857): fetches `/api/history`, renders each `MissionSummary` as a row — directive, status pill, `duration_seconds` formatted as `Xm Ys`, `done_task_count/task_count` and `finding_count` badges, a `learned_signatures` chip list when non-empty, and a **▶ replay** button (`onclick="openReplay(${m.id})"` — wired in Task 8) — plus a click handler that fetches `/api/history/{id}` and renders the phase/task/finding/execution drill-down into `#completed-detail`.

```js
function renderCompletedTab(){
  fetch('/api/history').then(r=>r.json()).then(d=>{
    const list = document.getElementById('completed-list');
    const missions = d.missions || [];
    if(!missions.length){ list.innerHTML = '<div class="empty">no finished missions yet</div>'; return; }
    list.innerHTML = missions.map(m => `
      <div class="tqueen-card" data-mid="${m.id}">
        <div class="row"><b>${m.directive}</b> <span class="pill">${m.status}</span></div>
        <div class="row muted">${fmtDuration(m.duration_seconds)} · ${m.done_task_count}/${m.task_count} tasks · ${m.finding_count} findings</div>
        ${(m.learned_signatures||[]).length ? `<div class="row learned">learned: ${m.learned_signatures.join(', ')}</div>` : ''}
        <button onclick="openMissionDetail(${m.id})">details</button>
        <button onclick="openReplay(${m.id})">▶ replay</button>
      </div>`).join('');
  }).catch(()=>{});
}
function fmtDuration(sec){
  sec = Math.max(0, Math.round(sec||0));
  return Math.floor(sec/60) + 'm ' + (sec%60) + 's';
}
function openMissionDetail(id){
  fetch('/api/history/'+id).then(r=>r.json()).then(d=>{
    const el = document.getElementById('completed-detail');
    const mv = d.mission;
    el.style.display = 'block';
    el.innerHTML = `<h3>${mv.directive}</h3>` +
      `<div>phases: ${(mv.phases||[]).map(p=>p.name+' ('+p.status+')').join(', ')}</div>` +
      `<div>findings: ${(mv.findings||[]).map(f=>f.type+'/'+f.target+' — '+f.status).join(', ') || 'none'}</div>` +
      (mv.pr_url ? `<div><a href="${mv.pr_url}" target="_blank">pull request</a></div>` : '');
  }).catch(()=>{});
}
```

- [ ] **Step 7: Manual browser check**

Run: `go build -o /tmp/corral ./cmd/corral && HOME=$(mktemp -d) CORRALAI_ADDR=127.0.0.1:9024 /tmp/corral &` then open `http://127.0.0.1:9024`, click the new tab, confirm it renders without a console error (empty-state is fine — no missions exist yet in a fresh brain). Kill the process.

- [ ] **Step 8: Commit**

```bash
git add internal/ui/ui.go internal/ui/web/index.html internal/ui/ui_test.go cmd/corral/main.go && git commit -m "feat(ui): Completed tab — mission list + drill-down detail, skin-aware

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 7: Replay stream — merged, time-ordered reconstruction

**Files:**
- Create: `internal/brain/replay.go`
- Test: `internal/brain/replay_test.go`, `internal/brain/testdata/replay_golden.json`

**Interfaces:**
- Consumes: `queue.Store.List`, `queue.Store.FindingsFiltered`, `queue.Store.ExecutionsByMission` (Task 5), `telemetry.Store` (new accessor below).
- Produces (Task 8 consumes these exact names):
  - `func (s *telemetry.Store) EventsForMission(missionID int64) ([]Event, error)` — every event for a mission, oldest first.
  - `type brain.ReplayEvent struct { TS float64; Kind, Actor, Subject string; Detail map[string]any }`
  - `func brain.BuildReplayStream(q *queue.Store, tel *telemetry.Store, missionID int64) ([]ReplayEvent, error)` — merged, sorted by `TS` ascending (ties broken by `Kind` for determinism), covering: task lifecycle (`task_claimed`/`task_done`/`task_cancelled`/`task_superseded` from `tasks` timestamps + status), findings (`finding_reported` from `created_ts`, `finding_resolved` from `resolved_ts` when set), executions (`execution` from `queue.executions`), and every telemetry event already recorded for the mission (covers `mission_completed`, `agent_activity`, `review_accepted`/`review_changes`, `proposal_opened`/`proposal_approved`, and any future kind — this is the graceful-degradation path: a mission with none of these simply contributes nothing from this source).
  - `GET /api/replay?mission=N` (wired in Task 8) → `[]ReplayEvent` as JSON.

- [ ] **Step 1: Write the failing golden-shape test**

```go
// internal/brain/replay_test.go
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
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/brain/ -run TestBuildReplayStream -count=1`
Expected: FAIL (`BuildReplayStream` undefined)

- [ ] **Step 3: Implement**

```go
// internal/brain/replay.go
// SPDX-License-Identifier: Elastic-2.0

package brain

import (
	"sort"

	"github.com/pdbethke/corralai/internal/queue"
	"github.com/pdbethke/corralai/internal/telemetry"
)

// ReplayEvent is one timestamped beat in a mission's reconstructed history —
// Part B's client replays a merged, sorted stream of these through the same
// apply()/render path the live canvas uses. Positions are never recorded
// here; the client recomputes layout, deliberately (see the spec).
type ReplayEvent struct {
	TS      float64        `json:"ts"`
	Kind    string         `json:"kind"`
	Actor   string         `json:"actor,omitempty"`
	Subject string         `json:"subject,omitempty"`
	Detail  map[string]any `json:"detail,omitempty"`
}

// BuildReplayStream reconstructs a mission's whole build from durable rows
// only — tasks, findings, executions, and (when present) the telemetry event
// log — merged and sorted oldest first. A mission recorded before Part C's
// new kinds shipped simply contributes nothing from telemetry; the stream
// still plays from tasks/findings/executions alone (graceful degradation).
func BuildReplayStream(q *queue.Store, tel *telemetry.Store, missionID int64) ([]ReplayEvent, error) {
	var out []ReplayEvent

	tasks, err := q.List(missionID)
	if err != nil {
		return nil, err
	}
	for _, t := range tasks {
		if t.ClaimedTS > 0 {
			out = append(out, ReplayEvent{TS: t.ClaimedTS, Kind: "task_claimed", Actor: t.ClaimedBy, Subject: t.Key,
				Detail: map[string]any{"role": t.Role, "title": t.Title}})
		}
		if t.DoneTS > 0 {
			kind := "task_" + t.Status // task_done, task_cancelled, task_superseded
			out = append(out, ReplayEvent{TS: t.DoneTS, Kind: kind, Actor: t.ClaimedBy, Subject: t.Key})
		}
	}

	findings, err := q.FindingsFiltered(missionID, "", "")
	if err != nil {
		return nil, err
	}
	for _, f := range findings {
		out = append(out, ReplayEvent{TS: f.CreatedTS, Kind: "finding_reported", Actor: f.Reporter, Subject: f.Target,
			Detail: map[string]any{"type": f.Type, "severity": f.Severity}})
		if f.ResolvedTS > 0 {
			out = append(out, ReplayEvent{TS: f.ResolvedTS, Kind: "finding_resolved", Subject: f.Target,
				Detail: map[string]any{"status": f.Status}})
		}
	}

	execs, err := q.ExecutionsByMission(missionID)
	if err != nil {
		return nil, err
	}
	for _, e := range execs {
		out = append(out, ReplayEvent{TS: float64(e.TS), Kind: "execution", Actor: e.Agent, Subject: e.Command,
			Detail: map[string]any{"ok": e.OK, "exit_code": e.ExitCode, "role": e.Role}})
	}

	if tel != nil {
		evs, err := tel.EventsForMission(missionID)
		if err != nil {
			return nil, err
		}
		for _, e := range evs {
			var detail map[string]any
			out = append(out, ReplayEvent{TS: e.TS, Kind: e.Kind, Actor: e.Actor, Subject: e.Subject, Detail: detail})
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TS != out[j].TS {
			return out[i].TS < out[j].TS
		}
		return out[i].Kind < out[j].Kind
	})
	return out, nil
}
```

Add `EventsForMission` to `internal/telemetry/store.go`, after `MissionCompletedAt`:

```go
// EventsForMission returns every recorded event for a mission, oldest first —
// Part B's replay merges this with the durable task/finding/execution rows so
// ambience (mission_completed, agent_activity, reviews, proposals, …) rides
// the same timeline as the mission's own state changes.
func (s *Store) EventsForMission(missionID int64) ([]Event, error) {
	rows, err := s.db.Query(
		`SELECT ts, kind, COALESCE(actor,''), COALESCE(subject,''), COALESCE(detail,'') FROM events WHERE mission_id=? ORDER BY ts ASC`,
		missionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		var detail string
		if err := rows.Scan(&e.TS, &e.Kind, &e.Actor, &e.Subject, &detail); err != nil {
			return nil, err
		}
		if detail != "" {
			_ = json.Unmarshal([]byte(detail), &e.Detail)
		}
		e.MissionID = missionID
		out = append(out, e)
	}
	return out, rows.Err()
}
```

- [ ] **Step 4: Generate the golden file**

Run: `UPDATE_GOLDEN=1 go test ./internal/brain/ -run TestBuildReplayStreamGoldenOrder -count=1` (writes `internal/brain/testdata/replay_golden.json`), then inspect the file by hand to confirm the kind/subject order matches the seeded scenario's real chronology (claim → execution/finding in either order depending on real clock granularity → done → resolved → mission_completed) before trusting it.

- [ ] **Step 5: Run both tests to verify green**

Run: `go test ./internal/brain/ -run TestBuildReplayStream -count=1`
Expected: PASS

- [ ] **Step 6: Full package + vet**

Run: `go build ./... && go test ./internal/brain/ ./internal/telemetry/ -count=1 && go vet ./internal/brain/...`
Expected: PASS, clean

- [ ] **Step 7: Commit**

```bash
git add internal/brain/replay.go internal/brain/replay_test.go internal/brain/testdata/replay_golden.json internal/telemetry/store.go && git commit -m "feat(brain): replay stream — merged, time-ordered reconstruction from durable rows only, graceful degradation without telemetry

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 8: Replay player UI — scrub, speed, SSE pause

**Files:**
- Modify: `internal/ui/ui.go` (`Deps` gains `Replay func(missionID int64) ([]brain.ReplayEvent, error)`; new `/api/replay` handler)
- Modify: `internal/ui/web/index.html` (`es` becomes reassignable; replay state, `openReplay`/`closeReplay`/`replayStep`/`applyReplayEvent`/scrub UI; `completed` tab's replay button already calls `openReplay(id)` from Task 6)
- Modify: `cmd/corral/main.go` (wire `Replay` into `ui.Deps`)
- Test: `internal/ui/ui_test.go` (new test for `/api/replay`)

**Interfaces:**
- Consumes: `brain.BuildReplayStream` (Task 7), `brain.ReplayEvent` (Task 7).
- Produces: `GET /api/replay?mission=N` → `{"events": [...ReplayEvent]}` (400 on a missing/non-numeric `mission`, 500 on a store error); client globals `replayEvents`, `replayIdx`, `replayPlaying`, `replaySpeed` and functions `openReplay(missionId)`, `closeReplay()`, `replayStep()`, `applyReplayEvent(ev)`, `seekReplay(idx)` — read-only by construction: none of these call any `POST`/mutating endpoint.

- [ ] **Step 1: Write the failing endpoint test**

```go
// in internal/ui/ui_test.go
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
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/ui/ -run TestReplayEndpoint -count=1`
Expected: FAIL

- [ ] **Step 3: Implement the endpoint**

In `internal/ui/ui.go`, add to `Deps`:

```go
	// Replay reconstructs a mission's whole build from durable rows for
	// playback on the canvas. nil => /api/replay is disabled (404).
	Replay func(missionID int64) ([]brain.ReplayEvent, error)
```

Add the handler and route:

```go
func (s *Server) replay(w http.ResponseWriter, r *http.Request) {
	if s.deps.Replay == nil {
		http.NotFound(w, r)
		return
	}
	idStr := r.URL.Query().Get("mission")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if idStr == "" || err != nil {
		http.Error(w, "mission query param required", http.StatusBadRequest)
		return
	}
	events, err := s.deps.Replay(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"events": events})
}
```

```go
	mux.HandleFunc("/api/replay", s.replay)
```

- [ ] **Step 4: Run to verify green**

Run: `go test ./internal/ui/ -count=1`
Expected: PASS

- [ ] **Step 5: Wire Deps in main.go**

```go
		Replay: func(missionID int64) ([]brain.ReplayEvent, error) {
			return brain.BuildReplayStream(queueStore, telStore, missionID)
		},
```

- [ ] **Step 6: Build the player**

In `internal/ui/web/index.html`:

1. Change `const es = new EventSource('/events');` to `let es = new EventSource('/events'); es.onmessage = e => { try{ apply(JSON.parse(e.data)); }catch(_){} };` (the existing `onerror` line stays) — `let` so replay can close/reopen it.
2. Add replay state near the other module-scope arrays (`nodes`/`links`/`bursts`):

```js
let replayEvents = [], replayIdx = 0, replayPlaying = false, replaySpeed = 1, replayTimer = null, replaySSEPaused = false;

function openReplay(missionId){
  fetch('/api/replay?mission='+missionId).then(r=>r.json()).then(d=>{
    replayEvents = d.events || [];
    replayIdx = 0;
    nodes.clear(); links.length = 0; bursts.length = 0; buzzes.length = 0;
    if(es){ es.close(); replaySSEPaused = true; }
    setView('replay');
    renderReplayScrub();
  }).catch(()=>{});
}
function closeReplay(){
  replayPlaying = false;
  if(replayTimer) clearTimeout(replayTimer);
  if(replaySSEPaused){ es = new EventSource('/events'); es.onmessage = e => { try{ apply(JSON.parse(e.data)); }catch(_){} }; replaySSEPaused = false; }
  setView('swarm');
}
function toggleReplayPlay(){
  replayPlaying = !replayPlaying;
  if(replayPlaying) replayStep();
}
function setReplaySpeed(x){ replaySpeed = x; }
function replayStep(){
  if(replayIdx >= replayEvents.length){ replayPlaying = false; return; }
  applyReplayEvent(replayEvents[replayIdx++]);
  renderReplayScrub();
  if(replayPlaying) replayTimer = setTimeout(replayStep, Math.max(16, 250 / replaySpeed));
}
function seekReplay(target){
  replayIdx = 0;
  nodes.clear(); links.length = 0; bursts.length = 0; buzzes.length = 0;
  while(replayIdx < target && replayIdx < replayEvents.length) applyReplayEvent(replayEvents[replayIdx++]);
  renderReplayScrub();
}
function applyReplayEvent(ev){
  const at = (id, label) => ensure(id, 'worker', label || id);
  switch(ev.kind){
    case 'task_claimed':
      if(ev.actor){ at(ev.actor); links.push({a:'brain', b:ev.actor}); }
      break;
    case 'execution':
      bursts.push({x: 0, y: 0, t0: performance.now(), ok: ev.detail && ev.detail.ok});
      break;
    case 'finding_reported':
      buzzes.push({text: (ev.detail && ev.detail.severity || '') + ' ' + ev.subject, t0: performance.now()});
      break;
    default:
      break; // unrecognized/ambience-only kinds are ignored — graceful degradation
  }
}
function renderReplayScrub(){
  const scrub = document.getElementById('replay-scrub');
  if(!scrub) return;
  scrub.max = replayEvents.length;
  scrub.value = replayIdx;
  const label = document.getElementById('replay-label');
  if(label) label.textContent = replayIdx + ' / ' + replayEvents.length;
}
```

3. Add the `#replay` panel + controls markup (mirrors the `#completed` panel's structure):

```html
<div id="replay" class="tab-panel">
  <div class="row">
    <button onclick="toggleReplayPlay()">play/pause</button>
    <input type="range" id="replay-scrub" min="0" max="0" value="0" oninput="seekReplay(+this.value)">
    <span id="replay-label">0 / 0</span>
    <select onchange="setReplaySpeed(+this.value)">
      <option value="1">1x</option><option value="2">2x</option><option value="4">4x</option>
      <option value="8">8x</option><option value="16">16x</option>
    </select>
    <button onclick="closeReplay()">exit replay</button>
  </div>
</div>
```

4. Extend `setView(v)` with the `replay` case (same pattern as `completed`):

```js
  document.getElementById('replay').classList.toggle('show', v==='replay');
```

- [ ] **Step 7: Manual browser check**

Run a scratch brain with a seeded mission (`corral-admin mission create ...` then let it complete), open the Completed tab, click ▶ replay, confirm the scrub bar moves, speed selector changes playback rate, and Exit replay resumes the live feed (watch the network tab: `/events` closes on open, reopens on exit).

- [ ] **Step 8: Commit**

```bash
git add internal/ui/ui.go internal/ui/web/index.html internal/ui/ui_test.go cmd/corral/main.go && git commit -m "feat(ui): replay player — scrub bar, 1x-16x speed, pauses live SSE, read-only by construction

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 9: Live verification against the recorded demo mission + README/DESIGN docs

**Files:**
- Modify: `README.md` (new "Watch it back" subsection), `deploy/demo/README.md` (new "Watch it back" section), `docs/DESIGN.md` (new roadmap entry, sibling to P8)

**Steps:**

- [ ] **Step 1: Full suite + security gate**

Run: `go test ./... -count=1 && go vet ./... && bash scripts/check-security.sh` (use this repo's actual gosec invocation if the script name differs — check how P8's Task 11 ran it)
Expected: all green

- [ ] **Step 2: Live check against tonight's demo mission**

Stash `deploy/demo/.env` first (memory `corralai-demo-dev-env` — it masks first-time-reviewer behavior). Then:

```bash
make demo-mission
```

Once the mission reaches `done` (or `awaiting_review` → accept it), open `http://localhost:9019`, click the **Completed** tab, confirm the just-run mission appears with a non-zero duration and correct task/finding counts, click **details** and confirm phases/findings/executions render, then click **▶ replay** and confirm: the scrub bar advances, nodes/links/bursts appear on the canvas as the stream plays, switching to 16× visibly speeds it up, and **Exit replay** resumes the live view (the `/events` SSE connection reopens — check dev tools' network tab). Screenshot the scrub bar mid-replay. Restore `deploy/demo/.env` afterward.

- [ ] **Step 3: README.md — "Watch it back" subsection**

In `README.md`, insert a new subsection right after "### The knowledge corpus (CORRAL.md)" (currently ending ~line 134) and before the "**Coordinate — one swarm or many.**" paragraph (~line 136):

```markdown
### Watch it back (mission history + replay)

Nothing about a finished mission is thrown away: every task's claim and
completion, every finding and its resolution, every command a bee actually
ran, and the event log itself survive indefinitely. A **Completed tab** lists
past missions — directive, duration, task/finding counts, and (best-effort)
what got learned from them — with a detail view per mission and a **▶ replay**
button. Replay is read-only: it reconstructs the whole build from durable rows
and plays it back on the same corral canvas, at up to **16×**, with a scrub
bar — pause live traffic, watch history move. It works on missions that ran
before this shipped (positions are recomputed, not stored, so nothing needed
to be recorded in advance for the shapes to replay); missions recorded from
here on carry richer ambience too — tool-call activity, claim leases, host
sightings — so future replays show more of what the herd was actually doing,
not just what it built.
```

- [ ] **Step 4: `deploy/demo/README.md` — "Watch it back" section**

In `deploy/demo/README.md`, insert a new `## Watch it back (mission history + replay)` section right after "## Watch it learn (the learning loop)" (currently ending ~line 187) and before "### Ask the brain about the codebase" (~line 189), mirroring that section's numbered-steps style:

```markdown
## Watch it back (mission history + replay)

`make demo-mission` records everything durably as it runs — so once it
finishes, you can replay it:

1. **Open the Completed tab.** `http://localhost:9019` → **completed**. The
   mission you just ran appears with its directive, duration, and
   task/finding counts.
2. **Drill in.** Click **details** — phases with their status, findings with
   outcomes, the PR link if the mission targeted a repo.
3. **Replay it.** Click **▶ replay**. The live view pauses (the `/events`
   feed disconnects) and the whole build plays back on the same canvas:
   agents spawning as they first claim work, execution bursts landing,
   findings surfacing and resolving — reconstructed entirely from durable
   rows, not a recording.
4. **Scrub and speed up.** Drag the scrub bar to jump anywhere in the run, or
   push the speed selector to **16×** to watch a whole mission converge in
   seconds.
5. **Exit replay** to resume the live feed — nothing about replay mode
   touches the running swarm; it's read-only by construction.

Missions run before this shipped still replay (durable rows are all replay
needs); missions run from here on carry more ambience — tool-call activity,
claim leases, host sightings — so their replays show more texture.
```

- [ ] **Step 5: `docs/DESIGN.md` — roadmap entry**

In `docs/DESIGN.md`, add a new roadmap entry immediately after the P8 entry (currently ending ~line 177) and before "### Open threads (next)" (~line 179). Fill in the "**Verified live**" sentence using what Step 2 actually observed (the real mission directive, duration, and counts) — do not invent numbers; if a claim can't be verified live, don't make it:

```markdown
- **P9 — mission history + replay (DONE 2026-07-03).** Every finished mission
  gets a **Completed tab** (`mission_history` read surface, mirroring
  `mission_analytics`'s shape) — directive, status, duration (task-timestamp
  derived until a mission speaks `mission_completed`, then event-based),
  task/finding counts, best-effort learned-linkage (promoted proposals whose
  signature matches the mission's findings), and a detail drill-down
  (phases/tasks/findings/executions). A **▶ replay** button reconstructs the
  whole build from durable rows only — `internal/brain/replay.go` merges task
  lifecycle, findings, executions, and (when present) the telemetry event log
  into one time-ordered stream — and plays it back on the same corral canvas
  through the existing render path at 1×–16×, scrub bar included; live SSE
  pauses while replaying; positions are recomputed, never recorded; a mission
  with no ambience telemetry still replays from its durable rows alone
  (graceful degradation). Recording got richer alongside it: `mission_completed`
  (the engine finally speaks telemetry, on both its auto-complete and
  review-accept paths), `findings.resolved_ts` (the row is no longer
  timeline-blind), `agent_activity` (capped at 2,000/mission with a loud log
  at cap), `claim_made`/`claim_released`, `host_seen` (first sighting +
  material change only), and `memory_written` (metadata only — slug/type/shared,
  never the body). **Verified live** [fill in after Step 2: the demo mission's
  directive, its recorded duration, and what the Completed tab / replay scrub
  actually showed].
```

- [ ] **Step 6: Full suite once more**

Run: `go test ./... -count=1`
Expected: PASS (docs-only changes since Step 1, but confirm nothing regressed)

- [ ] **Step 7: Commit**

```bash
git add README.md deploy/demo/README.md docs/DESIGN.md && git commit -m "docs: mission history + replay — README sugar, demo walkthrough, DESIGN roadmap (verified live)

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

## Self-review notes (performed at write time)

- **Spec coverage:** Part A (list + detail + duration derivation + best-effort learned-linkage + Completed tab) → Tasks 5-6. Part B (durable-only replay, graceful degradation, positions recomputed, live SSE paused, read-only by construction, golden-file test) → Tasks 7-8. Part C (all five new kinds + `resolved_ts` + volume guard + metadata-only discipline) → Tasks 1-4. The spec's "Testing" section maps 1:1: history unit tests (Task 5 Step 5), replay golden-file + graceful-degradation tests (Task 7 Steps 1-5), per-kind emission tests + cap test + memory_written canary (Tasks 1-4), live check (Task 9 Step 2). Deliberately-out-of-scope items (replay-as-eval, recording positions/chatter, retention/GC, cross-mission analytics beyond `mission_analytics`) are untouched by every task above — confirmed no task drifts into them.
- **Addendum coverage (README sugar):** added at Task 9 Steps 3-5 — root README "Watch it back" subsection (durable-only v1 fidelity stated honestly: replay works on pre-existing missions because positions are recomputed, not because old missions were secretly recorded; richer ambience is explicitly framed as forward-looking, not retroactive), `deploy/demo/README.md` walkthrough section showing the demo mission replaying itself, and a `docs/DESIGN.md` P9 entry sibling to P8's structure — its "Verified live" sentence is explicitly left for the implementer to fill from Step 2's actual observation rather than invented, per the addendum's "no overselling" instruction.
- **Placeholder scan:** the one bracketed instruction (`docs/DESIGN.md`'s "[fill in after Step 2: ...]") is intentional and load-bearing — it is a live-observation slot, not a TBD; every other step in this plan carries complete, runnable code or exact verbatim prose. No other "TODO"/"handle appropriately"/"similar to Task N" patterns are present.
- **Type consistency:** `queue.Execution` (`ID,MissionID,Agent,Role,Command,ExitCode,OK,TS`) matches the `executions` schema at `internal/queue/store.go:116-126` as verified via source read. `learn.Proposal.Signature`/`Status`/`learn.StatusApproved` match `internal/learn/store.go` (verified against the already-merged learning-loop plan's Task 1 interfaces). `mission.PhaseView`, `mission.MissionView`, `mission.Mission.ReviewRounds` verified against `internal/mission/store.go:47-107`. `Engine.OnMissionCompleted`'s signature `func(missionID int64, status string, reviewRounds int)` is used identically in Task 1's test, its main.go wiring, and referenced (not re-typed) by Task 9's DESIGN.md prose. `brain.MissionSummary`/`MissionDetail`/`ReplayEvent` are defined once (Tasks 5 and 7) and consumed by name only thereafter (Tasks 6, 8, 9) — no renaming drift. `ui.Deps` field names (`History`, `HistoryDetail`, `Replay`) match between Task 6/8's `ui.go` additions and Task 6/8's `main.go` wiring steps.
- **Drift flags carried from research (resolved in-plan):** `ActivityRing`'s feed method is `Add`, not `Feed` — used correctly throughout Task 3. `phases.status` is derived, never `UPDATE`d — Task 5's `summarize()` reads task-derived counts via `q.List`/`q.FindingsFiltered`, never touches `phases` directly, consistent with that invariant. `engine.go` does not import `internal/telemetry` — Task 1 keeps the emission at the two call sites that already import it (`missions.go`, `main.go`), never inside `internal/mission`. No `ExecutionsByMission` existed before this plan — added fresh in Task 5, reused by Task 7 (not redefined).
