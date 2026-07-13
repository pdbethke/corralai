<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Corral re-focus — a true audit for software change (eliminate the building part)

**Status:** design / product re-focus (2026-07-13). Precedes an implementation plan.

## The tell

We could not answer "what is `create_mission` for?" without four plausible answers. That is the mish-mosh smell Bill named: corral read as **three half-products sharing a brain** — a builder (herd builds software from a directive), an accountability warehouse (DuckDB/MotherDuck records), and a control plane (gate PRs). Each is defensible alone; stacked with no spine they compete with each other for the product's identity. `create_mission` is the tell because it belongs to the *builder*, the most commodity of the three.

## The spine: one noun — accountability

Every component we built is already an **accountability instrument**; we had dis-integrated them into buckets:

- **swarm / role-based agents** → accountable verification (role-*separated*, so no one certifies their own work)
- **recordings / replay** → the tamper-evident, re-watchable **evidence** of exactly what was checked and how
- **`certify` / `certverify` / `transparency` / `attest`** → the **signed, hash-chained, independently-verifiable** record (verified against an *external* anchor, so even the org running corral can't forge it; optional Rekor witness)
- **scrub pipeline** → the record is **shareable** — deny-list + human-review manifest strips the org's secrets/paths/actors so it can go to an outside auditor
- **bwrap jail** → certify **by execution** (re-run the checks, don't trust a reported green)
- **control-gate poller + control-authoring/vetting loop** → the gate and the owner's vetted controls
- **MotherDuck warehouse** → the org-wide accountability aggregate
- **gate-earned routing + leaderboard** → trust that carries a receipt

Nothing here is mish-mosh. It was an **audit system** the whole time, wrapped around a builder and shown off as a replay demo.

### Identity (locked)

> **Corral is a true audit for software change.** The CISO's reactive, org-owned control point: swap the freeform "build me X" box for the org's change stream; turn an adversarial, role-separated agent swarm loose on every change — executing the owner's vetted controls *and* trying to break it; certify by execution what survives; emit a **signed, hash-chained, independently-verifiable, scrubbable** record — *shareable evidence, not a trust-me log* — and gate the merge on it.
>
> With a **developer plug-in layer** (shared standards, vetted controls, design conformance) that drives adoption and *enriches* the audit — every dev feature accountability-native, never a standalone dev product.
>
> Not an IDE. Not a builder. Not an orchestration/dev tool.

## Why this wins (and the builder doesn't)

- **We never win the builder race.** "Our herd builds it" is undifferentiated against Cursor/Devin/Copilot/Claude Code, who own distribution and models. Entering that race is losing it.
- **Reactive is the high ground, not a downgrade.** Being *after* the builders makes corral **downstream of and agnostic to all of them** — it sits at the one chokepoint the org controls (the merge). Let them fight over who *writes* the code; corral certifies whatever they produce.
- **The buyer is the control owner (CISO/compliance/audit), not the developer.** Budgeted, board-level, less-crowded market — and a tailwind that *strengthens every time the builders get better*: as AI floods machine-written code into every repo, "who wrote this and was it really checked?" becomes *nobody knows*, and a cryptographically true audit of an adversarial swarm's verdict is the one thing they can't get elsewhere and that SSDF/PCI/SOC2/FedRAMP actually want — **evidence, not attestation.**
- **Signed = a category difference.** Everyone has logs ("trust me, this happened"). A signed, hash-chained, independently-verifiable record is *evidence* ("verify the signatures"). Scrubbable makes it safe to hand outside the building.

## The one change: eliminate the building part

This is a re-**focus**, not a rewrite — we keep ~90% of what's built. **The building part is the only gravitational center**; it's what pulled the swarm toward "coders," memory toward "build context," recordings toward "demo," the whole thing toward "IDE." Remove it and every remaining component relaxes into accountability, because there's nothing else left for it to serve.

### Delete (the builder)
- Build-from-directive: the freeform directive → plan → **herd-writes-the-code** flow.
- `create_mission`-as-builder and the "client accepts" build-convergence loop.
- Complexity-based build-*plan* sizing (`classifyComplexity`/`ScaledPlan`) as a build sizer; the coder/builder roles that *author* code.
- The "watch us build" framing (demos, hero runs) → becomes "replay the audit."

> This also **dissolves the security finding** that started the whole thread: an ungated "any authenticated agent can launch a build" verb doesn't exist in this world. Certification runs are triggered by the poller watching PRs or by the control owner — never by an arbitrary agent. `create_mission` isn't *gated*; it's *deleted*.

### Keep (already accountability) — re-pointed from *make* to *prove*
- **Swarm / roles / staffing** — **staffing is retained wholesale**; the Sense→Judge→Clamp orchestration is untouched. Only the *roster* changes: roles flip from coder/builder to **security-breaker, correctness-reviewer, exploit-attempter, edge-hunter**. (Proven live: this session's own 6-pass, role-separated adversarial review found three real HIGHs in freshly-shipped code — that *is* the product.)
- **Gate-earned routing IS the staffing logic** (one of the "1–2 novel," and *stronger* in the audit frame): staffing routes each role to the agent/model that has **earned** it — the ones the **DuckDB leaderboard** shows catch real findings, efficiently. The gate produces the trust signal → DuckDB records efficiency/effectiveness → staffing consumes it → best-earned agents get the next certification. A compounding loop, not a side feature. **Lineage:** the route-to-the-fittest / continuous-re-evaluation idea is borrowed from **Sakana's Fugu** — but the differentiation (the field note "Fugu, and the knife it leaves unwashed") is that **corral's fitness signal is *gate-earned*: certified by execution and signed, not self-reported.** Same evolutionary routing, a *trustworthy* fitness underneath it.
- **jail** (execute controls) · **certify/certverify/transparency/attest** (signed chain) · **recordings + scrub** (shareable evidence) · **control-gate poller + authoring/vetting loop** (the gate + the owner's controls) · **warehouse** (aggregate) · **gate-earned routing** (trust with a receipt) · **UI / replay-player / recordings gallery / cockpit** (audit **review** + **archive**, same pixels/new noun).

### Developer plug-in layer (accountability-native — the adoption + enrichment path)
The two-sided play: CISO buys the gate top-down (mandate); devs plug in bottom-up (value) → land-and-expand. **Discipline test for every dev feature: does it serve the audit, or is it a standalone dev product?**
- **Shared memory** → the org's *codified standards + accumulated findings* the swarm reviews against, growing *from* the gate. (✗ if it's "Confluence, but ours.")
- **Shared skills** → a "skill" is a **vetted control / review procedure** the gate runs. (✗ if dev-productivity macros.)
- **Lookbook** → the **design standard the gate certifies conformance to**. (✗ if an inspiration board.)

Virtuous loop: dev value → adoption → more org context/standards/intent in the system → richer, more trustworthy audit → more CISO value. Devs hooking in *deepens* the audit.

### The front-door swap (the minimal mechanical change)
The input stops being **a unit of work-to-do** (a feature) and becomes **a unit of change to hold accountable** (a PR/commit/diff). The trigger stops being **a human typing a directive** and becomes **the change stream** (the poller on every PR, plus the control owner's on-demand "certify this"). "Distributed" = every team/repo/builder funnels output through the one gate; every verdict federates, signed + scrubbed, into one warehouse — **distributed accountability**, not one repo's CI.

## Shape: CLI-first — the audit is a command, not a server

Because corral is reactive, headless, and not a workspace, its atom is a **command**:

- **`corral certify <change>`** (a PR / commit / diff) → runs the adversarial swarm + the owner's vetted controls in the jail → emits a **signed, scrubbable audit record**. That is the whole unit. It runs in CI, a git hook, or a control owner's shell — **no server, no UI required.**
- This is the proven **signed-attestation-CLI** model (cosign / trivy / syft): a CLI that emits independently-verifiable records, with an *optional* platform to aggregate. It's exactly the category "true audit for software change" belongs in.

Two layers fall out cleanly:

- **Core (required): the `corral certify` CLI** — the certification atom. Headless, CI-native, trivially adoptable. A dev or a pipeline can run it today.
- **Platform (optional — the "expand"): the brain-daemon + warehouse + UI** — the poller watching PRs across repos, the MotherDuck aggregate, the replay/review archive, gate-earned routing, and the shared dev context (memory / skills / lookbook). Built *on* the CLI atom; the org runs it to get **distributed** accountability, but it isn't needed to certify a single change.

This reinforces "not an IDE" — the product is **infrastructure you run**, not a place you work — and it de-risks adoption: a dev/CI can `corral certify` immediately; the CISO platform (warehouse, review, org-wide gate, cross-repo trust) is the upsell.

### Record flow: scrub-at-source → aggregate → federate

The CLI *could* run purely local — but you wouldn't want to, precisely **because the scrubbing makes bubbling-up safe.** Each record is scrubbed at the source (deny-list + manifest), so every downstream hop leaves the boundary already clean and can't leak:

`corral certify` (CLI, scrubbed) → **the brain** — the local org aggregate. The CISO queries it **with the same CLI** (`corral` both *produces* and *interrogates* the audit — one tool, two verbs) → **and/or MotherDuck** — the federated, cross-team / cross-org accountability warehouse, queryable with plain SQL/BI.

This is already the plumbing: `certify` (DuckDB-native signed records) → `fleet` (replicate the action stream to a remote / `md:` MotherDuck DuckDB) → the DSN-flip federation (same schema, local ↔ MotherDuck). Scrubbing is the enabler that makes the whole path leak-safe. The **"accountability warehouse for distributed dev shops"** is the top of this flow, not a separate product.

## Open decisions (not resolved here)
- **Brand/metaphor:** the corral/herd/wrangle **ranch** metaphor is a *builder* brand (extends to code identifiers per prior direction). An audit product may strain it. Reopen vs. leave — a real downstream call, deferred.
- **Renames:** mission → *certification / audit-run*; herd → *swarm / panel*; directive → *the change under review*. Scope + timing TBD.
- **First implementation slice:** what to delete/re-point first (likely: elevate the control-gate poller to the primary surface, retire the build-mission path) — belongs in the implementation plan, not this doc.

## Non-goals (YAGNI)
Not an IDE; not a builder; not an orchestration/dev-tool product; not a standalone knowledge base or design tool. Any feature that can't pass the accountability-native test is out.
