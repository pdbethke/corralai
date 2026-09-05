<!-- SPDX-License-Identifier: Elastic-2.0 -->

# Before and after: the same repository, the same day, two exams

**Status:** the tables below are GENERATED — never hand-edit them. They come
from `docs/design/fixtures/before-after-requests-before.json` and
`-after.json` (each the slimmed output of `corral scans show <id> --timing
--json` against the ledger that run wrote) via `scripts/gen-before-after.py`,
checked in CI by `scripts/gen-before-after.py --check`. The same tables are
spliced into the field note
[What is actually new here](../../site/src/content/field-notes/what-is-actually-new-here.mdx)
between the same markers.

What changed between the runs, all merged the same afternoon: `--top` became
one bound over every door (#244); the authored pass runs the authored test
alone for its baseline, canary and positive control (#247); the mutant
budget is derived from the file's complexity and fitted to the clock (#247);
every verdict carries its 95% interval and the exam's reach (#248). The
after-run also names a shadow writer.

<!-- before-after:start -->

**Two runs of `corral certify --repo` on `psf/requests@414f051`, same herd, same 30-minute per-file timeout, 2026-09-04.** Generated from the two runs' own ledgers by `scripts/gen-before-after.py`; never hand-edited.

| run | files audited | converged | proven gaps | mutants graded | time in audited files | model calls | tokens in / out |
|---|---|---|---|---|---|---|---|
| before (main @ 925dddc) | 8 | 2 of 8 | **11** | 255 | 3h28m | 232 | 1.9M / 68.6k |
| after (main @ d951419) | 3 | 2 of 3 | **27** | 112 | 1h12m | 136 | 2.3M / 89.7k |
| primed (main @ 7f49ddf, --prior on the after-run's record) | 3 | 3 of 3 | **30** | 112 | 1h14m | 117 | 2.0M / 76.8k |

*Time in audited files* sums each audited file's own phases plus the one selection pass; the runs' clock times — **4h15m** before, **1h20m** after, **1h22m** primed, from the launcher logs — are longer by the files the scan probed and then could not grade (three baseline failures before, none after) and by setup nothing attributes to a file.

The kill rates below are **not** a before/after of requests' tests: the exam changed (the *mutants* column says how — a flat five per seat became a complexity-derived budget), so a rate over one exam is not comparable to a rate over the other. What *is* comparable across the runs: wall clock, whether a file converged, the gaps proven by execution, and the width of each rate's 95% interval. The before-run's reach reads *not recorded* because mutant spans were not stored until #248.

### before (main @ 925dddc)

| file | mutants | kill rate (95% interval) | survivors | proven | reach | dev pass | authored | total | |
|---|---|---|---|---|---|---|---|---|---|
| _internal_utils.py | 10 (flat, 5 per seat) | 0.90 (0.60–0.98, n=10) | 1 | 1 | not recorded | 8m37s | 2m21s | 12m24s | converged |
| _types.py | 20 (flat, 5 per seat) | 0.25 (0.11–0.47, n=20) | 15 | 0 | not recorded | 7m25s | 22m45s | 31m37s | timed out |
| adapters.py | 37 (flat, 5 per seat) | 0.49 (0.33–0.64, n=37) | 19 | 0 | not recorded | 11m38s | 18m19s | 31m37s | timed out |
| api.py | 39 (flat, 5 per seat) | 0.08 (0.03–0.20, n=39) | 36 | 0 | not recorded | 3m56s | 25m51s | 31m05s | timed out |
| auth.py | 34 (flat, 5 per seat) | 0.71 (0.54–0.83, n=34) | 10 | 10 | not recorded | 2m00s | 3m16s | 6m01s | converged |
| cookies.py | 37 (flat, 5 per seat) | 0.32 (0.20–0.49, n=37) | 25 | 0 | not recorded | 10m52s | 18m58s | 31m37s | timed out |
| structures.py | 40 (flat, 5 per seat) | 0.72 (0.57–0.84, n=40) | 11 | 0 | not recorded | 10m28s | 19m38s | 31m37s | timed out |
| tests/testserver/server.py | 38 (flat, 5 per seat) | 0.47 (0.32–0.63, n=38) | 20 | 0 | not recorded | 19m13s | 10m32s | 31m22s | timed out |

### after (main @ d951419)

| file | mutants | kill rate (95% interval) | survivors | proven | reach | dev pass | authored | total | |
|---|---|---|---|---|---|---|---|---|---|
| adapters.py | 40 (complexity) | 0.59 (0.43–0.74, n=37) | 15 | 15 | 14/19 symbols, 28/53 decisions | 17m01s | 3m29s | 22m17s | converged |
| models.py | 40 (complexity) | 0.47 (0.33–0.63, n=40) | 21 | 0 | 35/51 symbols, 42/149 decisions | 24m02s | 5m49s | 31m37s | timed out |
| utils.py | 40 (complexity) | 0.66 (0.49–0.79, n=35) | 12 | 12 | 29/44 symbols, 40/143 decisions | 9m56s | 5m43s | 17m26s | converged |

### primed (main @ 7f49ddf, --prior on the after-run's record)

| file | mutants | kill rate (95% interval) | survivors | proven | reach | dev pass | authored | total | |
|---|---|---|---|---|---|---|---|---|---|
| adapters.py | 40 (complexity) | 0.65 (0.49–0.78, n=37) | 13 | 8 | 13/19 symbols, 25/53 decisions | 17m07s | 3m19s | 22m03s | converged, primed (37 prior edits) |
| models.py | 40 (complexity) | 0.69 (0.54–0.81, n=39) | 12 | 11 | 32/51 symbols, 54/149 decisions | 19m18s | 8m38s | 29m51s | converged, primed (40 prior edits) |
| utils.py | 40 (complexity) | 0.67 (0.50–0.80, n=36) | 12 | 11 | 34/44 symbols, 43/143 decisions | 10m33s | 8m58s | 21m23s | converged, primed (35 prior edits) |

### The file both runs audited

`--top 3` chose different files before and after (#244 stopped evidence widening from adding files past the bound, and stopped `tests/utils.py` from out-ranking the library file it was named after), so only these appear in both:

| file | | mutants | kill rate (95% interval) | survivors | proven | authored phase | total | |
|---|---|---|---|---|---|---|---|---|
| adapters.py | before | 37 (flat, 5 per seat) | 0.49 (0.33–0.64, n=37) | 19 | 0 | 18m19s | 31m37s | timed out |
| adapters.py | after | 40 (complexity) | 0.59 (0.43–0.74, n=37) | 15 | 15 | 3m29s | 22m17s | converged |
| adapters.py | primed | 40 (complexity) | 0.65 (0.49–0.78, n=37) | 13 | 8 | 3m19s | 22m03s | converged, primed (37 prior edits) |

### Cumulative reach — what the prior bought

Decision points a fault landed on, per run and across both, from the recorded mutant spans of the after-run and the primed run against the extractor's decision spans. If the prior had done nothing, the union would sit near the larger of the two; it sits near their sum.

| file | decision points | after-run reached | primed run reached | **both runs together** |
|---|---|---|---|---|
| adapters.py | 53 | 28 | 25 | **38** |
| models.py | 149 | 42 | 54 | **79** |
| utils.py | 143 | 40 | 43 | **68** |

### By model

Per seat, from `scan_model_calls`. The after-run adds a **shadow writer** (`claude-sonnet-5`) that attacked the *same survivors* as the primary writer in the *same run* — the only comparison between two models that is controlled. Its per-file outcome is on the run log (`the challenger writer … proved N of M survivor(s)`); the ledger records the pair's overlap only when the union of both writers' misses reaches the minimum the coefficient needs, and on these files it did not (both writers proved nearly everything), so the Jaccard column is honestly empty rather than a number over two misses — until the primed run's `adapters.py`, where the two writers' misses reached the minimum and the coefficient was computed: both missed 1 of the 5 either missed, Jaccard 0.200.

| run | seat | model | calls | tokens in / out | model wall clock |
|---|---|---|---|---|---|
| before | mutant-generator | `gemini-3.6-flash` | 54 | 77.2k / 20.6k | 16m16s |
| before | test-critic | `claude-haiku-4-5` | 41 | 944.1k / 17.1k | 2m49s |
| before | test-writer | `gemini-3.6-flash` | 137 | 908.4k / 30.9k | 37m05s |
| after | mutant-generator | `gemini-3.6-flash` | 24 | 65.9k / 10.2k | 6m05s |
| after | test-critic | `claude-haiku-4-5` | 16 | 419.6k / 5.8k | 1m14s |
| after | test-writer | `gemini-3.6-flash` | 48 | 746.4k / 8.4k | 10m54s |
| after | test-writer-shadow | `claude-sonnet-5` | 48 | 1.0M / 65.3k | 10m50s |
| primed | mutant-generator | `gemini-3.6-flash` | 24 | 143.4k / 9.9k | 7m25s |
| primed | test-critic | `claude-haiku-4-5` | 15 | 437.1k / 5.7k | 1m07s |
| primed | test-writer | `gemini-3.6-flash` | 41 | 640.8k / 7.0k | 9m29s |
| primed | test-writer-shadow | `claude-sonnet-5` | 37 | 781.8k / 54.2k | 9m05s |

<!-- before-after:end -->

## How to read it

- **Duration and convergence are the before/after.** Same commit, same
  suite, same timeout, same models: 4h15m and six timeouts became 1h20m and
  one (clock times from the launcher logs, `run-rest.out` / `run-after.out`;
  the table's per-file sums are the ledger's), and the one is a 900-line hub file (`models.py`, 300 covering tests)
  whose dev pass alone was 24 minutes — three mutants hung to the five-minute
  per-mutant cap. That is the next lever (first-failing-test ordering), and it
  is not in this table.
- **Proven gaps are the before/after.** 11 became 27, on three files instead
  of eight, because the proof phase now converges: no seat was left
  unattempted on the two files that finished.
- **Kill rates are not.** The before-run planted 39 faults on `api.py` — eight
  one-line wrappers — and the after-run would plant 8; the after-run planted
  40 on `models.py` where the before-run could not grade it at all. A rate is
  a property of the exam it was scored on, and the *mutants* column names the
  exam. The intervals say the rest: the before-run's narrow `0.03–0.21` on
  `api.py` was precise about an exam that measured almost nothing.
- **Reach is new.** The after-run says which symbols and decision points a
  fault actually landed on; the before-run cannot, because spans were not
  recorded. `adapters.py` reached 28 of 53 decision points with 40 mutants —
  the ceiling — which is the case for a larger ceiling on a large file, and
  for saying so rather than calling 0.59 a grade.
- **The primed run is the third exam.** Same commit, same herd, the
  after-run's ledger and mutant set handed back as `--prior`: every file was
  told the 35–40 edits already tried on its bytes and asked for different
  ones. Three of three converged, 30 gaps proven, and the cumulative-reach
  table says the second exam went to places the first did not — 79 of
  `models.py`'s 149 decision points across both runs against 42 and 54 alone.
  Its kill rates are not comparable to either earlier run's; its convergence,
  its proven gaps and its reach are.
- **By model is one repository's.** The shadow writer proved 10 of 12 on
  `utils.py` and 15 of 15 on `adapters.py` against the same survivors the
  primary proved 12 of 12 and 15 of 15 on; both writers missing almost
  nothing is why the overlap coefficient is withheld. `corral models rank`
  prints a ranking from this evidence and never staffs a seat.
