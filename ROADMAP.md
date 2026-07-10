<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Corralai Roadmap

> **Directional, not committed.** Solo-maintained and moving fast (v0.1). This is
> where corral is heading and *why*; the reasoning in depth lives in
> [`docs/superpowers/notes/2026-07-05-corral-as-an-operator-system.md`](docs/superpowers/notes/2026-07-05-corral-as-an-operator-system.md).
> No dates — priorities shift with what real use surfaces.

Corral's arc is from *impressive demo* to *usable system* — the environment where
you **run a team of AI agents across any model**. The foundations are already in
place (per-action attribution, the jail + human gate, the learning loop, durable
replay, embedded DuckDB analytics, MCP, jail→repo). Most of what's ahead is
**surfacing** that foundation, not re-architecting it — which is the best place to
be standing at the start of the hard part.

A guiding invariant runs through all of it: **the brain's API is the boundary —
writes go through it, reads share the analytical store read-only, every kind of
sharing lends the *capability* and holds the *credential*, and the core stays one
Go binary.**

## Shipped
- The adaptive loop: a directive → a mission → the herd builds → verifies against
  a deterministic gate → re-plans on findings → converges
- The learning loop (findings → human-approved skills) behind the human gate
- Multi-model, multi-forge; the `bwrap` jail; the attributed action ledger
- Mission history + read-only replay; the corralai.dev site + recordings gallery
- Shared memory, skills, and hooks (fleet-synced); OIDC identity
- Fleet analytics to MotherDuck
- **The story engine** — the replay streams the agents' *real, captured reasoning*
  alongside their commands; click any agent mid-scrub to inspect its reconstructed
  state; **click any task** for its causal chain (what triggered it, what it
  unblocked, the commands that ran under it); a **file-tree lens** reconstructs the
  paths the herd touched, filling in as the tape plays; and **one scrub bar drives
  the whole cockpit** — canvas, progress, and files — through the same moment in
  time. Filter the console per agent. Watch the herd **think**, not just move.
- **Egress scan** — the herd's output is vetted before it ships (committed secrets
  are *blocking*; new/vulnerable deps and license conflicts are advisory), on any
  forge — containment on the way *out*, not just the way in.
- **Complexity-aware planning** + **multi-role workers** — the plan scales to the
  task (no nine-role ceremony for a one-liner), and a small herd covers every role
  without deadlocking.
- **Model×role telemetry** — the brain computes each model's per-role performance
  (sample-weighted, honest about thin data) and infers per-agent health from the
  attributed ledger — the data layer the leaderboard and self-staffing read from.
- **Graceful degradation over deadlock** — no mission strands or falsely
  certifies: the verify gate certifies by *execution* (not worker self-report) at
  every task and re-checks the final tree; orphaned/blocked-dep tasks are swept
  and dep keys validated at the source; a no-progress backstop and reflex-cap fail
  a non-converging mission instead of hanging; stale claims are reaped, a
  force-reclaimed worker backs off (self-heal, not quarantine), and generalist /
  multi-role workers no longer trip false stall alarms; a drained queue that still
  holds an open critical/high finding routes to a **needs-review** human gate
  rather than shipping a known defect.
- **Per-mission herd composer.** A visual **Mission Composer** builds each mission's
  team before launch: drag a model/agent onto each role, pick the **MCP endpoints**
  the herd may consume, and attach **lookbook** design directives. The choice is
  stored *per mission* (role→agent map + endpoints + lookbook) and injected into the
  herd's instructions — so a mission carries its own team, not a global default.
  (v1 runs one mission at a time; concurrent, differently-composed missions are next.)
- **Portable credential keystore.** Provider keys + the worker token, configured once
  and stored securely, cross-platform: an embedded resolver (env → OS keyring →
  age-encrypted file) with a `corral secret` CLI — the GCP-ADC pattern in the one
  binary. Secrets never touch argv, logs, or plaintext-at-rest; the age identity
  fails closed. Security-reviewed adversarially before shipping.
- **`corral certify` — the accountability wedge.** One line in a pipeline
  (`corral certify -- <check>`) runs the check *itself* and mints a signed,
  tamper-evident, **independently-verifiable** record of what ran — certify **by
  execution** (a real exit code, not a self-report), in the in-toto/SLSA provenance
  format, with the models that produced the change carried as provenance materials.
  Stored in DuckDB (the same schema federates to MotherDuck by swapping the DSN).
  Ships with `corral certify verify` — verify a record against the brain's **published**
  key (`GET /api/certify/pubkey`), never a key embedded in the record itself. The
  signing key lives only in the brain; the verify path refuses without an external
  trust anchor. Central-trust v1 (a holder of the published key can confirm the brain's
  records honestly bind what ran); the trustless tier is next. Adversarially reviewed —
  the review caught and closed a silent-pass and a circular-trust-anchor bug before merge.

## Now — make it operable and unbreakable
- **The front door.** The Mission Composer (above) is the first cut. Still ahead: an
  optional brain-led clarify step (→ a crisp directive with explicit acceptance
  criteria) and a pre-flight feasibility check (roles the plan needs vs. workers
  available). Specifying well is also a reliability fix.
- **Resumability.** A mission survives a crash or quota blip and resumes from
  durable state instead of starting over.
- **Dogfooding.** Use corral to build corral — the forcing function for all of it.

## The operator surface — the IDE moves up a level
The developer's seat moves from *author* to *conductor*: write directives, set the
model mix, watch the board, approve the merges.
- **Model management.** Assign models to roles from a settings panel; a model
  registry with empirical per-role performance — a leaderboard your herd *earns*;
  role→hardware scheduling.
- **Cross-model evaluation.** Controlled per-role benchmarks — accuracy (the verify
  gate as ground-truth oracle), speed, and cost.
- **A VS Code cockpit.** Mission control docked beside the diff; browse the herd's
  output through git/PR and a read-only live view — never reaching into the jail.
- **Shared reach.** Register any service (yours, wrapped as MCP) with the gateway.
  The herd then shares *knowledge, behavior, context, **and reach*** — one uniform
  pattern, access shared, credential held brain-side.

## Ready for teams
- **Cost governance.** Per-mission / role / model cost, budget caps, pre-flight
  estimates.
- **Concurrency & multi-tenancy.** Many missions at once — scheduled, isolated,
  fair.
- **Mid-mission steering.** Pause, redirect, or re-scope a running mission — not
  just approve/reject at the ends.
- **Memory hygiene.** The shared corpus stays *fresh*, not merely growing.

## Accountability — prove what any agent ships
`corral certify` (shipped, above) is the first brick of a second arc: corral not only
*runs* a herd, it becomes the way a team **accounts** for what its agents — the herd's,
or a dev's own — actually produced. The engine that already contains, certifies, and
records is exactly the engine that can *attest*.
- **The trustless tier — witness anchoring.** Ship the ledger head to an external,
  append-only, timestamped witness (a shared MotherDuck warehouse, or Sigstore **Rekor**)
  so tampering is detectable *even if you don't trust the brain*. Central-trust today
  becomes tamper-evident-against-everyone next. (Honest all the way: evident, never
  "proof" — you can't make a party's own machine tamper-proof, only detectable.)
- **One-command agent onboarding.** Independent, dev-driven agents (starting with
  **Claude Code**, richest hooks) join with `corral hooks install` — deterministic passive
  telemetry to the brain via the agent's own hooks, no behavior change, no reliance on the
  model *choosing* to report. Capture is deterministic — the same principle as certify-by-
  execution: don't trust the agent's word, capture it. The `corral certify` CLI is the
  agent-agnostic floor beneath it (it wraps the *check*, not the agent).
- **The MotherDuck accountability warehouse.** Signed records from every dev, CI runner,
  and project federate into one shared, queryable warehouse — the *same* DuckDB schema,
  a DSN flip. Read-only **shares** hand a client, an auditor, or a conference room a live,
  verifiable slice with zero infra. The firehose stays local and cheap; only signed
  summaries federate.

## The through-line
Every capability above is the **same pattern** — brain-mediated, human-gated,
attributed, *share the capability and hold the credential* — and increasingly just
a query over one attributed ledger. The moat isn't any single UI; it's the data
model underneath all of them. Competitors can clone a dashboard; they can't
retroactively have recorded years of attributed, multi-model runs.

---
*Want to shape this? Issues and verified-harness PRs are welcome — see the
[README](README.md) and [CONTRIBUTING](CONTRIBUTING.md).*
