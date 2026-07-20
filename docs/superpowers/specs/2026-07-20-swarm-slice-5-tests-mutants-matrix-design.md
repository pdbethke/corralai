<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Swarm Slice 5 — The Tests × Mutants Matrix — Design

**Status:** approved design, 2026-07-20. Ready for an implementation plan.

**Goal:** Establish **per-test adequacy by execution** — for every test in a
suite, which of the run's mutants it actually catches — by scoring each test
alone against every mutant (`T tests × M mutants`, the "big swarm"). This yields
two products: an execution-proven, human-gated **"safe to delete" candidate
list** (tests that caught zero planted mutants), and the **auto-CONFIRM** signal
the critic-accuracy feature reserved (a `whole-test` critic finding whose test
kills nothing is corroborated by execution).

**One-line motivation:** An agentic dev's suite accumulates thousands of green
tests; passing-count is not strength. The matrix reads *down the per-test axis*
to say, by execution and without an LLM's opinion, which tests are load-bearing
and which caught nothing the herd threw at them.

---

## Background — current state (grounded)

- **The intent is already specified.** Parent spec
  `docs/superpowers/specs/2026-07-19-resource-aware-metrics-driven-swarm-design.md`
  §:48-63 and §:122-124: "for each `(test, mutant)` cell, run that test against
  that mutant in the jail — 'does this test catch this bug?' … per-test adequacy
  by execution (which tests are load-bearing vs dead weight — the
  execution-proven version of 'vacuous', no LLM opinion)"; "a test that kills
  **zero** mutants is *provably* dead weight … an execution-proven, opinion-free
  'safe to delete' list."
- **No per-test attribution exists today.** `adequacy.Score`
  (`internal/adequacy/score.go:135`) runs the **whole suite** once per mutant and
  records only mutant IDs killed/survived (`Report`, score.go:92-97) — there is
  no test dimension. `JailScorer.Score` (`internal/advpool/gate.go:110-128`) wraps
  it; `scoreWorkspace` (gate.go:138-157) handles single-file mode (dev test
  overlaid at the synthetic `advPoolTestPath`) vs repo-aware mode (`BaseFiles`
  set, dev test already in the tree, project `TestCmd` authoritative).
- **No test-enumeration primitive exists.** `lang.Plugin` (`internal/lang/lang.go:13-28`)
  can *run* one named test (`SingleTestCmd`, lang.go:24-27 — go+python only;
  ruby/js/ts return `nil,false`) but cannot *list* a suite's tests. `repoindex`
  can statically walk function names but has zero test-awareness. So slice 5 must
  add a `ListTests` capability first, and the matrix is **go/python-only** on day
  one.
- **The reserved hook + the working single-row prototype.** `aggregate.go:19,42-43`
  keeps `blockingFindingOpen` "for a future EXECUTION-VERIFIED finding path";
  `driver.go` passes `false` today. The critic auto-refute loop in `tickAggregate`
  (`driver.go:1201-1249`) already scores **one** flagged test against `run.mutants`
  via `SingleTestCmd` — it is a working single-row proof-of-concept of the matrix.
  The full merged mutant set is already retained on `runState.mutants`
  (driver.go:236-240), staged for exactly this.
- **The auto-CONFIRM the critic-accuracy feature reserved.**
  `docs/superpowers/specs/2026-07-20-critic-accuracy-metrics-design.md` §:287-291:
  "once per-test attribution exists for the *whole* suite, a `whole-test` finding
  whose test kills nothing across a matrix that *does* target its code becomes a
  positive signal." That feature made **"auto never confirms"** a hard invariant
  precisely because, without the matrix, 0 kills was inconclusive. This slice
  supplies the matrix and, per the approved decision, **wires auto-CONFIRM** —
  guarded by a soundness floor (below).
- **Cost knobs today** bound mutants and concurrency, never the test axis:
  `--n-mutants` (certify_local.go:94, default 5 per shard), `--max-shards`
  (default 8), `--swarm` (concurrency, `resolveSwarm` :525-538, cap 8),
  `--test-timeout`. Default M ≈ `n-mutants × max-shards` ≈ 40. A full matrix
  multiplies that by T.

**The gap this closes:** there is no per-test adequacy signal anywhere, so
neither the "safe to delete" list nor a sound critic auto-CONFIRM is expressible.

---

## Non-goals

- **A statically-derived or sampled matrix.** The matrix is scored by real
  execution, every cell (approved decision: full matrix, concurrency-bounded).
- **Auto-deleting tests.** The "safe to delete" list is a **human-gated candidate
  list**, never an automatic edit. Corral never deletes the user's code.
- **Test→code coverage mapping.** The matrix gives per-test *kill* counts, not a
  coverage map. The residual "0 kills could mean no mutant targeted this test's
  code" ambiguity is mitigated (soundness floor) but not eliminated here.
- **js/ts/ruby support.** No per-test run path exists for them; the matrix is
  go+python day one, fail-closed elsewhere.
- **Persisting the full T×M cell grid in DuckDB.** The store holds per-test rows
  (T rows); the full grid rides the tape for replay. Avoids T×M row explosion.
- **Making the matrix default-on.** It is opt-in (`--matrix`) — a deliberate heavy
  mode, never a silent tens-of-thousands-of-runs surprise on a normal audit.

---

## Architecture

Five components, each independently testable:

1. **`lang.Plugin.ListTests`** — runtime test enumeration (`internal/lang`).
2. **The matrix engine** — score every test against every mutant, concurrency-
   bounded (`internal/matrix` + a `tickMatrix` driver phase).
3. **`internal/matrixstore`** — per-test adequacy rows (DuckDB).
4. **Safe-to-delete candidate list** — surfaced in the verdict + `corral matrix`.
5. **Auto-CONFIRM tie-in** — the matrix drives critic adjudication into
   `criticscore` (refute AND confirm), with a soundness floor.

### 1. `lang.Plugin.ListTests(testPath string) (selectors []string, ok bool)`

Runtime enumeration in the jail (the toolchain is already required for scoring):
- **go:** `go test -list '.*' ./...` → parse the printed test names into
  `go test -run ^Name$`-compatible selectors (matching `SingleTestCmd`'s form).
- **python:** `pytest --collect-only -q <testPath>` → parse the `path::Class::test`
  node ids (matching `SingleTestCmd`'s pytest selector form).
- **ruby/js/ts:** `return nil, false` — matrix unavailable, fail-closed.

The returned selectors MUST be exactly what `SingleTestCmd(testPath, selector)`
accepts, so enumeration and single-test execution compose. Enumeration runs
through the jail (a new `Jail`/sandbox invocation that captures stdout), so a
malformed suite that can't be collected → `ok=true, selectors=[]` handled as
"no tests found" (fail-soft: the matrix is empty, not a crash), while an
unsupported language → `ok=false` (matrix skipped, verdict notes it).

### 2. The matrix engine

**`internal/matrix`** — a pure, testable orchestrator:

```go
type TestAdequacy struct {
    Selector, TestFile string
    Kills              int      // # run mutants this test caught
    KilledMutantIDs    []string // the cells it filled (for the tape/grid)
    MutantsTotal       int
    Scored             bool     // false if the test could not be scored (jail/baseline error)
    DeleteCandidate    bool     // Scored && MutantsTotal>0 && Kills==0
}
type Result struct {
    Rows         []TestAdequacy
    MutantsTotal int
    Catchable    bool   // union of KilledMutantIDs across all rows is non-empty
                        // (⇒ the mutant set is demonstrably catchable, not all-equivalent)
}
// Build scores each selector via scoreTest (which returns kills, killed IDs, and
// whether the test was scored at all), concurrently up to `workers`. Pure: no
// jail knowledge, driven by the callback, so unit-tested with a fake.
func Build(ctx context.Context, tests []struct{ Selector, TestFile string },
    mutantsTotal, workers int,
    scoreTest func(ctx context.Context, sel, testFile string) (kills int, killedIDs []string, scored bool)) Result
```

**Driver phase `tickMatrix`** (`internal/advpool/driver.go`), gated on
`RunSpec.Matrix`, runs after `tickDevAdequacy` (mutants staged on
`run.mutants`) and feeds `tickAggregate`:
- Enumerate via `p.ListTests(<the run's dev-test path>)` — the same test path
  `scoreWorkspace` places/uses; if `!ok`, skip the matrix (record "matrix:
  unavailable for <lang>").
- `scoreTest(sel)` = build the single-test command from `p.SingleTestCmd(testFile, sel)`,
  then run it against `run.mutants` — the same mechanism the critic auto-refute
  loop uses (driver.go:1218-1229), BUT via a scorer that returns the full
  `adequacy.Report` (not just killRate/survivors), because the matrix must
  distinguish two `killRate==0` cases the critic path did not:
  - `report.CompliantPass == false` (baseline couldn't pass) → **`scored=false`**
    (jail/collection failure) → not a delete-candidate, not adjudicable.
  - `report.CompliantPass == true` (baseline passed) → **`scored=true`**,
    `kills = len(report.Killed)`, `killedIDs = report.Killed`. `kills==0` here is
    a *genuine* zero-kill → a delete-candidate. This is the crux the plain
    `killRate>0` gate would wrongly collapse into "not scored".

  Concretely, add a `Scorer` method that surfaces the Report — e.g. `ScoreReport(ctx,
  codePath, code, test string, mutants []adequacy.Mutant, testCmd string)
  (adequacy.Report, error)` on `JailScorer` (it already computes the Report at
  gate.go:113, discarding `CompliantPass`/`Killed`) — so `scored`/`kills`/`killedIDs`
  come straight from `CompliantPass`/`len(Killed)`/`Killed`, no inference.
- Concurrency = the run's resolved `--swarm` width, via a bounded errgroup.
  `JailScorer.Score` is concurrency-safe (each call is an independent jail run
  with its own file map — verify no shared mutable state before parallelizing).
- Loud readout before running: `matrix: <T> tests × <M> mutants (<T*M> cells) across <N> workers` — the cost is never hidden. After: `matrix: <D> delete-candidate test(s)`.
- Store `run.matrix = result` for `tickAggregate`.

### 3. `internal/matrixstore` (DuckDB)

Mirror `bugcatch`/`criticscore` conventions (`Open(dsn)`, additive-migration
ledger, `#nosec`-clean perms, `Close`). Append-per-run (one row per test per
converged run), like bugcatch. Table `matrix_test_adequacy`:

| column | meaning |
|---|---|
| `ts`, `record_id`, `record_head` | run + signed-record linkage |
| `repo`, `commit`, `mission_id`, `lang` | provenance |
| `test_selector`, `test_file` | the test |
| `kills`, `mutants_total` | per-test adequacy |
| `delete_candidate` | `scored && mutants_total>0 && kills==0` |

Methods: `Record(ctx, recordID, head string, rows []Row)`; `DeleteCandidates(ctx) ([]Row, error)`
(latest per (repo,commit,test) where `delete_candidate`); `List(ctx)`. Guarded on
`record_id != 0` (parity with bugcatch). Only per-test rows persist; the full
cell grid rides the `--record` tape (a `pool_matrix` event with the per-test
`KilledMutantIDs`).

### 4. Safe-to-delete candidate list (human-gated)

A `TestAdequacy` with `DeleteCandidate==true` is surfaced as a candidate, never
acted on:
- On the verdict/tape: a `matrix` summary (T scored, D delete-candidates) + the
  candidate selectors.
- `corral matrix list` (over the brain HTTP API, DuckDB single-process — mirror
  `httpScorecardReader`) prints per-test adequacy + the delete-candidate list;
  `--json` carries the rows. `GET /api/matrix`.
- **Honest framing, stated everywhere it appears:** *"caught 0 of N planted
  mutants — review for deletion. Relative to this mutant set; a test may still
  guard behavior no mutant probed."* Corral hands a candidate list; the human
  deletes.

### 5. Auto-CONFIRM tie-in (feeds `criticscore`, with a soundness floor)

When `RunSpec.Matrix` is on, the matrix **drives** critic adjudication in
`tickAggregate` (replacing the single-test auto-refute loop for that run — the
matrix already scored every test). For each `whole-test` critic finding whose
`TestSelector` matches a matrix row:
- `Scored && Kills >= 1` → **refuted** (`source=auto`) — execution proved it can fail.
- `Scored && Kills == 0 && result.Catchable` → **confirmed** (`source=auto`) —
  the test caught none of a *demonstrably catchable* mutant set.
- otherwise (not scored, or `Kills==0` but the whole mutant set was
  uncatchable/equivalent, or no matching row) → **unadjudicated**.

`dead-check`-scoped findings are still **never** auto-touched (the guardrail from
the critic feature holds). Emitted through the existing `CriticFindingSink` →
`criticscore.Record`; the store's **human-wins** invariant means a human
adjudication is never clobbered. When `--matrix` is **off**, today's single-test
auto-refute-only path runs unchanged.

**The soundness floor + its honest limit.** `result.Catchable` (≥1 mutant killed
by *some* test) ensures we never confirm merely because every mutant was
equivalent/uncatchable. The residual risk — a test killing 0 because no mutant
targeted *its* code, not because it's vacuous — remains, since the matrix has no
coverage map. Documented plainly; a full coverage-aware confirm is future work.

---

## Data flow (end-to-end)

1. `tickDevAdequacy` scores the dev suite, stages `run.mutants` (unchanged).
2. If `RunSpec.Matrix`: `tickMatrix` enumerates tests (`ListTests`), scores each
   against `run.mutants` concurrently (reusing `Scorer.Score` per test with the
   `killRate>0` gate), builds `matrix.Result`, stores `run.matrix`, prints the
   cost + candidate readout.
3. `tickAggregate`: builds/signs the verdict (unchanged); if `run.matrix` present,
   (a) emits per-test rows to a new `MatrixSink` → `matrixstore` (brain path),
   (b) drives critic adjudication from the matrix (refute/confirm/unadjudicated)
   → `CriticFindingSink` → `criticscore`, (c) adds the matrix summary +
   delete-candidates to the verdict/tape.
4. `corral matrix` / `/api/matrix` surface per-test adequacy + candidates;
   `corral criticscore` shows the now-auto-confirmed findings; `corral scorecard`
   critic precision reflects the new confirms.

---

## Error handling / fail-closed / soundness

- **Unsupported language** (`ListTests ok=false`) → matrix skipped, verdict notes
  "matrix unavailable for <lang>"; never a crash, never a partial claim.
- **A test that can't be scored** (jail error, baseline `CompliantPass=false`) →
  `Scored=false` → excluded from delete-candidates AND from auto-confirm/refute
  (stays `unadjudicated`). The matrix reads `CompliantPass` directly (via
  `ScoreReport`), so a baseline-fail is never mistaken for a genuine zero-kill
  delete-candidate, and a genuine zero-kill is never mistaken for "not scored".
- **The matrix never gates a verdict** — it is measurement + advisory findings,
  exactly like bugcatch/criticscore. `aggregate` is still called
  `blockingFindingOpen=false`.
- **Auto never confirms without the catchable floor**, and human always overrides.
- **`record_id==0`** runs persist nothing (parity with the other stores).
- **Cost is never hidden** — the T×M readout prints before the work runs.

## Testing strategy

- `ListTests` per plugin (go: parse `go test -list` output; python: parse
  `pytest --collect-only -q`; ruby/js/ts → `ok=false`).
- `matrix.Build` (pure, fake `scoreTest`): correct per-test kills/candidates;
  `Catchable` true iff some test killed something; a `scored=false` test is never
  a delete-candidate; concurrency bound respected.
- `tickMatrix` (fake `Scorer` + fake `ListTests`): a vacuous test (kills 0) →
  delete-candidate; a load-bearing test (kills≥1) → not; a baseline-fail test →
  `Scored=false`, not a candidate.
- Auto-CONFIRM logic: `whole-test` + kills≥1 → refuted; + kills==0 + catchable →
  confirmed; + kills==0 + NOT catchable → unadjudicated; `dead-check` never
  touched; not-scored → unadjudicated. (The guardrail parity test from the critic
  feature, extended for the confirm path.)
- `matrixstore` round-trip + `DeleteCandidates` + `record_id==0` guard.
- End-to-end on the eval corpus (e.g. `eval/corpus/passwd_py`): a planted vacuous
  test shows as a delete-candidate and auto-confirms its `whole-test` finding.
- Deploy gate: `gofmt -l` clean, `bash scripts/check-security.sh` OK,
  `go test ./... -race` green.

## Rollout / scope / honesty

- **Opt-in `--matrix`** (+ `RunSpec.Matrix`, + a brain `start_adversarial_run`
  param, off by default). The cost readout prints the cell count.
- **go + python day one**; others fail-closed with a clear note.
- **Brain-path persistence** for the store + the human gate (like all metrics);
  `--local --matrix` computes + shows the matrix and drives adjudication inline,
  persisting only when a brain store is present.
- The "relative to the mutant set" and "0-kills ≠ provably vacuous" caveats are
  stated on the delete list, the CLI, and the docs. No overclaiming.

## Future (out of scope here)

- **Coverage-aware confirm** — map each test to the code it exercises so a 0-kill
  confirm is grounded in "a mutant *did* target its code," removing the residual
  ambiguity.
- **The full cell grid as a queryable store** (per-mutant "which tests catch it").
- **js/ts/ruby** single-test + enumeration, extending the matrix to all languages.
- **Federating** delete-candidate / attrition patterns into the shared corpus.
