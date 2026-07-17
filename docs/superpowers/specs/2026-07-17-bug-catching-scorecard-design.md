<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Bug-catching scorecard — execution-proven, per model×role, DuckDB-native (design)

**Status:** design (2026-07-17). Precedes an implementation plan. Builds on the shipped adversarial pool (`internal/advpool`), the gate-earned leaderboard (`internal/brain/leaderboard.go`), and the DuckDB record ledger (`internal/buildstore`).

## The thesis (why this exists)

Fugu routes each task to the best model using performance scores from a **trained black box** — you can't inspect them, and they're a model's judgement of a model. Corral can publish the sharper, more defensible thing: a scoreboard of **which model actually catches bugs, proven by execution** — a fault a test *demonstrably killed when it ran in the jail*, not a claim. That is a metric a CISO can trust and a number no competitor whose signal is internal can show.

Two consequences shape the design:

1. **A "catch" is only ever execution-proven.** The headline recall/precision come exclusively from the adversarial pool's `proven_missed` — a survivor the model's authored test *killed*. Softer signals (the critic's theater-flags, build-mission findings) are tracked but kept OUT of the headline and labeled lower-confidence.
2. **The scorecard is an accountability record, not a dashboard cache.** Every observation is append-only, anchored to the signed verdict it came from, and stored DuckDB-native so it federates to the MotherDuck warehouse by a later DSN flip — same schema local and shared ([[corralai-motherduck-accountability-warehouse]]).

## What already exists (verified 2026-07-17)

- The pool `Verdict` already carries the raw proven signals per run: `DevKillRate`, `Survivors` (faults the dev suite missed), `ProvenMissed` (survivors the pool's authored test then **killed** — execution-proven catches), `VacuousFindings` (the critic's theater-flags), `MutantsTotal`, `ModelsByRole`.
- `internal/brain/leaderboard.go` `BuildLeaderboard` already builds a per-(model,role) `LeaderboardCell` (TasksCompleted, ExecPassRatePct, FindingsRaised/Resolved, …) from telemetry, exposed at `/api/leaderboard`. It feeds routing via the StaffingManager. **But it feeds only on `certified` runs and has no notion of proven bug-catches.**
- `internal/advpool/driver.go` already has an optional `LeaderboardSink` fed once per run in `tickAggregate`, *after* the verdict is signed, **gated to `StatusCertified`**.
- `internal/buildstore` is the canonical DuckDB store pattern to mirror (go-duckdb/v2, `CREATE TABLE IF NOT EXISTS` on open, parameterized `?`, injected clock — no `time.Now()` inside the store).

**Key foundation:** all per-model attribution rests on the worker being identified distinctly. The `identity()` collapse bug (a worker named `corral-svc/x` under principal `client:corral-svc` collapsed to the principal) is FIXED (workers must be named `client:corral-svc/<name>`); without it, every model's catches would pool under one principal and the scorecard would be meaningless. This spec assumes correct per-model attribution.

## The metric (v1 — execution-proven only)

Per **(model, role)**, aggregated over runs:

### Headline (execution-proven), from the test-writer seat
- **Catches** = `proven_missed` — survivors this model's authored test killed in the jail.
- **Opportunities** = `survivors` — the real gaps it was asked to catch (recall denominator).
- **Recall (catch rate)** = `Σ catches / Σ opportunities`. Undefined when `Σ opportunities == 0` (a perfect dev suite left nothing to catch → the test-writer was moot → contributes no recall sample). Never counted from a claim; only from a test that ran and killed a mutant.
- **Precision (soundness)** = `Σ sound_tests / Σ authored_tests`. A test-writer's authored test is **sound** when it compiled, was not critic-flagged vacuous, and correctly discriminated (failed on a mutant, passed on the original — the pool already validates this via `CompileTest` + `Score`). **unsound** otherwise. This is the "does this model write real tests, not theater" number.

### Lower-confidence lines (tracked, labeled, NOT in the headline)
- **test-critic — theater-detection:** `critic_flags` = vacuous/tautological tests it flagged. A judgement, not execution-proven; surfaced as its own column with an explicit "reviewer judgement, not proven" label.
- **mutant-generator — adversary potency:** `mutants_planted` / `mutants_survived` (against the dev suite). This is *adversary quality*, not bug-catching — the mutant-generator plants faults, it doesn't catch them. Kept as a separate line so a strong adversary isn't mistaken for a strong catcher.
- **build-mission findings (future):** findings raised that reproduced/resolved-as-real vs dismissed → a precision line. NOT execution-verified the way the pool is; explicitly out of v1's store schema, noted here so the schema leaves room (a nullable `source` discriminator).

### Confidence
Every cell carries its sample counts (`opportunity_samples`, `authored_samples`, `runs`). A cell with `runs < 3` is a **data point, not a ranking** — the explore-in-production lesson from the Fugu note ([[corralai-dev-site]] field notes). Consumers (routing, UI) must render thin cells as provisional, never as a leader.

## Architecture

### 1. The store — `internal/bugcatch`
Mirror `buildstore.Open` exactly (go-duckdb/v2 driver, `CREATE TABLE IF NOT EXISTS` on open, parameterized SQL, injected `clock func() time.Time`). Append-only; never updated in place (an accountability ledger, not a cache).

One observation row per (converged run × model × role):
```sql
CREATE TABLE IF NOT EXISTS bugcatch_observations (
  ts             TIMESTAMP,   -- observation time (injected clock)
  record_id      BIGINT,      -- the signed verdict this came from (tamper-evident anchor); 0 if signing was skipped
  record_head    VARCHAR,     -- the record's hash-chain head, for cross-store join to buildstore
  mission_id     BIGINT,
  repo           VARCHAR,
  commit         VARCHAR,
  model          VARCHAR,     -- e.g. "claude-sonnet-5"; never a principal — the attributed worker's model
  role           VARCHAR,     -- test-writer | test-critic | mutant-generator
  source         VARCHAR,     -- "pool" (v1). Reserved for "build" later.
  -- execution-proven (test-writer):
  catches        INTEGER,     -- proven_missed contributed by this seat
  opportunities  INTEGER,     -- survivors (recall denominator); 0 when moot
  sound_tests    INTEGER,     -- authored tests that were sound (precision numerator)
  authored_tests INTEGER,     -- authored tests (precision denominator)
  -- lower-confidence, per-role (null/0 when not applicable):
  critic_flags     INTEGER,   -- test-critic theater-flags (judgement)
  mutants_planted  INTEGER,   -- mutant-generator adversary line
  mutants_survived INTEGER
);
```

API (mirror buildstore):
- `Open(dsn string, clock func() time.Time) (*Store, error)` — default dsn `~/.claude/corralai_bugcatch.duckdb` (via a `CORRALAI_BUGCATCH_DSN` env; `md:...` points at MotherDuck — the flip).
- `Record(ctx, obs []Observation) error` — append one run's per-role observations in one tx.
- `Scorecard(ctx) ([]Cell, error)` — the aggregation query below.

Aggregation (`Scorecard`):
```sql
SELECT model, role,
  SUM(catches)                         AS catches,
  SUM(opportunities)                   AS opportunities,
  CASE WHEN SUM(opportunities) > 0
       THEN SUM(catches)*1.0/SUM(opportunities) END AS recall,
  SUM(sound_tests)                     AS sound_tests,
  SUM(authored_tests)                  AS authored_tests,
  CASE WHEN SUM(authored_tests) > 0
       THEN SUM(sound_tests)*1.0/SUM(authored_tests) END AS precision,
  SUM(critic_flags)                    AS critic_flags,
  SUM(mutants_planted)                 AS mutants_planted,
  SUM(mutants_survived)                AS mutants_survived,
  COUNT(*)                             AS runs
FROM bugcatch_observations
WHERE source = 'pool'
GROUP BY model, role
ORDER BY recall DESC NULLS LAST, catches DESC;
```

### 2. The feed — an optional `BugCatchSink` on the pool driver
Mirror the existing optional `LeaderboardSink` (nil ⇒ no-op; driver stays pure + testable). One new sink, fed once per converged run in `tickAggregate`, **AFTER signing** (so `record_id`/`record_head` are set) and — unlike the leaderboard — on **BOTH `certified` and `needs-review`** (a catch or a miss is meaningful regardless of the overall verdict; certified runs contribute `opportunities=0` recall-moot rows + soundness).

The driver builds the per-role `Observation`s from run state it already holds:
- test-writer seat (`ModelsByRole["test-writer"]`): `catches = ProvenMissed`, `opportunities = Survivors`, `authored_tests = (test-writer ran ? 1 : 0)`, `sound_tests = (authored test compiled + not vacuous + discriminated ? 1 : 0)`.
- test-critic seat: `critic_flags = len(VacuousFindings)`.
- mutant-generator seat: `mutants_planted = MutantsTotal`, `mutants_survived = Survivors`.

Wire in the brain exactly like `advpoolLeaderboardSink` — a thin adapter over `bugcatch.Store`, injected via `brain.Options`, threaded from `cmd/corral/main.go`.

### 3. Surfacing — `BuildBugCatchScorecard` + `/api/bugcatch`
- `internal/brain/bugcatch_view.go` `BuildBugCatchScorecard(store) (Scorecard, error)` returns the cells (JSON-tagged, mirroring `Leaderboard`). Thin-cell flag (`runs < 3 ⇒ Provisional bool`) computed here so every consumer inherits the honesty.
- Expose read-only at `/api/bugcatch` (mirror `/api/leaderboard`'s handler + authz). A `corral` CLI verb (`corral scorecard`) mirrors the `certify`/`leaderboard` CLI pattern.
- The `/recordings` analytics page and a product panel are **follow-ups**, not v1.

### 4. MotherDuck federation (config, not code)
The store opens whatever `CORRALAI_BUGCATCH_DSN` names — a local `.duckdb` file by default, `md:corralai?...` for the shared warehouse. Same schema both places, so the flip is a config change, proven later. **We do not claim MotherDuck integration until the flip runs against live MD** ([[corralai-motherduck-accountability-warehouse]] honesty note).

## Soundness / honesty invariants
- **A catch is execution-proven or it is not counted.** `catches` derives only from `ProvenMissed` (a test that killed a mutant in the jail). No claim, no self-report, ever reaches the headline.
- **Per-model attribution is a precondition.** The scorecard is only as honest as the worker identity; it assumes the `client:corral-svc/<name>` naming fix. A run whose seats collapse to a principal must be rejected or attributed to `(unknown model)`, never silently pooled.
- **Record-anchored + tamper-evident.** Every observation carries the signed `record_id`/`record_head`; the scorecard is auditable back to the signed verdict, and a shared MotherDuck row can be verified against the record.
- **Thin data is labeled, never ranked.** `runs < 3 ⇒ provisional`; consumers must not present a provisional cell as a leader.
- **Softer signals stay soft.** Critic theater-flags and (future) build findings never enter the headline recall/precision; they are separate, labeled columns.

## Non-goals (this design)
- **The eval harness that generates volume** — running `corral certify` across a suite of changes so the numbers gain statistical power — is a SEPARATE spec/slice. This spec is the store + feed + query + endpoint; it makes the metric *correct*, not yet *voluminous*.
- **A UI/scorecard page** (product panel or /recordings section) — follow-up.
- **Routing off the new metric** — the existing leaderboard already routes; folding proven bug-catches into routing is a later, deliberate change.
- **Live MotherDuck wiring** — schema-compatible now, flip proven later.
- **Build-mission finding precision** — schema leaves room (`source`), logic is future.

## Testing posture
- **Store** (mirror buildstore tests): open (schema created), `Record` a run's observations, `Scorecard` aggregates → recall/precision math with an injected clock; a moot run (`opportunities=0`) contributes no recall but does contribute soundness; thin data (`runs=1`) surfaces with its sample count.
- **Driver feed** (fake `BugCatchSink`): a `needs-review` run with `survivors=2, proven_missed=1` emits a test-writer observation `catches=1, opportunities=2`; a `certified` run (`survivors=0`) emits `opportunities=0` and still records soundness; the critic seat emits `critic_flags=len(VacuousFindings)`; the mutant-generator seat emits potency. Assert NO claim path reaches `catches`.
- **View + endpoint:** `BuildBugCatchScorecard` marks `runs<3` provisional; `/api/bugcatch` returns the cells under authz, read-only, no writes.
- Keep the pool/leaderboard/certify suites green (the sink is additive + nil-safe).

## Open question for review
- **`corral scorecard` output shape:** a terse table (model, role, recall, precision, runs) by default, `--json` for the raw cells? (Recommend: table default, `--json` flag — mirrors the other CLI verbs.)
