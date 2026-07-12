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
- **Headless daemon + thin rendering clients.** The brain emits no UI. It hosts the
  console as a versioned, **release-signed** `/console` bundle; each client
  (`corral-admin`, read-only `corral-observe`, `corral-desktop`) fetches that bundle,
  **verifies its signature against a pinned corralai key** before rendering, caches it
  by version, serves it locally, and reverse-proxies only `/api|/events|/mcp` to the
  daemon with a bearer that **never crosses to the browser**. One UI, many purpose-built
  windows into one daemon. The proxy is guarded by a loopback host-gate (DNS-rebinding),
  a same-origin check, and a per-session `SameSite=Strict` secret cookie. Adversarially
  reviewed end-to-end — the whole-branch review caught that the real SPA's
  header-incapable transports (SSE/WebSocket) couldn't satisfy a header-based gate; the
  fix (the session cookie) was then verified in a live browser.
- **The repo gate — corral as a required, signed merge check (control-plane v1).** `corral
  certify`, *inverted*: instead of a developer voluntarily certifying in their own CI, the
  **brain** polls covered repos' open PRs and, on each new head commit, checks it out, runs
  the repo's declared check **in the bwrap jail** (untrusted PR code), signs the result via
  the same tamper-evident certify path, and posts `corral/gate = pass|fail` to that commit —
  a status the org's branch protection **requires**, so a red or missing verdict blocks the
  merge. Certify **by execution**, enforced: no self-report, fail-closed (a `success` is only
  ever posted on a real exit-0), and the gated SHA is provably the merged SHA. This is the
  **separation-of-duties control a CISO is already required to prove**, mechanized. v1 is
  GitHub + opt-in (`CORRALAI_GATE_POLICIES`; zero change to a brain that doesn't set it).
  Honest about scope: the **posture verifier** (proving branch protection can't be silently
  disabled), the **GitHub App check-run** (an un-forgeable green), and **self-hosted
  GitLab/Gitea** (the forge a bank actually runs) are the next cut, not this one.
- **The control gate — the flipside of agentic development is agentic testing.** The merge
  gate above runs the *repo's own* declared check; the control gate runs the **control
  owner's independently-vetted tests** against each PR head — the person accountable for code
  they didn't write (a CISO in a bank, an eng lead or QA/platform lead elsewhere) sets the
  bar, and the code author can't grade their own homework. Per open PR the brain checks out
  head, runs the owner's vetted tests in the bwrap jail, signs the verdict, and posts a
  **distinct `corral/control-gate` required check** — fail-closed (green only on a signed
  all-pass; a missing target, a failing control, or zero vetted controls → red). "Control
  owner" is the industry-standard GRC term (NIST 800-53 / ISO 27001 / SOC 2), and it maps
  corral onto the recognized **owner → operator → assessor** separation of duties: *a judge
  may not certify herself.* Wired v1 (`CORRALAI_CONTROL_GATE`, off by default), Go-only,
  **run+post** — the tests are seeded by an operator via `corral control seed` standing in for
  the owner. Next: the owner's authoring/vetting surface (goal → agent-drafted candidate →
  mutation-adequacy score → human approval), then the first live-gated PR — and only then the
  field note ships (don't advertise unbuilt).

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
