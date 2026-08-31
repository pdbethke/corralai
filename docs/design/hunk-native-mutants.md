<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Hunk-native mutants — what chunking measured

Shipped 2026-08-30/31 on `feat/hunk-native-mutants`, the branch after the
cost telemetry (#173) printed its first line — `cost: 0.5M tokens in across
2 calls — test-writer` for a 36 KB file — and the cause was found in one
grep: the writer prompt carried the **whole mutated file per survivor**
(24 survivors × 36 KB), because `Mutant.Code` was a copy of the file and
every consumer inherited it.

## The rules

- **A mutant is its hunk.** `adequacy.Mutant{ID, ParentSHA256, Span,
  Search, Replace}` with `Apply(original)`; the mutated file exists only
  inside a tree, materialised at apply time. `Apply` is byte-identical to
  the old whole-file application — proven against a verbatim copy of the
  old algorithm on the tricky anchors, and against **both recorded real
  sets** (requests 39/39, flask 40/40 identical bytes) — and refuses an
  original whose sha256 does not match `ParentSHA256`. A refusal is an
  INVALID mutant (`anchor:` reason), never a kill or a survivor.
- **The recorded set is kilobytes.** `corral-mutants-2` stores hunks;
  `corral-mutants-1` documents still replay (each entry becomes a
  whole-file mutant, applied verbatim). Re-recording a v1 replay writes a
  v2 document that contains whole-file entries, and says so.
- **Prompts carry diffs, not files.** One renderer (`RenderHunk`):
  unified-diff blocks with dual numbering — original lines on `-` and
  before-context, mutated lines on `+` and after-context. The writer
  prompt is the file once plus one block per survivor; 24 survivors of a
  36 KB file fit in under 60 KB (asserted in tests).
- **One writer call per survivor, in parallel** (`--writer-mode
  per-survivor`, the default; `batched` keeps the old one-call shape).
  The shared prefix (file + signatures + rules) travels as the SYSTEM
  half of the prompt, byte-identical across a file's seats, so providers
  that cache prefixes do (Anthropic `cache_control`; Gemini implicitly);
  what the cache actually did is *recorded*, never assumed
  (`cached_input_tokens`, `cache_write_input_tokens`, NULL when the
  provider says nothing). Repair is per survivor; proof is per survivor,
  each authored test alone in its own tree. The operator receives the
  proven tests as one concatenated file (Go, Python, JS/TS, Ruby
  concatenators; an unmergeable part is handed back separately, rendered
  in full). Partial failure is disclosed (`writer: per-survivor (25
  calls, N seats ungraded)`); a survivor whose seat never graded is not
  an attempt row, not an agreement data point, and not a claim.
- **The cost shape is stated where the flag is:** each survivor's proof
  runs its own compliant baseline, so a file with N survivors pays N
  baselines where batched paid one — on a slow suite, that is the trade
  for per-survivor proving.
- **Generator shards see their symbols**, not the file: preamble + the
  shard's own bodies with elisions named; SEARCH anchors are still
  validated verbatim-and-unique against the **whole** original.
  Disclosed as `prompt_shape: chunk` (or `file` when any shard could not
  be sliced). Measured on a real 735-line file: each shard's prompt is
  7–19% of the whole-file prompt.
- **Disclosure, no generation bump.** `writer_mode` and `prompt_shape`
  ride the verdict, the report line, `scan_files`, the attestation and
  the warehouse; `--writer-mode` is part of the verdict cache key, so a
  cached verdict is only ever served to the mode that earned it. What a
  kill is and what proven means are unchanged; the dev-pass partition on
  a replayed set is byte-identical by construction.

## Measured

All `gemini-3.6-flash`, `--critic-model off`, `--substrate workspace`,
6 trees, 24 cores, replaying the recorded mutant sets (so the dev pass is
fixed and identical in every row — 0.40 / 40 mutants on flask, 0.59 / 39
on requests, in every single run). Three runs per mode per file; tokens
and calls from `corral_model_calls`.

| target | mode | proven (3 runs) | writer in (3 runs) | cached | wall (3 runs) |
|---|---|---|---|---|---|
| `flask/cli.py` (24 survivors, ~3 s suite) | pre-branch, whole-file survivors | 5 | 0.5M × 2 calls | — | 2m26s |
| | batched + diffs | **6 / 4 / 6** | 41.2k / 19.5k / 19.5k | — | 1m51s / 1m00s / 1m12s |
| | **per-survivor** | **21 / 23 / 22** | 0.4M ea (25–27 calls) | 0.2M / 0.1M / 0.1M | 3m14s / 3m38s / 3m51s |
| `requests/adapters.py` (16 survivors, ~77 s suite) | pre-branch, whole-file survivors | 12 | 0.1M × 1 call | — | 17m54s |
| | batched + diffs | **15 / 15 / 16** | 11.8k / 27.2k / 26.8k | — | 17m40s / 23m05s / 23m07s |
| | per-survivor | 16 / 14 / 15 | 0.1M ea (16 calls) | none reported | 34m04s / 34m16s / 34m06s |

Every one of the twelve runs (and the pre-branch references) produced the
**identical dev-pass partition** on its recorded set — 16/24 on flask,
23/16 on requests — verified mutant-by-mutant from `scan_mutants`.

What that says, plainly:

- **The diff prompts alone cut the writer's input ~12×** on flask (0.5M →
  41k per run) with everything else unchanged.
- **Per-survivor is the proving play, not the cost play.** On flask it
  proved 21–23 of 24 survivors versus 4–6 under
  batching — one unbuildable test no longer sinks the batch — at
  roughly triple the (still small) wall clock, with about half its gross
  input served from Gemini's implicit prefix cache.
- **On a slow suite the diff prompts already did the work.** Requests's batched mode proved 15–16 of 16 once the survivors arrived as diffs (11.8–27k tokens — the same file cost 122k with whole-file survivors), so per-survivor bought equivalent proving for ~13 extra minutes of per-proof baselines. The flag's guidance is now a measured fact: per-survivor for many-survivor/fast-suite files, batched for slow suites. Gemini reported no cached tokens on the requests fan-out — recorded as absent, not assumed.
- **The proven count remains a draw** run to run (the writer is a model);
  the dev-pass number is pinned by the replayed set and did not move
  anywhere.

## Residuals

- Per-survivor mode on the **jail** substrate serializes its N calls
  (`perFileSwarm` is 1 there); the fan-out is a workspace-substrate
  feature until the jail's per-file worker budget is revisited.
- The brain's hosted path stays batched by design (`RunSpec.WriterMode
  == ""`); one line to change when wanted.
- The `corral-agent` HTTP hop carries the system half by a shared typed
  struct; no wire-level test yet.
- The challenger yields fewer readable agreements under per-survivor
  runs (`Sufficient` follows the genuine overlap of both seats'
  attempts) — scarce and honest over plentiful and fabricated; the lever
  is the per-seat retry budget.
- The concatenators rename collisions textually, not by AST; the Ruby
  framework check is a regex.
- Pre- and post-branch numbers are not two samples of one measurement
  (the models see different prompts); every comparison above runs off a
  recorded set, and `writer_mode`/`prompt_shape` mark every row.
