<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Shadow-seat selection — the challenger is drawn, the verdict is not

**Status: designed, not built (2026-10-04).** Today an operator names at most
one challenger per shadow seat, by hand, and the scorecard learns about
exactly the models someone thought to type. This note lets an operator name
a *pool* of challengers instead, and has each run draw one by Thompson
sampling over the evidence the scorecard already holds. The draw touches the
two shadow seats only. Every seat that can move a verdict stays exactly as
the operator named it.

## The decision this implements

Two options were on the table (handoff, 2026-10-01):

- **(a)** Thompson sampling chooses the **shadow** seats only. Verdict seats
  stay operator-named.
- **(b)** Also replace the brain's greedy verdict re-seater
  (`advPoolAssign`, `internal/brain/advpool.go`) with Thompson sampling,
  every fence kept.

**The founder chose (a), for both shadow seats.** The reason is in
[model-ranking.md](model-ranking.md): *disclosure, never selection.* That
rule came from a production incident in which performance statistics
overrode the configured model list and re-selected a retired model,
permanently, and nobody looked because the routing had been "earned". (a)
keeps statistics out of every seat that gates. It also addresses that
incident's actual mechanism, which was greedy: routing that never re-tests
an early winner. Thompson sampling keeps re-testing by construction.

The shadow seats are the right place to start because they already never
gate. `Shadow` on a seat is the driver's own marker for that
(`internal/advpool/driver.go`), and shadow rows are already flagged in the
scorecard (`bugcatch_observations.shadow`). The randomized comparison arm
this design needs already exists. What is missing is randomization.

## Slice one — the scorecard learns the language

This ships first and stands on its own.

`bugcatch_observations` gains two columns, added through the existing
`bugcatchObservationsMigrationCols` path so that an old store upgrades in
place on its next open:

| column | type | meaning |
|---|---|---|
| `lang` | `VARCHAR` | the run's language plugin name |
| `shadow_drawn` | `BOOLEAN` | this shadow seat's model was drawn from a pool, not named |

Both sinks pass `lang` in: `localBugCatchSink`
(`cmd/corral/certify_local_bugcatch.go`) and the brain's
`advpoolBugCatchSink`. Two sinks write this table, so both get the rule.

**Rows written before the migration keep `lang` NULL.** They do not get `''`,
and nothing guesses a language for them. A row whose language was never
recorded must not be read as evidence about any language, so old rows feed
no posterior. `models rank` does not change in this slice. The column is
there to group by later.

## The posterior

Each pool member gets a **Beta(1 + successes, 1 + failures)** posterior,
computed from scorecard rows matching that model, that base role and that
run's language:

| seat | success | trials |
|---|---|---|
| shadow test-writer | `catches` | `opportunities` |
| shadow mutant-generator | `mutants_survived` | `mutants_planted` |

- **The writer's** metric is exactly the one `models rank` uses: proven
  gaps per survivor attempted.
- **The generator's** trial is a planted fault, and it succeeds only if it
  became a valid fault the dev suite missed. A fault that does not build
  counts as a failure, so a generator cannot look good by planting junk.
- **Rows from both primary and shadow seats count.** It is the same model
  doing the same job, so `role` is matched on the **base** role and writer
  evidence feeds writer posteriors only.
- **Dropped rows (`dropped = true`) are excluded.** The seat did not
  finish, so its zeros were never measured.
- **A model with no rows gets Beta(1, 1).** That flat prior is what makes
  an untested candidate likely to be drawn, which greedy routing never
  allowed.

**There is deliberately no evidence floor and no recency decay.** The fence
against the retired-model incident is a different one: the pool is only
ever what the operator typed on this command line. A model nobody typed
cannot be drawn, whatever its history says. Decay is left for a later
change, if the data shows early winners lingering.

## The draw happens once, at the flag layer

A new package, `internal/shadowpool`, is pure apart from reading the store:

```go
Draw(pool []string, role, lang string, history History, rng *rand.Rand) Selection
```

Each command calls it **once, immediately after parsing flags, and writes
the drawn model into the existing `shadowModel` / `shadowWriterModel`
string.** From then on every existing rule sees an ordinary named
challenger and applies unchanged: registry resolution, the vendor check on
the challenger seat in `certify_local.go`, credential resolution, the
verdict-cache key and the signed record.

This is the main design choice. corral's most frequent defect has been a
rule enforced at one door and missing at another. A draw that resolves to a
plain model name before any door is reached adds no new door for a rule to
be missing from.

## The doors

Each pool flag is registered by **one shared helper**, beside the flag it
replaces: `--shadow-pool` wherever `--shadow-model` is registered,
`--shadow-writer-pool` wherever `--shadow-writer-model` is.

Today that is `certify --local` and `certify --repo` for both, and
`doctor` for `--shadow-model` only. **`doctor` has no
`--shadow-writer-model`**, so it cannot check the challenger writer's
credential at all. That is an existing gap of the same rule-at-one-door
kind, and this design closes it: `doctor` gains `--shadow-writer-model`
and `--shadow-writer-pool`.

Pools are **validated before the draw.** Every member must resolve through
the model registry and have a usable credential. If any member fails, the
run is refused and the refusal names that member. Validating only the
drawn member would let a command pass ten times and fail on the eleventh,
depending on the draw. `doctor` checks every member the same way.

The run is also refused if:

- `--shadow-model` and `--shadow-pool` are both given (same for the writer
  pair): a seat is either named or drawn;
- a pool has fewer than two members (name it with `--shadow-model`);
- a pool contains `off` or a duplicate.

**`certify --repo` draws once per invocation, not once per file.** Every
file in the run then shares one challenger, so the per-file Jaccard and
kappa the writer-shadow records stay comparable across the run. A per-file
draw would give more samples per run, and is left for later.

## Disclosure

`RunSpec` gains `ShadowSelection`, holding for each drawn seat:

- the pool
- each member's α, β and sampled value
- the chosen model
- the seed
- the history state: `read` with a row count, or `not read`

It is copied in `verdictFromSpec` (`internal/advpool/aggregate.go`). Both
verdict construction sites (`tickAggregate` and `timeoutVerdict`) start
from that function, so the selection reaches the signed record on a
converged run and on a timed-out one alike. Scorecard rows from a drawn
seat carry `shadow_drawn = true`. Stderr gets one line per drawn seat:

```
challenger drawn from a pool of 3: gemini-3.6-flash (α=12 β=31, sampled 0.29) — seed 0x5f…
```

## When there is nothing to read

- **If the scorecard store will not open,** the draw still happens with
  every member on the flat prior, and the record says **`history: not
  read`**. This is a could-not-measure, and it is labelled as one: it must
  never render as members with zero evidence.
- **If the store opens but holds no rows for this language,** the record
  says `history: 0 rows for <lang>`. That is a measured zero, and it is
  reported as one.

**The seed** comes from `crypto/rand` and is recorded. A hidden
`--shadow-seed` flag accepts it back, so a reviewer can reproduce the exact
draw from the record.

## Tests

1. **`shadowpool` unit tests:**
   - the posterior arithmetic;
   - dropped rows excluded, NULL-`lang` rows excluded;
   - a fixed seed always gives the same choice;
   - over many seeds, a candidate with no history and a candidate with a
     strong record are both drawn at a frequency well above zero. This is
     the regression guard for the incident.
2. **A door test** that walks every command's flag set and asserts that any
   command registering `--shadow-model` also registers `--shadow-pool`, and
   any command registering `--shadow-writer-model` also registers
   `--shadow-writer-pool`. The list is derived from the flag sets, never
   enumerated by hand: an enumerated list is exactly how a fourth door
   would go unguarded. A second assertion pins `doctor` to both writer
   flags, because the derived check alone would pass on a door that is
   missing both.
3. **`ShadowSelection` reaches the verdict on both paths,** converged and
   timed out.
4. **A pool with one uncredentialed member is refused** before any jail,
   store or spend, and the refusal names the member.
5. **Migration:** an old store gains `lang` and `shadow_drawn` on open, and
   its existing rows read back NULL for `lang`.

## Not in this design

- The LLM judge (`internal/mission/routing.go`, Sense → Judge → Clamp)
  proposing a lineup. Proposing without seating changes nothing that runs.
- Per-file draws in `certify --repo`.
- Recency decay or an evidence floor on the posterior.
- Reading history from the ledger or a pushed warehouse instead of the
  local store.
- Any verdict seat. Moving statistics into a seat that gates is option
  (b), and needs its own decision.
