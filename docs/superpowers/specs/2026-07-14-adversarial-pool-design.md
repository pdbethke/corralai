<!-- SPDX-License-Identifier: Elastic-2.0 -->
# The adversarial testing pool — a brain-coordinated herd that certifies a change (design)

**Status:** design (2026-07-14). Precedes an implementation plan. Third slice of the audit re-focus (`docs/superpowers/specs/2026-07-13-corral-refocus-audit-not-builder-design.md`); slice 2 shipped the standalone `corral certify` CLI. This is the innovation exhibit: the distributed adversarial verification the field note describes.

## The vision (where this is going)

A code change arrives. The brain turns a **pool of role-separated worker agents** loose on it — a **test-writer** authors tests fitted to the change, a **mutant-generator** seeds violations, a **reviewer** (a *different* model) triages, a **pentester** hunts the hole nobody tested — each claiming role-typed tasks from the queue and running in its own jail. The brain proves the tests by **mutation** (adequacy kill-rate), aggregates the findings, routes each role to the **model that earned it** off the leaderboard, gates the verdict on a **human**, and emits a **signed** record. Certify-by-adversarial-adequacy, distributed across the herd.

This spec designs **sub-slice 1: the spine** — the driver + a hybrid 2.5-role DAG proven end-to-end distributed. Slices 2–3 add the pentester, dynamic gate-earned routing, concurrent runs, and the CLI trigger.

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
- **Freeform roles** (`reviewer` in this slice; `pentester` later): the worker runs its normal LLM+jail loop against an instruction and files **findings** (the existing untyped `Finding` contract). Judgment/exploration where autonomy helps.

This requires a **testgen seam**: expose prompt-render + output-parse separately from the LLM call, so a structured role reuses testgen's exact prompt as a task instruction and the brain parses/validates the worker's result. (REUSE-WITH-CHANGES to `internal/testgen`.)

**Flexibility principle — roles are data.** A role is defined by three things: a *prompt-render* (target → instruction), a *result contract* (structured-and-validated, or freeform-findings), and its *DAG deps*. New adversarial roles (edge-hunter, dependency-auditor, …) compose by adding a data entry — no new driver plumbing. This keeps the pool maximally flexible while the machinery stays fixed.

## Sub-slice 1 — scope

### The run
A **run** = one **target**: a unit of code under review + a goal + a test command. Shaped after the control-gate's `StageRequest`: `RunSpec{Repo, Commit, Goal, CodePath, Code, TestCmd, NMutants}`. A run is a queue **mission** (`mission_id` = the run id) — reusing all the queue's mission-scoped bookkeeping. **One active run at a time** (reuse the existing single-active constraint); concurrent runs are slice 3.

### The DAG (enqueued by the driver)
```
[test-writer]  (structured, model A)  ─┐
[mutant-gen]   (structured, model B)  ─┼─→  (brain) adequacy.Score ─→ [reviewer] (freeform, model C ≠ A) ─→ (brain) aggregate
                                        │        kill-rate + survivors      triage survivors → findings          → verdict {kill_rate, survivors, verdicts, findings}
                                        └────────────────────────────────────────────────────────────────────────→ human gate (promote/reject) → sign
```
- **Worker tasks**: `test-writer`, `mutant-generator`, `reviewer`. Distinct roles ⇒ distinct workers ⇒ **distinct models = decorrelation by construction** (writer ≠ reviewer). `reviewer` `DependsOn` the adequacy result.
- **Brain-side steps** (in the driver's tick, using the shared isolator): run `adequacy.Score` once `test-writer` + `mutant-generator` are `done`; **aggregate** once `reviewer` is `done`; **sign**.

### Gate-earned routing (this slice: record; slice 3: actively route)
The pool is a set of role-typed workers, each started with a model (`AGENT_ROLE`+`AGENT_MODEL`). The driver enqueues role tasks; whichever role-matching worker claims runs its model. Every completion feeds the leaderboard (`PerformanceTracker`) with that model's per-role result — so the **gate-earned fitness data accumulates from day one**. *Actively* selecting the best-earned model per role (the driver spawning/tagging by staffing decision) is deferred to slice 3; slice 1 achieves decorrelation structurally (distinct roles→distinct workers→distinct models) and records the signal.

### The driver: `StartAdversarialPool(ctx, brainOpts)`
Following `StartControlGate`'s shape. Enable via `CORRALAI_ADVERSARIAL_POOL=1` (off by default). Responsibilities:
1. Expose an admin-gated MCP tool `start_adversarial_run(RunSpec) → run_id` (the slice-1 trigger; `corral certify --adversarial` is slice 3).
2. On a new run: extract signatures (`repoindex`), render the structured prompts, `Enqueue` the DAG tasks (mission = run).
3. A tick loop (reusing the engine's promote/gate/backstop pattern, adapted): `PromoteReady` → when `test-writer`+`mutant-gen` done, run `adequacy.Score` in the jail, store the report, promote `reviewer` with the survivors in its instruction → when `reviewer` done, **aggregate** → **human gate** (open finding ≥ block-severity, or a low kill-rate, routes to `needs-review`; else auto or on `promote`) → **sign** the verdict via the certify chain → run `done`. A no-progress backstop fails a stalled run.
4. Validation seams: compile-verify the test-writer's artifact (lift `authoring.compileVerify`); schema-parse the mutant-generator's artifact.

### The worker changes (`cmd/corral-agent`)
- Structured roles produce a **typed result**: the worker recognizes a structured task (a flag/role) and returns the artifact as its `complete_task` result in the agreed shape (test source; mutants JSON) — validated brain-side, refused (existing verify-gate refusal loop) if it doesn't compile/parse.
- Freeform `reviewer` uses the existing loop + `report_finding`.
- Keep the worker generic: no role→behavior dispatch table beyond the structured-vs-freeform result contract + the instruction the driver renders.

### The signed verdict
Reuse the certify chain (`certify.BuildLedger`/`BuildAttestation`/`SignDSSE`, `buildstore`). The verdict statement's subject = the change (repo@commit); byproducts = `{kill_rate, mutants_total, survivors, review_verdicts, findings_summary, models_by_role}`. Verifiable offline with the existing `corral certify verify` path. This is where the pool's output becomes **evidence**.

## Soundness invariants — what keeps the "wow" defensible

The wow is the herd. The **soundness is the deterministic, no-trust gate the herd feeds.** These are load-bearing invariants a sharp reviewer will test, so they are stated first-class, not left implicit:

1. **The verdict is deterministic, not the LLM's word.** The kill-rate is computed by the **brain** re-running the test against the mutants *in a jail* (`adequacy.Score`); given the same test + mutants it is reproducible. LLM creativity *feeds* the gate; it is never *the* gate. A signed record is reproducible evidence, not "the model said pass."
2. **Never trust a worker.** Every worker artifact is **validated** brain-side (the test must compile; mutants must parse the `adequacy.Mutant` schema) and a worker's *self-reported* outcome is never taken as the result — the brain computes it. Worker code runs **only in the jail**. A lazy or hostile worker cannot forge a green.
3. **Human gate + signed, offline-verifiable record.** No auto-certify past a blocking finding or a below-threshold kill-rate; the verdict routes to `needs-review`. The record is signed and independently verifiable with the existing `corral certify verify` (no trust in the brain that produced it).
4. **Honest, scoped claims.** Slice 1 *records* the routing signal (it does not yet dynamically route); decorrelation is achieved *structurally* (distinct roles → distinct workers → distinct models); the record states exactly what ran and with which models. Nothing is labeled "autonomous" or "AI-certified."
5. **Earned complexity — why distributed, not in-process.** The pool is distributed for three *load-bearing* reasons, each recorded in the verdict so the choice is defensible, not decorative:
   - **Multi-model decorrelation** — different model *endpoints* per role (writer ≠ reviewer), which a single in-process client cannot provide; this is the "a judge may not certify herself" mechanism made real.
   - **Independent per-agent attribution** — each role's contribution is separately recorded, scored on the leaderboard, and named in the signed record (`models_by_role`).
   - **Horizontal scale** — roles and targets fan out across workers.
   If any of these three were false, the herd *would* be theater — so the design makes them central, and the demo leads with the deterministic spine, not the swarm animation.

**Method mirrors product:** this system is itself built subagent-driven, with per-task and whole-branch adversarial review and a signed, honest gate — the same discipline it certifies.

## Data flow (one run)
```
start_adversarial_run(RunSpec)
  → driver: repoindex signatures; render WriteTest+GenerateMutants prompts; Enqueue DAG (mission=run)
  → worker(test-writer,  model A): claim → produce test  → complete_task(result=test)     → brain compile-verifies
  → worker(mutant-gen,   model B): claim → produce mutants→ complete_task(result=mutants)  → brain schema-parses
  → driver tick: both done → adequacy.Score(test, mutants, testCmd) in jail → kill-rate + survivors
                 → promote reviewer with survivors in instruction
  → worker(reviewer, model C≠A): claim → triage → report_finding(...) → complete_task
  → driver tick: reviewer done → aggregate {kill_rate, survivors, verdicts, findings}
                 → human gate (needs-review if blocking finding / low kill-rate) → sign → run done
  → leaderboard records each (model, role, outcome)
```

## Non-goals (this slice — explicit)
- **Pentester** role (freeform exploit loop → findings) — slice 2.
- **Dynamic gate-earned routing** (driver actively assigns best-earned model per role; model-scoped claiming; worker spawning by staffing) — slice 3. Slice 1 records the signal and decorrelates structurally.
- **Concurrent runs** — one active run; the single-active constraint holds. Slice 3.
- **`corral certify --adversarial` / gate-poller trigger** — slice 3. Slice 1 triggers via the admin MCP tool.
- **Multi-target runs** (a whole diff → many targets) — slice 1 is one target per run; fan-out later.
- No new metaphor/rename; no changes to the merge gate or the shipped `corral certify` standalone path.

## Open decisions / risks
- **Testgen prompt seam:** splitting prompt-render from the `Ask` call must not change the prompts (the proven pipeline). The plan pins this as a pure refactor with a golden-prompt test.
- **Worker structured-result contract:** how the worker returns a compile-clean test vs. the brain refusing it — reuse the verify-gate refusal loop (`ok:false` → worker retries with the failure in its next instruction, `refusalCap`).
- **Adequacy needs the code + test + mutants co-located in a jail workspace** (`adequacy.Score` takes `codePath`/`code`/`mutants`/`testCmd`) — the driver assembles the workspace; the shared isolator runs it. No new jail.
- **Threading staffing/leaderboard into the driver:** `staffingMgr`/`perfTracker` live in `main()` closures, not `brain.Options` — add the minimal field(s) to `Options` (the one wiring gap the map flagged), or construct the tracker in the subsystem as `main.go` does.
- **Global claiming vs run-scope:** with one active run, global role-claiming is fine (only one run's tasks exist). Concurrent runs (slice 3) will need run-scoped claiming or model/run task tags.
- **Trust boundary:** workers are semi-trusted; their artifacts are always **validated** (compile/parse) and their code runs **only in the jail** — a worker cannot make the brain sign a verdict without the deterministic adequacy run and the human gate. Never trust a worker's self-reported kill-rate; the brain computes it.

## Testing posture
- Unit: the testgen prompt seam (golden prompts unchanged); the driver's DAG enqueue (assert the task shape/deps); the tick state machine (fakes for queue completions → assert adequacy runs at the right point, reviewer promoted with survivors, verdict aggregated, sign called); the structured-result validation (a non-compiling test is refused; malformed mutants rejected).
- Integration (hermetic, fake workers): drive a full run with in-test fake workers completing each role's task with canned artifacts → assert a signed verdict is produced with the right kill-rate and that `corral certify verify` accepts it; a blocking finding routes to `needs-review`.
- Keep the shipped control-gate + `corral certify` + queue/coord tests green; the pool is additive and off by default.
