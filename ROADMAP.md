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
  state; filter the console per agent. Watch the herd **think**, not just move.
- **Egress scan** — the herd's output is vetted before it ships (committed secrets
  are *blocking*; new/vulnerable deps and license conflicts are advisory), on any
  forge — containment on the way *out*, not just the way in.
- **Complexity-aware planning** + **multi-role workers** — the plan scales to the
  task (no nine-role ceremony for a one-liner), and a small herd covers every role
  without deadlocking.
- **Model×role telemetry** — the brain computes each model's per-role performance
  (sample-weighted, honest about thin data) and infers per-agent health from the
  attributed ledger — the data layer the leaderboard and self-staffing read from.

## Now — make it operable and unbreakable
- **The front door.** A first-class *"what should the herd build?"* composer, with
  an optional brain-led clarify step (→ a crisp directive with explicit acceptance
  criteria) and a pre-flight feasibility check (roles the plan needs vs. workers
  available). Specifying well is also a reliability fix.
- **Reliability hardening.** Graceful degradation over deadlock — role-staffing
  gaps, refusal storms, and stale claims must never strand a mission.
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

## The through-line
Every capability above is the **same pattern** — brain-mediated, human-gated,
attributed, *share the capability and hold the credential* — and increasingly just
a query over one attributed ledger. The moat isn't any single UI; it's the data
model underneath all of them. Competitors can clone a dashboard; they can't
retroactively have recorded years of attributed, multi-model runs.

---
*Want to shape this? Issues and verified-harness PRs are welcome — see the
[README](README.md) and [CONTRIBUTING](CONTRIBUTING.md).*
