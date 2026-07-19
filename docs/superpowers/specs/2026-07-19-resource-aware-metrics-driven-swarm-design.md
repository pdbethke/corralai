# Resource-aware, metrics-driven adversarial swarm — design

**Status:** design (for review before build)
**Date:** 2026-07-19
**Author:** corral team

## The idea, in one line

Turn the fixed 3-role adversarial pool into a **bounded swarm** whose width is
set by *execution-proven yield metrics* (which roles/models actually catch bugs,
and how fast) and *clamped to what the host can actually run* (cores + RAM),
using the probe→clamp machinery that already exists.

This is the concrete landing of two standing theses: **continuous
re-evaluation** ([[corralai-continuous-reevaluation-differentiator]]) and the
**self-orchestrating brain** ([[corralai-self-orchestrating-brain]]) —
metrics-driven, not static config; resource-aware, not a hardcoded count.

## What already exists (this is wiring, not greenfield)

- **Resource probe:** `mission.StaffingManager.Sense()` returns
  `WorkstationResources{CPUCores, TotalRAMGB, GPUVRAMGB}` (routing.go), inside a
  `Sense→Judge→Clamp` staffing loop. The probe and the clamp pattern are built.
- **Execution-proven yield:** `bugcatch_observations` (DuckDB) — one row per
  `(run × model × role)` with catches/opportunities/recall, fed ONLY from
  execution-proven `ProvenMissed` (never a self-report).
- **Latency substrate:** `telemetry.events` — timestamped, model-attributed;
  per-role latency = `task_done.ts − task_claimed.ts`. Not yet aggregated.
- **The pool driver + `--local`:** the DAG (mutant-generator → dev-adequacy →
  test-writer → pool-adequacy → aggregate → sign) and the `--record` tape.
- **Concurrent claiming:** `queue.Store` already hands out tasks to concurrent
  workers; `--local` just drives one worker sequentially today.

The gaps: (1) the DAG is a *fixed 3 roles* (agent count doesn't scale with the
code); (2) `--local` runs one worker; (3) latency isn't aggregated; (4) nothing
sizes the swarm from resources × yield.

## The swarm shape (where the width comes from)

The natural, non-redundant fan-out (see the "trio per test" analysis — the trio
belongs to the *code*, not a test):

1. **Generation sharded by code region.** Use `repoindex` (tree-sitter
   signatures) to split the file by top-level symbol; one mutant-generator per
   shard, each attacking its own function. A 30-function file → up to 30
   independent generators (bounded — see budget). *Also a coverage win:* every
   function gets probed, not just whatever one generator picked.
2. **The scoring matrix (the big swarm).** Score is the parallel unit: for each
   `(test, mutant)` cell, run that test against that mutant in the jail —
   "does this test catch this bug?" `T tests × M mutants` = the massive,
   *meaningful* swarm, and it yields **per-test adequacy by execution** (which
   tests are load-bearing vs dead weight — the execution-proven version of
   "vacuous", no LLM opinion). This is also the execution-verification of the
   test-critic discussed in "the-code-is-the-code".
   **Killer product payoff — test-attrition detection.** Read the matrix down the
   per-test axis: a test that kills **zero** mutants is *provably* dead weight
   (the feature it covered was removed/refactored; it decayed into a passing
   no-op). On a 1,000-test suite this finds the ~hundred stale tests that are
   pure CI drag and false confidence — an execution-proven, opinion-free "safe to
   delete" list. "Stronger" is signal-per-test, not test count: the matrix both
   adds the missing killing test AND cuts the dead ones. This is a concrete,
   universally-felt ROI (faster CI + honest green) and it's the single strongest
   demo of why the swarm's per-test axis matters.
3. **Test-writer per survivor; critic per test cluster** — the remaining
   fan-out, gated behind the suite-level survivor set (a real barrier).

## The optimizer (resources × yield → bounded swarm)

```
                 Sense (cores, RAM, VRAM)          bugcatch yield + telemetry latency
                          │                                   │
                          ▼                                   ▼
   ceiling = clamp(cores − reserve, RAM / per_jail_run_RAM)   allocation weights
                          │                                   │
                          └──────────────┬────────────────────┘
                                         ▼
                       swarm size = min(desired_by_yield, ceiling, budget)
```

- **Ceiling (hard, from the probe):** the max number of *concurrent jailed runs*
  the box can sustain without thrashing. `max_concurrent = clamp(CPUCores −
  reserve, floor(AvailRAM / per_jail_run_RAM))`. `per_jail_run_RAM` is measured
  (or estimated per language) and refined from telemetry.
- **Allocation (soft, from metrics):** give more concurrency to roles/models
  with high **recall per second** (bugcatch recall ÷ telemetry latency); cap the
  slow/low-yield ones. Best-fit off the gate-earned scorecard, exactly the
  leaderboard-driven routing already used for role→model.
- **Budget (the operator clamp — "limit the swarms"):** `--swarm N` /
  `CORRALAI_SWARM_BUDGET` caps the whole thing regardless, so a demo runs a
  visible-but-bounded swarm and a laptop never melts.
- **Adaptive (the differentiator):** each run writes its own perf (per-role
  latency, per-jail RAM high-water, yield) back to DuckDB, so the *next* run's
  sizing is refined. Continuous re-evaluation, closed loop.

## Honesty / invariants (must not regress)

- **Correctness is unchanged.** Kill-rate is still execution-proven in the jail;
  the swarm changes *how many* jailed runs happen in parallel and *which roles
  get workers*, never *what counts as killed*. A signed verdict asserts only
  execution, exactly as today (see the-code-is-the-code).
- **Fail-closed stays fail-closed.** A resource probe failure clamps to a safe
  minimum (1 worker), never to "unbounded" or "unsandboxed".
- **Bounded by default.** No swarm exceeds the probe ceiling or the budget; the
  1000-agent backstop and the jail are the hard floors.
- **Single-box truth:** on one host the *displayable* swarm can exceed the
  *runnable* one; true massive parallelism = the fleet (multi-host claiming off
  the shared queue), which the brain already coordinates. Don't claim 1,000
  concurrent jailed runs on one laptop.

## Incremental build slices (each shippable + testable)

1. **Concurrent bounded `--local` workers.** A pool of N goroutines draining the
   queue; `--swarm N` flag; N from `min(Sense ceiling, budget)`. Foundation +
   the first visible parallelism. (Small, self-contained.)
2. **Sharded generation.** One mutant-generator per `repoindex` symbol, bounded
   by budget; aggregate mutants into the dev-adequacy set. First real swarm +
   coverage win. (Driver DAG change — the careful one.)
3. **Latency aggregation.** Roll telemetry ts-deltas into a per-`(role,model)`
   latency view (mirror bugcatch); expose `recall-per-second`.
4. **The optimizer.** Wire Sense-ceiling × yield-allocation × budget into the
   swarm sizer; write per-run perf back to DuckDB (the closed loop).
5. **The scoring matrix (tests × mutants).** Per-test adequacy by execution +
   the execution-verification of the critic. The biggest swarm + the strongest
   metric. (Largest slice; do last, on the proven foundation.)

## Relationship to Sakana Fugu (re-read 2026-07-19)

The swarm is corral's answer to the same problem Fugu solves — *adaptive,
route-to-the-fittest orchestration of a worker pool* — but with inverted
epistemics. Four things re-reading the Fugu report (docs/corral/sakana-fugu-analysis.md)
adds to this design:

1. **Route-to-fittest, PROVEN not learned — the core contrast.** Fugu learns
   routing (SVFT + CMA-ES + GRPO over a softmax of worker *reward* metrics) into
   an opaque hidden-state prediction head. Our "allocation from yield" is the
   same route-to-fittest, but the signal is **execution-proven** (bugcatch
   recall from `ProvenMissed`, telemetry latency) and the policy is a
   **transparent deterministic clamp**, not a trained black box. Same goal;
   auditable vs. opaque; measured reality vs. a reward proxy. This is the
   defensible differentiator we already claim — the swarm is where it becomes
   load-bearing.

2. **Keep the orchestration decision CHEAP and non-generative — Fugu's core
   efficiency lesson.** Fugu's whole speed story is *deciding without
   autoregressive decoding*. Our swarm sizing must likewise be a deterministic
   `clamp(Sense, DuckDB-yield, budget)` computation, **never an LLM call**. This
   validates keeping the optimizer out of the model's mouth entirely — the brain
   sizes the swarm by arithmetic over measured metrics, not by generating a plan.

3. **Our fan-out avoids "orchestration collapse" BY CONSTRUCTION.** Fugu had to
   engineer access-list isolation so an early agent's actions don't bias every
   downstream agent. Our massive-parallel layer — per-region generators and
   per-`(test,mutant)` scoring cells — has **zero cross-agent context** by
   design: each cell is an independent jailed run. So the failure mode Fugu
   built machinery to prevent is *impossible* in the scoring matrix. (The genuine
   exceptions are the dependent steps — survivor aggregation and the
   test-writer — which sit behind an explicit barrier; those, and only those,
   need Fugu-style context discipline.)

4. **Extend allocation to per-shard model routing — Fugu's dynamic specialist
   insertion, execution-grounded.** Beyond "how many workers per role", the
   scorecard lets us pick "*which model* for *this shard*" (the model with the
   best proven recall on this kind of code), the honest version of Fugu routing
   math to GPT and trivia to Gemini. Off the gate-earned leaderboard, not a
   learned head.

Fugu's **persistent shared memory** maps to something we ALREADY BUILT and only
have to re-point: the `memory` (multi-tier vector/FTS, shared/`SetShared`) +
`learn` (proposals → lessons → versioned skills) human-gated learning loop. It
was aimed at the *builder* (lessons like "add jittered backoff on a retry"); the
retire-the-builder pivot left it intact but pointed at the wrong world. Re-aim it
at AUDIT and it becomes the swarm's persistent memory:

- **Proven mutation operators per code shape** — a growing library of
  goal-violating edits that actually survived good suites, so the
  mutant-generator gets sharper over runs (the audit analog of a build skill).
- **Proven test-blind-spot patterns** — "this shape of test provably misses this
  shape of bug", accumulated from execution, so the swarm knows where to aim.
- **Per-code-type model fitness** — `bugcatch` is already this: re-pointed
  learning, execution-proven.
- **Verdict cache / dedup keyed on `ParentSHA256`** — never re-audit unchanged
  code, never re-run an identical `(test,mutant)` cell (Fugu's
  redundant-tool-call avoidance, exact analog).

**Trust caveat (inherited from the-code-is-the-code):** the re-pointed loop must
carry the same rule — only **execution-proven** audit knowledge is promoted to
shared/skills, never an LLM's opinion. Otherwise we'd accumulate the critic's
hallucinations into "skills" and poison the swarm. The human gate + the
execution-proof requirement together keep the accumulated audit memory honest.
So: the swarm learns what actually catches bugs and where tests actually miss —
transparently, provably, across runs — instead of a Fugu-style learned reward
proxy. The infrastructure exists; the work is re-aiming and re-gating it.

### It's client-server — so the memory is SHARED to the clients (the real moat)

corral is a client-server system, and the memory-sharing + skills loop was built
to distribute knowledge across principals (`memory` shared/`SetShared`/
`sharedOnly`; `learn` versioned skills; `fleet` → MotherDuck federation of signed
records; the per-principal `gateway`). It helped devs BUILD. The same pipes carry
AUDIT knowledge to every dev running a client:

- A blind-spot pattern proven once ("length-only validators miss the character-
  class rules") becomes a shared, versioned skill every client's swarm can pull.
- Effective mutation operators + per-code-type model fitness accumulate centrally
  and flow outward — so a first-time client audits with the whole network's
  execution-proven experience, not from zero.
- The more devs audit, the sharper every dev's audit gets — a data flywheel, but
  made of **execution-proven, signed, human-gated, versioned** facts, not opaque
  weights. (Honesty guard: this is the DESIGN direction; the flywheel is not yet
  claimed proven — same guard as the "Nobody Fails a Test They Never Took" note.)

**Cross-principal caveats (the new bar):** shared audit memory must carry
**patterns, never code** — a blind-spot shape transfers; Org A's source must not
leak to Org B. This raises the scrub bar above the single-org recording scrub:
promotion to the shared tier must strip repo-specific identifiers, and every
shared skill must stay **signed + attributable** (provenance), so a wrong or
poisoned skill is traceable and revocable, and only **execution-proven** lessons
are ever promoted. The human gate + the proof requirement + attribution are what
keep a shared commons from becoming a shared liability.

This is the founder's gold-seal vision made concrete ([[corralai-eval-harness]]):
an independent, open, self-verifying accountability commons for AI code — CT /
Sigstore for *test adequacy* — where contributing proven audit knowledge and
drawing on it are the same act. Fugu bakes learning into one opaque orchestrator's
weights; corral federates a transparent, execution-proven, human-gated knowledge
commons to every client. Same instinct; opposite trust model — and the moat.

## Open questions for review

- Per-jail-run RAM: measure live (cgroup/rusage) or estimate per language first?
- Slice 2's DAG change: extend the current driver, or a new "sharded" run mode
  behind a flag so the single-file path stays byte-identical?
- Do we size the swarm once (at start, from Sense) or re-clamp mid-run as RAM
  moves?
