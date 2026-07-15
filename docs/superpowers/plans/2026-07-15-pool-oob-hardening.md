<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Adversarial pool — out-of-the-box hardening plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Steps use checkbox (`- [ ]`).

**Goal:** Make a first-time pool run reach a clean **signed verdict** with no apparent hang and no error spam, over **both** the CF tunnel and loopback — the [[corralai-out-of-box-quality-bar]] the founder set ("can't sell if it fails out of the box… needs to run both over tunnels and not tunnels").

**Context / already done (Task 0, on this branch `fix/corral-agent-brain-auth`, committed `7b83f62`, needs review+merge):** corral-agent's MCP transport now attaches the `CORRALAI_BRAIN_KEY` bearer (via new shared `brainclient.AuthedHTTPClient`) and sets `DisableStandaloneSSE:true` — so it works against an authed brain AND over a tunnel, not just an auth-disabled loopback brain. The final review + tunnel/loopback verification (Task 4) covers it.

**The remaining failures (root-caused, verified 2026-07-15):**
1. The freeform **test-critic** runs corral-agent's *general builder* agentic loop (`cmd/corral-agent/main.go` `runTask`, `for step:=0;step<15`): a builder system prompt that invites `report_thought`, a builder toolset (`write_file`/`run_command`/`edit_file`/…), and no `report_thought` cap. Gemini-pro burned all 15 steps on `report_thought`, hallucinated an unregistered `repo_snapshot` tool, and never filed a finding or concluded → the run *looks* hung ~5 min. Structured roles (mutant-generator/test-writer) are immune (single-shot `Chat` with `tools=nil`).
2. **No wall-clock bound** anywhere in the pool: `driver.checkNoProgress` explicitly stands down whenever a task is `StatusClaimed` ("slow is not stuck"), so a claimed-but-wedged task stalls the run forever; `runState` holds no timestamp; `advPoolTickMaxErrors` only counts *Tick errors*, not clean-but-stalled ticks.
3. **MotherDuck trial-ended** → `cmd/corral/main.go` `startFleetSync` logs `fleet: sync error` every 30s with no backoff/throttle — a wall of red on every run.

**Tech Stack:** Go 1.26.5; `cmd/corral-agent`, `internal/advpool`, `cmd/corral`.

## Global Constraints
- New files start `// SPDX-License-Identifier: Elastic-2.0`. Module `github.com/pdbethke/corralai`, Go 1.26.5.
- **No behavior change for the non-pool / non-failing paths:** the critic fix must key on the test-critic role only and leave builder/tester/generalist behavior unchanged; the fleet throttle must not change behavior when MotherDuck is healthy (a successful sync resets everything); the driver deadline default must be generous enough never to fire on a healthy run.
- **The gate stays honest:** a run that hits the wall-clock deadline must produce a **signed `needs-review` verdict** stating it timed out — never a fake `certified`, never a silently-dropped run. The human gate semantics are unchanged.
- **Decorrelation, off-by-default, admin-only, signed-record invariants unchanged.**
- Verify gate before each commit: `gofmt -l` (empty) on touched files, `go build ./...`, `go test ./<touched package>/...` (advpool driver test with `-race`), `bash scripts/check-security.sh` (exit 0).

---

### Task 1: Freeform test-critic concludes fast (focused prompt + restricted tools + bounded loop)

**Files:**
- Modify: `cmd/corral-agent/main.go` (`isStructuredRole` ~590; `runTask` freeform branch: system-prompt build ~701-726, tool list, the `for step:=0;step<15` loop ~744-805)
- Test: `cmd/corral-agent/main_test.go` (or the existing agent test file — find it)

**Interfaces:**
- Consumes: `effectiveTaskRole` (already routes on the claimed task's role), `agentTools`, `backend.Chat`.
- Produces: a `isPoolCriticRole(role string) bool` helper (`role == "test-critic"`); a focused critic path in `runTask` (distinct system prompt, restricted tools, tighter budget, `report_thought` cap).

**Design:** the test-critic's job is narrow — read the dev tests (already in the `instruction`), file a `report_finding` for each vacuous test, then stop. It does NOT write files or run commands. So for `isPoolCriticRole(role)`:
- **System prompt:** a focused critic prompt (NOT the builder boilerplate) — see Step 3.
- **Tools:** restrict to `report_finding` + `report_thought` only (filter `agentTools` down, or build a small critic toolset). Removing `write_file`/`run_command`/`edit_file`/`claim_paths` stops the model groping for irrelevant/hallucinated tools.
- **Budget:** `critFreeformSteps = 6` (not 15).
- **`report_thought` cap:** after 2 `report_thought` calls, the loop injects a nudge result ("You have reflected enough. Now file a report_finding for each vacuous test, or reply DONE if the tests are sound.") instead of the normal result, so the model is pushed to conclude.

- [ ] **Step 1: Write the failing tests**

Find the agent test file (`ls cmd/corral-agent/*_test.go`). Add table tests for the pure helpers you introduce. Use a fake `Backend` that scripts `Chat` responses (grep the test file for an existing fake Backend and reuse it; if none, write a minimal one satisfying the `Backend` interface). Assert:

```go
func TestIsPoolCriticRole(t *testing.T) {
	if !isPoolCriticRole("test-critic") { t.Fatal("test-critic must be a pool critic role") }
	for _, r := range []string{"mutant-generator", "test-writer", "builder", "tester", ""} {
		if isPoolCriticRole(r) { t.Fatalf("%q must not be a pool critic role", r) }
	}
}

// The critic tool set excludes builder tools and includes only findings/thought.
func TestCriticToolsRestricted(t *testing.T) {
	names := toolNames(criticTools()) // criticTools() returns the restricted []any; toolNames extracts names
	must := map[string]bool{"report_finding": false, "report_thought": false}
	for _, n := range names {
		if _, ok := must[n]; ok { must[n] = true }
		if n == "write_file" || n == "run_command" || n == "edit_file" || n == "claim_paths" {
			t.Fatalf("critic tools must not include builder tool %q", n)
		}
	}
	for n, seen := range must { if !seen { t.Fatalf("critic tools missing %q", n) } }
}

// After 2 report_thoughts the loop nudges toward concluding rather than looping to 15.
func TestCriticLoopBoundsReportThought(t *testing.T) {
	// A fake backend that ALWAYS returns a report_thought tool call. Run the
	// critic path; assert it terminates in <= critFreeformSteps steps (i.e. the
	// fake's Chat is called at most critFreeformSteps times) and that after the
	// 2nd report_thought the injected user message contains the "file a
	// report_finding" nudge. Reuse whatever seam runTask exposes; if runTask is
	// hard to call directly, extract the loop into a testable helper
	// runCriticLoop(backend, sys, instruction, tools, brain) and test that.
}
```

If `runTask` is not directly unit-testable (it wires many brain calls), extract the critic interaction into a small pure-ish helper `runCriticLoop(...)` that takes the `backend`, the system prompt, the instruction, the tools, and a `brainCall`-like func, and returns the summary — then `runTask` calls it for `isPoolCriticRole(role)`. Test the helper. This keeps the change surgical and testable.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./cmd/corral-agent/ -run 'TestIsPoolCriticRole|TestCriticToolsRestricted|TestCriticLoopBoundsReportThought' -v`
Expected: FAIL to compile (`isPoolCriticRole`/`criticTools`/`runCriticLoop` undefined).

- [ ] **Step 3: Implement the focused critic path**

Add near `isStructuredRole` (~590):

```go
// isPoolCriticRole is the adversarial pool's freeform critic. Unlike a builder
// it neither writes files nor runs commands — it reads the dev tests handed to
// it and files findings — so it gets a focused prompt, a restricted toolset,
// and a tight loop rather than the general 15-step builder loop (which a model
// otherwise burns on report_thought / groping for tools it doesn't have).
func isPoolCriticRole(role string) bool { return role == "test-critic" }

// critFreeformSteps bounds the critic's tool loop (the general path is 15).
const critFreeformSteps = 6
```

In `runTask`, BEFORE the general freeform prompt build (~701), branch:

```go
	if isPoolCriticRole(role) {
		return runCriticLoop(backend, name, role, title, instruction, brain, missionID, taskID), nil
	}
```

Add `criticTools()` (restrict the global toolset to findings/thought — filter `agentTools(false)` by name, or construct just those two entries the same way `agentTools` builds them) and `runCriticLoop`:

```go
// runCriticLoop runs the test-critic to a conclusion: read the dev tests (in
// `instruction`), file a report_finding per vacuous test, then stop. Restricted
// tools + a tight budget + a report_thought cap keep it from looping.
func runCriticLoop(backend Backend, name, role, title, instruction string, brain func(string, map[string]any) string, missionID, taskID int64) string {
	sys := fmt.Sprintf(`You are %q, a TEST CRITIC in an adversarial audit. Your ONLY job: read the developer's tests provided below and decide whether any are vacuous, tautological, or designed to pass without exercising the goal.

You have EXACTLY two tools: report_finding (file one flaw — name the test and say what it fails to check) and report_thought (optional, at most twice). You CANNOT write files or run commands; do not attempt to.

Procedure: (1) read the tests; (2) call report_finding once per vacuous test — or none if the tests are sound; (3) reply with a one-line summary to finish. Do not narrate. Conclude within a few steps.

Task: %s
%s`, name, title, instruction)
	messages := []omsg{{Role: "system", Content: sys}, {Role: "user", Content: "Begin. Read the tests, file findings, then finish."}}
	tools := criticTools()
	last := "reviewed the dev tests"
	thoughts := 0
	for step := 0; step < critFreeformSteps; step++ {
		brain("heartbeat", map[string]any{"status": "critiquing"})
		m, err := backend.Chat(messages, tools)
		if err != nil {
			return "critic error: " + err.Error()
		}
		callName, args, ok := extractCall(m)
		if !ok {
			if c := oneline(m.Content); c != "" {
				last = c
			}
			return last
		}
		if callName == "report_finding" {
			if args == nil {
				args = map[string]any{}
			}
			args["mission_id"] = missionID
			args["task_id"] = taskID
		}
		if callName == "report_thought" {
			if args == nil {
				args = map[string]any{}
			}
			args["mission_id"] = missionID
			args["role"] = role
			args["name"] = name
			thoughts++
		}
		result := dispatch(name, role, "", brain, callName, args)
		// After 2 thoughts, stop accepting reflection — push to conclude.
		nudge := result
		if callName == "report_thought" && thoughts >= 2 {
			nudge = "You have reflected enough. Now call report_finding for each vacuous test (or none if the tests are sound), then reply with a one-line summary to finish."
		}
		messages = append(messages,
			omsg{Role: "assistant", Content: assistantEcho(m.Content, callName, args)},
			omsg{Role: "user", Content: fmt.Sprintf("[result of %s] %s", callName, nudge)})
	}
	return last
}
```

Notes for the implementer: match `dispatch`/`extractCall`/`assistantEcho`/`oneline`/`omsg` to their real signatures (the general loop at ~744 is your reference). `criticTools()` must return the SAME tool-schema shape `agentTools` produces (so `backend.Chat` accepts it) — reuse the `fn(...)` builders for just `report_finding` + `report_thought`, or filter the `agentTools(false)` slice by name. The caller path: `runTask` returns this summary and `runQueueLoop` calls `complete_task` with it exactly as today — so the critic always completes, fast.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/corral-agent/... -v`
Expected: PASS (new + existing). Confirm the general builder loop is untouched (a `builder`-role test, if present, still uses the 15-step path).

- [ ] **Step 5: Commit**

```bash
gofmt -w cmd/corral-agent/main.go cmd/corral-agent/*_test.go
go build ./... && go test ./cmd/corral-agent/... && bash scripts/check-security.sh
git add cmd/corral-agent/main.go cmd/corral-agent/*_test.go
git commit -m "corral-agent: focused, bounded test-critic loop (no builder-tool groping, no report_thought spin)"
```

---

### Task 2: Pool run-level wall-clock deadline → signed needs-review timeout verdict

**Files:**
- Modify: `internal/advpool/driver.go` (`Driver` struct; `NewDriver`; `runState` ~103; `StartRun` ~186; `Tick` ~265; add a deadline check + a timeout-verdict path)
- Test: `internal/advpool/driver_test.go`

**Interfaces:**
- Consumes: existing `aggregate`/`Signer.SignVerdict`, `Verdict`, `d.mu`.
- Produces: `Driver.Now func() time.Time` (injected clock; defaults to `time.Now` in `NewDriver` when nil) and `Driver.RunDeadline time.Duration` (0 = disabled; a sane non-zero default set in the brain wiring); `runState.startedAt time.Time`; a `Tick` deadline branch that, when exceeded before convergence, builds a `Status: StatusNeedsReview` verdict noting the timeout, signs it, stores it, and returns it (terminal).

**Design:** a wall-clock deadline is the backstop `checkNoProgress` can't be (it stands down on claimed tasks). On deadline, the run converges to a **signed needs-review** verdict ("did not complete within <deadline>") using whatever partial data exists (dev kill-rate if scored, else 0) — honest and terminal, so the CLI gets an answer and the single active slot frees.

- [ ] **Step 1: Write the failing test**

```go
func TestRunDeadlineProducesNeedsReviewVerdict(t *testing.T) {
	// Build a driver with an injected clock and a short RunDeadline; start a run
	// but do NOT complete its tasks (simulate a stall). Advance the clock past
	// the deadline, Tick, and assert: a non-nil Verdict with Status ==
	// needs-review, and that SignVerdict was called (the fake signer recorded a
	// call), and that a second Tick returns the same stored verdict (idempotent).
	clk := &fakeClock{t: someBaseTime}      // reuse/define; Now() returns clk.t
	d := newTestDriverWithClock(t, clk)     // adapt to the file's constructor + set d.RunDeadline
	// ... StartRun(7, spec, sigs) ...
	clk.advance(d.RunDeadline + time.Second)
	v, err := d.Tick(ctx, 7)
	if err != nil { t.Fatalf("deadline Tick should not error: %v", err) }
	if v == nil || v.Status != StatusNeedsReview { t.Fatalf("want needs-review verdict, got %+v", v) }
	// signer called; second Tick returns the same verdict (converged).
}
```

Base time must be passed in (the driver test cannot call `time.Now()` freely if the file forbids it — check the file's convention; if it uses real time in tests that's fine, but prefer the injected `fakeClock`). Reuse the file's existing fake Signer.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/advpool/ -run TestRunDeadlineProducesNeedsReviewVerdict -v`
Expected: FAIL to compile (`Now`/`RunDeadline`/`startedAt` undefined).

- [ ] **Step 3: Implement the clock + deadline**

Add to `Driver`: `Now func() time.Time` and `RunDeadline time.Duration`. In `NewDriver`, after constructing the driver: `if d.Now == nil { d.Now = time.Now }`. Add `startedAt time.Time` to `runState`; set it in `StartRun`: `d.runs[missionID] = &runState{rs: rs, sigs: sigs, testWriterTaskID: twID, startedAt: d.Now()}` (under `d.mu`, matching Task-2-era locking).

In `Tick`, after the early already-converged return and before the state-machine steps, add the deadline check:

```go
	if d.RunDeadline > 0 && d.Now().Sub(run.startedAt) > d.RunDeadline {
		v := d.timeoutVerdict(run)
		if d.Signer != nil {
			recordID, head, serr := d.Signer.SignVerdict(ctx, v)
			if serr != nil {
				return nil, fmt.Errorf("advpool: sign timeout verdict: %w", serr)
			}
			v.RecordID, v.RecordHead = recordID, head
		}
		d.mu.Lock()
		run.verdict = &v
		d.mu.Unlock()
		return &v, nil
	}
```

Add `timeoutVerdict` (mirror `aggregate`'s field-fill, but forced needs-review and never fed to the leaderboard — a timed-out run earns no fitness):

```go
// timeoutVerdict builds the signed needs-review verdict for a run that did not
// converge within RunDeadline. It uses whatever partial data was scored (dev
// kill-rate if the dev-adequacy step ran, else zero) and is NEVER certified and
// NEVER fed to the leaderboard.
func (d *Driver) timeoutVerdict(run *runState) Verdict {
	return Verdict{
		Repo: run.rs.Repo, Commit: run.rs.Commit,
		DevKillRate:  run.devKillRate,
		MutantsTotal: run.mutantsTotal,
		Survivors:    len(run.devSurvivors),
		ProvenMissed: run.provenMissed,
		ModelsByRole: d.Assign,
		Status:       StatusNeedsReview,
	}
}
```

(Confirm `d.Now()` calls outside the lock are fine — `run.startedAt` is set once at StartRun and never mutated, so reading it needs the lock only for the map lookup already done at Tick's head; keep the `run.verdict` write under `d.mu` as shown, consistent with `tickAggregate`.)

- [ ] **Step 4: Run tests to verify they pass (with -race)**

Run: `go test ./internal/advpool/ -race -run 'TestRunDeadline|TestRunStatus|TestVerdict' -v` then `go test ./internal/advpool/ -race`
Expected: PASS, no races.

- [ ] **Step 5: Wire a default deadline in the brain**

In `internal/brain/advpool.go` `StartAdversarialPool`, after `advpool.NewDriver(...)`, set `driver.RunDeadline` from an env with a sane default, e.g.:

```go
	driver.RunDeadline = 12 * time.Minute
	if s := strings.TrimSpace(os.Getenv("CORRALAI_ADVPOOL_RUN_DEADLINE_S")); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			driver.RunDeadline = time.Duration(n) * time.Second
		}
	}
```

Add `strconv` to the imports if needed. (12 min is generous — a healthy frontier run finishes in 2-4 min; this only catches a genuine stall.) Log it in the "advpool: ENABLED" line or a following line.

- [ ] **Step 6: Commit**

```bash
gofmt -w internal/advpool/driver.go internal/advpool/driver_test.go internal/brain/advpool.go
go build ./... && go test ./internal/advpool/... -race ./internal/brain/... && bash scripts/check-security.sh
git add internal/advpool/driver.go internal/advpool/driver_test.go internal/brain/advpool.go
git commit -m "advpool: run-level wall-clock deadline -> signed needs-review timeout verdict"
```

---

### Task 3: Throttle the fleet/MotherDuck sync-error spam

**Files:**
- Modify: `cmd/corral/main.go` (`startFleetSync` ~397-462; the sync-error log ~446 and the registration-retry error ~758)
- Test: if `startFleetSync`'s loop is not unit-testable as-is, extract the "should I log this failure?" decision into a tiny pure helper and test THAT (do not try to test the goroutine).

**Interfaces:**
- Produces: a small failure-throttle so a persistently-failing remote (e.g. MotherDuck trial ended) logs the first failure loudly, then at most once per ~N minutes — and a recovery logs once. No behavior change on success.

**Design:** mirror the counter pattern `advPoolTickMaxErrors` established. Keep a consecutive-failure count in the loop; log on the 1st failure and then every Kth (e.g. K such that it's ~once/10min at a 30s interval → K=20), and on the transition back to success log `fleet: sync recovered`. Optionally widen the effective retry interval under sustained failure, but log-throttling alone satisfies the bar.

- [ ] **Step 1: Write the failing test (pure helper)**

```go
func TestFleetFailureThrottle(t *testing.T) {
	th := &syncThrottle{logEvery: 20}
	// 1st failure logs; next 19 don't; 21st (every 20th) does; a success resets + signals recovery.
	if !th.shouldLogFailure() { t.Fatal("first failure must log") }
	for i := 0; i < 19; i++ { if th.shouldLogFailure() { t.Fatalf("failure %d must be throttled", i+2) } }
	if !th.shouldLogFailure() { t.Fatal("the 20th-later failure must log") }
	if rec := th.recordSuccess(); !rec { t.Fatal("first success after failures must signal recovery") }
	if rec := th.recordSuccess(); rec { t.Fatal("a success with no prior failure must not signal recovery") }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/corral/ -run TestFleetFailureThrottle -v`
Expected: FAIL to compile (`syncThrottle` undefined).

- [ ] **Step 3: Implement `syncThrottle` and use it in `startFleetSync`**

```go
// syncThrottle keeps persistent fleet-sync failures (e.g. a MotherDuck trial
// that ended) from spamming the log every interval: the first failure logs, then
// at most every logEvery-th, and the first success after failures logs recovery.
type syncThrottle struct {
	logEvery int
	fails    int
}

func (s *syncThrottle) shouldLogFailure() bool {
	s.fails++
	return s.fails == 1 || (s.logEvery > 0 && s.fails%s.logEvery == 0)
}

// recordSuccess resets the failure run and returns true iff this success ended a
// prior failure streak (so the caller can log a single "recovered" line).
func (s *syncThrottle) recordSuccess() bool {
	recovered := s.fails > 0
	s.fails = 0
	return recovered
}
```

In `startFleetSync`'s goroutine, replace the unconditional error log:

```go
	th := &syncThrottle{logEvery: 20} // ~once/10min at the 30s default
	for range t.C {
		...
		if n, err := fleet.Sync(cfg, target, brainID); err != nil {
			if th.shouldLogFailure() {
				log.Printf("fleet: sync error (%d consecutive): %v", th.fails, err)
			}
		} else {
			if th.recordSuccess() {
				log.Printf("fleet: sync recovered")
			}
			if n > 0 {
				log.Printf("fleet: synced %d reporting rows to %s", n, target)
			}
		}
		...
	}
```

Apply the same throttle to the `regRetry` error branch (~758) if it shares the loop (a second `syncThrottle`), or leave it if `regRetry` stops after first success — verify which and note it. Keep the successful-sync log (`synced N rows`) unchanged.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./cmd/corral/ -run TestFleetFailureThrottle -v` then `go test ./cmd/corral/...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
gofmt -w cmd/corral/main.go cmd/corral/*_test.go
go build ./... && go test ./cmd/corral/... && bash scripts/check-security.sh
git add cmd/corral/main.go cmd/corral/*_test.go
git commit -m "fleet: throttle persistent sync-error spam (log first + every Nth + recovery)"
```

---

### Task 4: Deploy + prove tunnel AND loopback (verification, controller-run)

Not a subagent task — the controller runs this after merge+deploy:
1. Final whole-branch review (opus) of the full batch (Task 0 auth/SSE + Tasks 1-3), then merge + deploy; confirm `advpool: ENABLED` + the new deadline log line, health 200, and that `fleet: sync error` no longer repeats every 30s.
2. Rebuild the worker from merged main on Hetzner (worktree). Run it **over the tunnel** (`CORRAL_BRAIN=https://brain.corralai.dev/mcp/`) and trigger `corral certify --adversarial` on `internal/fence` → assert it converges to a **signed verdict** (certified or needs-review) with the critic filing/among findings, no 5-minute stall.
3. Repeat **over loopback** (`http://127.0.0.1:9019/mcp/`) → same clean verdict.
4. Only then is the run-path "shipped" per [[corralai-out-of-box-quality-bar]].

## Self-Review (plan author)
- Coverage: critic-hang → T1; no-wall-clock-bound → T2; fleet spam → T3; tunnel+loopback proof → T4; auth/SSE → Task 0 (done, reviewed in the final pass).
- Type consistency: `isPoolCriticRole(string) bool`, `criticTools() []any`, `runCriticLoop(...) string`; `Driver.Now func() time.Time` + `Driver.RunDeadline time.Duration` + `runState.startedAt time.Time`; `syncThrottle{logEvery,fails}`. Implementers verify exact neighboring signatures (`dispatch`/`extractCall`/`aggregate`/`fleet.Sync`) against the real code — line numbers from a 2026-07-15 map may drift.
- Honesty: the deadline yields a SIGNED needs-review verdict, never a fake certified; the leaderboard is not fed on timeout.
