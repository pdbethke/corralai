<!-- SPDX-License-Identifier: Elastic-2.0 -->
# The adversarial testing pool — a brain-coordinated herd that certifies a change (design)

**Status:** design (2026-07-14). Precedes an implementation plan. Third slice of the audit re-focus (`docs/superpowers/specs/2026-07-13-corral-refocus-audit-not-builder-design.md`); slice 2 shipped the standalone `corral certify` CLI. This is the innovation exhibit: the distributed adversarial verification the field note describes.

## The vision (where this is going)

A code change arrives — **with the tests the developer wrote for it.** The brain turns a **pool of role-separated worker agents** loose on it, and the primary question is not "do the tests pass?" but **"do the dev's tests actually test anything?"** A **mutant-generator** seeds violations into the code; the brain runs the **developer's own tests** against those mutants — the **kill-rate is the dev suite's grade**. Tests that pass every mutant catch *nothing*: they suck, or they're **designed to pass** (vacuous, tautological, CI-theater). A **test-writer** then authors tests that *do* kill the survivors — proving the missed bugs are real and catchable, not equivalent mutants. A **test-critic** (a *different* model) reads the dev's tests for gamed/vacuous patterns. Each role runs the **model that earned it** off the leaderboard, in its own jail; the brain aggregates, gates the verdict on a **human**, and emits a **signed** record: *"this change's tests catch K% of injected bugs; here are the ones they miss; here are the tests written to pass without testing."* Certify-by-adversarial-adequacy, distributed across the herd.

**This is the CISO-facing verdict nobody else produces.** SLSA proves the build ran; CI proves the tests passed. Neither tells you whether the tests were *worth* passing. The pool does — objectively (mutation kill-rate) and adversarially (a decorrelated herd trying to prove the suite hollow).

This spec designs **sub-slice 1: the spine** — the driver + a hybrid 3-role DAG **with dynamic gate-earned routing**, proven end-to-end distributed. Slices 2–3 add the pentester, concurrent runs, and the CLI trigger.

## The re-focus context (why this is mostly assembly)

Retire-the-builder removed only the *driver* — the thing that decomposed work into tasks and drove the herd. The substrate survived intact:

- **`internal/queue`** (REUSE-AS-IS): a generic pull-queue. `TaskSpec{Key,Role,Title,Instruction,DependsOn,Verify}` → `Enqueue(missionID, specs)`; `PromoteReady` (pending→ready once deps done); `ClaimNextAs(bee,instance,roles,lease)` (atomic role-matched claim, `role IN (roles…,'')`); `Complete(id,bee,result)`. Claiming is **global across missions** today.
- **`internal/queue` findings** (REUSE-AS-IS): `Finding{Type,Severity,Target,Evidence,Reporter,ReporterModel,…}` is *already* the adversarial-output schema. `AddFinding`, `Findings(missionID,status)`, `SeverityRank`; `blockingFindingOpen` gates convergence.
- **`internal/coord`** (REUSE-AS-IS): `Register`/`Heartbeat`/`ListActive`/`ClaimPaths`/`ReapAbsentClaims` — presence + advisory leases. Task ownership is guaranteed by the queue's single-writer atomic claim; coord is a separate advisory layer.
- **`cmd/corral-agent`** (REUSE-WITH-CHANGES): the claim→LLM-loop→jail(`run_command`)→`complete_task`(+findings) worker. Role today only changes prompt framing; per-task behavior comes from the enqueued `Instruction`. Adding structured roles needs a typed output contract (below).
- **`internal/testgen` / `adequacy` / `authoring`** (REUSE, some WITH-CHANGES): `WriteTest(m,goal,code,sigs)`, `GenerateMutants(m,goal,code,sigs,n)`, `TriageSurvivors(m,goal,code,test,survivors)`, `adequacy.Score(jail,base,codePath,code,mutants,testCmd)→Report.KillRate()`, `adequacy.NewJail(backend,timeout)`. Pure functions of an `LLM`/`Jail` interface — liftable.
- **`internal/mission` engine** (pattern REUSE-WITH-CHANGES): `Tick` = promote deps → sweep dead deps → detect drained → `blockingFindingOpen` gate → converge/`needs-review` + no-progress backstop. Dormant but copy-adaptable: "decompose a build directive → certify done" becomes "decompose a change → compute an adequacy verdict."
- **`StaffingManager`** (REUSE-AS-IS): `Staff(directive)` Sense→Judge→Clamp, standalone, role→model off the gate-earned leaderboard.
- **Wiring** (REUSE-AS-IS pattern): the `Start*(ctx, brainOpts)` convention (`StartGate`/`StartControlGate`), the shared `sandbox.Isolator` (`execBackend`, hoisted so every subsystem shares one), the certify chain (`certifyKey`/`buildStore`/`witness`), `mcp.AddTool` typed handlers, `isHumanAdmin` gating. **New:** nothing like `StartAdversarialPool` exists — it's assembled from these materials.

## The key architecture decision: distributed structured vs freeform (HYBRID — locked)

Because the pool is **distributed**, the LLM generation for a role happens **on the worker** (the worker's own gate-earned model) — the brain cannot run testgen with a worker's model. So the structured/freeform split is about the **output contract**, not where the model runs:

- **Structured roles** (`test-writer`, `mutant-generator`): the driver renders the *proven testgen prompt* into the task `Instruction`; the worker's model produces a **typed, validated artifact** (a test that must compile; mutants in the `adequacy.Mutant` schema); the brain **validates** it (compile-verify; schema-parse) and feeds the **deterministic** `adequacy.Score` (jail kill-rate). Reliability where it matters.
- **Freeform roles** (`test-critic` in this slice; `pentester` later): the worker runs its normal LLM+jail loop against an instruction and files **findings** (the existing untyped `Finding` contract). Judgment/exploration where autonomy helps.

This requires a **testgen seam**: expose prompt-render + output-parse separately from the LLM call, so a structured role reuses testgen's exact prompt as a task instruction and the brain parses/validates the worker's result. (REUSE-WITH-CHANGES to `internal/testgen`.)

**Flexibility principle — roles are data.** A role is defined by three things: a *prompt-render* (target → instruction), a *result contract* (structured-and-validated, or freeform-findings), and its *DAG deps*. New adversarial roles (edge-hunter, dependency-auditor, …) compose by adding a data entry — no new driver plumbing. This keeps the pool maximally flexible while the machinery stays fixed.

## Sub-slice 1 — scope

### The run
A **run** = one **target**: the code under review **plus the developer's tests for it**, a goal, and the command that runs those tests. `RunSpec{Repo, Commit, Goal, CodePath, Code, DevTestPath, DevTestCode, TestCmd, NMutants}` — extends the control-gate's `StageRequest` with the **dev's tests as first-class input** (`TestCmd` runs `DevTestCode`). A run is a queue **mission** (`mission_id` = the run id). **One active run at a time**; concurrent runs are slice 3.

### The DAG (enqueued by the driver)
```
[mutant-generator] (structured, B) ─→ (brain) adequacy.Score(DEV's tests, mutants) ─→ dev_kill_rate + survivors
                                                                                          │
[test-critic] (freeform, C) ── reads the dev's tests for vacuous/designed-to-pass ─┐      ├─→ [test-writer] (structured, A≠C):
                                → findings                                         │      │      write a test that KILLS the survivors
                                                                                   │      └─→ (brain) adequacy.Score(pool's test, survivors) → proven_missed bugs
                                                                                   └──────────────────────────────────────────────────────→ (brain) aggregate
                                                                                          → verdict {dev_kill_rate, survivors, proven_missed, vacuous_findings, models_by_role}
                                                                                          → human gate → sign
```
- **The headline is the dev suite's kill-rate.** `adequacy.Score` runs the **developer's own tests** against the mutant-generator's mutants; the survivors are the bugs the dev's tests *miss*. A near-zero kill-rate means the tests don't test — they suck, or they're designed to pass.
- **Worker tasks**: `mutant-generator` (mutate the code → violations), `test-writer` (author tests that kill the *survivors* — proving the missed bugs are real and catchable, not equivalent mutants), `test-critic` (freeform: flag vacuous/tautological/designed-to-pass dev tests → findings). Each stamped with a **leaderboard-assigned model**, **decorrelation enforced** (`test-writer` model ≠ `test-critic` model). `test-writer` `DependsOn` the dev-adequacy result (it needs the survivors).
- **Brain-side steps** (driver tick, shared isolator): `adequacy.Score(dev tests, mutants)` once `mutant-generator` is done → `dev_kill_rate` + survivors; `adequacy.Score(pool test, survivors)` once `test-writer` is done → `proven_missed`; **aggregate** once `test-critic` + pool-adequacy are done; **sign**.

### Gate-earned routing — DYNAMIC (this slice — the wow)
Before enqueuing a run's tasks, the driver asks `StaffingManager` (Sense→Judge→Clamp) to assign **each role → the model that has *earned* it** off the DuckDB leaderboard (`PerformanceTracker`), and **stamps the assigned model onto each task** (a new `TaskSpec.Model` field). Workers claim role tasks as usual and **run the task's assigned model** (their backend is a multi-model gateway — see assumptions), instead of a fixed `AGENT_MODEL`. This needs no model-scoped claiming and no worker-spawning — just a per-task model stamp.

- **Decorrelation is enforced, not hoped:** the `test-critic`'s model is the best-earned model that is *not* the `test-writer`'s. A single model can never both write the exposing test and grade the dev's tests.
- **The compounding loop, live:** every completion feeds the leaderboard `(model, role, certified-outcome)`; the *next* run routes better. The brain continuously re-evaluates who's best at each adversarial role **from the signed verdicts** and routes accordingly (the [[corralai-continuous-reevaluation-differentiator]], made visible — Fugu's route-to-the-fittest with a *gate-earned* fitness signal).
- **Cold start is honest:** with thin data the assignment is sample-weighted defaults / exploration (StaffingManager is already "honest about thin data"), sharpening as certified runs accumulate; the signed record states which basis was used.

### The driver: `StartAdversarialPool(ctx, brainOpts)`
Following `StartControlGate`'s shape. Enable via `CORRALAI_ADVERSARIAL_POOL=1` (off by default). Responsibilities:
1. Expose an admin-gated MCP tool `start_adversarial_run(RunSpec) → run_id` (the slice-1 trigger; `corral certify --adversarial` is slice 3).
2. On a new run: **call `StaffingManager` to assign role→model off the leaderboard** (decorrelation-enforced: `test-critic` ≠ `test-writer`); extract signatures (`repoindex`); render the structured prompts; `Enqueue` the initial tasks (mission = run), **each stamped with its assigned model** (`TaskSpec.Model`).
3. A tick loop (reusing the engine's promote/gate/backstop pattern, adapted): `PromoteReady` → when `mutant-generator` done, run `adequacy.Score(DEV's tests, mutants)` in the jail → `dev_kill_rate` + survivors, store them, and promote `test-writer` with the survivors in its instruction → when `test-writer` done, run `adequacy.Score(pool test, survivors)` → `proven_missed` → when `test-critic` + pool-adequacy done, **aggregate** → **human gate** (open finding ≥ block-severity, or `dev_kill_rate` below threshold, routes to `needs-review`; else on `promote`) → **sign** the verdict via the certify chain → run `done`. A no-progress backstop fails a stalled run.
4. Validation seams: compile-verify the `test-writer`'s artifact (lift `authoring.compileVerify`); schema-parse the `mutant-generator`'s artifact. The dev's tests are run as-provided (they are the subject under grade).

### The worker changes (`cmd/corral-agent`)
- Structured roles produce a **typed result**: the worker recognizes a structured task (a flag/role) and returns the artifact as its `complete_task` result in the agreed shape (test source; mutants JSON) — validated brain-side, refused (existing verify-gate refusal loop) if it doesn't compile/parse.
- Freeform `test-critic` uses the existing loop + `report_finding` (flagging vacuous/designed-to-pass dev tests).
- Keep the worker generic: no role→behavior dispatch table beyond the structured-vs-freeform result contract + the instruction the driver renders.

### The signed verdict
Reuse the certify chain (`certify.BuildLedger`/`BuildAttestation`/`SignDSSE`, `buildstore`). The verdict statement's subject = the change (repo@commit); byproducts = `{dev_kill_rate, mutants_total, survivors, proven_missed, vacuous_findings, models_by_role}`. Verifiable offline with the existing `corral certify verify` path. This is where the pool's output becomes **evidence** — a signed statement that this change's tests catch `dev_kill_rate` of injected bugs, that here are the ones they miss (proven catchable), and that here are the tests written to pass without testing.

## Soundness invariants — what keeps the "wow" defensible

The wow is the herd. The **soundness is the deterministic, no-trust gate the herd feeds.** These are load-bearing invariants a sharp critic will test, so they are stated first-class, not left implicit:

1. **The verdict is deterministic, not the LLM's word.** The headline — the dev suite's kill-rate — is computed by the **brain** re-running the **developer's own tests** against the mutants *in a jail* (`adequacy.Score`); given the same tests + mutants it is reproducible. The "designed to pass" charge is not an opinion: a vacuous test kills ~0 mutants, which is an **objective, reproducible** measurement — the `test-critic`'s judgment corroborates it but the kill-rate is load-bearing. LLM creativity *feeds* the gate; it is never *the* gate.
2. **Never trust a worker.** Every worker artifact is **validated** brain-side (the test must compile; mutants must parse the `adequacy.Mutant` schema) and a worker's *self-reported* outcome is never taken as the result — the brain computes it. Worker code runs **only in the jail**. A lazy or hostile worker cannot forge a green.
3. **Human gate + signed, offline-verifiable record.** No auto-certify past a blocking finding or a below-threshold kill-rate; the verdict routes to `needs-review`. The record is signed and independently verifiable with the existing `corral certify verify` (no trust in the brain that produced it).
4. **Honest, scoped claims.** Slice 1 *records* the routing signal (it does not yet dynamically route); decorrelation is achieved *structurally* (distinct roles → distinct workers → distinct models); the record states exactly what ran and with which models. Nothing is labeled "autonomous" or "AI-certified."
5. **Earned complexity — why distributed, not in-process.** The pool is distributed for three *load-bearing* reasons, each recorded in the verdict so the choice is defensible, not decorative:
   - **Multi-model decorrelation** — different model *endpoints* per role (`test-writer` ≠ `test-critic`), which a single in-process client cannot provide; this is the "a judge may not certify herself" mechanism made real.
   - **Independent per-agent attribution** — each role's contribution is separately recorded, scored on the leaderboard, and named in the signed record (`models_by_role`).
   - **Horizontal scale** — roles and targets fan out across workers.
   If any of these three were false, the herd *would* be theater — so the design makes them central, and the demo leads with the deterministic spine, not the swarm animation.
6. **The routing fitness signal is gate-earned, not gameable.** Dynamic routing is only as trustworthy as the signal it optimizes. That signal is **not** a self-report or a vanity metric — a model's fitness is measured by outcomes the **deterministic, execution-verified gate certified** (did the `test-writer`'s test actually kill the survivors; did the `mutant-generator`'s mutants compile and expose gaps; did the `test-critic`'s findings hold). A model cannot climb the leaderboard by *claiming* it did well. And decorrelation is a hard constraint on the assignment (`test-critic` ≠ `test-writer`), so routing can never collapse the pool onto one model to farm a metric. Cold-start uses honest thin-data defaults, stated in the record — never fabricated confidence.

**Method mirrors product:** this system is itself built subagent-driven, with per-task and whole-branch adversarial review and a signed, honest gate — the same discipline it certifies.

## Data flow (one run)
```
start_adversarial_run(RunSpec{code + DEV's tests + testCmd})
  → driver: StaffingManager assigns role→model off the leaderboard (decorrelation: test-writer ≠ test-critic);
            repoindex signatures; render mutant-gen + test-critic prompts;
            Enqueue {mutant-generator(B), test-critic(C)} (mission=run) — each task STAMPED with its model
  → worker(mutant-generator, B): claim → run B → produce mutants → complete_task(result=mutants) → brain schema-parses
  → worker(test-critic, C):      claim → run C → read the DEV's tests → report_finding(vacuous/designed-to-pass) → complete_task
  → driver tick: mutants done → adequacy.Score(DEV's tests, mutants, testCmd) in jail → dev_kill_rate + survivors
                 → promote test-writer(A≠C) with the SURVIVORS in its instruction
  → worker(test-writer, A):      claim → run A → write a test that kills the survivors → complete_task(result=test) → brain compile-verifies
  → driver tick: writer done → adequacy.Score(pool test, survivors) in jail → proven_missed
                 → aggregate {dev_kill_rate, survivors, proven_missed, vacuous_findings, models_by_role}
                 → human gate (needs-review if blocking finding / dev_kill_rate below threshold) → sign → run done
  → leaderboard records each (model, role, CERTIFIED-outcome)  → sharpens the NEXT run's routing
```

## Non-goals (this slice — explicit)
- **Pentester** role (freeform exploit loop → findings) — slice 2.
- **Model-scoped claiming and worker-spawning-by-staffing** — NOT needed: dynamic routing is achieved by stamping the assigned model on each task + a multi-model worker backend. (Dynamic gate-earned routing itself is now IN slice 1.)
- **Concurrent runs** — one active run; the single-active constraint holds. Slice 3.
- **`corral certify --adversarial` / gate-poller trigger** — slice 3. Slice 1 triggers via the admin MCP tool.
- **Multi-target runs** (a whole diff → many targets) — slice 1 is one target per run; fan-out later.
- No new metaphor/rename; no changes to the merge gate or the shipped `corral certify` standalone path.

## Open decisions / risks
- **Testgen prompt seam:** splitting prompt-render from the `Ask` call must not change the prompts (the proven pipeline). The plan pins this as a pure refactor with a golden-prompt test.
- **Worker structured-result contract:** how the worker returns a compile-clean test vs. the brain refusing it — reuse the verify-gate refusal loop (`ok:false` → worker retries with the failure in its next instruction, `refusalCap`).
- **Adequacy needs the code + test + mutants co-located in a jail workspace** (`adequacy.Score` takes `codePath`/`code`/`mutants`/`testCmd`) — the driver assembles the workspace; the shared isolator runs it. No new jail.
- **Threading staffing/leaderboard into the driver:** `staffingMgr`/`perfTracker` live in `main()` closures, not `brain.Options` — add the minimal field(s) to `Options` (the one wiring gap the map flagged), or construct the tracker in the subsystem as `main.go` does. Dynamic routing makes this mandatory (the driver must call `StaffingManager`).
- **Multi-model worker backend (assumption for dynamic routing):** a worker must be able to run the task's *assigned* model, not a fixed one — i.e. its LLM backend is a gateway serving multiple models (local Ollama with several pulled models, or an OpenAI-compatible router). Single-model frontier-CLI harnesses (one worker = one model) can still participate, but only for the role/model they are, and the driver must fall back to role-based routing for them. The plan pins the per-task model plumbing: `TaskSpec.Model` → the worker passes it to `backend.Chat` instead of `AGENT_MODEL`.
- **Routing needs the assignment to be recorded in the signed verdict** (`models_by_role`) so the record is self-explaining and the routing choice is auditable.
- **Global claiming vs run-scope:** with one active run, global role-claiming is fine (only one run's tasks exist). Concurrent runs (slice 3) will need run-scoped claiming or model/run task tags.
- **Trust boundary:** workers are semi-trusted; their artifacts are always **validated** (compile/parse) and their code runs **only in the jail** — a worker cannot make the brain sign a verdict without the deterministic adequacy run and the human gate. Never trust a worker's self-reported kill-rate; the brain computes it.

## Testing posture
- Unit: the testgen prompt seam (golden prompts unchanged); the driver's DAG enqueue (assert the task shape/deps); the tick state machine (fakes for queue completions → assert dev-tests adequacy runs when mutants done, `test-writer` promoted with survivors, pool-adequacy runs, verdict aggregated, sign called); the structured-result validation (a non-compiling test is refused; malformed mutants rejected); a low dev kill-rate → `needs-review`.
- Integration (hermetic, fake workers): drive a full run with in-test fake workers completing each role's task with canned artifacts → assert a signed verdict is produced with the right kill-rate and that `corral certify verify` accepts it; a blocking finding routes to `needs-review`.
- Keep the shipped control-gate + `corral certify` + queue/coord tests green; the pool is additive and off by default.
