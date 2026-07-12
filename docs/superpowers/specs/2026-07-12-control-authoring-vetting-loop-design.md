<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Control-owner authoring/vetting loop — design (v1)

**Status:** approved-in-principle 2026-07-12 (user delegated the two design calls). Feeds an implementation plan.

## Goal

Wire the already-built-but-unwired authoring pipeline into a live, in-process surface so a **control owner** can: (1) define the bar (import/list goals), (2) trigger an independent agent to **author** a candidate test for a goal + target and **score its mutation-adequacy**, and (3) **review and approve** it into the vetted store that the (already-live) control gate runs. This closes the loop from "seeded" to "authored → owner-vetted → gate-runs-it," the last step before a first honestly-gated PR.

## Background — what exists (see [[corralai-control-stack-wiring]])

The whole pipeline exists and is unit-tested; it has **zero runtime callers**:
- `controlgate.StageCandidate(ctx, writer, reviewer testgen.LLM, jail adequacy.Jail, store *controlspec.Store, req StageRequest) (controlspec.GateTest, error)` — `authoring.Author` (WriteTest → GenerateMutants → compileVerify → `adequacy.Score`) → recover survivors → `testgen.TriageSurvivors` → `SaveCandidate` (unvetted).
- `controlspec`: goals (`SaveGoal`/`GetGoal`/`ListGoals`, `LoadBundle`/`ImportBundle` from `bundles/asvs-l1.json`); vetted store (`SaveCandidate`/`Promote`/`Reject`/`ListPending`/`ListVetted`/`GetVetted`).
- `testgen.LLM` = `Ask(ctx, system, user) (string, error)`, satisfied by `*llm.Client` / `llm.FromEnv()` — **already constructed as the brain's `narrator`** (`cmd/corral/main.go`).
- The gate reads `ListVetted` (live).

The **memory-vetting cycle** is the exact model to mirror: `promote_memory` (`internal/brain/memory.go:122`) is an admin-gated, principal-attributed, audited MCP tool over `memory.SetShared`; companion `list_memory`/`get_memory`/`add_memory`.

## Design calls (resolved)

- **Surface = in-process MCP tools** (not CLI). Rationale: DuckDB is single-writer per file — with the gate enabled the brain holds the controlspec store open R/W, so a separate-process CLI writer would lock-conflict. In-process tools also reuse the brain's `narrator` LLM + `GateBackend` jail, get an **audit trail** on the owner's approval, and mirror the memory tools. `corral control seed` stays as the offline bootstrap shortcut (unchanged).
- **One-model v1:** `llm.FromEnv()` is passed as **both** writer and reviewer (seat independence via distinct system prompts, same model). Distinct-model routing off the earned leaderboard is roadmap, noted honestly.
- **Goal→file mapping deferred:** `stage_control` takes the target's code + paths explicitly (operator/owner supplies them), exactly as `seed` does. Auto-mapping via `repoindex.Search` is a later cut.

## Scope

**In (v1):**
1. **Glue fix:** `StageCandidate` must persist `CodePath`/`TestPath` on the `GateTest`, and staging must use the **same `controlgate.LangScaffold(lang)`** recipe the gate uses (so a vetted candidate reproduces by construction).
2. **Share the controlspec store**: open it once in the brain and hand it to both `StartControlGate` (the gate's `ListVetted`) and the new MCP tools — one open handle, no second opener.
3. **Six MCP tools** over the existing pipeline, admin/owner-gated + audited (mirror `promote_memory`):
   - `import_control_bundle` (ASVS → goals), `list_control_goals` (the bar)
   - `stage_control` (author + score → unvetted candidate)
   - `list_pending_controls`, `get_control` (review: test body + kill rate + survivors + triage)
   - `promote_control`, `reject_control` (the audited human gate)

**Out (deferred):** distinct writer/reviewer models; goal→file auto-mapping; auto-checkout-and-read of the target (v1 passes code inline); custom `add_control_goal` (v1 defines the bar via ASVS import — `SaveGoal` stays Go-API); a UI (MCP tools are the surface; a cockpit panel is later).

## Components

### 1. Glue fix — `StageCandidate` persists the recipe
`internal/controlgate/stage.go`: when building the `GateTest`, set `CodePath: req.CodePath`, `TestPath: req.TestPath` (from the embedded `authoring.Request`). Without this a promoted candidate can't run in the gate (the gate's `ControlCheck` needs both). Extend `stage_test.go` to assert both round-trip onto the stored candidate.

### 2. Shared controlspec store
`cmd/corral/main.go`: open the controlspec store once (DSN = `CORRALAI_CONTROL_GATE_SPEC_DB`), put it on `brain.Options` (new field `ControlSpec *controlspec.Store`). `StartControlGate` uses `opts.ControlSpec` if set (else opens its own, preserving today's behavior). The MCP tool layer uses the same handle. `adequacy.NewJail(opts.GateBackend, …)` and `llm.FromEnv()` are already available to the brain.

### 3. The MCP tools (`internal/brain/controltools.go`, new)
Registered alongside the memory tools, all **admin/owner-gated** (reuse the `isHumanAdmin`/principal check the memory tools use) and **audited** (reuse `auditKnowledge` or the equivalent). Each tool's owner scoping uses the caller's principal as `owner` (like memory tools), never a client-supplied owner it doesn't check.

- **`import_control_bundle{ bundle }`** → `controlspec.LoadBundle` + `ImportBundle(store, owner, b, now)`; returns count imported. (v1 bundle: `asvs-l1`.)
- **`list_control_goals{}`** → `ListGoals(owner)`; returns `[{id, ref, intent, level, mode}]`.
- **`stage_control{ goal_id, target, code, lang, code_path, test_path, n_mutants? }`** → look up the goal (`GetGoal(owner, goal_id)`; error if absent), build `StageRequest{ authoring.Request{ Goal: goal.Intent, Code: code, Lang: lang, CodePath: code_path, TestPath: test_path, Base+TestCmd from LangScaffold(lang), NMutants }, Owner: owner, GoalID: goal_id, Target: target, Now: now }`, call `StageCandidate(ctx, narrator, narrator, jail, store, req)`. Returns `{ goal_id, target, kill_rate, survived, triage:[{mutant,real_gap,rationale}], vetted:false }`. **Fail-loud** if `Report.CompliantPass` is false (the test didn't pass on compliant code → the candidate is invalid; surface it, don't store a junk candidate). NOTE: `stage_control` runs LLM calls + jail scoring — it can take tens of seconds; v1 is synchronous.
- **`list_pending_controls{}`** → `ListPending(owner)`; returns `[{goal, target, kill_rate}]` (summary; not the full test body).
- **`get_control{ goal, target }`** → the pending candidate's full `{ test, kill_rate, survived, triage }` for the owner to read as code. `ListPending(owner)` already returns full `GateTest` rows (test body + survivors + `VerdictsJSON`), so `get_control` **filters `ListPending` for the matching `goal`+`target`** — no new store method needed. (`GetVetted` is vetted-only, so it can't serve this.)
- **`promote_control{ goal, target }`** → `Promote(owner, goal, target, now)`; **the meaningful human gate** — attributed to the caller + audited. Returns ok/err (ok=false if nothing unvetted to promote).
- **`reject_control{ goal, target }`** → `Reject(owner, goal, target)`; audited.

### Data flow (the loop)
```
import_control_bundle(asvs-l1)  → goals exist (control_goals)
list_control_goals              → owner picks a goal id
stage_control(goal,target,code) → StageCandidate authors+scores → unvetted GateTest (gate_tests)
list_pending_controls / get_control → owner READS the test + kill rate + triage
promote_control(goal,target)    → vetted=TRUE (audited, attributed)   [the human gate]
  ── the live control gate (StartControlGate) now runs it on the next PR head ──
reject_control(goal,target)     → deleted
```

## Error handling / invariants

- **Human gate is invariant:** a staged candidate is ALWAYS unvetted (`SaveCandidate` forces it); only `promote_control` vets it, and that call is admin-gated + audited + principal-attributed — the recorded, accountable approval. `stage_control` can never vet.
- **Fail-loud authoring:** `CompliantPass == false` (test fails on compliant code) → `stage_control` returns an error, stores nothing. A zero-kill-rate candidate is stored but surfaced (kill_rate=0 is a visible red flag for the owner, not a silent pass).
- **Recipe coherence:** staging and the gate both derive `Base`/`TestCmd` from `LangScaffold(lang)`, and the candidate persists `CodePath`/`TestPath` — so a promoted test reproduces exactly at gate time.
- **Owner scoping:** every tool scopes to the caller's principal; no cross-owner read/write.
- **Store sharing:** one open controlspec handle (no second R/W opener → no DuckDB lock conflict).

## Testing strategy (TDD)

- `StageCandidate` glue: `CodePath`/`TestPath` persist onto the stored candidate (extend `stage_test.go`).
- Store: a `GetCandidate`/pending-read returns an unvetted row (if added).
- Each MCP tool with a fake `testgen.LLM` + fake/real jail + temp `controlspec.Store`: `stage_control` stores an unvetted candidate with the recipe; `CompliantPass=false` → error, nothing stored; `promote_control` vets + is gated (non-admin caller refused); `reject_control` deletes; `list`/`get` scope to owner; import creates goals.
- Gate integration: a `stage_control`→`promote_control`'d candidate is picked up by `ListVetted` and runnable by the existing gate (reuse the Task-3 runner test shape).

## Honesty seam

v1 gives the owner a real, audited authoring→vet loop, but: one model fills both generative seats (distinct prompts, not distinct models); the owner supplies the target code + paths (no goal→file auto-mapping); and it's an MCP-tool surface, not yet a cockpit UI. The field note still ships only after a **first live-gated PR whose control was authored (not seeded)** — [[corralai-ciso-gate-blog-post]].
