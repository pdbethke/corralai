<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Adversarial Testing Pool (slice 1) — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** A brain-coordinated distributed pool that **grades a change's own tests** by adversarial adequacy: a role-separated herd (`mutant-generator` + `test-writer` structured, `test-critic` freeform), each running the model **gate-earned** for its role (decorrelation-enforced), mutates the code, runs the **developer's tests** against the mutants (kill-rate = the dev suite's grade), authors tests that expose the survivors the dev's tests miss, flags designed-to-pass tests, and emits a **signed** verdict — gated on a human. The headline: *do this change's tests actually catch bugs, or are they written to pass without testing?*

**Architecture:** New pure driver `internal/advpool` (RunSpec, DAG builder, tick state machine over interfaces, aggregate/verdict, roles-as-data) + `internal/brain/advpool.go` (`StartAdversarialPool` wiring the real queue/jail/certify/staffing + the `start_adversarial_run` MCP tool + the tick loop) — mirroring the `internal/controlgate` + `internal/brain/controlgate.go` split. Reuses queue/coord/corral-agent, testgen/adequacy, StaffingManager, the certify chain. Off by default (`CORRALAI_ADVERSARIAL_POOL=1`).

**Tech Stack:** Go 1.26.5, module `github.com/pdbethke/corralai`.

## Global Constraints
- SPDX header on every new file.
- Per commit, from repo root: `export PATH="$PATH:$HOME/go/bin"` then `gofmt -l` (empty) && `go vet ./...` && `go build ./...` && `go test ./...` && `bash scripts/check-security.sh` — all green. **Paste the real `check-security.sh` last line + exit code in each report** (a prior effort falsely claimed green on gofmt).
- **Leaf-first**: every commit builds+passes. Off by default — nothing new runs unless `CORRALAI_ADVERSARIAL_POOL=1`.
- **Do not regress** the shipped merge gate, control gate, `corral certify`, or queue/coord — the pool is additive.
- **Soundness invariants (from the spec — enforce in code, verify in review):**
  1. The dev-suite kill-rate (the headline verdict) is computed by the **brain** running the DEVELOPER'S tests against the mutants via `adequacy.Score` in the jail — never a worker's self-report. "Designed to pass" = an objective ~0 kill-rate, not an opinion.
  2. Every worker artifact is **validated** brain-side (test must compile; mutants must parse) before use; an invalid artifact is refused (reuse the verify-gate refusal loop).
  3. **Human gate**: a blocking finding or a below-threshold kill-rate routes the run to `needs-review`, never auto-`done`.
  4. **Decorrelation enforced**: the `test-critic`'s model ≠ the `test-writer`'s model — a hard constraint on assignment, asserted in code + tested.
  5. **Gate-earned fitness**: the leaderboard is fed only `(model, role, certified-outcome)` after the deterministic gate runs — never a claim.
- **Testgen prompts must not change** in Phase 1 (pure refactor; golden-prompt test).

## File Structure
- **Create** `internal/advpool/` — `run.go` (RunSpec, Verdict, RoleAssignment), `roles.go` (roles-as-data: the DAG definition), `driver.go` (the tick state machine over interfaces), `aggregate.go` (verdict aggregation), + tests.
- **Create** `internal/brain/advpool.go` — `StartAdversarialPool(ctx, Options)`, the tick loop wired to queue/jail/certify/staffing, the `start_adversarial_run` MCP tool.
- **Modify** `internal/testgen/testgen.go` — expose `WriteTestPrompt`/`ParseTestOutput`, `GenerateMutantsPrompt`/`ParseMutantsOutput` (split from the `Ask` call).
- **Modify** `internal/queue/store.go` — add `Model` to `TaskSpec`/`Task` + schema column + plumbing.
- **Modify** `cmd/corral-agent/main.go` — run `task.Model` when set; structured-task fast path (single LLM call → raw artifact result).
- **Modify** `internal/brain/*` (Options) + `cmd/corral/main.go` — thread `staffingMgr`/`perfTracker`; call `StartAdversarialPool`.
- **Modify** `ROADMAP.md`/`README.md` — honest scope note.

## Interfaces produced (names later tasks rely on)
- `testgen.WriteTestPrompt(goal, code string, sigs []repoindex.Signature) (system, user string)`; `testgen.ParseTestOutput(raw string) string` — Task 1.1.
- `testgen.GenerateMutantsPrompt(goal, code string, sigs []repoindex.Signature, n int) (system, user string)`; `testgen.ParseMutantsOutput(raw string) ([]adequacy.Mutant, error)` — Task 1.2.
- `queue.TaskSpec.Model string` / `queue.Task.Model string` — Task 2.1.
- `advpool.RunSpec{Repo, Commit, Goal, CodePath, Code, DevTestPath, DevTestCode, TestCmd string; NMutants int}` (the DEV's tests are first-class input; `TestCmd` runs `DevTestCode`), `advpool.RoleAssignment map[string]string` (role→model), `advpool.Verdict{...}` — Task 4.1.
- `advpool.Roles` (roles-as-data) + `advpool.BuildDAG(RunSpec, RoleAssignment, sigs) []queue.TaskSpec` — Task 4.1.
- `advpool.Driver` (the tick state machine) with injected `Queue`/`Jail`/`Scorer`/`Signer`/`Findings` interfaces — Task 4.2/4.3.
- `brain.StartAdversarialPool(ctx, Options) (*advpool.Store, error)` + the `start_adversarial_run` tool — Task 5.

---

# PHASE 1 — testgen seam (pure refactor, prompts unchanged)

## Task 1.1: split `WriteTest` into prompt-render + parse
**Files:** Modify `internal/testgen/testgen.go`; Test `internal/testgen/testgen_test.go`.

**Interfaces — Produces:** `WriteTestPrompt(goal, code string, sigs []repoindex.Signature) (system, user string)` and `ParseTestOutput(raw string) string`. `WriteTest` becomes: `sys,usr := WriteTestPrompt(...); out,err := m.Ask(ctx,sys,usr); return ParseTestOutput(out), err` — **byte-identical prompts and parsing to today.**

- [ ] **Step 1: Write the characterization test (golden prompt)**
```go
func TestWriteTestPromptUnchanged(t *testing.T) {
	sigs := []repoindex.Signature{{Name: "Add", Text: "func Add(a,b int) int"}}
	sys, usr := WriteTestPrompt("cover Add", "func Add(a,b int)int{return a+b}", sigs)
	// pin the exact prompt so the refactor cannot drift it (paste the real
	// strings the current WriteTest builds — read them out of testgen.go first)
	if !strings.Contains(sys, /* the current system-prompt anchor */ "") { t.Fatal("system prompt drifted") }
	if !strings.Contains(usr, "func Add(a,b int)int{return a+b}") { t.Fatal("code missing from user prompt") }
}
func TestParseTestOutputStripsFences(t *testing.T) {
	got := ParseTestOutput("```go\npackage x\n```")
	if strings.Contains(got, "```") { t.Errorf("fences not stripped: %q", got) }
}
```
Before writing: READ the current `WriteTest` body and copy its exact prompt-build + output-cleanup into the assertions/impl. Do not invent new prompt text.

- [ ] **Step 2: Run → fail** (`undefined: WriteTestPrompt`). `go test ./internal/testgen/ -run TestWriteTestPrompt -v`.
- [ ] **Step 3: Implement** — extract the prompt-build into `WriteTestPrompt`, the output-cleanup into `ParseTestOutput`, rewrite `WriteTest` to call both. No behavior change.
- [ ] **Step 4: Run → pass.** Also run `go test ./internal/testgen/ ./internal/authoring/ ./internal/controlgate/` (the existing callers of WriteTest must still pass unchanged).
- [ ] **Step 5: Commit** `refactor(testgen): expose WriteTestPrompt/ParseTestOutput seam, prompts unchanged (advpool 1/N)`.

## Task 1.2: split `GenerateMutants` into prompt-render + parse
**Files:** Modify `internal/testgen/testgen.go`; Test same file.
**Interfaces — Produces:** `GenerateMutantsPrompt(goal, code string, sigs []repoindex.Signature, n int) (system, user string)`, `ParseMutantsOutput(raw string) ([]adequacy.Mutant, error)`. `GenerateMutants` = prompt+Ask+parse.
- [ ] **Step 1:** golden-prompt test + a parse test (a known LLM-shaped output → the expected `[]adequacy.Mutant`; and a malformed output → error). Copy the real prompt + parse logic from the current `GenerateMutants`.
- [ ] **Step 2:** run → fail.
- [ ] **Step 3:** extract; rewrite `GenerateMutants` to call both.
- [ ] **Step 4:** run → pass; `go test ./internal/testgen/ ./internal/authoring/ ./internal/controlgate/` green.
- [ ] **Step 5: Commit** `refactor(testgen): expose GenerateMutantsPrompt/ParseMutantsOutput seam (advpool 2/N)`.

---

# PHASE 2 — queue: per-task assigned model

## Task 2.1: add `Model` to TaskSpec/Task + schema
**Files:** Modify `internal/queue/store.go`; Test `internal/queue/store_test.go`.
**Interfaces — Produces:** `TaskSpec.Model string`, `Task.Model string`. `Enqueue` persists it; `ClaimNextAs`/task reads return it. Backward-compatible: empty `Model` = today's behavior (worker uses its own default).

- [ ] **Step 1: Failing test**
```go
func TestEnqueueCarriesAssignedModel(t *testing.T) {
	s := openTestQueue(t) // existing helper
	if err := s.Enqueue(1, []TaskSpec{{Key: "w", Role: "test-writer", Title: "t", Instruction: "i", Model: "qwen2.5-coder:7b"}}); err != nil { t.Fatal(err) }
	if _, err := s.PromoteReady(1); err != nil { t.Fatal(err) }
	task, err := s.ClaimNextAs("bee", "inst", []string{"test-writer"}, 60)
	if err != nil || task == nil { t.Fatalf("claim: %v", err) }
	if task.Model != "qwen2.5-coder:7b" { t.Errorf("Model = %q, want the assigned model", task.Model) }
}
```
- [ ] **Step 2: Run → fail** (unknown field `Model`).
- [ ] **Step 3: Implement** — add `Model` to both structs; `ALTER TABLE tasks ADD COLUMN model TEXT NOT NULL DEFAULT ''` in the schema migration block (mirror the existing `requires_review`/`review_rounds` migration pattern); include `model` in the `INSERT` (Enqueue) and every `SELECT ... FROM tasks` + `rows.Scan(...)` that builds a `Task` (there are several — update all so the column round-trips). Keep the existing column order stable.
- [ ] **Step 4: Run → pass**; full `go test ./internal/queue/...` green (all existing task round-trips still work with the new column).
- [ ] **Step 5: Commit** `feat(queue): per-task assigned Model column for gate-earned routing (advpool 3/N)`.

---

# PHASE 3 — worker: run the assigned model + structured result

## Task 3.1: worker runs `task.Model` when set
**Files:** Modify `cmd/corral-agent/main.go`; Test `cmd/corral-agent/*_test.go` (add if needed).
**Interfaces — Consumes:** `Task.Model`. Behavior: in the queue loop, when the claimed task has a non-empty `Model`, the worker uses it for this task's `backend.Chat` model instead of `AGENT_MODEL`; empty → `AGENT_MODEL` (unchanged). This is the mechanism that lets one multi-model worker serve whatever model the driver assigned.
- [ ] **Step 1: Failing test** — a table test on the "which model does this task use" selection helper: `taskModel(task, defaultModel) string` returns `task.Model` if set else `defaultModel`. (Extract the selection into a testable pure helper; the `backend.Chat` call reads it.)
- [ ] **Step 2:** run → fail.
- [ ] **Step 3:** add `taskModel` + thread it into the per-task chat call. If the worker's backend can't serve arbitrary models (single-model harness), and `task.Model` != its model, it should still run with its own model but **record the mismatch** in its completion result (honest: "ran <own> not assigned <x>") — do not silently pretend. (Slice-1 assumption: workers are multi-model; this is the honest fallback.)
- [ ] **Step 4:** run → pass; `go test ./cmd/corral-agent/...` green.
- [ ] **Step 5: Commit** `feat(agent): run the task's assigned model when set (advpool 4/N)`.

## Task 3.2: structured-task fast path (single LLM call → raw artifact)
**Files:** Modify `cmd/corral-agent/main.go`; Test.
**Interfaces:** A task marked structured (a convention: `Role` in {`test-writer`,`mutant-generator`} AND the instruction is a rendered prompt) is handled by a fast path: one `backend.Chat` with the task's instruction as the user prompt (no tool loop), the raw model output returned verbatim as the `complete_task` result (the brain validates/parses it). Freeform roles keep the existing tool loop. Signal structured-ness explicitly via a task field or an instruction sentinel — RECOMMENDED: a `Role`-based switch (`isStructuredRole(role)`), so no new task field.
- [ ] **Step 1: Failing test** — `isStructuredRole("test-writer")==true`, `isStructuredRole("mutant-generator")==true`, `isStructuredRole("test-critic")==false`; and a test that the structured path returns the model's raw output as the result (fake backend).
- [ ] **Step 2:** run → fail.
- [ ] **Step 3:** implement `isStructuredRole` + the fast-path branch in `runTask`/`runQueueLoop`.
- [ ] **Step 4:** run → pass; existing freeform worker tests green.
- [ ] **Step 5: Commit** `feat(agent): structured-role fast path returns the raw artifact (advpool 5/N)`.

---

# PHASE 4 — the driver (`internal/advpool`)

## Task 4.1: RunSpec + roles-as-data + BuildDAG (with staffing assignment)
**Files:** Create `internal/advpool/run.go`, `internal/advpool/roles.go`; Test `internal/advpool/roles_test.go`.
**Interfaces — Produces:**
```go
type RunSpec struct { Repo, Commit, Goal, CodePath, Code, DevTestPath, DevTestCode, TestCmd string; NMutants int }
type RoleAssignment map[string]string // role -> model
// A role is data: prompt-render + result contract + deps.
type Role struct {
	Name       string
	Structured bool
	Deps       []string
	// Render builds the task instruction from the run + signatures + (for
	// deps-satisfied roles) the survivors the dev's tests missed.
	Render func(rs RunSpec, sigs []repoindex.Signature, survivors []adequacy.Mutant) string
}
func Roles() []Role // {mutant-generator, test-writer, test-critic}
func BuildDAG(rs RunSpec, assign RoleAssignment, sigs []repoindex.Signature) []queue.TaskSpec
```
- `mutant-generator`: structured, no deps, `Render` = `testgen.GenerateMutantsPrompt` (mutate `Code`).
- `test-critic`: freeform, no deps, `Render` = a prompt asking the model to read `DevTestCode` and flag vacuous/tautological/designed-to-pass tests (files findings). Runs in **parallel** — it critiques the dev's tests directly, independent of mutation.
- `test-writer`: structured, `Deps: ["dev-adequacy"]` (a synthetic dep key the driver satisfies after scoring the dev's tests — see 4.2), `Render` = `testgen.WriteTestPrompt` **targeted at the survivors** (write a test that kills the mutants the dev's tests missed).
- `BuildDAG` initially enqueues `mutant-generator` + `test-critic` (no deps); `test-writer` enqueues/promotes once `dev-adequacy` is satisfied. Each `TaskSpec.Model = assign[role]`, `DependsOn` from `Role.Deps`.
- [ ] **Step 1: Failing test** — `BuildDAG` with a RunSpec + `{mutant-generator:"B", test-critic:"C", test-writer:"A"}`: `mutant-generator`(B) and `test-critic`(C) have no deps; `test-writer`(A) `DependsOn` the `dev-adequacy` key; assert the `mutant-generator` instruction contains the GenerateMutants prompt text (via the seam) and `test-critic`'s instruction references the dev's tests.
- [ ] **Step 2:** run → fail.
- [ ] **Step 3:** implement `RunSpec`/`Role`/`Roles()`/`BuildDAG`.
- [ ] **Step 4:** run → pass.
- [ ] **Step 5: Commit** `feat(advpool): RunSpec + roles-as-data + BuildDAG (advpool 6/N)`.

## Task 4.2: the tick state machine (dev-adequacy → test-writer → pool-adequacy → aggregate), over interfaces
**Files:** Create `internal/advpool/driver.go`, `internal/advpool/aggregate.go`; Test `internal/advpool/driver_test.go`.
**Interfaces — Produces:**
```go
type Scorer interface { // wraps adequacy.Score in the jail
	Score(ctx context.Context, codePath, code, test string, mutants []adequacy.Mutant, testCmd string) (killRate float64, survivors []adequacy.Mutant, err error)
}
type Validator interface { // brain-side artifact validation (soundness #2)
	CompileTest(ctx context.Context, codePath, code, test string) error
	ParseMutants(raw string) ([]adequacy.Mutant, error) // = testgen.ParseMutantsOutput
}
type Verdict struct {
	Repo, Commit string
	DevKillRate float64        // the headline: the DEV suite's kill-rate
	MutantsTotal, Survivors int
	ProvenMissed int          // survivors the pool's authored test then killed (real, catchable gaps)
	VacuousFindings []queue.Finding // test-critic's designed-to-pass/vacuous flags
	ModelsByRole map[string]string
	Status string // certified | needs-review
}
// Driver.Tick advances one run given the current task states; pure over
// injected effects (queue reads, Scorer, Findings). Returns the next action.
```
The tick logic (mirrors the mission-engine pattern, re-pointed):
1. `PromoteReady`.
2. When `mutant-generator` is `done`: **parse** its mutants (`ParseMutants`; refuse→reissue on malformed); run `Scorer.Score(DEV's tests, mutants, testCmd)` → `DevKillRate` + survivors; store them; satisfy the `dev-adequacy` dep key so `test-writer` promotes with the **survivors** in its instruction.
3. When `test-writer` is `done`: **validate** its result (`CompileTest`; refuse→reissue on non-compile); run `Scorer.Score(pool test, survivors, testCmd)` → `ProvenMissed` (how many survivors the pool's test kills = real gaps the dev's tests missed).
4. When `test-critic` is `done` **and** pool-adequacy is done: `aggregate` → `Verdict{DevKillRate, survivors, ProvenMissed, VacuousFindings, models_by_role}`; **human gate**: if `blockingFindingOpen` OR `DevKillRate < threshold` → `Status: needs-review`; else `Status: certified`.
5. No-progress backstop (reuse the pattern): a stalled run fails.
- [ ] **Step 1: Failing tests** (fakes for queue/Scorer/Findings): (a) with `mutant-generator` done, Tick runs `Score(dev tests)` and promotes `test-writer` with the survivors; (b) with `test-writer` done, Tick validates + runs `Score(pool test, survivors)` → `ProvenMissed`; (c) `test-critic` + pool-adequacy done + no blocking finding + `DevKillRate` above threshold → `Status==certified`; (d) a blocking finding → needs-review; (e) `DevKillRate` below threshold → needs-review; (f) **decorrelation**: an assignment where `test-critic` model == `test-writer` model is rejected (assert an error/guard).
- [ ] **Step 2:** run → fail.
- [ ] **Step 3:** implement the tick state machine + `aggregate` + the decorrelation guard.
- [ ] **Step 4:** run → pass; `go test -race ./internal/advpool/...`.
- [ ] **Step 5: Commit** `feat(advpool): tick state machine — dev-adequacy → test-writer → gated verdict (advpool 7/N)`.

## Task 4.3: sign the verdict + feed the leaderboard
**Files:** Modify `internal/advpool/driver.go` (+ a `Signer`/`LeaderboardSink` interface); Test.
**Interfaces — Produces:**
```go
type Signer interface { // wraps the certify chain
	SignVerdict(ctx context.Context, v Verdict) (recordID int64, head string, err error)
}
type LeaderboardSink interface { // gate-earned fitness feed (soundness #5)
	Record(model, role, outcome string) // outcome derived from the CERTIFIED result
}
```
On a terminal verdict, the driver calls `Signer.SignVerdict` (subject = repo@commit; byproducts = the Verdict fields incl `models_by_role`) and, **only after** the deterministic score + sign, calls `LeaderboardSink.Record` for each role's model with the certified outcome (`test-writer`: did its authored test kill the survivors = `ProvenMissed>0`; `mutant-generator`: did its mutants compile and expose survivors; `test-critic`: did its findings hold). The brain-side `Signer` reuses `certify.BuildLedger`/`BuildAttestation`/`SignDSSE` + `buildstore`; the record verifies with `corral certify verify`.
- [ ] **Step 1: Failing test** — fake Signer/Sink: on a certified verdict, `SignVerdict` is called with `ModelsByRole` populated and the leaderboard is fed `(model, role, outcome)` for all three roles; on `needs-review`, assert the run does NOT auto-sign a "certified" (it may sign a needs-review record, but never a passing one past the gate).
- [ ] **Step 2:** run → fail.
- [ ] **Step 3:** implement.
- [ ] **Step 4:** run → pass.
- [ ] **Step 5: Commit** `feat(advpool): sign the verdict + gate-earned leaderboard feed (advpool 8/N)`.

---

# PHASE 5 — brain wiring + MCP trigger

## Task 5.1: brain-side effects + `start_adversarial_run` MCP tool
**Files:** Create `internal/brain/advpool.go`; Test `internal/brain/advpool_test.go`.
**Interfaces — Produces:** `StartAdversarialPool(ctx context.Context, opts Options) (*advpool.Store, error)` and the `start_adversarial_run` tool. Wires the real effects:
- `Scorer` = `adequacy.NewJail(opts.GateBackend, gate.DefaultGateTimeout)` + `adequacy.Score`.
- `Validator.CompileTest` = lift `authoring.compileVerify` (jail compile); `ParseMutants` = `testgen.ParseMutantsOutput`.
- `Signer` = the existing `certifyBuild`-style path (reuse `opts.CertifyKey`/`BuildStore`/`Witness`).
- `LeaderboardSink` = `opts.Perf` (the `PerformanceTracker`).
- staffing = `opts.Staffing` (a `*mission.StaffingManager`, added to `Options` — see 5.2): assign role→model, **decorrelation-enforced** (`test-critic` = best-earned model ≠ `test-writer`'s).
- The tick loop: a goroutine ticking the `advpool.Driver` for the active run (single active run; reuse queue mission scoping).
- `start_adversarial_run(RunSpec) → {run_id}` is **`isHumanAdmin`-gated** (soundness #3 trigger); refuses worker principals.
- [ ] **Step 1: Failing test** — the tool is admin-gated (a worker principal is refused); a valid admin call enqueues a run's initial DAG (assert `mutant-generator` + `test-critic` tasks with stamped models + decorrelation: `test-critic` model ≠ `test-writer` model in the assignment).
- [ ] **Step 2:** run → fail.
- [ ] **Step 3:** implement `StartAdversarialPool` + the tool + the decorrelation-enforcing assignment.
- [ ] **Step 4:** run → pass; `go test ./internal/brain/...` green.
- [ ] **Step 5: Commit** `feat(brain): StartAdversarialPool + admin-gated start_adversarial_run (advpool 9/N)`.

## Task 5.2: thread staffing/leaderboard into Options + main.go wiring + enable flag
**Files:** Modify `internal/brain/brain.go` (Options), `cmd/corral/main.go`.
- [ ] **Step 1:** add `Staffing *mission.StaffingManager` (and confirm `Perf PerformanceTracker` is reachable) to `brain.Options`; in `main.go`, set `brainOpts.Staffing = staffingMgr`; after `StartControlGate`, add:
```go
if os.Getenv("CORRALAI_ADVERSARIAL_POOL") == "1" {
	if _, err := brain.StartAdversarialPool(context.Background(), brainOpts); err != nil {
		log.Printf("adversarial pool: disabled (%v)", err)
	} else {
		log.Printf("adversarial pool: ENABLED (start_adversarial_run live)")
	}
} else {
	log.Printf("adversarial pool: disabled (set CORRALAI_ADVERSARIAL_POOL=1)")
}
```
- [ ] **Step 2:** `go build ./...`; confirm the daemon compiles and boots with the flag unset (pool disabled) — no behavior change by default.
- [ ] **Step 3:** run the full gate.
- [ ] **Step 4: Commit** `feat(brain,cmd): wire StartAdversarialPool, off by default (advpool 10/N)`.

---

# PHASE 6 — integration + docs

## Task 6.1: hermetic end-to-end run + docs
**Files:** Test `internal/brain/advpool_integration_test.go`; Modify `ROADMAP.md`, `README.md`.
- [ ] **Step 1: Integration test (fake workers, real driver/scorer over a fake/real jail):** start a run; simulate the role tasks completing with canned artifacts (mutants; a `test-critic` designed-to-pass finding; a `test-writer` test); the fake `Scorer` returns a chosen `DevKillRate` + survivors for the dev's tests and a `ProvenMissed` for the pool's test; drive ticks; assert a **signed** verdict is produced with the right `DevKillRate`, `ProvenMissed`, `VacuousFindings`, and `models_by_role` (decorrelated), and that a `corral certify verify`-shaped check accepts the record; a variant where a low `DevKillRate` (the dev's tests catch nothing) → `needs-review` (no certified sign). Use the shared isolator or a fake `Scorer` for hermeticity (no network).
- [ ] **Step 2:** run → green (`go test ./internal/brain/ -run Adversarial -v`, `-race`).
- [ ] **Step 3: Docs (honest):** ROADMAP + README note the adversarial pool as **experimental, off by default**, describing exactly what slice 1 does (distributed 3-role hybrid + gate-earned routing + mutation-scored + human-gated + signed) and what it does NOT yet (pentester, concurrent runs, CLI trigger). Do NOT claim more than slice 1 ships. If any binary `-h` changed, run `bash scripts/gen-cli-docs.sh` + `--check`.
- [ ] **Step 4: Full gate** (vet/build/test/-race the new packages/check-security/gen-cli-docs --check). **Commit** `feat(advpool): hermetic end-to-end run + honest docs (advpool 11/11)`.

---

## Self-Review
- **Spec coverage:** driver + DAG (4.1), tick/adequacy/aggregate/human-gate (4.2), sign + leaderboard (4.3), dynamic routing + decorrelation-enforced (5.1, assignment), per-task model (2.1, 3.1), structured/freeform hybrid (3.2, roles-as-data 4.1), testgen seam (1.1/1.2), wiring off-by-default (5.2), integration + honest docs (6.1). Non-goals (pentester, concurrent runs, CLI trigger) untouched.
- **Soundness invariants → tasks:** deterministic verdict (4.2 Scorer, brain-side), validate artifacts (4.2 Validator + refusal), human gate (4.2/5.1), decorrelation-enforced (4.2 guard + 5.1 assignment, both tested), gate-earned fitness (4.3, fed only after certify).
- **Order safety:** leaf-first — testgen seam (1) → queue column (2) → worker (3) → pure driver over interfaces (4) → brain wiring (5) → integration (6). Each phase builds on green predecessors; nothing runs until 5.2, off by default.
- **Type consistency:** `RunSpec`/`RoleAssignment`/`Verdict`/`Role` defined in 4.1 and consumed by 4.2/4.3/5.1; `Scorer`/`Validator`/`Signer`/`LeaderboardSink` interfaces defined where first used and implemented in 5.1; `testgen` seam names consistent across 1.x and 4.1.
- **Placeholder note:** Phase 1 tests say "paste the real prompt strings" — that is a deliberate READ-then-pin instruction (the prompts live in existing code), not a deferred TODO. Phase 4 interface bodies are specified by behavior + injected effects; implementers write the state-machine code against the tests.
- **Risk:** the worker structured fast-path (3.2) and the multi-model backend assumption (3.1 honest fallback) are the least-proven; the integration test (6.1) uses fake workers so the driver is provable without a live multi-model herd.
