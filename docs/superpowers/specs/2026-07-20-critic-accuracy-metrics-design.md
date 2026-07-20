<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Critic-Accuracy Metrics — Design

**Status:** approved design, 2026-07-20. Ready for an implementation plan.

**Goal:** Make the test-critic's findings a *scored* artifact. Record, per
`(model, test-critic)`, whether each finding was **confirmed** (a real dead
test/check) or **disproven** (a hallucination), and surface a **critic
precision** number in the bug-catching scorecard — so "this critic model
hallucinated here" becomes a durable, queryable fact, exactly like the
execution-proven *recall* the scorecard already keeps for the other roles.

**One-line motivation:** In two real runs of the same file, a lighter
same-vendor critic (Claude Haiku) hallucinated that `test_negative_take` was
vacuous (it is not — `take(-3, …)` raises `ValueError`), while a stronger
decorrelated critic (Gemini 3.1 Pro) did not, and instead found two genuine
dead checks. Today the scorecard cannot tell those two critics apart: it records
only `CriticFlags = len(findings)` — a count, with no notion of correctness.

---

## Background — current state (grounded)

- **The critic role** (`internal/advpool/roles.go`): `RoleTestCritic` (roles.go:28)
  is registered `Structured: false` (roles.go:277) and emits **freeform** findings
  via `renderTestCritic` (roles.go:104-113), instructed to "flag ONLY a
  demonstrably vacuous test."
- **A finding** = `queue.Finding` (`internal/queue/findings.go:35-51`): carries
  `Target` (the test), `Evidence`, `Severity`, and `ReporterModel` (the critic
  model). Durable in the queue's SQLite `findings` table via `AddFinding`
  (findings.go:55).
- **Flow into the verdict**: `tickAggregate` (`internal/advpool/driver.go:1109`)
  loads findings (driver.go:1118), `filterCriticFindings` (driver.go:1352) drops
  operational ones, and `aggregate` stores them on `Verdict.VacuousFindings`
  (`internal/advpool/aggregate.go:30`, `Verdict` struct driver.go:106-128).
  Findings are **advisory only, never gating**: `aggregate` is called with
  `blockingFindingOpen=false` (driver.go:1130), and aggregate.go:42-43 reserves
  that flag "for a future **EXECUTION-VERIFIED** finding path." *This spec is that
  path.*
- **The scorecard store** (`internal/bugcatch/store.go`): append-only DuckDB, one
  `Observation` (store.go:37-69) per converged run × model × role, written via
  `Record` (store.go:150). The **critic row records only** `CriticFlags =
  len(v.VacuousFindings)` (`bugCatchObservations`, driver.go:1251-1254) — a bare
  count. The aggregate `Cell` (store.go:71-84) exposes `Recall` and `Precision`,
  but `Precision` is defined for the **test-writer** (`sound/authored`); **there
  is no precision notion for the critic at all**. Surfaced via
  `brain.BuildBugCatchScorecard` (`internal/brain/bugcatch_view.go:13`,
  `provisionalBelow=3`), `GET /api/bugcatch` (`internal/ui/ui.go:245,451`), and
  `corral scorecard` (`cmd/corral/scorecard.go:86`, reads over HTTP because DuckDB
  is single-process).
- **No per-test kill attribution exists.** `adequacy.Score`
  (`internal/adequacy/score.go:135`) runs the **whole suite** once per mutant
  (score.go:174-193) and records only killed/survived **mutant IDs**
  (`Report`, score.go:92-97). The tests×mutants matrix is unbuilt; the code notes
  survivor→test attribution is impossible without it (driver.go:1263-1287). So we
  **cannot** currently tell whether a critic-flagged test kills any mutant.
- **The human-gate pattern** (to mirror): `controlspec.SaveCandidate`
  (store.go:150, forces unvetted) / `Promote` (gate_tests.go:19) / `Reject`
  (gate_tests.go:35); `memory.SetShared` (store.go:409). "Worker proposes,
  superuser adjudicates," attributed and audited.
- **The brain wiring**: `advpoolBugCatchSink.Record`
  (`internal/brain/advpool.go:94-114`) adapts driver observations → store rows;
  wired per-run in `StartRun` (advpool.go:666-671); store injected via
  `StartAdversarialPool`/`opts.BugCatch` (advpool.go:831). **`certify --local`
  does not wire a bugcatch sink** — metrics accrue on the brain path only.

**The two gaps this design closes:** (1) critic findings are not persisted with a
correctness verdict — only counted; (2) there is no signal, automatic or manual,
that adjudicates a finding as real-vs-hallucination.

---

## Non-goals

- **The full tests×mutants matrix.** We build only a *narrow, findings-only*
  per-test run (score one flagged test against the run's mutants), not the whole
  matrix. The full matrix remains swarm slice 5.
- **Auto-adjudicating `dead-check` findings.** Only `whole-test` claims are ever
  auto-scored (see the guardrail below). Dead-check findings are human-only.
- **Retroactively scoring past runs.** Findings before this ships were never
  stored with scope; the metric accrues going forward.
- **Gating on critic findings.** They remain advisory. This measures the critic;
  it does not let the critic block a verdict.

---

## Architecture

Five components, each independently testable:

1. **Structured, scoped critic findings** (`internal/advpool/roles.go`,
   `internal/queue/findings.go`).
2. **The `critic_findings` store** (new package `internal/criticscore`).
3. **Inline conservative auto-refute** + a `RunSingleTest` capability on
   `lang.Plugin` (`internal/lang`, `internal/adequacy`, `internal/advpool`).
4. **Human-gate adjudication** (store methods + admin-gated brain MCP tools +
   a `corral criticscore` CLI verb).
5. **Scorecard surfacing** — critic precision in `brain.BuildBugCatchScorecard`,
   `/api/bugcatch`, and `corral scorecard`.

### 1. Structured, scoped critic findings

The critic must tell us **what** it is claiming, or we cannot auto-adjudicate
soundly. Add three fields the critic emits per finding:

- `scope` — enum: `whole-test` ("this entire test can never fail") or
  `dead-check` ("a specific check is dead, but the test still asserts something
  real"). **This is the guardrail: only `whole-test` is ever auto-refuted.**
- `test_file` — the file holding the flagged test (repo-relative).
- `test_selector` — a language-runnable selector for the single test
  (e.g. `tests/test_recipes.py::RandomPermutationTests::test_full_permutation`
  for pytest; `TestNegativeTake` for `go test -run`).

Implementation: the `test-critic` role emits **one fenced JSON block per
finding** — `{scope, test_file, test_selector, evidence, severity}` — while
keeping its freeform reasoning above the block (we do *not* switch the role to
full `Structured: true`, which would suppress the reasoning we capture on the
tape; the block is parsed out of the freeform reply). `renderTestCritic`
(roles.go:104-113) gains the instruction + the block schema; a new parser in the
critic-finding path extracts the fields. New fields land on `queue.Finding`
(`Scope`, `TestFile`, `TestSelector`) as **additive** columns on the SQLite
`findings` table (nullable/empty-defaulted; existing callers unaffected). A
finding whose block is missing or fails to parse a `scope` defaults to
`dead-check` (fail-safe: never auto-refuted).

### 2. The `critic_findings` store — `internal/criticscore`

A new DuckDB store mirroring `buildstore`/`bugcatch`/`controlspec` conventions
(same `Open(dsn)`, additive-migration column ledger, `#nosec`-clean file perms).
Unlike append-only `bugcatch`, this table is **mutable per-finding** (adjudication
status changes), like `controlspec.gate_tests`.

Table `critic_findings`:

| column | meaning |
|---|---|
| `id` | stable finding id (from `queue.Finding.ID`) |
| `ts` | first recorded |
| `record_id`, `record_head` | the signed verdict this finding rode |
| `repo`, `commit`, `mission_id` | run provenance |
| `model` | the critic model (`ReporterModel` / `ModelsByRole[test-critic]`) |
| `target_test`, `test_file`, `test_selector` | what it flagged |
| `scope` | `whole-test` \| `dead-check` |
| `evidence`, `severity` | the finding body |
| `adjudication` | `unadjudicated` \| `confirmed` \| `refuted` |
| `source` | `auto` \| `human` (who set the current adjudication) |
| `adjudicated_by` | principal (for `human`) or `"auto"` |
| `adjudicated_ts` | when |

Methods (mirror controlspec):
- `Record(ctx, []Finding) error` — upsert findings for a run (idempotent on `id`);
  never downgrades a `human` adjudication to `auto`.
- `Adjudicate(id, verdict, by string) (bool, error)` — human confirm/refute;
  sets `source=human`, always wins over `auto`.
- `ListPending(ctx) ([]Finding, error)` — `unadjudicated`, for the human queue.
- `Get(id)`, `List(ctx)` — reads.
- `Precision(ctx) ([]CriticCell, error)` — per-model aggregate:
  `confirmed`, `refuted`, `unadjudicated`, and `Precision = confirmed /
  (confirmed + refuted)` (nil when denominator 0).

### 3. Inline conservative auto-refute + `RunSingleTest`

**When:** during `tickAggregate`, while the jail and the run's mutants are still
live (post-`aggregate`, before/around the `BugCatch.Record` call at
driver.go:1162). Auto-adjudication *must* be inline — mutants are ephemeral.

**The sound rule:** for each finding with `scope == whole-test`:
- Run **that one test alone** against the run's mutants. If it **kills ≥1**, then
  "this test can never fail" is disproven by execution → **auto-refute**
  (`adjudication=refuted, source=auto`).
- If it kills 0 (or the language/selector can't be run) → leave `unadjudicated`
  (a human decides). Killing 0 is *not* proof of vacuity (mutants may not target
  the test's code), so we never auto-*confirm*.

`dead-check` findings are **never** auto-touched — persisted `unadjudicated`.

**`RunSingleTest`:** add to `lang.Plugin` a method that yields the command to run
exactly one test:
`SingleTestCmd(testPath, selector string) (cmd []string, ok bool)`.
- `python`: `pytest <test_file>::<selector>` → `ok=true`.
- `go`: `go test -run ^<selector>$ ./...` → `ok=true`.
- languages without an impl yet: `ok=false` → auto skipped, human-only (sound).

`adequacy` gains `ScoreSingleTest(ctx, j, base, codePath, mutants, singleCmd)
(kills int, err error)` — a thin reuse of the `Score` loop scoped to one test
command. Exposed to the driver through the existing `Scorer` seam (driver.go:24-25)
as `ScoreTest`. Cost is bounded: only flagged `whole-test` findings (typically
0–2/run) × the run's mutants.

**Output:** the auto verdict rides on the in-memory finding and into the
`Verdict`/`--record` tape (visible on `--local` too, even without a store), and is
persisted by the store on the brain path.

### 4. Human-gate adjudication

- **Store:** `Adjudicate(id, verdict, by)` (component 2).
- **Brain MCP tools** (admin-gated + audited, mirroring the control verbs):
  `list_pending_critic_findings`, `get_critic_finding`, and
  `adjudicate_critic_finding{ id, verdict: confirmed|refuted }`. A human's verdict
  always overrides an `auto` one.
- **CLI:** `corral criticscore` — `list` (pending), `show <id>`, `confirm <id>`,
  `refute <id>` — reading/writing over the brain API (DuckDB single-process, same
  reason `corral scorecard` reads over HTTP, scorecard.go:37-80).

### 5. Scorecard surfacing

- `brain.BuildBugCatchScorecard` (bugcatch_view.go) augments the **test-critic**
  cell (or adds a companion cell) with `CriticConfirmed`, `CriticRefuted`,
  `CriticUnadjudicated`, and `CriticPrecision` from `criticscore.Precision`.
  `provisional` when `confirmed+refuted < provisionalBelow` (reuse the existing 3).
- `GET /api/bugcatch` includes the new fields; `corral scorecard` prints a
  `C-PREC` column for the critic role (and `--json` carries the raw counts).

---

## Data flow (end-to-end)

1. Critic files findings → structured `queue.Finding{Scope, TestFile,
   TestSelector, …}` (component 1), durable in the queue (findings.go:55) and on
   `Verdict.VacuousFindings` (aggregate.go:30).
2. `tickAggregate` (driver.go:1109): after `aggregate`, for each `whole-test`
   finding run `ScoreTest` against the mutants; set `auto`-refuted where kills ≥1
   (component 3). Attach verdicts to the findings on the `Verdict`/tape.
3. On the brain path, a new `CriticFindingSink` (analog of `BugCatchSink`,
   driver.go:92-94) persists the findings + auto verdicts to `criticscore`
   alongside the `BugCatch.Record` call (driver.go:1162-1164). Wired in
   `internal/brain/advpool.go` next to the bugcatch sink; store injected via a new
   `opts.CriticScore` (mirror `opts.BugCatch`, advpool.go:831).
4. Async: a superuser reviews `list_pending_critic_findings`, confirms/refutes
   (component 4); human overrides auto.
5. `corral scorecard` / `/api/bugcatch` show critic precision per model
   (component 5).

---

## Error handling / fail-closed

- **Unparseable scope** → `dead-check` (never auto-refuted). Fail-safe.
- **`SingleTestCmd` unsupported** for a language → auto skipped, `unadjudicated`.
  A missing auto-signal never becomes a silent confirm.
- **`ScoreTest` error** (jail failure, selector doesn't match a test) → log,
  leave `unadjudicated`, continue the run. Auto-adjudication is best-effort and
  **must never fail the audit** (matches shadow-challenger fail-soft posture).
- **Never auto-*confirm*** — 0 kills is inconclusive, not proof. Only humans
  confirm; auto only ever refutes a `whole-test` claim.
- **`Record` idempotency**: re-recording a finding never downgrades a `human`
  adjudication. `record_id==0` runs (unsigned/timeout) do not persist (parity with
  bugcatch's nonzero-RecordID guard, driver.go:1162).

---

## Testing strategy

- **Store** (`internal/criticscore`): round-trip Record → Precision; Adjudicate
  overrides auto; ListPending filters; idempotent Record; human-not-downgraded.
- **Scope parsing**: structured critic output → `Scope/TestFile/TestSelector`;
  malformed → `dead-check` default.
- **`SingleTestCmd`** per language: correct selector command for python + go;
  `ok=false` for unimplemented.
- **Auto-refute logic** (the crux): a `whole-test` finding on a test that kills a
  mutant → `refuted/auto`; a `dead-check` finding on the *same* test → **left
  `unadjudicated`** (the mis-scoring guardrail, proven by test); 0-kills →
  `unadjudicated`; `ScoreTest` error → `unadjudicated`, run still converges.
- **Precision aggregate**: confirmed/refuted/unadjudicated counts and the
  `confirmed/(confirmed+refuted)` ratio; provisional below threshold.
- **End-to-end** (brain path, mirroring the more-itertools/passwd fixtures): a
  planted `whole-test` vacuous test auto-refutes; a real dead-check stays pending.
- All Go work must pass `gofmt -l` + `bash scripts/check-security.sh` +
  `go test ./... -race` (the deploy gate).

---

## Rollout / scope / honesty

- **Languages:** `SingleTestCmd` for **python first** (primary language + our
  fixtures), then go; others fail-closed to human-only.
- **Where it accrues:** the persisted metric + human gate are **brain-path**
  (like all bugcatch metrics). `--local` runs still compute the auto verdict inline
  and show it on the verdict/tape, but persist only when a brain store is present.
  Stated in docs, not hidden.
- **The critic's self-classification is trusted for routing only, not for truth:**
  a `dead-check` label just routes a finding to the human queue; it never asserts
  the finding is correct. Truth comes from execution (`auto` refute) or a human.

## Future (explicitly out of scope here)

- **Auto-*confirm* via the full tests×mutants matrix** (swarm slice 5): once
  per-test attribution exists for the *whole* suite, a `whole-test` finding whose
  test kills nothing across a matrix that *does* target its code becomes a
  positive signal. Plugs into the same `blockingFindingOpen` hook
  (aggregate.go:42-43).
- **Cross-run recurrence** of the same hallucination pattern feeding the shared
  corpus (the moat) — a refuted finding is a proven-negative pattern worth
  federating.
