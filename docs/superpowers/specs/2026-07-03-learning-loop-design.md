# The Learning Loop — lessons → proposals → skills

**Date:** 2026-07-03
**Status:** approved design, pre-implementation
**Prior art:** the distill-and-refine loop is inspired by Nous Research's
hermes-agent (MIT). Concepts only; no code derives from it.

## Problem

The swarm writes lessons liberally and searches them before work (live today),
and mission creation can weave vetted lessons into instructions (live, but
fail-closed without a Principals authority — so it has never fired in the
auth-off demo). What's missing is the loop that makes learning *compound*:
recurring lessons never crystallize into durable, versioned guidance or skills,
the artifacts sync layer has no producer, and none of it is visible in the demo.

## Goals

1. **Better instructions** — recurring lessons distilled into role-scoped
   guidance injected into future task instructions.
2. **Durable skills** — procedure-shaped knowledge crystallized into named,
   versioned `SKILL.md` artifacts, fleet-synced; harness agents (Claude Code
   family) consume them natively from `~/.claude/skills`.
3. **Pre-seeded knowledge** — repos ship human-first docs that double as seed
   memory, so a fresh swarm starts informed and developers can query their own
   agent about the codebase.
4. **Visible in the demo** — a reviewer watches the herd propose, an operator
   approve, and the next run measurably improve.

**Non-goal (rejected as an attack vector / slippery slope):** machine-enforced
behavioral reflexes. No lesson ever becomes an enforced rule automatically.

## Trust model (the load-bearing decision)

Deterministic detection → LLM phrasing → **human promotion**. Nothing shapes
agent behavior without a superuser approval. A poisoned lesson can reach a
*proposal*, where it dies in front of human eyes — that is the design working.
Promotion routes exclusively through the existing trust machinery
(`promote_memory`, superuser artifacts push); this feature adds no new
authority-granting paths.

## Pipeline

**Lesson → Proposal → Guidance/Skill → Refinement**

### 1. Detect (deterministic, brain-side)

A `learn` ticker in `cmd/corral` (beside the reap/mission ticks) sweeps two
recurrence signals:

- **Recurring findings:** groups of findings sharing a signature
  (`type + target`), within or across missions — the `recurring` flag already
  computed by the queue store.
- **Near-duplicate lessons:** memory entries of `type=lesson` clustered by
  DuckDB FTS similarity; a cluster of N≥3 becomes a candidate.

No LLM decides what recurs — counting does. Each new cluster becomes a
**proposal** row keyed by its recurrence signature (dedup: one open proposal per
signature). Sweep results are logged loudly and recorded to telemetry
(`proposal_opened`).

### 2. Draft (LLM phrases, never decides)

The brain's narrator model (same `MODEL_BACKEND` plumbing as ask/chatter) turns
a cluster into:

- a one-paragraph, role-scoped **guidance** distillation, and
- where the cluster is procedure-shaped, a draft **`SKILL.md`** (name,
  description, steps).

Both attach to the proposal. With no model configured the proposal still exists
carrying raw evidence; a human can promote hand-written text. Drafting failures
never block detection.

### 3. Gate (human, always)

Surfaces:

- `corral-admin proposals list | show <id> | approve <id> [--guidance-only|--skill-only] | reject <id> [--reason]`
- MCP tools `list_proposals`, `approve_proposal`, `reject_proposal` —
  superuser-gated via the existing `isAdmin`/Principals machinery.
- A **Proposals card** in the swarm UI (same visual grammar as the mission
  review gate): evidence cluster, drafted guidance, drafted skill,
  Approve/Reject buttons.
- Shep announces new proposals in a standup line.

Approval fans out to:

- **Guidance:** the distilled text is written as a vetted memory entry
  (`promote_memory` path) tagged with the target role(s) and the recurrence
  signature.
- **Skill:** the draft is pushed as `skills/<slug>/SKILL.md` into the artifacts
  store (rev-bumped, superuser-authored) → fleet sync. The skill body is also
  mirrored into vetted memory so reference agents have a single retrieval path.

Rejection records the reason; the signature is suppressed from re-proposal
unless its recurrence count grows past a higher threshold.

### 4. Refine (efficacy, deterministic again)

Every promotion stores its recurrence signature. If the same signature recurs
in missions created *after* promotion, the brain increments a `recurred_after`
counter; past a threshold the proposal reopens as a **revision draft** ("v2:
this didn't land — here's what changed since"), back through the same human
gate. Skills carry artifacts rev history: refinement is versioned,
attributable, reversible.

## Pre-seeding — developer docs the herd can read

Human-first files that double as seed memory. One corpus, three consumers:
developers reading the repo, the swarm searching before work, and **a
developer's own agent** (any MCP harness pointed at the brain) answering
questions like "how does the verify gate work" via `search_memory` /
`get_memory`.

Channels and trust:

| Channel | Location | Trust tier |
|---|---|---|
| Operator seeds | brain memory dir (exists today) | trusted (operator placed them) |
| Repo-shipped seeds | `CORRAL.md` (root, echoes the CLAUDE.md convention) + `docs/corral/*.md` | **advisory** until promoted — a hostile repo is an injection vector |
| Corralai's own seeds | same files in this repo, baked into the demo image | trusted (operator-built image) |

Repo-work missions ingest the target repo's seed files at snapshot time,
tagged to that repo. Format: plain prose, one fact per file, title as slug —
readable documentation first, ingestible second.

Deliverable alongside the feature: write corralai's own 4–5 seed docs (verify
gate, memory etiquette, claim/lease semantics, mission lifecycle, demo map) —
they double as new-contributor onboarding.

### The community corpus

Because seeds are ordinary repo files, **developers contribute knowledge the
same way they contribute code**: a PR adding or amending `docs/corral/*.md`.
The merge review *is* the human gate for repo-shipped knowledge — maintainers
vet a lesson exactly as they vet a function. Over time the corpus becomes
guided institutional memory for new commits: conventions, dead ends, "why it's
built this way," queryable by any developer's agent and searched by the herd
before it works. CONTRIBUTING.md gains a line inviting knowledge PRs.

Follow-on (out of scope for v1): the reverse flow — an approved swarm skill
that's repo-specific can be exported as a `docs/corral/` PR, so swarm-learned
knowledge lands in the repo through the same review gate humans use.

## Demo arc (learning you can watch)

The demo brain ships a seeded role authority (demo operator/superuser) so the
gates are real, not mocked:

1. Boot: corralai's baked seeds index; the memory tab shows them.
2. Run 1: bees hit a natural recurrence (the empty-workspace `go mod init`
   stumble is reliable); the sweep opens a proposal; Shep announces it.
3. The Proposals card appears; the operator clicks Approve.
4. The next mission's builder instruction visibly carries the guidance; the
   skill syncs to harness agents; the recurrence stops; telemetry shows the
   efficacy win.

## Implementation shape

- **`internal/learn` (new package):** proposals store (own SQLite —
  signature, evidence JSON, drafted guidance/skill, status
  pending|approved|rejected|revision, efficacy counters, lineage), the
  deterministic sweep, the drafter. Unit-testable in isolation.
- **Brain wiring (`cmd/corral`):** learn ticker; Options plumbing for the
  store + narrator; telemetry events (`proposal_opened`, `proposal_approved`,
  `proposal_rejected`, `proposal_reopened`).
- **MCP surface (`internal/brain`):** the three proposal tools,
  superuser-gated; `/api/state` gains a `proposals` block for the UI.
- **Surfaces:** `corral-admin proposals` verbs; UI Proposals card; Shep
  standup line (deterministic — it reads `/api/state`).
- **Injection (`internal/brain/missions.go`):** extend the existing
  `RecallLessons` weave with role filtering and skill summary lines, capped at
  ~3 items per instruction — seasoning, not a second prompt.
- **Seeds:** repo-mission ingest of `CORRAL.md` / `docs/corral/*.md`
  (advisory tier, repo-tagged); demo Dockerfile bakes corralai's seeds; write
  the seed docs themselves.
- **Demo authority:** seed a demo superuser (Principals) in the demo brain so
  fail-closed paths open honestly; the UI approve button acts as the demo
  operator identity.

## Testing

- Store/sweep unit tests: clustering thresholds, signature dedup, suppression
  after rejection, efficacy reopen.
- Drafter tests with a stub narrator (drafting failure never blocks).
- MCP wire test: seed lessons → sweep → proposal → approve → next
  mission's instructions carry the guidance; skill artifact revs.
- Injection cap and role-filter tests.
- Live demo verification: the two-run arc, end to end.

## Open questions deferred (explicitly out of scope)

- Vector (HNSW) clustering for lesson similarity — FTS-based clustering ships
  first; the memory store already supports vectors when wanted.
- Cross-brain (fleet-level) proposal aggregation — single-brain first.
- Skill consumption beyond summary-injection for `corral-agent` — revisit if
  summaries prove insufficient.
