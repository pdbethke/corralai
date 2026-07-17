<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Pool reasoning trace — Slice 1 (recording becomes complete + honest) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Steps use `- [ ]`.

**Goal:** Make an adversarial-pool run produce a complete, honest recording: the tracking mission counts correctly (meta), the replay carries the real evidence (mutants, critique, authored test), and the run emits ordered reasoning events (subject, dev-adequacy, verdict).

**Spec:** `docs/superpowers/specs/2026-07-16-pool-reasoning-trace-and-shared-tests-design.md` (Slices 1a–1c; 1d durable artifacts moved to Slice 2 where it's consumed).

**Architecture:** Four additive changes. (1) The pool runtime transitions its tracking mission to a terminal status on convergence → the mission enters `/api/history` → meta counts work. (2) `BuildReplayStream` threads the already-stored `Task.Result` and `Finding.Evidence` into the replay detail (reuse, zero new writes). (3) The pure `advpool.Driver` gains an OPTIONAL `EventSink` (mirroring `Signer`/`Leaderboard`) and emits `pool_subject`/`pool_dev_adequacy`/`pool_verdict`. (4) The brain wires that sink to telemetry keyed on the run's `missionID`. Nothing renders yet (Slice 3); this makes the DATA complete so an export is real.

**Tech Stack:** Go 1.26.5; `internal/advpool`, `internal/brain`, `internal/mission`, `internal/telemetry`.

## Global Constraints
- New files start `// SPDX-License-Identifier: Elastic-2.0`. Module `github.com/pdbethke/corralai`, Go 1.26.5.
- **Reuse, don't duplicate:** the mutants + authored test are already in `queue.Task.Result`; the critique in `queue.Finding.Evidence`. Surface them; do NOT re-store them. Only the small `pool_subject`/`pool_verdict`/`pool_dev_adequacy` events are new writes.
- **Driver stays pure:** the `EventSink` is an OPTIONAL interface (nil ⇒ no-op), exactly like `Signer`/`Leaderboard`. The driver must build + test with a nil sink unchanged.
- **Honesty:** events carry the real evidence/values, never a summary; the mission status maps faithfully from `Verdict.Status` (certified→a certified-ish terminal status, needs-review→a needs-review terminal status).
- Verify gate before each commit: `gofmt -l` (empty), `go build ./...`, `go test ./<touched package>/... [-race for advpool/queue]`, `bash scripts/check-security.sh` (exit 0).

---

### Task 1: Transition the tracking mission to terminal on convergence (fixes the meta)

**Files:**
- Modify: `internal/brain/advpool.go` (`AdvPoolRuntime.tick` — the branch that clears the active slot on a non-nil Verdict)
- Test: `internal/brain/advpool_test.go`

**Interfaces:**
- Consumes: `opts.Missions *mission.Store` (already on the runtime as `rt.missions`); `mission.Store.SetMissionStatus(id int64, status string) error` (store.go:341); `advpool.Verdict.Status` (`certified`|`needs-review`); `advpool.StatusCertified`/`StatusNeedsReview`.
- Produces: on convergence, `rt.missions.SetMissionStatus(id, <terminal>)` so the tracking mission leaves `status='running'` and enters `MissionHistoryList`/`summarize`.

**Design:** the pool tracking mission is created `status='running'` and never transitioned, so `MissionHistoryList` (which skips running/paused) excludes it → `/api/history` omits it → the export meta is 0/0/0. In `tick`, when `driver.Tick` returns a non-nil `Verdict`, set the mission status. Map: `StatusCertified`→`"certified"`, else `"needs-review"` (both terminal, both counted). Log it.

- [ ] **Step 1: Write the failing test**

In `internal/brain/advpool_test.go`, using the file's runtime+driver harness (the one that drives a run to a converged Verdict — reuse it), assert the tracking mission's status is terminal after convergence:

```go
func TestAdvPoolConvergenceSetsMissionTerminalStatus(t *testing.T) {
	// Build a runtime whose driver converges a run at mission id M with a
	// known Verdict.Status (reuse the file's converge harness). Its mission
	// store must be a real *mission.Store (so SetMissionStatus persists) — if
	// the harness uses a fake, extend it to expose the status, or use a real
	// in-memory mission.Store seeded with the tracking mission.
	rt, missions, mid := /* runtime + its mission store + the mission id */
	rt.tick(context.Background()) // drives Tick → Verdict → status transition
	got := /* missions status for mid */
	if got != "certified" && got != "needs-review" {
		t.Fatalf("converged pool mission must be terminal, got %q", got)
	}
	// And it must match the verdict: a certified run → "certified".
}
```

If the existing harness's mission store is a fake without a status getter, prefer seeding a real `mission.Store` (in-memory/temp DB, mirroring other brain tests) so `SetMissionStatus` + a read prove the transition. Confirm how the file's tests construct a mission store.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/brain/ -run TestAdvPoolConvergenceSetsMissionTerminalStatus -v`
Expected: FAIL — the mission stays `"running"`.

- [ ] **Step 3: Set the status on convergence**

In `AdvPoolRuntime.tick`, inside the `if verdict != nil` branch (where it logs convergence + clears `activeID`), add:

```go
	status := "needs-review"
	if verdict.Status == advpool.StatusCertified {
		status = "certified"
	}
	if rt.missions != nil {
		if err := rt.missions.SetMissionStatus(id, status); err != nil {
			log.Printf("advpool: run %d set mission status %q: %v", id, status, err)
		}
	}
```

Place it alongside the existing convergence log. Confirm `rt.missions` is the field name (grep the struct).

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/brain/ -run TestAdvPool -v` then `go test ./internal/brain/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/brain/advpool.go internal/brain/advpool_test.go
go build ./... && go test ./internal/brain/... && bash scripts/check-security.sh
git add internal/brain/advpool.go internal/brain/advpool_test.go
git commit -m "advpool: converged tracking mission -> terminal status (un-breaks recording meta counts)"
```

---

### Task 2: Replay carries the real evidence (Task.Result + Finding.Evidence)

**Files:**
- Modify: `internal/brain/replay.go` (`BuildReplayStream` — the task loop ~44-80 and the findings loop ~86-97)
- Test: `internal/brain/replay_test.go`

**Interfaces:**
- Consumes: `queue.Task.Result`; `queue.Finding.Evidence`/`SuggestedAction`/`Target`.
- Produces: `task_done` (the terminal task event) detail gains `result` (the mutants / the authored test — the OUTPUT); `finding_reported` detail gains `evidence` + `suggested_action` (the critic's actual argument). `task_created` keeps `instruction` (the INPUT) unchanged.

**Design:** the evidence is already stored; replay just drops it. Add `result` to the terminal task event's detail (not `task_created`, which already carries the large `instruction`) when `t.Result != ""`. Add `evidence`/`suggested_action` to `finding_reported`'s detail (currently only `{type, severity}`).

- [ ] **Step 1: Write the failing test**

Extend `internal/brain/replay_test.go` (reuse its queue+mission setup). Enqueue a task, claim+complete it with a `Result`, add a finding with `Evidence`, build the stream, assert the detail carries them:

```go
func TestReplayCarriesResultAndEvidence(t *testing.T) {
	q, tel, mid := /* the file's setup */
	// A completed task with a Result (the "mutants"/"authored test").
	// (Use the file's enqueue/claim/complete helpers.)
	// A finding with Evidence (the critic's argument).
	// ... AddFinding(queue.Finding{MissionID: mid, TaskID: <tid>, Type:"note",
	//        Severity:"high", Target:"TestX", Evidence:"the test asserts nothing"})
	events, err := BuildReplayStream(q, tel, mid)
	if err != nil { t.Fatal(err) }
	var sawResult, sawEvidence bool
	for _, e := range events {
		if e.Kind == "task_done" || e.Kind == "task_completed" {
			if r, _ := e.Detail["result"].(string); r != "" { sawResult = true }
		}
		if e.Kind == "finding_reported" {
			if ev, _ := e.Detail["evidence"].(string); ev != "" { sawEvidence = true }
		}
	}
	if !sawResult { t.Fatal("replay must carry the task Result (the evidence source)") }
	if !sawEvidence { t.Fatal("replay must carry the finding Evidence (the critic's argument)") }
}
```

Match the terminal task-event kind the code actually emits (grep `task_` in replay.go — it builds `task_<status>` from `DoneTS`; confirm the exact kind string, e.g. `task_done`).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/brain/ -run TestReplayCarriesResultAndEvidence -v`
Expected: FAIL — no `result`/`evidence` in detail.

- [ ] **Step 3: Thread Result + Evidence into the detail**

In `BuildReplayStream`:
- In the task loop, where the terminal (`DoneTS`) event's `Detail` is built, add `result` when non-empty:
  ```go
  if t.Result != "" {
      detail["result"] = t.Result
  }
  ```
  (Match the actual detail-map variable + the terminal-event construction; the map already carries `role`/`title` on create — mirror that shape.)
- In the findings loop, extend the `finding_reported` detail (currently `{type, severity}`) with:
  ```go
  if f.Evidence != "" { detail["evidence"] = f.Evidence }
  if f.SuggestedAction != "" { detail["suggested_action"] = f.SuggestedAction }
  ```
  Keep `Subject = f.Target` as-is.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/brain/ -run 'TestReplay' -v` then `go test ./internal/brain/...`
Expected: PASS (new + existing replay tests; existing detail assertions unaffected — we only ADD keys).

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/brain/replay.go internal/brain/replay_test.go
go build ./... && go test ./internal/brain/... && bash scripts/check-security.sh
git add internal/brain/replay.go internal/brain/replay_test.go
git commit -m "replay: carry Task.Result + Finding.Evidence (the evidence a trace needs)"
```

---

### Task 3: Driver `EventSink` + reasoning-event emission

**Files:**
- Modify: `internal/advpool/driver.go` (add the interface + a `Events` field; emit in `StartRun`, `tickDevAdequacy`, `tickAggregate`)
- Test: `internal/advpool/driver_test.go`

**Interfaces:**
- Produces: `type EventSink interface { Emit(missionID int64, kind, subject string, detail map[string]any) }`; `Driver.Events EventSink` (optional, nil ⇒ no-op); emissions:
  - `pool_subject` in `StartRun` — detail `{goal, code, dev_test_code, code_path, dev_test_path}` from the `RunSpec` (the inputs the trace opens on).
  - `pool_dev_adequacy` in `tickDevAdequacy` after scoring — detail `{dev_kill_rate, mutants_total, survivors}` (+ survivor mutant IDs; NOT the mutant source — that's recovered from the mutant-gen task Result).
  - `pool_verdict` in `tickAggregate` after signing — detail `{status, dev_kill_rate, mutants_total, survivors, proven_missed, models_by_role, record_id, record_head}`.

**Design:** mirror the existing optional `Signer`/`Leaderboard` fields (nil-guarded). Add a tiny helper `func (d *Driver) emit(missionID int64, kind, subject string, detail map[string]any)` that no-ops when `d.Events == nil`. Call it at the three milestones. The driver takes NO telemetry dependency — `EventSink` is the seam.

- [ ] **Step 1: Write the failing test**

In `internal/advpool/driver_test.go`, add a fake sink capturing emissions and assert the three events fire with the right payload across a full run (reuse the file's converge harness):

```go
type fakeEventSink struct{ events []struct{ mid int64; kind, subject string; detail map[string]any } }
func (f *fakeEventSink) Emit(mid int64, kind, subject string, detail map[string]any) {
	f.events = append(f.events, struct{ mid int64; kind, subject string; detail map[string]any }{mid, kind, subject, detail})
}

func TestDriverEmitsReasoningEvents(t *testing.T) {
	sink := &fakeEventSink{}
	d := /* newTestDriver with d.Events = sink */
	// drive a full run to convergence (reuse completeFullRun / the converge helper)
	kinds := map[string]map[string]any{}
	for _, e := range sink.events { kinds[e.kind] = e.detail }
	for _, want := range []string{"pool_subject", "pool_dev_adequacy", "pool_verdict"} {
		if _, ok := kinds[want]; !ok { t.Fatalf("missing emit %q; got %v", want, keysOf(kinds)) }
	}
	if kinds["pool_verdict"]["status"] == nil || kinds["pool_verdict"]["dev_kill_rate"] == nil {
		t.Fatalf("pool_verdict detail incomplete: %v", kinds["pool_verdict"])
	}
	if kinds["pool_subject"]["dev_test_code"] == nil {
		t.Fatal("pool_subject must carry the dev tests (the subject)")
	}
}

// And nil-sink safety: a Driver with Events==nil drives a run with no panic.
func TestDriverNilEventSinkNoop(t *testing.T) {
	d := /* newTestDriver, Events left nil */
	// drive a full run; assert it converges without panicking (Events nil).
}
```

Reuse the file's helpers (`newTestDriver`, `completeFullRun`, `keysOf`). Adapt payload-key assertions to the exact detail keys you emit.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/advpool/ -run 'TestDriverEmits|TestDriverNilEventSink' -v`
Expected: FAIL to compile — `EventSink`/`d.Events` undefined.

- [ ] **Step 3: Implement the sink + emissions**

Add near `Signer`/`LeaderboardSink`:

```go
// EventSink receives the pool's reasoning milestones as replay/telemetry
// events. Optional (nil ⇒ no-op), like Signer/Leaderboard — the pure Driver
// takes no telemetry dependency; the brain wires this to its telemetry store
// keyed on the run's missionID. Kinds: pool_subject, pool_dev_adequacy,
// pool_verdict. detail carries the real values/evidence, never a summary.
type EventSink interface {
	Emit(missionID int64, kind, subject string, detail map[string]any)
}
```
Add `Events EventSink` to `Driver` (near `Signer`), and:
```go
func (d *Driver) emit(missionID int64, kind, subject string, detail map[string]any) {
	if d.Events != nil {
		d.Events.Emit(missionID, kind, subject, detail)
	}
}
```
Emit at:
- End of `StartRun` (after the run is registered): `d.emit(missionID, "pool_subject", rs.CodePath, map[string]any{"goal": rs.Goal, "code": rs.Code, "dev_test_code": rs.DevTestCode, "code_path": rs.CodePath, "dev_test_path": rs.DevTestPath})`.
- End of `tickDevAdequacy` after `run.devKillRate`/`run.devSurvivors` are set: `d.emit(missionID, "pool_dev_adequacy", "", map[string]any{"dev_kill_rate": run.devKillRate, "mutants_total": run.mutantsTotal, "survivors": len(run.devSurvivors), "survivor_ids": survivorIDs(run.devSurvivors)})` (survivorIDs = a helper returning the `[]string` of `Mutant.ID`).
- In `tickAggregate` after signing (after `v.RecordID` is set, before/after storing `run.verdict`): `d.emit(missionID, "pool_verdict", v.Commit, map[string]any{"status": v.Status, "dev_kill_rate": v.DevKillRate, "mutants_total": v.MutantsTotal, "survivors": v.Survivors, "proven_missed": v.ProvenMissed, "models_by_role": v.ModelsByRole, "record_id": v.RecordID, "record_head": v.RecordHead})`.

Confirm `tickDevAdequacy`/`tickAggregate` have `missionID` in scope (Tick passes it; the tick helpers take it or the run — thread `missionID` if a helper lacks it).

- [ ] **Step 4: Run tests to verify they pass (with -race)**

Run: `go test ./internal/advpool/ -race -run 'TestDriver' -v` then `go test ./internal/advpool/ -race`
Expected: PASS, no races.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/advpool/driver.go internal/advpool/driver_test.go
go build ./... && go test ./internal/advpool/... -race && bash scripts/check-security.sh
git add internal/advpool/driver.go internal/advpool/driver_test.go
git commit -m "advpool: optional EventSink emits pool_subject/dev_adequacy/verdict reasoning events"
```

---

### Task 4: Wire the EventSink to telemetry (brain-side)

**Files:**
- Modify: `internal/brain/advpool.go` (`StartAdversarialPool` — set `driver.Events`)
- Test: `internal/brain/advpool_test.go`

**Interfaces:**
- Consumes: `advpool.EventSink` (Task 3); `rec(tel, missionID, kind, actor, subject, detail)` (internal/brain/telemetry.go:16); `opts.Telemetry *telemetry.Store`.
- Produces: an `advpoolEventSink{ tel *telemetry.Store }` whose `Emit` calls `rec(s.tel, missionID, kind, "corral-advpool", subject, detail)`; `StartAdversarialPool` sets `driver.Events = advpoolEventSink{tel: opts.Telemetry}`.

**Design:** mirror `advpoolLeaderboardSink` (same file) — a tiny telemetry-backed adapter. `rec` already no-ops on nil `tel`. The events land in telemetry keyed on `missionID`, so `BuildReplayStream`'s `EventsForMission` picks them up automatically.

- [ ] **Step 1: Write the failing test**

```go
func TestAdvPoolEventSinkRecordsToTelemetry(t *testing.T) {
	tel := /* an in-memory telemetry.Store (reuse the file's telemetry setup) */
	sink := advpoolEventSink{tel: tel}
	sink.Emit(7, "pool_verdict", "abc123", map[string]any{"status": "certified"})
	evs, err := tel.EventsForMission(7)
	if err != nil { t.Fatal(err) }
	found := false
	for _, e := range evs {
		if e.Kind == "pool_verdict" && e.Detail["status"] == "certified" { found = true }
	}
	if !found { t.Fatal("EventSink.Emit must record a pool_verdict telemetry event for the mission") }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/brain/ -run TestAdvPoolEventSinkRecordsToTelemetry -v`
Expected: FAIL to compile — `advpoolEventSink` undefined.

- [ ] **Step 3: Implement + wire**

Add (near `advpoolLeaderboardSink`):
```go
// advpoolEventSink adapts the driver's reasoning events to the brain's
// telemetry store, keyed on the run's mission id so BuildReplayStream surfaces
// them in the run's replay. Actor is the fixed pool actor. nil tel => rec()
// no-ops (telemetry optional everywhere).
type advpoolEventSink struct{ tel *telemetry.Store }

func (s advpoolEventSink) Emit(missionID int64, kind, subject string, detail map[string]any) {
	rec(s.tel, missionID, kind, "corral-advpool", subject, detail)
}
```
In `StartAdversarialPool`, after constructing the driver (near `driver.Signer = ...`): `driver.Events = advpoolEventSink{tel: opts.Telemetry}`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/brain/... -race` then `go build ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w internal/brain/advpool.go internal/brain/advpool_test.go
go build ./... && go test ./internal/brain/... && bash scripts/check-security.sh
git add internal/brain/advpool.go internal/brain/advpool_test.go
git commit -m "advpool: wire the EventSink to telemetry (reasoning events ride the run's replay)"
```

---

### Task 5: Deploy + verify a real recording (controller)

Not a subagent task. After merge+deploy: re-run the cross-vendor fence pool run, export it (`scripts/export-golden-run.sh --mission <id> --brain-url https://brain.corralai.dev --bearer <jwt> --i-know --yes --platform-inference "vendor cloud …" --slug <name>`), and confirm: the meta has REAL counts (task_count>0, finding_count reflects the critic, duration>0), and the replay stream now carries `pool_subject`/`pool_dev_adequacy`/`pool_verdict` events plus `result`/`evidence` in the task/finding details. That proves the recording is complete (Slice 3 will render it; Slice 2 will share the tests).

## Self-Review (plan author)
- Slice-1 coverage: 1b→T1 (status/meta), 1a→T2 (replay evidence), 1c→T3 (emit) + T4 (wire). 1d (durable artifacts) explicitly deferred to Slice 2.
- Type consistency: `EventSink.Emit(missionID int64, kind, subject string, detail map[string]any)` identical in T3 (def), T3 test (fake), T4 (impl); `rec` signature matches telemetry.go:16; `SetMissionStatus(id, status)` matches store.go:341.
- No-regression: T2 only ADDS detail keys; T3's sink is nil-safe (TestDriverNilEventSinkNoop); T1 only transitions a previously-stuck status.
