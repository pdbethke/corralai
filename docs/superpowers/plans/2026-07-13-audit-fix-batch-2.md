<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Audit fix batch 2 — reliability + N+1/DRY + jail/browser + auth (H-3) + Lows — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Close everything still open from the 2026-07-12 core audit after batch 1 — the mission/queue reliability + correctness Mediums, the N+1/DRY cleanups, the jail + browser hardening, the H-3 OIDC/auth hardening (design folded in), and the security-relevant Lows.

**Architecture:** Five phases across `internal/mission`, `internal/queue`, `internal/repoindex`, `internal/memory`, `internal/sandbox`, `internal/brain`, `internal/auth`, plus one new shared package `internal/searchmerge`. Where the target logic is buried in a loop or handler, extract a testable helper and TDD the helper; otherwise the existing suites + a new fake-IdP JWT harness are the net. Phases are independent; execute in order and treat each phase boundary as a review checkpoint.

**Tech Stack:** Go 1.26.5.

## Global Constraints
- SPDX (`// SPDX-License-Identifier: Elastic-2.0`) on every new file; TDD; per commit run `export PATH="$PATH:$HOME/go/bin"` then `go vet ./...` + `go build ./...` + `go test ./...` + `bash scripts/check-security.sh` — all green.
- Fixes are behavior-correcting but MUST NOT regress the audit's verified-clean invariants: fail-closed gates, claim atomicity (one bee per task ever), the credential boundary, and the sandbox `nil backend ⇒ never unsandboxed` guard.
- Determinism preserved (injected clocks where a store/lib has one; assert on call-count/behavior, not wall-clock, where it doesn't).
- corral metaphor; "control owner", never "CISO".
- Non-loosening: every gate/interlock this batch touches ends stricter or equal, never looser.

## Module path
`github.com/pdbethke/corralai`.

---

# PHASE 1 — Mission + queue reliability & correctness

## Task 1.1: reflex cap must count only OPEN remediation (not completed)

**Files:** Modify `internal/mission/replan.go`; Test `internal/mission/replan_test.go`

**Bug:** `replan` builds `reflexCount` from `e.q.List(missionID)` (every status) counting any `fix-*`/`verify-*` key (`replan.go:92-97`), then `reflexCount+len(specs) > e.ReflexMaxTasks` (`:125`) drives `failMission` (irreversible terminal `failed`). A long healthy mission that completed many remediation cycles accumulates >cap DONE reflex tasks and the next finding falsely fails it. The cap is meant to bound *in-flight, non-converging* remediation; the code comments at `:87` and `:131-133` already say "open".

**Interfaces:** Produces nothing new — the counting loop learns to skip terminal statuses (`queue.StatusDone`/`StatusCancelled`/`StatusSuperseded`, from `internal/queue/store.go:26-32`).

- [ ] **Step 1: Failing test** — add to `internal/mission/replan_test.go`, using `reflexEngine(t)` (`:62`) and the claim+`q.Complete` cycle from `TestReplanDeduplicatesRecurringFindings` (`:173-192`):
```go
func TestReflexCapExcludesCompletedRemediation(t *testing.T) {
	e, q, m := reflexEngine(t)
	mid, err := CreateMission(m, q, "converge", []PhaseSpec{{Name: "build", Role: "coder", Instruction: "x"}}, false)
	if err != nil { t.Fatal(err) }
	e.ReflexMaxTasks = 2 // tiny cap

	// Drive 3 full fix→verify remediation cycles to DONE (well past the cap of 2),
	// then assert a NEW finding does NOT fail the mission — completed reflex tasks
	// must not count toward the in-flight cap.
	for i := 0; i < 3; i++ {
		if _, err := q.AddFinding(queue.Finding{MissionID: mid, Reporter: "r", Type: "bug", Severity: "high", Target: fmtTarget(i), Evidence: "e"}); err != nil { t.Fatal(err) }
		if err := e.replan(mid); err != nil { t.Fatal(err) }
		drainReflex(t, q, mid) // claim+complete every ready fix-*/verify-* task
	}
	// One more finding; with 6 DONE reflex tasks and cap=2, the buggy code fails the mission here.
	if _, err := q.AddFinding(queue.Finding{MissionID: mid, Reporter: "r", Type: "bug", Severity: "high", Target: "final", Evidence: "e"}); err != nil { t.Fatal(err) }
	if err := e.replan(mid); err != nil { t.Fatal(err) }
	mi, _ := m.Mission(mid)
	if mi.Status == "failed" {
		t.Fatalf("mission falsely failed: completed remediation counted toward the reflex cap")
	}
}
```
> Implementer: reuse/inline the neighboring tests' helpers — `TestReplanCapStopsRunaway` (`:217`) and `TestReflexCapFailsMissionNotOscillatingPause` (`:234`) show the exact cap/finding wiring; `TestReplanDeduplicatesRecurringFindings` shows `q.PromoteReady`→`q.ClaimNext`→`q.Complete`. Write `drainReflex`/`fmtTarget` inline or adapt `drain`. Keep it deterministic (real sqlite temp stores).
- [ ] **Step 2: Run, watch fail** — `go test ./internal/mission/ -run TestReflexCapExcludesCompletedRemediation` → mission is `failed`.
- [ ] **Step 3: Implement.** In `replan.go`, replace the counting loop (`:92-97`) so only non-terminal reflex tasks count:
```go
	// Count only OPEN reflex tasks. The cap bounds IN-FLIGHT, non-converging
	// remediation — a mission that legitimately completed many fix→verify cycles
	// must not be failed for its lifetime throughput. Terminal reflex tasks
	// (done/cancelled/superseded) are converged history, not runaway.
	reflexCount := 0
	for _, t := range existing {
		if !isReflexTask(t.Key) {
			continue
		}
		switch t.Status {
		case queue.StatusDone, queue.StatusCancelled, queue.StatusSuperseded:
			// terminal — do not count
		default:
			reflexCount++
		}
	}
```
- [ ] **Step 4: Run, watch pass.** Full gate. **Commit:** `fix(mission): reflex cap counts only open remediation — no false-fail on healthy long missions (audit M)`.

---

## Task 1.2: dep-sweep blocker must keep gating convergence (no false PR)

**Files:** Modify `internal/mission/replan.go` (and/or `engine.go`); Test `internal/mission/engine_test.go`

**Bug:** `sweepBlockedDeps` files a `Type:"missing-req"`, `Severity:"high"`, `Reporter:"dep-sweep"` finding (`engine.go:601-606`). Because `missing-req` is an actionable reflex type (`reflexRules`, `replan.go:23`), the next `replan` turns it into a `fix/verify` pair AND flips it to `FindingAddressed` (`replan.go:144-148`). The convergence gate `blockingFindingOpen` only reads `FindingOpen` (`engine.go:267`), so the cancelled-work-chain blocker vanishes → `Tick` falls through to `done` + `finishRepoMission` → `OpenPR` (`engine.go:407-420, 755`). A PR is opened despite a dead dependency chain, defeating the needs-review human gate (`engine.go:401-406`).

**Design decision (which seam):** Fix at the `reflexRules`/`replan` seam — a dep-sweep blocker (`Reporter=="dep-sweep"`, i.e. `Target` has the `blocked-dep:` prefix) is a *structural* problem that requires re-planning or human judgment, NOT an auto-remediable `fix/verify` pair. It must stay OPEN so `blockingFindingOpen` keeps the mission at `needs-review`. So: `replan` must not auto-address dep-sweep findings. Keep them in the open set; they route to the human gate. (This preserves the existing `needs-review` path rather than inventing a new gate branch.)

**Interfaces:** `replan` gains a guard: findings with `Reporter == "dep-sweep"` are skipped by the reflex-remediate+address loop (they remain `FindingOpen`).

- [ ] **Step 1: Failing test** — add to `internal/mission/engine_test.go`, modeled on `TestEngineSweepsBlockedDependencies` (`:415`), using `drain` (`:124`), the `fakeRepo` spy's `prCalls` (`staffing_model_test.go:23`), and the `OnMissionCompleted` capture (`:335`):
```go
func TestBlockedDepChainRoutesToNeedsReviewNotPR(t *testing.T) {
	// Reuse the TestEngineSweepsBlockedDependencies setup: a task whose dependency
	// was cancelled, so sweepBlockedDeps files a high 'missing-req' dep-sweep finding.
	// ... (build q, m, e; e.Repo = &fakeRepo{}; create mission w/ an orphaned dep) ...
	repo := &fakeRepo{}
	e.Repo = repo
	// drive to convergence
	for i := 0; i < 60; i++ {
		_ = e.Tick()
		drain(t, q)
		mi, _ := m.Mission(mid)
		if mi.Status == "done" || mi.Status == "needs-review" || mi.Status == "failed" { break }
	}
	mi, _ := m.Mission(mid)
	if mi.Status == "done" {
		t.Fatalf("mission converged to done despite a cancelled dependency chain")
	}
	if repo.prCalls != 0 {
		t.Fatalf("opened %d PR(s) despite a blocked dep chain; want 0 (should be needs-review)", repo.prCalls)
	}
	if mi.Status != "needs-review" {
		t.Fatalf("status = %q, want needs-review", mi.Status)
	}
}
```
> Implementer: copy the orphaned-dependency construction verbatim from `TestEngineSweepsBlockedDependencies` (it already cancels a dep and asserts the dep-sweep finding is filed). The delta is asserting final `needs-review` + `prCalls==0`.
- [ ] **Step 2: Run, watch fail** — mission converges to `done`, `prCalls==1`.
- [ ] **Step 3: Implement.** In `replan.go`, in the loop that calls `reflexRules` + `SetFindingStatus(FindingAddressed)`, skip dep-sweep findings so they stay open:
```go
		// A dep-sweep blocker (cancelled work chain) is a STRUCTURAL failure, not an
		// auto-remediable defect. Leave it OPEN so the convergence gate holds the
		// mission at needs-review (the human gate) instead of auto-addressing it into
		// a false convergence that opens a PR over dead work.
		if f.Reporter == "dep-sweep" {
			continue
		}
```
  Place this at the top of the per-finding loop body in `replan`, before `reflexRules(f)` is consulted. (Confirm the loop variable is `f` and that `continue` skips both the spec-generation and the `SetFindingStatus(FindingAddressed)` call for that finding.)
- [ ] **Step 4: Run, watch pass.** Full gate. **Commit:** `fix(mission): dep-sweep blockers stay open — cancelled work chain routes to needs-review, not a false PR (audit M)`.

---

## Task 1.3: staffing must not head-of-line block the tick or re-probe every tick

**Files:** Modify `internal/mission/engine.go`; Test `internal/mission/*_test.go`

**Bug:** `Tick` runs `e.Staffing.Judge(ctx, ...)` (a 30s-bounded blocking LLM call) inline on the single tick goroutine, first thing per running mission (`engine.go:327-347`). It iterates all running missions sequentially, so one mission's staffing round-trip stalls every other mission's promote/converge/PR. Worse, the `staffed[mi.ID]=true` latch is set only on success (`:345`); on failure the next tick re-runs the full 30s probe — every tick, no backoff, no give-up.

**Design decision:** Move the Judge call OFF the tick goroutine (async, per mission) and add a failure cooldown + attempt cap so a failing probe doesn't spin. Guard the staffing bookkeeping (`staffed`, plus new `staffing map[int64]bool` in-progress set, `staffAttempts map[int64]int`, `staffGiveUp map[int64]bool`) with a small `sync.Mutex` since a goroutine now touches them. `RoleModels.Set` is already threadsafe (`engine.go:343`). Mirror the existing `prAttempts`/`prGaveUp`/`maxPRAttempts` backoff shape (`engine.go:186-187, 218, 778`).

**Interfaces:** `Engine` gains `staffMu sync.Mutex`, `staffInflight map[int64]bool`, `staffAttempts map[int64]int`, `staffGaveUp map[int64]bool` (all initialized in the engine constructor alongside `staffed`). Add `const maxStaffAttempts = 3`.

- [ ] **Step 1: Failing test** — new test in package `mission` using `fakeLLM` (`routing_test.go:13-23`) + `fakePerf` (`:25-31`) + `rolemodel.New()`:
```go
func TestStaffingDoesNotReprobeEveryTickOnFailure(t *testing.T) {
	// fakeLLM.Generate returns an error and counts calls.
	llm := &countingFailLLM{} // Generate: calls++; return "", errBoom
	// build e with e.Staffing = &StaffingManager{LLM: llm, Perf: &fakePerf{}, RoleModels: rolemodel.New(), ...}
	// one running mission
	for i := 0; i < 10; i++ { _ = e.Tick(); e.waitStaffingIdle() /* test hook: block until no inflight */ }
	if llm.calls > maxStaffAttempts {
		t.Fatalf("staffing probed %d times across 10 ticks; want <= %d (backoff/give-up)", llm.calls, maxStaffAttempts)
	}
}
```
> Implementer: because staffing becomes async, add an unexported test-only sync helper (e.g. `func (e *Engine) waitStaffingIdle()` that locks `staffMu` and waits until `len(e.staffInflight)==0`, or have Tick dispatch via a `sync.WaitGroup` the test can await). Keep the hook minimal and unexported. Assert by call-count, not wall-clock (no injected clock in this package).
- [ ] **Step 2: Run, watch fail** — `calls` grows ~1 per tick (10), exceeding `maxStaffAttempts`.
- [ ] **Step 3: Implement.** Replace the inline staffing block (`engine.go:327-347`) with an async dispatch guarded by the mutex + attempt cap:
```go
		// Staff at most once per mission, OFF the tick goroutine, with a bounded
		// attempt cap so a failing Judge doesn't re-probe every tick or head-of-line
		// block other missions behind a 30s LLM round-trip.
		if e.Staffing != nil && e.Staffing.LLM != nil && e.Staffing.LLM.Available() {
			e.staffMu.Lock()
			skip := e.staffed[mi.ID] || e.staffInflight[mi.ID] || e.staffGaveUp[mi.ID]
			if !skip {
				e.staffInflight[mi.ID] = true
			}
			e.staffMu.Unlock()
			if !skip {
				go e.staffMission(mi.ID, mi.Directive)
			}
		}
```
  Add the worker:
```go
// staffMission runs the Sense→Judge→Clamp staffing pass for one mission off the
// tick goroutine. On success it latches staffed[id]; on failure it counts an
// attempt and gives up after maxStaffAttempts (falling back to the default policy),
// so it never re-probes every tick or stalls the tick loop.
func (e *Engine) staffMission(missionID int64, directive string) {
	defer func() {
		e.staffMu.Lock()
		delete(e.staffInflight, missionID)
		e.staffMu.Unlock()
	}()
	resources := e.Staffing.Sense()
	stats := e.Staffing.Perf.GetRoleModelStats()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	assignments, loadOrder, err := e.Staffing.Judge(ctx, directive, resources, stats, 3, 3)
	cancel()
	if err != nil {
		e.staffMu.Lock()
		e.staffAttempts[missionID]++
		if e.staffAttempts[missionID] >= maxStaffAttempts {
			e.staffGaveUp[missionID] = true
			log.Printf("mission %d: dynamic staffing gave up after %d attempts — using default policy", missionID, e.staffAttempts[missionID])
		} else {
			log.Printf("mission %d: dynamic staffing judge failed (attempt %d): %v", missionID, e.staffAttempts[missionID], err)
		}
		e.staffMu.Unlock()
		return
	}
	clamped := e.Staffing.Clamp(assignments, resources)
	log.Printf("mission %d: dynamic staffing complete. Clamped: %+v, Load Order: %v", missionID, clamped, loadOrder)
	for role, model := range clamped {
		e.Staffing.RoleModels.Set(role, staffedModelRef(model))
	}
	e.staffMu.Lock()
	e.staffed[missionID] = true
	e.staffMu.Unlock()
}
```
  Add the fields to the `Engine` struct + `const maxStaffAttempts = 3`, and initialize the three new maps in the constructor next to `staffed: map[int64]bool{}`. Add the `waitStaffingIdle` (or WaitGroup) test hook. Import `sync` if not already.
- [ ] **Step 4: Run, watch pass.** Also run `go test -race ./internal/mission/...` (the new goroutine touches shared maps). Full gate. **Commit:** `fix(mission): staffing runs off the tick goroutine with bounded retries — no head-of-line block or per-tick re-probe (audit M)`.

---

## Task 1.4: ClaimNextAs — status-guard the claim UPDATE

**Files:** Modify `internal/queue/store.go`; Test `internal/queue/*_test.go`

**Bug:** the claim UPDATE (`store.go:423-428`) writes `... WHERE id=?` with no status guard — correctness rests solely on `SetMaxOpenConns(1)`. The self-heal reissue UPDATE (`:372-374`) is the same shape. Add an explicit `AND status=?` guard + `RowsAffected()==1` assertion so the claim is provably atomic regardless of pool size.

**Interfaces:** none new; the two UPDATEs gain a status predicate and a rows-affected check.

- [ ] **Step 1: Failing test** — add to `internal/queue`, reusing `open(t)` (`store_test.go:11`), `Enqueue`, `PromoteReady`, `ClaimNextAs` (see `reclaim_test.go:24-54`). Assert that the SELECTed task is claimed only from `ready`, and that the guard rejects a task whose status changed. A behavioral test: enqueue+promote one task, claim it once (ok), then a second `ClaimNextAs` returns no task (nothing ready) — and, by inspecting via a helper, the claim UPDATE affected exactly one row. (Since a true concurrent race isn't reproducible under pool-of-1, the test pins the SQL contract: the guarded UPDATE returns `sql.Result` with `RowsAffected()==1` on success and the code errors if it's 0.)
```go
func TestClaimNextAsStatusGuardedUpdate(t *testing.T) {
	s := open(t)
	_, err := s.Enqueue(/* one task, mission M */)
	if err != nil { t.Fatal(err) }
	if _, err := s.PromoteReady(missionID); err != nil { t.Fatal(err) }
	got, err := s.ClaimNextAs("bee", "inst", []string{"coder"}, 60)
	if err != nil || got == nil { t.Fatalf("first claim failed: %v", err) }
	// The claimed task is no longer 'ready'; a second claim finds nothing.
	got2, err := s.ClaimNextAs("bee2", "inst", []string{"coder"}, 60)
	if err != nil { t.Fatal(err) }
	if got2 != nil { t.Fatalf("second claim returned a task; the guarded UPDATE must not re-claim a non-ready task") }
}
```
> Implementer: reuse the exact `Enqueue` signature/args from `reclaim_test.go`. The strongest failing assertion is that the code path now checks `RowsAffected` — if reachable, add a test that forces a 0-rows update to error; otherwise this is a contract test + the code review is the net for the rows-affected addition.
- [ ] **Step 2: Run, watch behavior.**
- [ ] **Step 3: Implement.** Change the claim UPDATE (`store.go:423-428`) to guard on the ready status and assert one row:
```go
	res, err := tx.Exec(
		`UPDATE tasks SET status=?, claimed_by=?, claimed_ts=?, claim_expires_ts=?, claimed_instance=? WHERE id=? AND status=?`,
		StatusClaimed, bee, ts, exp, instance, t.ID, StatusReady,
	)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return nil, nil // lost the race to claim — task no longer ready
	}
```
  Apply the same `AND status=?` (StatusClaimed) guard + rows-affected check to the self-heal reissue UPDATE at `:372-374` (its SELECT already filters `status=StatusClaimed`). Return `nil, nil` (no task) rather than an error on a 0-row claim, matching the "nothing to claim" contract of the surrounding function.
- [ ] **Step 4: Run, watch pass.** Full gate. **Commit:** `fix(queue): status-guard the claim UPDATE + assert one row — atomic claim no longer relies solely on pool-of-1 (audit M)`.

---

## Task 1.5: SupersedeTask — read dependents inside the transaction (close TOCTOU)

**Files:** Modify `internal/queue/supersede.go`; Test `internal/queue/supersede_test.go`

**Bug:** `SupersedeTask` reads pending dependents on `s.db` (`supersede.go:81-111`) BEFORE `s.db.Begin()` (`:113`); the rewrites run inside the tx (`:140-154`). A dependent enqueued/changed between the read and the commit is missed → orphaned dependency that waits on the superseded key forever.

**Interfaces:** the dependents SELECT (and the pre-read metadata SELECT at `:66` + `uniqueKey` at `:164`) move inside the tx via `tx.Query`/`tx.QueryRow`.

- [ ] **Step 1: Failing test** — add to `internal/queue/supersede_test.go`, reusing `open(t)`, `seedPipeline` (`recovery_test.go:15`), `taskByKey` (`:33`), modeled on `TestSupersedeRewritesPendingDependents` (`:76-111`). Assert that a dependent present at supersede time is rewritten to the replacement key (the correctness the tx must preserve); the TOCTOU-specific interleave is hard to force under pool-of-1, so pin the invariant that read+write share one tx by asserting no orphaned dep remains after supersede.
```go
func TestSupersedeReadsDependentsInTx(t *testing.T) {
	s := open(t)
	seedPipeline(t, s) // build-core -> build -> {test, docs}
	old := taskByKey(t, s, "build")
	newID, err := s.SupersedeTask(old.ID, TaskSpec{Key: "build-v2", Role: "coder", Title: "rebuild", Instruction: "x"})
	if err != nil || newID == 0 { t.Fatalf("supersede: %v", err) }
	// every former dependent of 'build' now depends on 'build-v2', none orphaned on 'build'
	for _, k := range []string{"test", "docs"} {
		d := taskByKey(t, s, k)
		for _, dep := range d.DependsOn {
			if dep == "build" { t.Fatalf("%s still depends on superseded 'build' — orphaned", k) }
		}
	}
}
```
- [ ] **Step 2: Run** (should pass for the happy path; it locks the behavior the refactor must not break).
- [ ] **Step 3: Implement.** Move the dependents read (`:81-111`) to AFTER `tx, err := s.db.Begin()` (`:113`), swapping `s.db.Query(...)` → `tx.Query(...)`. Also move the metadata read (`:66`, `SELECT mission_id, key, verify`) and the `uniqueKey` uniqueness check inside the tx (pass `tx` into `uniqueKey` or inline its `tx.QueryRow COUNT(*)`), so the entire read-decide-write sequence is one atomic transaction. Keep `defer tx.Rollback()` and the final `tx.Commit()`.
- [ ] **Step 4: Run, watch pass.** Full gate. **Commit:** `fix(queue): SupersedeTask reads dependents inside the tx — closes the TOCTOU orphaned-dependency hang (audit M)`.

---

## Task 1.6: CancelTaskGuarded — hoist the per-node full-table List (N+1)

**Files:** Modify `internal/queue/supersede.go`; Test `internal/queue/recovery_test.go`

**Bug:** the BFS over dependents calls `s.List(t.MissionID)` (a full mission scan) once PER frontier node (`supersede.go:192-220`) — O(nodes × mission-size).

**Interfaces:** fetch the mission's tasks ONCE before the BFS and walk the dependency graph in memory.

- [ ] **Step 1: Failing test** — reuse `TestCancelGuardedRefusesWithLiveDependents` (`recovery_test.go:97-130`) as the behavioral guard (there's no query-count hook, so assert the cascade result is unchanged after the refactor). Add a case with a deeper chain (3+ levels) so the in-memory BFS is exercised:
```go
func TestCancelGuardedCascadeDeepChain(t *testing.T) {
	s := open(t)
	seedPipeline(t, s)
	root := taskByKey(t, s, "build-core")
	cancelled, blocked, err := s.CancelTaskGuarded(root.ID, true /* cascade */)
	if err != nil { t.Fatal(err) }
	if len(blocked) != 0 { t.Fatalf("cascade should not report blocked: %v", blocked) }
	// build-core, build, test, docs all cancelled
	if len(cancelled) < 4 { t.Fatalf("cascade cancelled %d, want >=4 (whole chain)", len(cancelled)) }
}
```
- [ ] **Step 2: Run** (locks current behavior).
- [ ] **Step 3: Implement.** Fetch once, index in memory:
```go
	all, err := s.List(t.MissionID) // fetch the mission's tasks ONCE
	if err != nil {
		return nil, nil, err
	}
	// index direct dependents by dependency key for O(1) BFS steps
	dependentsOf := map[string][]Task{}
	for _, x := range all {
		for _, d := range x.DependsOn {
			dependentsOf[d] = append(dependentsOf[d], x)
		}
	}
```
  Replace the in-loop `s.List(...)` + inner scan with a walk over `dependentsOf[cur.key]`, keeping the same `seen`/terminal-status filters (`StatusDone`/`Cancelled`/`Superseded`) and frontier logic. The behavior is identical; only the per-node full scan is removed.
- [ ] **Step 4: Run, watch pass.** Full gate. **Commit:** `fix(queue): CancelTaskGuarded walks dependents in memory — removes the per-node N+1 full-table List (audit M)`.

---

## Task 1.7: RetargetDependents — conditional UPDATE to prevent lost updates

**Files:** Modify `internal/queue/supersede.go`; Test `internal/queue/recovery_test.go`

**Bug:** `RetargetDependents` reads a snapshot via `s.List` (`supersede.go:246`) then blind-writes each task's whole `depends_on` array back with `WHERE id=?` (`:294`) — a concurrent `depends_on`/status change between read and write is silently clobbered.

**Interfaces:** the write becomes conditional on the previously-read `depends_on` value (`WHERE id=? AND depends_on=?` with the old JSON) and verifies `RowsAffected`; skips (does not clobber) a row that changed underneath.

- [ ] **Step 1: Failing test** — reuse `TestRetargetDependents` (`recovery_test.go:134-172`) as the happy-path guard. Add a test that a row whose `depends_on` changed between snapshot and write is NOT clobbered — simulate by mutating one task's deps after the snapshot but before retarget (call `RetargetDependents`, then a direct `Enqueue`/dep change is hard mid-call; instead assert the conditional-UPDATE contract: retarget only rewrites rows whose deps still match the snapshot). Practical assertion: after a normal retarget, the count returned equals the number of rows actually changed:
```go
func TestRetargetDependentsCountMatchesChanges(t *testing.T) {
	s := open(t)
	seedPipeline(t, s)
	n, err := s.RetargetDependents(missionID, "build", "build-v2")
	if err != nil { t.Fatal(err) }
	// test + docs depended on build => exactly 2 rewrites
	if n != 2 { t.Fatalf("retargeted %d, want 2", n) }
	for _, k := range []string{"test", "docs"} {
		d := taskByKey(t, s, k)
		found := false
		for _, dep := range d.DependsOn { if dep == "build-v2" { found = true } }
		if !found { t.Fatalf("%s not retargeted to build-v2", k) }
	}
}
```
- [ ] **Step 2: Run** (locks behavior).
- [ ] **Step 3: Implement.** Make the write conditional on the snapshot value and count only real changes:
```go
		oldJSON := mustJSON(x.DependsOn) // the snapshot value we read
		newJSON := mustJSON(deps)
		res, err := s.db.Exec(
			`UPDATE tasks SET depends_on=? WHERE id=? AND depends_on=?`,
			newJSON, x.ID, oldJSON,
		)
		if err != nil {
			return n, err
		}
		if aff, _ := res.RowsAffected(); aff == 1 {
			n++ // only count rows we actually changed; a concurrently-modified row is skipped, not clobbered
		}
```
  Reuse the existing JSON-marshal idiom (the function already does `json.Marshal(deps)`); factor a tiny `mustJSON` or inline both marshals. Serialize `x.DependsOn` the same way it was stored so the equality predicate matches. (Optionally wrap the whole read+writes in one `tx` per the package idiom; the conditional UPDATE alone closes the lost-update, the tx is belt-and-braces — keep the change minimal unless the test needs it.)
- [ ] **Step 4: Run, watch pass.** Full gate. **Commit:** `fix(queue): RetargetDependents uses a conditional UPDATE — no lost update on concurrent dep changes (audit M)`.

---

# PHASE 2 — N+1 & DRY

## Task 2.1: repoindex — batch the per-file embedding calls

**Files:** Modify `internal/repoindex/store_cgo.go`, `internal/repoindex/store_nocgo.go`; Test `internal/repoindex/store_test.go`

**Bug:** `IndexFiles` calls `s.embedder.Embed(...)` once per file (`store_cgo.go:54-68`, `store_nocgo.go:39-53`) → N HTTP round-trips. `embed.Client.Embed([]string)` (`internal/embed/embed.go:62`) is already batch-capable (one vector per input).

**Interfaces:** one `Embed(allChunkTexts)` call per `IndexFiles`, then slice `[][]float32` back per file. Per-file insert tx unchanged.

- [ ] **Step 1: Failing test** — add to `internal/repoindex/store_test.go`. `fakeEmbedServer` (`:21`) already returns one vector per input; add a request counter to it (or a wrapper) and assert one HTTP call for a multi-file `IndexFiles`. `seedSearch` (`search_test.go:9`) already indexes two files in one call — reuse that scenario.
```go
func TestIndexFilesBatchesEmbedCalls(t *testing.T) {
	var calls int32
	// fakeEmbedServer variant that atomic-increments calls per POST
	emb, count := fakeCountingEmbedServer(t, &calls)
	s := openStore(t, emb)
	files := []FileInput{{Path: "a.go", Text: "package a\nfunc A(){}"}, {Path: "b.go", Text: "package b\nfunc B(){}"}, {Path: "c.go", Text: "package c\nfunc C(){}"}}
	if err := s.IndexFiles(1, files); err != nil { t.Fatal(err) }
	if got := count(); got != 1 {
		t.Fatalf("embed HTTP calls = %d for 3 files; want 1 (batched)", got)
	}
}
```
> Implementer: reuse the existing `fakeEmbedServer` body (`store_test.go:21`) and just thread a counter through the `httptest` handler. Reuse `countRows` (`store.go:31`) to assert chunks still landed.
- [ ] **Step 2: Run, watch fail** — 3 calls for 3 files.
- [ ] **Step 3: Implement (both build-tag files identically).** First pass: chunk every file and accumulate all texts; one `Embed` call; second pass: per-file insert tx using `vecs[off:off+n]`:
```go
	type staged struct {
		f      FileInput
		chunks []Chunk
		off, n int
	}
	var items []staged
	var allTexts []string
	for _, f := range files {
		cs := chunkFile(f.Path, f.Text)
		off := len(allTexts)
		for _, c := range cs {
			allTexts = append(allTexts, c.Text)
		}
		items = append(items, staged{f: f, chunks: cs, off: off, n: len(cs)})
	}
	var vecs [][]float32
	if s.embedder != nil && len(allTexts) > 0 {
		if v, err := s.embedder.Embed(allTexts); err == nil {
			vecs = v
		} else {
			log.Printf("repoindex: embed %d chunks: %v", len(allTexts), err)
		}
	}
	for _, it := range items {
		var fileVecs [][]float32
		if vecs != nil && it.off+it.n <= len(vecs) {
			fileVecs = vecs[it.off : it.off+it.n]
		}
		// ... existing per-file tx: DELETE FROM chunks WHERE mission_id=? AND path=?, then
		//     insert it.chunks[i] with embedding from fileVecs[i] (VecLiteral cgo / serialize nocgo) ...
	}
```
  Preserve the exact per-file transaction + `embCol`/`serializeVector` insert logic already in each file (only the embed call is hoisted). Keep the nil-embedder path (no vectors) working. Mirror byte-for-byte across cgo/nocgo except the vector literal.
- [ ] **Step 4: Run, watch pass.** `go test ./internal/repoindex/...` (both tags if buildable). Full gate. **Commit:** `perf(repoindex): batch embeddings into one call per IndexFiles (audit N+1)`.

---

## Task 2.2: memory — wrap the embedding write-back in a transaction

**Files:** Modify `internal/memory/store_cgo.go`, `internal/memory/store_nocgo.go`; Test `internal/memory/store_test.go`

**Bug:** `buildLocked` writes embeddings back one `s.db.Exec("UPDATE ...")` per row with no surrounding tx (`store_cgo.go:133-160`, `store_nocgo.go:117-145`). The earlier insert phase already uses a tx — mirror it.

**Interfaces:** the write-back loop runs inside one `tx` (Begin/Exec-per-row/Commit).

- [ ] **Step 1: Failing test** — `TestMemoryEmbedOnBuildAndPreserve` (`store_test.go:144`) already exercises this path and counts embedded inputs. Add/extend to assert all embeddings land after a build (correctness the tx must preserve) and that a second build re-embeds nothing:
```go
func TestMemoryEmbeddingWriteBackAtomic(t *testing.T) {
	dir := t.TempDir()
	writeEntry(t, dir, "e1", "d1", "proj", "body one")
	writeEntry(t, dir, "e2", "d2", "proj", "body two")
	s := openMem(t, countingEmbedServer(t))
	if _, err := s.Build([]string{dir}); err != nil { t.Fatal(err) }
	// both rows have embeddings; a re-build embeds 0 new rows (WHERE embedding IS NULL empty)
	if _, err := s.Build([]string{dir}); err != nil { t.Fatal(err) }
	// assert via a hybrid/semantic search that both entries are retrievable
}
```
> Implementer: reuse `writeEntry` (`:42`) and the inline counting embed server from `TestMemoryEmbedOnBuildAndPreserve`.
- [ ] **Step 2: Run.**
- [ ] **Step 3: Implement (both files).** Wrap the per-row UPDATE loop in a tx, mirroring the insert phase already in the same function:
```go
		if len(texts) > 0 {
			if vecs, err := s.embedder.Embed(texts); err != nil {
				log.Printf("memory: embed %d entries: %v", len(texts), err)
			} else {
				tx, err := s.db.Begin()
				if err != nil {
					log.Printf("memory: embedding tx begin: %v", err)
				} else {
					for i, p := range paths {
						if i < len(vecs) {
							// cgo:  "UPDATE mem SET embedding = "+embed.VecLiteral(vecs[i])+"::FLOAT[] WHERE path = ?"  (#nosec G202)
							// nocgo: "UPDATE mem SET embedding = ? WHERE path = ?", serializeVector(vecs[i])
							if _, err := tx.Exec(/* per-tag stmt */); err != nil {
								log.Printf("memory: embedding UPDATE for %s: %v", p, err)
							}
						}
					}
					if err := tx.Commit(); err != nil {
						log.Printf("memory: embedding tx commit: %v", err)
					}
				}
			}
		}
```
  Keep each file's existing literal/serialize form; only the transaction wrapper is added.
- [ ] **Step 4: Run, watch pass.** Full gate. **Commit:** `perf(memory): write embeddings back in one transaction (audit N+1)`.

---

## Task 2.3: memory — collapse the duplicated Hit scanners onto the shared helper

**Files:** Modify `internal/memory/store.go`, `internal/memory/store_cgo.go`, `internal/memory/store_nocgo.go`; Test `internal/memory/store_test.go`

**Bug:** the `Hit` SELECT-column list + `rows.Scan(&h.Slug, ...)` is duplicated ~4× though a canonical `scanHits` already exists (`store.go:347`). The two cgo semantic scanners (`store_cgo.go:291-300`, `:327-336`) are byte-identical; the nocgo one (`store_nocgo.go:237-248`) has a trailing `embedding` column instead of a score.

**Interfaces:** add `const hitColumns = "slug, name, project, type, description, shared, author"` and a `scanHit(rows *sql.Rows) (Hit, error)` helper (score column) + a `scanHitVia(rows, via)` wrapper; point the two cgo scanners at it. The nocgo semantic scanner keeps its embedding tail but reuses `hitColumns` for the SELECT and a shared scan of the 7 leading fields.

- [ ] **Step 1: Failing test** — behavior is covered by `TestMemoryAuthorAttribution` (`:18`), `TestMemoryHybridSemantic`, `TestMemoryBuildSearchScope` (`:57`). Add no new behavior; instead assert the refactor keeps `Via`/`Author`/`Score` intact through `Search` (these tests already do). Treat this task as a pure refactor gated by the existing green suite — run them BEFORE and AFTER.
- [ ] **Step 2:** Run the existing memory suite green (baseline).
- [ ] **Step 3: Implement.** In `store.go`, add:
```go
const hitColumns = "slug, name, project, type, description, shared, author"

// scanHit scans the 7 hitColumns plus a trailing score column into a Hit.
func scanHit(rows *sql.Rows) (Hit, error) {
	var h Hit
	err := rows.Scan(&h.Slug, &h.Name, &h.Project, &h.Type, &h.Description, &h.Shared, &h.Author, &h.Score)
	return h, err
}
```
  Rewrite `store_cgo.go` `searchHNSWFiltered` (`:291-300`) and `searchBruteForce` (`:327-336`) scan loops to `h, err := scanHit(rows); ...; h.Via = "semantic"; out = append(out, h)`, and build their SELECTs from `hitColumns`. For `store_nocgo.go` `searchSemantic` (`:237-248`), SELECT `hitColumns + ", embedding"`, scan the 7 leading fields via a shared `scanHitLead(rows, &strEmb)` (or inline the 7-field scan + `&strEmb`), then compute `h.Score = cosineSimilarity(...)` and set `h.Via = "semantic"` as today. Keep `scanHits` (`:347`) as-is (used by `List`/`searchKeyword`).
- [ ] **Step 4: Run, watch pass** — the full memory suite stays green. Full gate. **Commit:** `refactor(memory): collapse duplicated Hit scanners onto shared hitColumns/scanHit (audit DRY)`.

---

## Task 2.4: extract the copy-pasted mergeHits into `internal/searchmerge`

**Files:** Create `internal/searchmerge/searchmerge.go`, `internal/searchmerge/searchmerge_test.go`; Modify `internal/memory/store.go`, `internal/repoindex/search.go`; Test the two existing suites stay green

**Bug:** `mergeHits` is copy-pasted over two distinct `Hit` types (`internal/memory/store.go:221-270` keyed by `Slug`; `internal/repoindex/search.go:67-112` keyed by `Path:StartLine`) — the repoindex copy's comment admits it mirrors memory. Same algorithm: max-normalize each arm to [0,1], union, keep higher score on collision, tag `"both"`, sort desc, truncate.

**Interfaces:** a generic `searchmerge.Merge[T any](kw, sem []T, opt Accessors[T], limit int) []T` where `Accessors[T]` exposes `Key(T) string`, `Score(*T) float64`, `SetScore(*T, float64)`, `SetVia(*T, string)`. Preserves memory's defensive-copy behavior (don't mutate caller slices).

- [ ] **Step 1: Failing test** — create `internal/searchmerge/searchmerge_test.go` with a local test struct exercising: normalization to [0,1], collision keeps the max score and tags `"both"`, disjoint keys preserved, sort-desc, truncate-to-limit, and caller slices unmutated.
```go
// SPDX-License-Identifier: Elastic-2.0
package searchmerge

import "testing"

type row struct{ id string; score float64; via string }

func acc() Accessors[row] {
	return Accessors[row]{
		Key:      func(r row) string { return r.id },
		Score:    func(r *row) float64 { return r.score },
		SetScore: func(r *row, s float64) { r.score = s },
		SetVia:   func(r *row, v string) { r.via = v },
	}
}

func TestMergeNormalizesUnionsAndTags(t *testing.T) {
	kw := []row{{"a", 2, "keyword"}, {"b", 1, "keyword"}}
	sem := []row{{"a", 10, "semantic"}, {"c", 5, "semantic"}}
	out := Merge(kw, sem, acc(), 10)
	// a in both -> via "both", normalized scores in [0,1], sorted desc, 3 rows
	if len(out) != 3 { t.Fatalf("len=%d want 3", len(out)) }
	if out[0].score > 1.0 { t.Fatalf("not normalized: %v", out[0].score) }
	var a *row
	for i := range out { if out[i].id == "a" { a = &out[i] } }
	if a == nil || a.via != "both" { t.Fatalf("a via=%v want both", a) }
	if kw[0].score != 2 { t.Fatalf("caller slice mutated: %v", kw[0].score) }
}
```
- [ ] **Step 2: Run, watch fail** (package/symbols undefined).
- [ ] **Step 3: Implement `internal/searchmerge/searchmerge.go`:**
```go
// SPDX-License-Identifier: Elastic-2.0

// Package searchmerge fuses keyword + semantic hit lists: each arm is
// max-normalized to [0,1], the two are unioned by a caller-supplied key,
// collisions keep the higher score and are tagged "both", and the result is
// sorted by score descending and truncated. It is the shared home for the
// hybrid-merge logic previously copy-pasted in memory and repoindex.
package searchmerge

import "sort"

type Accessors[T any] struct {
	Key      func(T) string
	Score    func(*T) float64
	SetScore func(*T, float64)
	SetVia   func(*T, string)
}

func Merge[T any](kw, sem []T, a Accessors[T], limit int) []T {
	norm := func(hs []T) []T {
		cp := make([]T, len(hs))
		copy(cp, hs) // defensive: never mutate the caller's slice
		var max float64
		for i := range cp {
			if s := a.Score(&cp[i]); s > max {
				max = s
			}
		}
		if max > 0 {
			for i := range cp {
				a.SetScore(&cp[i], a.Score(&cp[i])/max)
			}
		}
		return cp
	}
	nk, ns := norm(kw), norm(sem)
	idx := map[string]int{}
	var out []T
	add := func(h T) {
		k := a.Key(h)
		if j, ok := idx[k]; ok {
			if a.Score(&h) > a.Score(&out[j]) {
				a.SetScore(&out[j], a.Score(&h))
			}
			a.SetVia(&out[j], "both")
			return
		}
		idx[k] = len(out)
		out = append(out, h)
	}
	for _, h := range nk {
		add(h)
	}
	for _, h := range ns {
		add(h)
	}
	sort.SliceStable(out, func(i, j int) bool { return a.Score(&out[i]) > a.Score(&out[j]) })
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
```
- [ ] **Step 4: Run, watch pass** — `go test ./internal/searchmerge/`.
- [ ] **Step 5: Rewire callers.** Replace `memory.mergeHits` body (`store.go:221`) with a call to `searchmerge.Merge` using a `Key: h.Slug` accessor; replace `repoindex.mergeHits` (`search.go:67`) with `Key: h.Path + ":" + strconv.Itoa(h.StartLine)`. Delete both old bodies. Run `go test ./internal/memory/... ./internal/repoindex/...` — both suites (which pin `Via`==keyword/semantic/both and ordering) stay green.
- [ ] **Step 6: Commit.** Full gate. **Commit:** `refactor(search): extract hybrid mergeHits into internal/searchmerge (audit DRY)`.

---

# PHASE 3 — Jail & browser hardening

## Task 3.1: macOS sandbox — invert reads to deny-by-default (match bwrap)

**Files:** Modify `internal/sandbox/isolator_darwin.go`; Test (new) `internal/sandbox/isolator_darwin_test.go`

**Bug:** the sandbox-exec profile sets `(deny default)` then immediately `(allow file-read*)` (`isolator_darwin.go:52-56`) — allow-by-default reads of the whole host FS, minus a 3-entry denylist (`$HOME`, `/Library/Keychains`, `/private/var/db/dslocal`, `:66-70`). Linux bwrap is deny-by-default (binds only `/usr`, TLS roots, workspace; `isolator_linux.go:66-80`). This is a jail-strength regression on the dev (darwin) backend.

**Design decision:** remove the blanket `(allow file-read*)`; keep `(deny default)` as the read posture; explicitly allow-read only the toolchain paths bwrap binds' macOS equivalents (`/usr`, `/bin`, `/sbin`, `/System/Library`, `/Library/Developer`, TLS roots under `/private/etc/ssl`) plus the workspace (already allowed at `:72`) and the temp write dirs (already `:74-78`). Use `/private/...` real paths (macOS `/etc`→`/private/etc` symlink). The 3 deny-subpaths become redundant but harmless — leave them as belt-and-braces.

**Interfaces:** none; `Wrap` builds a stricter profile string. Do NOT touch `sandbox.go`'s `nil backend ⇒ refuse` guards (`:65-67, 174-176`) or `Resolve`'s no-weaker-fallback.

- [ ] **Step 1: Failing test** — create `internal/sandbox/isolator_darwin_test.go` (`//go:build darwin`), mirroring `isolator_linux_test.go` and reusing `argvHas` (`isolator_test.go:9`). The profile is `argv[2]` (layout `{"sandbox-exec","-p",profile,"sh","-c",cmd}`):
```go
//go:build darwin
// SPDX-License-Identifier: Elastic-2.0
package sandbox

import "strings"
import "testing"

func TestSandboxExecDeniesReadsByDefault(t *testing.T) {
	iso, err := newSandboxExecIsolator()
	if err != nil { t.Fatal(err) }
	argv, err := iso.Wrap("echo hi", Options{Workspace: "/tmp/ws"}, nil)
	if err != nil { t.Fatal(err) }
	profile := argv[2]
	if strings.Contains(profile, "(allow file-read*)\n") || strings.Contains(profile, "(allow file-read* )") {
		t.Fatalf("profile still grants blanket file-read*:\n%s", profile)
	}
	if !strings.Contains(profile, "(deny default)") {
		t.Fatalf("profile must default-deny:\n%s", profile)
	}
	if !strings.Contains(profile, `(allow file-read* (subpath "/tmp/ws"))`) {
		t.Fatalf("workspace must be readable:\n%s", profile)
	}
	if !strings.Contains(profile, `(allow file-read* (subpath "/usr"))`) {
		t.Fatalf("toolchain /usr must be readable:\n%s", profile)
	}
}
```
> This test only builds/runs on darwin. Note in the commit that CI on Linux won't exercise it; the reviewer verifies the profile by reading it.
- [ ] **Step 2: Run on darwin, watch fail** (blanket allow present). On Linux, confirm it compiles under `GOOS=darwin go vet ./internal/sandbox/`.
- [ ] **Step 3: Implement.** In `isolator_darwin.go` `Wrap`, delete the blanket `sb.WriteString("(allow file-read*)\n")` (`:56`) and replace with explicit toolchain allows:
```go
	// Deny-by-default reads (matching Linux bwrap). Allow ONLY the toolchain paths
	// a build/test needs plus the workspace — not the whole host FS. macOS /etc is a
	// symlink to /private/etc, so allow the real /private paths.
	for _, p := range []string{"/usr", "/bin", "/sbin", "/System/Library", "/Library/Developer", "/private/etc/ssl", "/private/etc/ca-certificates"} {
		sb.WriteString(fmt.Sprintf("(allow file-read* (subpath %q))\n", p))
	}
```
  Keep the workspace allow (`:72`), the temp write allows (`:74-78`), and (harmlessly) the 3 deny-subpaths. Leave the `process*`/`signal*`/`sysctl*` allows.
- [ ] **Step 4: Run, watch pass** (on darwin if available; else `GOOS=darwin go build ./internal/sandbox/` + reviewer reads the profile). Full gate on the host platform. **Commit:** `fix(sandbox): macOS jail denies reads by default — allowlist toolchain only, matching bwrap (audit M)`.

---

## Task 3.2: BrowserManager — evict idle Chromium tabs (TTL sweep)

**Files:** Modify `internal/brain/browser.go`; Test `internal/brain/browser_test.go`

**Bug:** `BrowserManager.pages` (`agentName → *rod.Page`) is inserted in `getPage` (`browser.go:144-153`) and never evicted — one Chromium tab per unique agent, forever; the only teardown is whole-browser `Close()`. Mirror the `WorkerSessions` lazy-TTL sweep (`internal/brain/worker_sessions.go:84-137`), which solves the identical unbounded-map problem with a clock seam + sweep-on-access.

**Interfaces:** `BrowserManager` gains `lastTouch map[string]time.Time`, `now func() time.Time` (clock seam, defaults to `time.Now`), and `const browserPageTTL = 30 * time.Minute`. `getPage` refreshes the touched agent's timestamp and sweeps+`page.Close()`es any agent idle past the TTL (under the existing lock).

- [ ] **Step 1: Failing test** — add to `internal/brain/browser_test.go`. Since a live `*rod.Page` needs Chromium, test the eviction bookkeeping via the clock seam without launching a real tab: refactor the map/TTL logic into an unexported `sweepIdle(nowT time.Time)` that operates on the map (closing pages), and unit-test it with fake entries.
```go
func TestBrowserManagerSweepsIdlePages(t *testing.T) {
	bm := NewBrowserManager("127.0.0.1:9019")
	base := time.Unix(1_000_000, 0)
	bm.now = func() time.Time { return base }
	// inject two tracked agents with nil pages (sweep must tolerate/ను close-guard nil in test)
	bm.trackForTest("old", base.Add(-2*browserPageTTL))
	bm.trackForTest("fresh", base)
	bm.sweepIdle(base)
	if _, ok := bm.pages["old"]; ok { t.Fatal("stale agent 'old' not evicted") }
	if _, ok := bm.pages["fresh"]; !ok { t.Fatal("fresh agent wrongly evicted") }
}
```
> Implementer: add unexported test helpers `trackForTest(agent string, at time.Time)` (sets `lastTouch` + a sentinel/nil page entry) and make `sweepIdle` nil-page-safe (only `page.Close()` when non-nil) so the bookkeeping is testable without Chromium — mirroring how `worker_sessions_test.go` drives the `now` seam. Keep helpers unexported.
- [ ] **Step 2: Run, watch fail** (`sweepIdle`/`now`/`lastTouch` undefined).
- [ ] **Step 3: Implement.** Add the fields (init in `NewBrowserManager`), `browserPageTTL`, and:
```go
// sweepIdle closes and forgets any agent's page untouched for longer than
// browserPageTTL. Called under bm.mu from getPage — amortized cleanup that keeps
// the tab count bounded without a sweeper goroutine (mirrors WorkerSessions).
func (bm *BrowserManager) sweepIdle(nowT time.Time) {
	for agent, ts := range bm.lastTouch {
		if nowT.Sub(ts) > browserPageTTL {
			if p := bm.pages[agent]; p != nil {
				_ = p.Close()
			}
			delete(bm.pages, agent)
			delete(bm.lastTouch, agent)
		}
	}
}
```
  In `getPage` (under the held lock), call `bm.sweepIdle(bm.now())` at entry and set `bm.lastTouch[agent] = bm.now()` whenever an agent's page is used (both the cache-hit and freshly-created branches). Import `time`.
- [ ] **Step 4: Run, watch pass.** `go test ./internal/brain/ -run TestBrowserManagerSweepsIdlePages`. Full gate. **Commit:** `fix(brain): evict idle browser tabs on a TTL sweep — BrowserManager.pages no longer grows unbounded (audit M)`.

---

# PHASE 4 — Auth hardening (H-3 + auth Mediums)

> **The H-3 design pass (decided here, from the auth investigation):**
> 1. **Namespace machine principals.** `pickPrincipal` returns human claims (`email`, `preferred_username`) bare, but machine fallbacks as `client:<clientID>` / `client:<azp>`, so a machine token can never match a human email allowlist entry. **OPERATOR ACTION (loud):** existing machine/service-account principals in the allowlist must be re-added with the `client:` prefix or they fail closed (can't authenticate). This is defense-in-depth; failing closed is the safe direction. Record in the corralai ops memory + note in the commit.
> 2. **email_verified.** Read `email_verified`; only trust the `email` claim as a principal when it is `true`. Unverified email falls through to `preferred_username`/machine; if nothing remains, reject.
> 3. **Empty audience = refuse by default.** An empty configured `Audience` no longer silently sets `SkipClientIDCheck`; `NewVerifier` errors unless `CORRALAI_OIDC_ALLOW_EMPTY_AUDIENCE=1` is set (explicit opt-in). Prod sets `audience=corral-svc`, so prod is unaffected.
> 4. **Auth-disabled ⇒ loopback-only interlock.** At startup, if the verifier is disabled (no issuer) AND `CORRALAI_ADDR` binds a non-loopback host, refuse to start (`log.Fatal`) unless `CORRALAI_ALLOW_INSECURE=1`. Prod binds `127.0.0.1`; the demo compose sets the override if it binds non-loopback.
> All four are defense-in-depth interlocks; prod (Zitadel, audience set, loopback bind) is already mitigated by configuration — this makes the code enforce what was operator convention.

## Task 4.1: build a fake-IdP signed-JWT test harness

**Files:** Create `internal/auth/testjwt_test.go`; (no production change)

**Why:** the OIDC `VerifyToken` path (JWKS verify, `email_verified`, audience) is currently **untested** — no fake IdP, no signed-JWT helper. Tasks 4.2/4.3/4.4/4.7 need one. Build it once. `go-jose/v4` is present (indirect); use it directly in the test (adding a direct test-scope dep is fine).

**Interfaces (test-only):** `newFakeIdP(t) *fakeIdP` running an `httptest` server that serves `/.well-known/openid-configuration` + `/keys` (JWKS) for an RSA keypair; `func (i *fakeIdP) sign(t *testing.T, claims map[string]any) string` mints a signed JWT; `i.issuer` is the URL to pass in an `auth.Pair`.

- [ ] **Step 1:** Write `internal/auth/testjwt_test.go` (package `auth`) implementing `fakeIdP` with `go-jose/v4` (RSA key, JWKS endpoint, discovery doc) and `sign(claims)`. Model the request/verify flow on `subagent_test.go:28-35` (which runs a token through `sdkauth.RequireBearerToken(vf.VerifyToken, nil)`).
- [ ] **Step 2: Smoke test** — `TestFakeIdPMintAndVerify`: build a `Verifier` via `NewVerifier(ctx, []Pair{{Issuer: idp.issuer, Audience: "corral-svc"}})`, mint a token with `aud: "corral-svc"`, `email: "a@b.co"`, `email_verified: true`, and assert `VerifyToken` returns a `TokenInfo` with `UserID=="a@b.co"`.
- [ ] **Step 3: Run, watch pass.** `go test ./internal/auth/ -run TestFakeIdPMintAndVerify`. Full gate. **Commit:** `test(auth): fake-IdP signed-JWT harness for OIDC verification tests (H-3 prep)`.

> If wiring a live JWKS discovery against `coreos/go-oidc` proves impractical in-test (clock/discovery quirks), fall back to testing the claim-mapping logic by extracting the post-verify principal derivation (`pickPrincipal` + email_verified gate) into a pure function unit-tested directly, and cover audience/issuer via `go-oidc`'s own guarantees + a review note. Prefer the live harness.

## Task 4.2: namespace machine principals as `client:<id>` + email_verified gate

**Files:** Modify `internal/auth/oidc.go`; Test `internal/auth/principal_test.go` (+ harness)

**Bug:** `pickPrincipal` (`oidc.go:217-224`) flattens `email/preferred_username/client_id/azp` into one allowlist; no `email_verified` check (`oidc.go:240,248`).

- [ ] **Step 1: Failing tests** — extend `TestPickPrincipal` in `principal_test.go` (white-box, calls the unexported func). Change `pickPrincipal`'s signature to take `emailVerified bool`:
```go
func TestPickPrincipalNamespacesMachineAndGatesEmail(t *testing.T) {
	cases := []struct{ email string; ev bool; pu, cid, azp, want string }{
		{"a@b.co", true, "", "", "", "a@b.co"},              // verified email wins, bare
		{"a@b.co", false, "alice", "", "", "alice"},          // unverified email skipped -> preferred_username
		{"", false, "", "svc-1", "", "client:svc-1"},         // machine namespaced
		{"", false, "", "", "azp-9", "client:azp-9"},         // azp namespaced
		{"a@b.co", false, "", "", "", ""},                    // only unverified email -> no principal
	}
	for _, c := range cases {
		if got := pickPrincipal(c.email, c.ev, c.pu, c.cid, c.azp); got != c.want {
			t.Errorf("pickPrincipal(%q,ev=%v,%q,%q,%q)=%q want %q", c.email, c.ev, c.pu, c.cid, c.azp, got, c.want)
		}
	}
}
```
- [ ] **Step 2: Run, watch fail** (signature/behavior mismatch).
- [ ] **Step 3: Implement.** Rewrite `pickPrincipal` (`oidc.go:217`):
```go
// pickPrincipal maps verified claims to an allowlist principal. Human claims
// (a verified email, else preferred_username) are returned bare; machine claims
// (client_id/azp) are NAMESPACED as "client:<id>" so a service account can never
// match a human email allowlist entry. An unverified email is not trusted.
func pickPrincipal(email string, emailVerified bool, preferredUsername, clientID, azp string) string {
	if email != "" && emailVerified {
		return email
	}
	if preferredUsername != "" {
		return preferredUsername
	}
	if clientID != "" {
		return "client:" + clientID
	}
	if azp != "" {
		return "client:" + azp
	}
	return ""
}
```
  Add `EmailVerified bool \`json:"email_verified"\`` to the claims struct (`oidc.go:239-247`) and pass `c.EmailVerified` at the call site (`:248`). Update the doc-comment (`:212-216`).
- [ ] **Step 4: Run, watch pass.** Full gate. **Commit:** `fix(auth): namespace machine principals as client:<id> + require email_verified (H-3)`.

## Task 4.3: empty audience refuses by default (opt-in)

**Files:** Modify `internal/auth/oidc.go`; Test `internal/auth/*_test.go` (+ harness)

**Bug:** empty `Audience` silently sets `SkipClientIDCheck=true` (`oidc.go:195-205`) → within-issuer token confusion.

- [ ] **Step 1: Failing test** — `NewVerifier` with a `Pair{Issuer: idp.issuer, Audience: ""}` (no env opt-in) returns an error; with `CORRALAI_OIDC_ALLOW_EMPTY_AUDIENCE=1` set (via `t.Setenv`) it succeeds and skips the aud check.
```go
func TestNewVerifierRefusesEmptyAudienceByDefault(t *testing.T) {
	idp := newFakeIdP(t)
	if _, err := NewVerifier(context.Background(), []Pair{{Issuer: idp.issuer, Audience: ""}}); err == nil {
		t.Fatal("empty audience must be refused without explicit opt-in")
	}
	t.Setenv("CORRALAI_OIDC_ALLOW_EMPTY_AUDIENCE", "1")
	if _, err := NewVerifier(context.Background(), []Pair{{Issuer: idp.issuer, Audience: ""}}); err != nil {
		t.Fatalf("opt-in should allow empty audience: %v", err)
	}
}
```
- [ ] **Step 2: Run, watch fail.**
- [ ] **Step 3: Implement.** In `NewVerifier` (`oidc.go:195-205`), gate the skip:
```go
		cfg := &oidc.Config{ClientID: p.Audience}
		if p.Audience == "" {
			if os.Getenv("CORRALAI_OIDC_ALLOW_EMPTY_AUDIENCE") != "1" {
				return nil, fmt.Errorf("auth: OIDC issuer %q configured with no audience; set an audience or CORRALAI_OIDC_ALLOW_EMPTY_AUDIENCE=1 to opt in to skipping the aud check", p.Issuer)
			}
			cfg.SkipClientIDCheck = true
		}
```
  (`os`/`fmt` — verify imports.)
- [ ] **Step 4: Run, watch pass.** Full gate. **Commit:** `fix(auth): refuse empty OIDC audience unless explicitly opted in (H-3)`.

## Task 4.4: reject empty-principal tokens

**Files:** Modify `internal/auth/oidc.go`; Test `internal/auth/*_test.go` (+ harness)

**Bug:** a token that verifies but carries none of email/preferred_username/client_id/azp yields `TokenInfo{UserID:""}` with `err==nil` (`oidc.go:248-262`); with an empty allowlist `Allowed("")` returns `true` → authenticated-anonymous.

- [ ] **Step 1: Failing test** — mint a token with no identity claims (just `aud`/`iss`/`exp`); `VerifyToken` must return an error (not a `TokenInfo`).
```go
func TestVerifyTokenRejectsEmptyPrincipal(t *testing.T) {
	idp := newFakeIdP(t)
	vf, _ := NewVerifier(context.Background(), []Pair{{Issuer: idp.issuer, Audience: "corral-svc"}})
	tok := idp.sign(t, map[string]any{"aud": "corral-svc", "exp": /* +1h */})
	// drive through the middleware harness (subagent_test.go pattern)
	if _, err := vf.VerifyToken(context.Background(), tok); err == nil {
		t.Fatal("token with no principal claim must be rejected")
	}
}
```
- [ ] **Step 2: Run, watch fail.**
- [ ] **Step 3: Implement.** In `VerifyToken`, after computing `principal` (`oidc.go:248`), before building `TokenInfo`:
```go
	if principal == "" {
		return nil, sdkauth.ErrInvalidToken // authenticated but no usable principal — never authenticate as anonymous
	}
```
  (Confirm the sentinel the SDK expects — `sdkauth.ErrInvalidToken` or return a plain `fmt.Errorf`; match what `RequireBearerToken` treats as a 401.)
- [ ] **Step 4: Run, watch pass.** Full gate. **Commit:** `fix(auth): reject verified tokens with no principal — no authenticated-anonymous (audit M)`.

## Task 4.5: delegation HMAC key minimum-length floor (env path)

**Files:** Modify `cmd/corral/main.go`; Test `cmd/corral/*_test.go` (or `internal/auth` if the floor moves into `EnableDelegation`)

**Bug:** `delegationKey()` env path returns `[]byte(v)` with no length check (`main.go:229-232`), while the systemd-credential path enforces `>=16` and the fallback is 32 random bytes. A short `CORRALAI_DELEGATION_SECRET` → forgeable HMAC.

**Design decision:** enforce the floor in `auth.EnableDelegation` (`oidc.go:76-81`) so it's testable in the auth package and covers both wiring paths — reject (ignore + log) a key `< 32` bytes. Also add the floor at the `main.go` env path with a `log.Fatal` (an operator who explicitly set a too-short secret should be told, not silently fall back).

- [ ] **Step 1: Failing test** — in `internal/auth/deleg_test.go`, `EnableDelegation` with a 4-byte key leaves delegation DISABLED (a subsequent mint/verify fails), while a 32-byte key works (existing `delegVerifier` uses exactly 32).
```go
func TestEnableDelegationRejectsShortKey(t *testing.T) {
	vf, _ := NewVerifier(context.Background(), nil)
	vf.EnableDelegation([]byte("tiny")) // < floor
	if vf.delegKey != nil { t.Fatal("short delegation key must be rejected") }
}
```
- [ ] **Step 2: Run, watch fail.**
- [ ] **Step 3: Implement.** In `EnableDelegation` (`oidc.go:76-81`):
```go
func (vf *Verifier) EnableDelegation(key []byte) {
	if len(key) < 32 {
		if len(key) > 0 {
			log.Printf("auth: delegation key too short (%d bytes) — need >= 32; delegation stays disabled", len(key))
		}
		return
	}
	vf.delegKey = key
}
```
  (Import `log` in `oidc.go` if absent.) In `main.go` `delegationKey()` env branch (`:229-232`), enforce the same floor with a `log.Fatalf` when a non-empty env secret is under 32 bytes (fail loud on an explicit misconfiguration) — keep the systemd-credential `>=16` path as-is or raise it to 32 for consistency (raise it; note in commit).
- [ ] **Step 4: Run, watch pass.** Full gate. **Commit:** `fix(auth): floor delegation HMAC key at 32 bytes on every load path (audit M)`.

## Task 4.6: auth-disabled ⇒ loopback-only startup interlock

**Files:** Modify `cmd/corral/main.go`; Test `cmd/corral/*_test.go` (extract the check into a pure helper)

**Bug:** auth disables by leaving `CORRALAI_OIDC_ISSUER` empty (`oidc.go:187-189`); nothing prevents running auth-disabled while bound to a non-loopback address (`main.go:489`, no interlock).

**Interfaces:** extract `func insecureBindRefused(authEnabled bool, addr string, allowInsecure bool) error` (pure, testable) — returns an error when `!authEnabled && addr` host is non-loopback && `!allowInsecure`. Call it at startup after the verifier is built and before `listen`.

- [ ] **Step 1: Failing test** — `cmd/corral/main_test.go` (package `main`):
```go
func TestInsecureBindRefused(t *testing.T) {
	// auth off + non-loopback + no override => refuse
	if err := insecureBindRefused(false, "0.0.0.0:9019", false); err == nil { t.Fatal("must refuse auth-off on 0.0.0.0") }
	// loopback is fine
	if err := insecureBindRefused(false, "127.0.0.1:9019", false); err != nil { t.Fatalf("loopback ok: %v", err) }
	// override allows it
	if err := insecureBindRefused(false, "0.0.0.0:9019", true); err != nil { t.Fatalf("override ok: %v", err) }
	// auth on => never refused
	if err := insecureBindRefused(true, "0.0.0.0:9019", false); err != nil { t.Fatalf("auth-on ok: %v", err) }
}
```
- [ ] **Step 2: Run, watch fail** (undefined).
- [ ] **Step 3: Implement.**
```go
// insecureBindRefused returns an error when the brain would run auth-DISABLED
// while bound to a non-loopback interface — an open control plane. Loopback
// binds (dev) and CORRALAI_ALLOW_INSECURE=1 (explicit opt-in) are allowed.
func insecureBindRefused(authEnabled bool, addr string, allowInsecure bool) error {
	if authEnabled || allowInsecure {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}
	if host == "" { // e.g. ":9019" — binds all interfaces
		return fmt.Errorf("auth is disabled and CORRALAI_ADDR %q binds all interfaces; set CORRALAI_OIDC_ISSUER or CORRALAI_ALLOW_INSECURE=1", addr)
	}
	if ip := net.ParseIP(host); ip != nil && !ip.IsLoopback() {
		return fmt.Errorf("auth is disabled and CORRALAI_ADDR %q is not loopback; set CORRALAI_OIDC_ISSUER or CORRALAI_ALLOW_INSECURE=1", addr)
	}
	if host != "localhost" && net.ParseIP(host) == nil {
		// a hostname we can't classify — be conservative, refuse unless overridden
		return fmt.Errorf("auth is disabled and CORRALAI_ADDR host %q is not a loopback literal; set CORRALAI_OIDC_ISSUER or CORRALAI_ALLOW_INSECURE=1", host)
	}
	return nil
}
```
  Wire at startup: after `verifier` is built and `addr` known, `if err := insecureBindRefused(verifier.Enabled(), addr, os.Getenv("CORRALAI_ALLOW_INSECURE") == "1"); err != nil { log.Fatalf("%v", err) }`. Import `net`. **Update `deploy/demo/docker-compose.yml`** to set `CORRALAI_ALLOW_INSECURE=1` (the demo runs auth-off intentionally) so the demo still boots — verify its bind addr and add the env next to the auth-off comment (`:123`).
- [ ] **Step 4: Run, watch pass.** Full gate. **Commit:** `fix(auth): refuse to start auth-disabled on a non-loopback bind (H-3)`.

---

# PHASE 5 — security-relevant Lows

## Task 5.1: triage + fix the security Lows

**Files:** TBD per finding (investigate first); Tests alongside each

The audit's Low list mixes benign items with a few real authz gaps. This task **investigates each candidate, fixes the real ones with a test, and explicitly logs (in the commit body) any deliberately deferred as benign** — no silent drops.

Prioritize (confirm each in code first):
- **`mint_observer` gates `isAdmin` not `isHumanAdmin`** — a machine/delegated principal may mint observer tokens. If confirmed, tighten to `isHumanAdmin` (the same gate every other privileged write uses per the audit's verified-clean note). Test: a non-human-admin principal is refused.
- **`send_instruction` cross-principal ungated write** — one principal can drive another's session. If confirmed, add the claimer/ownership guard (mirror the M-4 claimer-guard shape from batch 1). Test: cross-principal `send_instruction` is refused.
- **`observer` token can drive narrator + NL→SQL** — confirm the observer scope and, if it exceeds read-only, constrain it. Test accordingly.
- **forge query-param unescaped** (`repo/provider.go`) — use `url.Values`/`url.QueryEscape` for any interpolated query param. Test: a param with `&`/space round-trips safely.
- **telemetry read-only guard is prefix-only** (`read_csv` reachable) — tighten the guard so only intended read verbs pass. Test: `read_csv(...)`-style bypass is rejected.

- [ ] **Step 1:** For EACH bullet, read the current code (brain server tool registration for `mint_observer`/`send_instruction`/observer scope; `internal/repo/provider.go` for the query param; the telemetry guard) and confirm the finding is real against current main. Note any already-fixed.
- [ ] **Step 2–4 (per confirmed finding):** TDD it — failing test, minimal fix, green — committing each separately with a `fix(<area>): ... (audit Low)` message. Group trivially-related ones only if they share a file + test.
- [ ] **Step 5:** In the final commit body (or a short `docs/superpowers/notes/2026-07-13-audit-lows-triage.md`), list every Low from the audit and its disposition (fixed / deferred-benign + why) so nothing is silently dropped.

---

## Self-Review
- **Coverage:** every open item in the 2026-07-12 audit backlog maps to a task —
  - Mission/queue reliability: reflex over-count→1.1, dep-sweep false-convergence→1.2, staffing head-of-line/re-probe→1.3, ClaimNextAs guard→1.4, SupersedeTask TOCTOU→1.5, CancelTaskGuarded N+1→1.6, RetargetDependents lost-update→1.7.
  - N+1/DRY: repoindex embed batch→2.1, memory write-back tx→2.2, memory Hit scan DRY→2.3, mergeHits extract→2.4.
  - Jail/browser: macOS jail→3.1, browser leak→3.2.
  - Auth: H-3 (namespacing+email_verified→4.2, empty-aud→4.3, disabled-interlock→4.6) + harness→4.1 + empty-principal→4.4 + delegation floor→4.5.
  - Lows→5.1.
- **Placeholder scan:** the two hardest-to-force-race tests (1.4 ClaimNextAs, 1.5 TOCTOU) explicitly state they pin the SQL contract + invariant rather than a real interleave (documented, not a hidden gap); 4.1 carries a stated fallback if the live JWKS harness is impractical; 3.1's darwin test is stated as darwin-only + reviewer-verified on Linux CI.
- **Type consistency:** `pickPrincipal` signature changes in 4.2 (adds `emailVerified bool`) — every reference is in `oidc.go` (`:223` def, `:248` call) and `principal_test.go`; both updated in 4.2. `searchmerge.Merge`/`Accessors[T]` defined in 2.4 and consumed by both callers in the same task. `staffedModelRef` (batch 1) reused unchanged in 1.3.
- **No regression to clean invariants:** 1.1/1.2/1.4/1.5 make gates/claims stricter; 1.3 preserves the once-per-mission latch + nil-guards and adds `-race` coverage; 3.1 tightens the jail and leaves the nil-backend guard untouched; all Phase-4 tasks tighten auth; 2.x are behavior-preserving (existing suites are the net). None touches the ledger/certify hash-chain, the credential boundary, or claim single-winner semantics.
- **Operator action flagged:** 4.2 machine-principal namespacing requires re-adding `client:`-prefixed allowlist entries in prod (fail-closed); 4.3/4.6 add opt-in envs the demo/prod configs must set where they intentionally run permissive. Record in the corralai ops memory when 4.2/4.6 land.
