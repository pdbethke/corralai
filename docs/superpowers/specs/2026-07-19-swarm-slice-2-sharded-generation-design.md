# Swarm slice 2 — sharded mutant generation + shadow challenger

**Status:** design (approved in brainstorm 2026-07-19, for build)
**Date:** 2026-07-19
**Author:** corral team
**Parent spec:** `2026-07-19-resource-aware-metrics-driven-swarm-design.md` (slice 2)

## The idea, in one line

Fan the single mutant-generator out into **N generators, one per group of
top-level symbols**, so every function in the file gets probed instead of
whatever one generator happened to pick — and run a **shadow challenger** over
the same regions so every run yields an execution-proven, region-controlled
head-to-head between generator models.

Slice 1 (05ae760) gave the run bounded concurrent workers. Slice 2 gives it
something to actually fan out *onto*.

## What changes, at a glance

| Before | After |
|---|---|
| 1 `mutant-generator` task, keyed by role name | N tasks keyed `mutant-generator/<i>`, one per symbol group |
| `--n-mutants` = the whole run's budget | `--n-mutants` = per-shard budget; `--max-shards` is the new width dial |
| `taskByKey(mid, RoleMutantGenerator)` | `tasksByRole(mid, RoleMutantGenerator)`, gated on all shards terminal |
| One unparseable result aborts the run | Bounded per-shard retry, then drop; shortfall recorded in the signed verdict |
| 1 generator row in `bugcatch_observations` | One row per shard, plus a paired shadow row |
| Generator model fitness inferred across runs | Same-region head-to-head every run |

## 1. What a shard is

A shard is **a group of top-level symbols, not a slice of the file.** Each
shard's prompt carries the WHOLE file as context, plus a directive naming the
symbols that shard is responsible for attacking.

This is forced by the patch-based mutant format (2739ec4): mutants are
SEARCH/REPLACE hunks anchored against the original file, validated by unique
anchor + round-trip + `Mutant.ParentSHA256`. Handing a generator a fragment
would break anchor uniqueness and the tamper-evidence chain. **Sharding changes
aim, not context.**

`ShardSymbols(sigs []repoindex.Signature, maxShards int) []Shard` bin-packs
symbols into at most `maxShards` groups, balanced by **complexity** (§4.1),
greedy hardest-first into the lightest bin — deterministic, no RNG. Balancing by
complexity rather than line span keeps a shard of gnarly branch-heavy functions
from being paired against a shard of one-line getters purely because the line
counts matched.

### Selection policy: bin-pack, never top-N

All symbols are packed into the available shards. `--max-shards` controls
**parallelism only, never coverage**. A 30-symbol file at `--max-shards 8`
yields 8 shards of ~4 symbols each — all 30 probed.

Rejected: top-N-by-size and exported-first. Both silently make the parent
spec's coverage claim ("every function gets probed") false while the readout
still says "sharded." "We probed 8 of your 30 functions" is exactly the kind of
thing that surfaces embarrassingly in a real audit.

### Degenerate cases collapse to today's exact path

One shard, whole file, no symbol directive — a **byte-identical prompt** to what
ships today — when any of:

- No signatures available. Two real causes: `CGO_ENABLED=0` builds
  (`signatures_nocgo.go` returns `ErrUnsupportedLang`), and languages with a
  tree-sitter grammar but no extractor. **Today only `go` and `python` have
  extractors** (`signatures_cgo.go`), so Ruby/JS/TS runs shard to 1 by default.
- `maxShards <= 1`.
- Fewer than 2 extracted symbols.

`ExtractSignatures` failure is already best-effort at both call sites
(`certify_local.go:333`, `internal/brain/advpool.go`), and stays that way: a
signature failure degrades to one shard, never refuses the run.

## 2. DAG change

`RunSpec.MaxShards int` — default 8, matching `localSwarmAutoCap`.

`BuildDAG` emits N `mutant-generator` tasks keyed `mutant-generator/0` …
`mutant-generator/N-1`, each `Title`d with its symbol group so the queue and the
cockpit show which region each seat is attacking. `test-critic` and
`test-writer` are unchanged.

In the driver, `taskByKey(missionID, RoleMutantGenerator)` becomes
`tasksByRole(...)`, and `tickDevAdequacy` gates on **all shards terminal**
(done, or dropped per §3) rather than one task done.

**Mutant IDs are shard-prefixed** (`s3/m1`). Collision avoidance across shards,
and a traceability win: every survivor — including the ones quoted into the
test-writer prompt via `renderTestWriter` — names the region it came from.

### Rollout: on everywhere, brain included

`BuildDAG` is called only from `advpool.Driver.StartRun`, whose callers are
`certify_local.go:338` and `internal/brain/advpool.go:483`. Sharding is **on by
default for both**. The hosted pool fans shards out across real remote workers,
which is where the parallelism story gets big.

Accepted consequence: this changes prod behavior on the commit that lands it.
The deploy wants a real audit run watched end to end before it's called done.
(Shadow followed the same path on the same rollout footing — see §8's note,
"Shadow on the brain: DECIDED 2026-07-19".)

## 3. Failure handling — honest, accountable, traceable

Per-shard retry counter in `runState`, keyed by task **key** (not task id), so a
lease-expiry re-claim and a parse-failure reopen both count against the same
budget — a shard cannot retry forever by alternating failure modes.

- K = 2 reopens on an unparseable result.
- Then the shard is **dropped**, and the run proceeds on the shards that parsed.

`Verdict` gains `RegionsProbed`, `RegionsTotal`, and `DroppedRegions []string`
(the symbol names that went unprobed), **carried into the signed statement**. A
partial audit is provably a partial audit. `--local` prints it alongside the
`swarm:` line.

Rejected alternatives: strict (one flaky shard aborts a run and wastes the other
seven seats' spend — sharding would make convergence *worse*); and
proceed-on-terminal with no record (a run where 6 of 8 shards failed prints a
confident kill-rate, indistinguishable from a good run at the CLI).

**Known limit:** `runState` is in-memory, so a brain restart mid-run loses the
retry counters and a restarted run retries from zero. Consistent with how the
driver already handles restarts.

## 4. Metrics — per shard, never summed

**One `bugcatch_observations` row per shard.** Summing shards back into a single
generator row would collapse 8 seats into 1 and make an underperforming seat
invisible by construction.

Additive columns (existing queries keep working):

- `shard` — index
- `region` — symbol group
- `region_lines` — the group's line span
- `region_complexity` — the group's summed complexity (§4.1)
- `test_complexity` — complexity of the dev test file (§4.2)

### The attribution problem, stated honestly

**Per-shard yield alone cannot show that an agent underperformed.** Shard A got
three tiny getters; shard B got the gnarly core parser. Different yield, same
model — that is region difficulty, not agent quality. Calling the low one
"underperforming" would be an unproven inference.

What makes it defensible:

1. **Complexity as the difficulty control** (§4.1) — effectiveness is read
   *conditioned on* complexity, not averaged across it.
2. **Cross-run aggregation** — sharding produces ~8 generator rows per run
   instead of 1, so the existing bugcatch aggregate sharpens ~8× faster and
   region noise averages out.
3. **The shadow challenger (§5)** — removes the confound outright.

### 4.1 Complexity, and why effectiveness must be weighted by it

An aggregate recall number hides the failure that matters most: a model that
performs well on simple functions and collapses on branch-heavy ones reads as
merely "average." Averaging across difficulty destroys exactly the signal a
per-task router needs.

**`repoindex.Signature` gains `Complexity int` and `Lines int`**, computed in
`signatures_cgo.go` during the existing tree-sitter walk — cyclomatic-style: 1 +
the count of branch, loop, case, catch, and boolean-operator nodes in the
symbol's subtree. Per-language node-type sets, alongside the existing per-language
extractors. The nocgo path needs no change: it returns no signatures at all, so
such a run is already a single unsharded shard per §1 and never reaches the
packer.

**Model effectiveness is reported per `(model, role, complexity band)`, on the
SHADOW side only** — not pooled, and, as of this branch, not on the primary
side. Bands are computed from the corpus tertiles rather than hardcoded
thresholds, so they stay meaningful as the corpus grows and across languages
whose complexity distributions differ.

**Why the asymmetry, stated honestly:** `mutants_survived` is recorded
differently on the two sides, and correctly so. The shadow challenger scores
each region's mutants in its own `Scorer.Score` call (§5), so its survivor
count IS genuinely per-region — a shadow bugcatch row's `region`/
`region_complexity` accurately describes what was scored. The primary side
does not: `tickDevAdequacy` scores the MERGED mutant set across all
all-shards-terminal shards in ONE `Scorer.Score` call (§2, §3), because the
dev suite is graded once against the whole exam, not once per shard — so the
run's single survivor/kill-rate figure is parked on one bugcatch row (today,
shard 0's) rather than truly attributed per shard. Recording that figure
against each shard's `region`/`region_complexity` would be fabricating
per-region data the run never actually measured.

**Consequence:** whole-file aggregate comparison across models (the shadow
head-to-head, §5) is sound today. **Per-region and per-complexity-band potency
for the PRIMARY generator cannot be computed from what this branch records** —
only the shadow side supports that cut. This is not abandoned, just not yet
supported: closing it would need the primary side to score per-shard
survivors the way the shadow side already does (e.g. scoring each shard's
merged-in mutants separately before folding them into the run's aggregate
kill-rate) — deferred, not scheduled, as of this branch.

Once the primary side supports it, this becomes the concrete substrate for the
per-task complexity router: "route this symbol to the model with the best
proven potency *at this symbol's complexity band*" — the execution-grounded
version of the routing the parent spec contrasts against Fugu's learned head.
Today only the shadow challenger's numbers can honestly be read that way.

### 4.2 Test complexity — and an honest limit

`test_complexity` is the same measure applied to the dev test file, so
effectiveness can be conditioned on **both** axes: how hard the source is, and
how sophisticated the suite defending it is. A model that only wins against
naive suites is a materially different proposition from one that wins against
rigorous ones.

**The limit, stated plainly:** slice 2 can only record test complexity at
**file** granularity. Attributing a specific test to a specific region requires
knowing which tests exercise which code — which is precisely what the
`tests × mutants` matrix (slice 5) establishes by execution. Until that lands,
`test_complexity` is a whole-suite property, and any per-region test-complexity
claim would be unproven. The column is recorded now so slice 5 can refine it
in place rather than backfill.

### Directly attributable per-shard signals

These need no region control, because the region cannot cause them:

- **parse-failure retries** (0/1/2 before drop) — a seat that cannot return
  well-formed output is underperforming, full stop
- **drop** — produced nothing usable after K tries
- **latency** — the `task_done.ts − task_claimed.ts` delta, which is also
  slice 3's aggregation input

Each shard also emits a `d.emit` event on completion (shard index, symbols,
mutants produced, dropped-or-not, duration) — feeding the `--record` tape, the
cockpit, and telemetry from one write.

## 5. The shadow challenger

### Why not "a different model per shard"

Assigning model X to some shards and model Y to others is **not** a controlled
comparison — it is confounded by region in exactly the way raw per-shard yield
is. It also breaks the exam: the mutant-generator *sets the difficulty*, so a
weaker model on shard 3 plants easier mutants, the dev suite kills them, and the
file's kill-rate goes **up**. Certification would be applying a fixed
`localCertifyThreshold = 0.8` to a blended exam whose difficulty varies by run
and by which model drew which region. Rejected.

### The shape

Every shard is attacked **twice**: once by the primary generator model, once by
a challenger. Same region, same file, same goal, same commit — the confound is
removed rather than swapped.

- **Primary mutants alone feed dev-adequacy and the verdict.** Exam difficulty
  is unchanged; certification means exactly what it means today.
- **Shadow mutants are parsed, scored in the jail, and recorded** — never
  aggregated into the verdict's kill-rate.

Scoring the shadow mutants (rather than only counting them) is what makes the
comparison worth having: it measures not just who plants more *usable* mutants
but who plants mutants that actually **survive a good suite** — the real measure
of an adversary. Execution-proven potency, head-to-head, same region.

**Narrowing this claim to what is actually recorded (see §4.1):** the shadow
side genuinely scores each region separately (its own `Scorer.Score` call per
shard), so shadow-vs-shadow-across-runs and whole-file shadow-vs-primary
aggregate comparisons are sound. But the primary side scores the run's
MERGED mutant set in one call, so it has no per-region survivor count to put
on the other side of a *per-region* head-to-head — today's comparison is
"the challenger's region-scoped potency" against "the primary's whole-file
potency," not two truly region-scoped numbers. Closing that requires scoring
the primary side per-shard too (see §4.1's note); until then, read
"same-region head-to-head" as a same-region, same-input comparison of what
each model *plants*, with genuinely region-scoped *survival* numbers only on
the shadow side.

### Structural exclusion (the load-bearing invariant)

**Shadow work gets its own role key: `mutant-generator-shadow`.** So
`tasksByRole(missionID, RoleMutantGenerator)` structurally cannot return a
shadow task.

This is deliberately not a boolean on the task. This is the gate; a flag is a
thing someone forgets to check at one of four call sites. Exclusion by
construction.

### Defaults and cost

- On by default; challenger = `claude-haiku-4-5` (already the critic model —
  same `ANTHROPIC_API_KEY`, no new credential, cheap).
- `--shadow-model` overrides; `--shadow-model ""` disables.
- Cost: roughly doubles generator API calls, and adds a second scoring pass of
  jail runs (the wall-clock cost). Both bounded by `--swarm`.
- The run **announces it out loud**, the way `swarm:` already does:
  `shadow: 8 challenger seats (claude-haiku-4-5) — recorded, never gating`

### What it does NOT measure

Shadow rows measure **generation** quality (usable mutants, potency, retries,
latency). They are not a catching-recall signal, and `recall` must never be
computed from them.

## 6. Invariants that must not regress

- **Correctness unchanged.** Kill-rate is still execution-proven in the jail.
  Sharding changes how many generators run and what each aims at — never what
  counts as killed.
- **The exam's difficulty is set by one model.** Only primary-model mutants
  reach dev-adequacy.
- **Fail-closed stays fail-closed.** No signatures → 1 shard, never "skip
  generation." Jail resolution is untouched.
- **Coverage is never silently cut.** `--max-shards` bounds parallelism;
  shortfall from drops is in the signed record.
- **Decorrelation still enforced.** `CheckDecorrelation` runs before any I/O;
  the shadow role is additive and never grades anything.

## 7. Build sequence

Gate-critical work lands and is proven before the shadow layer goes on top.

0. **`Signature.Complexity` + `Lines`** — extend the tree-sitter walk in
   `signatures_cgo.go` (go + python), nocgo returns 0. Table-driven tests over
   known-complexity fixtures. Additive to an existing struct; nothing consumes
   it yet, so it lands safely on its own.
1. **`ShardSymbols` + bin-packing** — pure function, complexity-balanced,
   table-driven tests (degenerate cases, balance, determinism).
2. **`RunSpec.MaxShards` + `BuildDAG` fan-out** — N task specs, shard-prefixed
   titles; assert 1-shard case is byte-identical to today's prompt.
3. **Driver collection** — `tasksByRole`, all-shards-terminal gate, mutant union
   with prefixed IDs. **The careful one.** Tests: partial completion, ordering.
4. **Per-shard retry + drop + verdict coverage fields** — including the signed
   statement carrying `RegionsProbed`/`DroppedRegions`.
5. **Per-shard bugcatch rows + emit events** — the five columns, per-shard rows,
   complexity-banded effectiveness reporting.
6. **Shadow challenger** — new role key, second scoring pass, paired rows,
   readout line. Test that asserts a shadow mutant can never reach
   dev-adequacy.

Steps 1–4 are shippable on their own and constitute slice 2 proper; 5–6 are the
metrics/comparison layer the parent spec had deferred to slice 4, pulled forward
because runs made before it exists produce data that cannot answer "did this
agent underperform."

## 8. Open questions

- **Complexity measure:** cyclomatic-style branch counting is a proxy, and its
  node-type set is per-language, so cross-language complexity numbers are not
  strictly comparable. Tertile banding within a corpus mitigates this; a
  language-normalized measure is deferred until there is enough multi-language
  data to calibrate against.
- **Band count:** tertiles are the first cut. Whether three bands are enough
  resolution for routing is an empirical question the first corpus of runs
  should answer.
- **`--max-shards` default of 8:** matches `localSwarmAutoCap`. Whether the
  brain should carry a different default (its fleet has more workers) is
  deferred until the fleet path has real shard runs behind it.
- **Shadow on the brain: DECIDED 2026-07-19, enabled before a watched prod
  run.** This section originally read "ship it on for `--local` and evaluate
  the brain default after the first watched prod run" — the project owner has
  since decided to turn it on for the hosted daemon NOW, in the same change
  that landed sharding for the hosted path (`internal/brain/advpool.go`'s
  `StartAdversarialPool` now sets `RunSpec.MaxShards` and resolves
  `CORRALAI_ADVPOOL_SHADOW_MODEL`, mirroring `certify --local`). Stated
  honestly: this reverses the original rollout plan's ordering. Shadow is on
  for the hosted brain BEFORE the "first watched prod run" gate this section
  called for was met. The accepted tradeoff: sharding itself (§2's "on
  everywhere, brain included") already carried the "changes prod behavior on
  the commit that lands it, wants a watched run" caveat, and shadow riding
  the same commit was judged not to materially change that risk profile — the
  two features share the same failure surface (the driver's tick loop, the
  hosted worker-claim path, the deadline backstop) and watching one run
  covers both. What DID change to make this safe to ship without waiting:
  the review that prompted this note also caught and fixed two gaps the
  original "ship --local first" caution was implicitly relying on staying
  --local-only to avoid — (1) the hosted `RunDeadline` was never widened by
  `advpool.ShadowTimeBudget` the way `--local`'s `resolveRunDeadline` already
  was, so shadow work could force a timeout `needs-review` verdict on the
  hosted path specifically (fixed: `resolveAdvPoolRunDeadline` in
  `internal/brain/advpool.go`, sharing `advpool.ResolveRunDeadline` with the
  CLI); and (2) an unservable shadow seat (e.g. an all-ollama fleet asked to
  run the default `claude-haiku-4-5` challenger) had no bound on how many
  times it could cycle claim→404→release→reclaim, continuously consuming
  workers the primary shards need to converge — a failure mode that only
  exists once shadow tasks are dispatched onto a REAL, heterogeneous remote
  worker fleet, which `--local`'s single in-process worker never exercises
  (fixed: `cmd/corral-agent`'s `handleTaskError` now abandons a shadow seat
  as unmeasured, via `advpool.ShadowProviderFailedResult`, on the FIRST such
  failure. An earlier version of this fix tracked consecutive failures
  server-side per task id via a new `bump_unreachable_attempts` brain tool
  before abandoning — that tool shipped with no authorization (its handler
  discarded the caller's identity, so any principal could bump the counter
  for a task it never claimed) and an unbounded process-wide map, and was
  removed rather than gated: a shadow seat is measurement that can never
  gate a verdict, so losing one region's comparison to a single
  unreachable-model failure is far cheaper than an ungated mutator in the
  control plane).
  `MaxShards` is additionally ceilinged at `maxAdvPoolShards` for a hosted
  run (`internal/brain/advpool.go`), closing a related cost-escape-hatch gap
  the floor-of-one-mutant-per-shard clamp had. With those closed, "the first
  watched prod run" becomes the verification step for an already-shipped
  default, not the gate before shipping it — a deliberately different (and
  weaker) posture than this section originally called for, recorded here so
  the spec doesn't quietly contradict the code.
