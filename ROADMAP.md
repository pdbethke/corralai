<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Corralai Roadmap

> **Directional, not committed.** Solo-maintained and moving fast (v0.1). This is
> where corral is heading and *why*. No dates — priorities shift with what real use
> surfaces.

Corral's arc is from a **certify CLI + a merge gate** to a full **accountability
plane for AI-written code**: the place a team proves — by execution, not opinion —
that a change is fit, and holds a signed, queryable record of what actually ran. The
foundation is in place (certify-by-execution, the jail + human gate, the attributed
ledger, the learning loop, durable replay, embedded DuckDB, MCP). Most of what's
ahead is **surfacing and federating** that foundation, not re-architecting it.

A guiding invariant runs through all of it: **the brain's API is the boundary —
writes go through it, reads share the analytical store read-only, every kind of
sharing lends the *capability* and holds the *credential*, and the core stays one
Go binary.**

## Shipped

**Certify by execution.**
- **`corral certify -- <check>`** — one line in a pipeline runs the check *itself*
  and mints a signed, tamper-evident, **independently-verifiable** record of what ran
  (a real exit code, not a self-report), in the in-toto/SLSA provenance format, with
  the models that produced the change carried as provenance materials. Stored in
  DuckDB (the same schema federates to MotherDuck by swapping the DSN). `corral certify
  verify` checks a record against the brain's **published** key, never one embedded in
  the record; the verify path refuses without an external trust anchor. Central-trust
  v1 (the trustless tier is below). Adversarially reviewed — the review caught and
  closed a silent-pass and a circular-trust-anchor bug before merge.
- **`corral certify --local` — the adversarial testing pool in one command.** Given a
  code file and the developer's own tests, it **mutates the code** (seeded
  goal-violations), runs the **developer's own tests** against those mutants in the
  jail — the kill-rate is the suite's adequacy grade, never a self-report — then a
  **test-writer** authors a test targeting whatever survived (a survivor stays
  *disclosed, unadjudicated* until a compiling test actually kills it — corral never
  calls an unproven survivor a real bug), **feeding the compiler's own error back on a
  non-compiling attempt so it corrects rather than blindly repeats**, and a decorrelated
  **test-critic** flags vacuous/designed-to-pass tests as **unverified advice that never
  gates**. A converged run (certified or needs-review)
  is signed via the same certify chain; a low kill-rate or a blocking finding always
  routes to needs-review, never auto-certified.
- **The swarm.** `--local` runs its role tasks through a **bounded concurrent worker
  pool** (`--swarm N`), and the mutant-generator is **sharded** — the file's functions
  are bin-packed (complexity-balanced, deterministic) into up to `--max-shards`
  (default 8) generator seats, so every function is probed, with per-shard retry/drop
  and the coverage shortfall carried into the **signed** verdict. A **shadow
  challenger** (`--shadow-model`, on by default) attacks every shard a second time for
  a region-controlled, execution-proven head-to-head between generator models,
  recorded to the scorecard — **it never feeds the kill-rate**. Wired for the hosted
  brain too (`start_adversarial_run`, `max_shards` ceilinged at 20, `shadow_model`
  daemon-defaulted via `CORRALAI_ADVPOOL_SHADOW_MODEL`); the daemon widens its run
  deadline when a shadow model is set, so shadow work can never force a timeout
  verdict. **The tests×mutants matrix** (`--matrix`, opt-in — T×M extra jail runs, so
  off by default): after the primary pass, every dev test is re-scored ALONE against
  the run's mutants for a per-test adequacy readout and an opinion-free "safe to
  delete" candidate list (a test that caught none of the planted mutants — relative to
  THIS run's mutant set, not proof the test is dead weight). `corral matrix list
  [--json]` reads it off the brain; persisted to DuckDB (`internal/matrixstore`),
  go + python only today. Wired for the hosted brain too (`start_adversarial_run`'s
  `matrix` param).
- **Dependency dirs bound, not copied (`--repo-dir`).** `node_modules`, `vendor`,
  `.venv`, `venv`, and `.bundle` are auto-detected and mounted **read-only** into the
  jail instead of copied into the workspace seed — they must already be present
  (vendored, the same way CI expects them); corral binds, it never installs.
  `--bind-dir <path>` (repeatable) covers dirs the auto-detected set misses;
  `--no-bind-deps` restores the copy-everything behavior. Only backends that can
  RELOCATE a dir into the jail workspace can bind: `bwrap` binds cleanly (no size
  limit); `--jail container` binds only world-readable dep dirs and copies the rest
  (subject to the size cap) — an honest, loud fallback, not silent. macOS
  `sandbox-exec` has no relocate primitive (it only grants read at a dir's original
  host path), so it always copies dep dirs into the seed, same as `--no-bind-deps`.
  Local-only today; the brain-gate path (hosted `start_adversarial_run`) is the
  remaining follow-up.
- **Multi-language** — Go, Python (pytest), Ruby (minitest/RSpec), JavaScript
  (node:test), TypeScript (tsc + node:test), the language inferred from the code
  path's extension, fail-closed on an unknown language or a failed preflight. C is next.
- **The bug-catching scorecard + eval harness** — which model actually *catches* bugs,
  execution-proven (recall from `ProvenMissed`, never a self-report), DuckDB-native;
  the eval harness runs the pool over a versioned known-adequacy corpus to give the
  scorecard volume and a soundness report (it says *do not publish* if the metric is
  miscalibrated).
- **Critic-accuracy metrics.** The test-critic's own findings are scored, the same
  execution-proven way: a whole-test finding is auto-refuted when the target test
  still kills its own seeded mutant (the finding was wrong), recorded to a mutable
  per-finding store (`internal/criticscore`) that a **human adjudication always wins**
  and can never be clawed back by a later auto pass. `list_pending_critic_findings` /
  `get_critic_finding` / `adjudicate_critic_finding` are admin-gated MCP tools; `corral
  criticscore list|show <id>|confirm <id>|refute <id>` is the CLI over the same brain
  API. `corral scorecard`'s **C-PREC** column shows the resulting per-model precision
  (confirmed/(confirmed+refuted)), marked provisional under a thin adjudicated sample
  even when the model has plenty of runs. Brain-path only, like the rest of the
  scorecard: `--local` shows the auto verdict on the run's tape but persists nothing.

**The gate — accountability, enforced.**
- **The repo (merge) gate — control-plane v1.** `corral certify`, *inverted*: the
  **brain** polls covered repos' open PRs and, on each new head commit, checks it out,
  runs the repo's declared check **in the jail** (untrusted PR code), signs the result,
  and posts `corral/gate = pass|fail` — a status branch protection **requires**, so a
  red or missing verdict blocks the merge. Fail-closed (a `success` is only ever posted
  on a real exit-0), and the gated SHA is provably the merged SHA. v1 is GitHub +
  opt-in (`CORRALAI_GATE_POLICIES`).
- **The control gate.** The merge gate runs the *repo's own* check; the control gate
  runs the **control owner's independently-vetted tests** against each PR head — the
  person accountable for code they didn't write sets the bar, and the author can't
  grade their own homework. A distinct required `corral/control-gate` check,
  fail-closed. The owner's **authoring/vetting loop** is wired too — seven admin-gated,
  audited MCP tools (`import_control_bundle` ASVS→goals, `stage_control` which authors a
  test and scores its mutation-adequacy, `list_pending_controls`/`get_control`, and
  `promote_control` — the **recorded, attributed human approval**). A candidate is
  always stored unvetted; only a human admin's `promote_control` vets it into the store
  the gate runs. It maps corral onto the recognized GRC **owner → operator → assessor**
  separation of duties: *a judge may not certify herself.*

**The substrate.**
- **Multi-model, multi-forge; the `bwrap` + container jail; the attributed action
  ledger** — every consequential action recorded and attributed to a verified
  principal; the subject of the record doesn't control the ledger.
- **The learning loop** (findings → human-approved skills) and **shared memory +
  skills** (fleet-synced), all behind the human gate — a worker proposes, only a
  superuser publishes.
- **Model×role telemetry / the gate-earned leaderboard** — each model's per-role
  performance (sample-weighted, honest about thin data), the data layer routing reads
  from.
- **Portable credential keystore** — provider keys + the worker token, an embedded
  resolver (env → OS keyring → age-encrypted file) with a `corral secret` CLI; secrets
  never touch argv, logs, or plaintext-at-rest; the age identity fails closed.
  Security-reviewed adversarially before shipping.
- **Headless daemon + thin, release-signed rendering clients** — the brain emits no
  UI; it hosts the console as a versioned `/console` bundle each client
  (`corral-admin`, read-only `corral-observe`, `corral-desktop`) fetches, **verifies
  against a pinned key** before rendering, and reverse-proxies only `/api|/events|/mcp`
  with a bearer that **never crosses to the browser**. Guarded by a loopback host-gate,
  a same-origin check, and a `SameSite=Strict` session cookie — adversarially reviewed
  end-to-end.
- **Durable replay + the story engine** — every run's rows survive indefinitely and
  replay on the corral canvas at up to 16×; with reasoning capture on, the replay
  streams each model's *real, captured reasoning* alongside its commands, so you watch
  the herd **think**, not just move.
- **Fleet analytics to MotherDuck.**
- **The accountability warehouse, in the browser** — a public page
  (`corralai.dev/warehouse`, "DuckDB integration" in the nav) runs DuckDB itself,
  compiled to **WebAssembly, client-side in the browser**, over corral's *real* audit
  data: two self-hosted parquet extracts — the signed hash-linked verdict ledger
  (`audit_ledger`) and per-model×role execution telemetry (`bug_catches`) — with preset
  queries, a live query box, and `?q=<sql>` deep-links (a claim is one click from the
  SQL that grounds it). No backend to query; the same schema federates to MotherDuck
  for streaming-live.

## Ahead — the accountability plane

`corral certify` is the first brick of a second arc: corral not only *runs* the audit,
it becomes the way a team **accounts** for what its agents — the herd's, or a dev's
own — actually produced. The engine that already contains, certifies, and records is
exactly the engine that can *attest* and *federate*.

- **The trustless tier — witness anchoring.** Ship the ledger head to an external,
  append-only, timestamped witness (a shared MotherDuck warehouse, or Sigstore
  **Rekor**) so tampering is detectable *even if you don't trust the brain*.
  Central-trust today becomes tamper-evident-against-everyone next. (Honest all the
  way: evident, never "proof" — you can't make a party's own machine tamper-proof,
  only detectable.)
- **One-command agent onboarding.** Independent, dev-driven agents (starting with
  **Claude Code**, richest hooks) join with `corral hooks install` — deterministic
  passive telemetry to the brain via the agent's own hooks, no behavior change, no
  reliance on the model *choosing* to report. Capture is deterministic — the same
  principle as certify-by-execution: don't trust the agent's word, capture it.
- **The MotherDuck accountability warehouse.** The public browser-DuckDB page
  (Shipped, above) proves the model on one project's data; the next step federates
  signed records from every dev, CI runner, and project into one shared, queryable
  warehouse — the *same* DuckDB schema, a DSN flip. Read-only **shares** hand a client,
  an auditor, or a conference room a live, verifiable slice with zero infra. The
  firehose stays local and cheap; only signed summaries federate.
- **The shared corpus — the moat.** A blind-spot pattern proven once ("length-only
  validators miss the character-class rules") becomes a shared, versioned, **signed**
  skill every client's audit can pull — so a first-time client audits with the whole
  network's execution-proven experience, not from zero. Value prop: *corral makes your
  local tests stronger by pulling from a shared corpus of verified, signed findings.*
  Patterns, never code; execution-proven, human-gated, attributable — a data flywheel
  made of facts, not opaque weights.
- **The swarm's remaining slices** — the resource-aware optimizer (size the fan-out
  from execution-proven yield × host resources) and per-region/per-complexity-band
  model effectiveness (the tests × mutants matrix itself SHIPPED — see above). See
  [`docs/superpowers/specs/2026-07-19-swarm-slice-2-sharded-generation-design.md`](docs/superpowers/specs/2026-07-19-swarm-slice-2-sharded-generation-design.md).

## Ahead — operate the gate at scale

The reviewer's seat moves from *author* to *assessor*: set the model mix, watch the
board, approve the merges.
- **Model management.** Assign models to audit roles from a settings panel; a registry
  with empirical per-role performance — a leaderboard your herd *earns*; role→hardware
  scheduling.
- **Cross-model evaluation.** Controlled per-role benchmarks — accuracy (the verify
  gate as ground-truth oracle), speed, and cost.
- **A review cockpit.** Verdicts and findings docked beside the diff — a VS Code panel
  and a read-only live view, never reaching into the jail.
- **Shared reach.** Register any service (yours, wrapped as MCP) with the gateway; the
  herd then shares *knowledge, behavior, context, **and reach*** — one uniform pattern,
  access shared, credential held brain-side.

## Ahead — ready for teams
- **Cost governance.** Per-audit / role / model cost, budget caps, pre-flight
  estimates.
- **Concurrency & multi-tenancy.** Many audits at once — scheduled, isolated, fair.
- **Memory hygiene.** The shared corpus stays *fresh*, not merely growing.

## The through-line
Every capability above is the **same pattern** — brain-mediated, human-gated,
attributed, *share the capability and hold the credential* — and increasingly just a
query over one attributed ledger. The moat isn't any single UI; it's the data model
underneath all of them. Competitors can clone a dashboard; they can't retroactively
have recorded years of attributed, execution-proven, multi-model audit runs.

---
*Want to shape this? Issues and verified-harness PRs are welcome — see the
[README](README.md) and [CONTRIBUTING](CONTRIBUTING.md).*
