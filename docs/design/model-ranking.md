<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Ranking the seats — what the evidence says, and nothing more

**Status: `corral models rank` ships reading two sources — the local
bug-catching ledger (the default) and a pushed warehouse via `--db`.** The
critic seat is scored only from the local adjudication store, because
adjudication is never pushed. The goal-deriver is not scored at all, on
purpose; the last section says why.

## The question

corral records a great deal about its own runs: which model sat in which seat,
what it produced, and — because every outcome here is execution-proven — what
happened when that output met a real test suite. `corral scorecard` already
prints a model × role table. What it does not do is answer the operator's
actual question, which is not "what are the numbers" but **"given what I have
run, which of the models I am willing to run has earned this seat?"**

`corral models rank` answers that, and refuses to answer it in three specific
situations where an answer would be worse than a shrug.

## One metric per seat, because the seats do different jobs

A single leaderboard number across the seats is a category error. The seats
produce different artifacts, judged by different evidence.

**`test-writer` — proven gaps per survivor attempted.** The writer is handed
surviving mutants and asked to author a test that catches them. There is
exactly one outcome that matters: did the authored test compile, run, and kill
the fault. Not whether it looked plausible, not whether the critic liked it,
not how many tests it wrote. `catches / opportunities` — execution-proven
catches over survivors attempted — is that outcome and nothing else.

**`mutant-generator` — valid mutants the dev suite missed, per run.** A
generator can fail in two opposite directions, and a single yield number hides
one of them. It can plant faults that do not build (caught by the compile gate,
counted as invalid), or it can plant faults so trivial that the repo's existing
tests kill every one of them. The second failure looks like *success* on any
raw yield metric: 100 mutants planted, 100 graded, a 100% kill rate, and
nothing learned. So the metric is the count of valid, graded mutants the dev
suite **failed** to kill, per run. That is yield and difficulty in one directly
measured count rather than a synthesised index, and the valid share
(`graded / planted`) is printed beside it so a low number can be read
correctly: easy faults, or faults that did not build.

**`test-critic` — precision against human adjudication.** The critic's
findings are the one seat whose output is checked by a person: `corral
criticscore confirm|refute` records a human verdict, and
`confirmed / (confirmed + refuted)` is the same C-PREC the scorecard already
computes. This command reuses that path rather than deriving a second number
that could disagree with it. One consequence, stated on the row: the critic's
**n is adjudications, not runs**. A critic can sit in a hundred runs without
one of its findings having been ruled on, and counting those runs as evidence
would recommend a critic nobody has checked.

**`goal-deriver` — not scored.** See the last section.

## Thin evidence is a data point, not a ranking

**`n` is always the metric's own denominator**, and the table names its unit:
survivors *attempted* for the writer, runs for the generator, adjudications for
the critic. This is not bookkeeping. The live scorecard carries a writer with
**22 runs** behind a `3/3` rate, because most of those runs handed it no
survivor to attempt — a floor counting runs would wave it straight through.

Below `--min-runs` (default **5**), a row is **printed with its real numbers**,
marked `insufficient evidence (n=3)`, sorted below every sufficient row, and
excluded from the `prefer:` line. If nothing clears the bar, there is no
`prefer:` line — the report says so instead of leaving a suggestive blank.

This is not a statistical threshold; it is the smallest n at which a single
lucky file stops dominating the rate. The failure it prevents is concrete and
currently visible in this project's own scorecard, which carries:

```
claude-sonnet-5   test-writer   3/3    100%   ...   22 runs
gemini-3.6-flash  test-writer   64/79   81%   ...   38 runs
```

A ranking that promotes the 100% row is worse than no ranking at all, because
it is confidently wrong in the operator's favour: it recommends the expensive
model on three observations over the one with thirty-eight runs behind it.

## Disclosure, never selection

**This command prints a table. It writes no config, changes no default, fills
no seat, and feeds no router.**

That is not a limitation left for later — it is the design. corral has no
default models: every seat is named by the operator or the run refuses to
start, because a verdict is only worth something if you know who produced it.
A ranking is exactly the mechanism by which that rule gets quietly reversed,
and this project has already watched it happen once. A production daemon ran a
critic below corral's own model-recency floor for weeks. The configuration was
not stale — **performance statistics OVERRODE the configured model list** and
re-selected a retired model, permanently, because a model that had once scored
well kept scoring well in a ranking nobody re-derived. The routing was
"earned", which is precisely why nobody looked at it.

So the boundary is drawn at the output: the ranking is a document a human
reads before typing a model name, and there is no code path from it to a seat.
The help text and the table's own header say so, so that an operator reading
the output cannot mistake it for something that has already acted.

Two smaller rules follow from the same principle:

- **A model the registry never declared is disclosed, never preferred.**
  When `.corral/models.json` exists, the report is in *registry mode*: rows are
  labelled with their alias, and only declared models are eligible for the
  `prefer:` line. Models the evidence carries but the project has not declared
  still appear — hiding evidence would be its own dishonesty — marked
  `(undeclared)`. With no registry, the report says it is in *evidence mode*
  and ranks the concrete model names it found.
- **A named-but-unreachable `--db` refuses (exit 2).** It never falls back to
  the local ledger, which would silently answer a different question about a
  different body of evidence.

## Language is a dimension of the evidence, not of the tool

A writer that is good at Python may be bad at Go; we have a real instance of
exactly that, and averaging the two produces a number that is true of neither.
Where the evidence records a language, the report groups by it and `--lang`
filters it.

The two sources differ here, and the report says which it is reading:

| source | how it is read | language | critic precision |
| --- | --- | --- | --- |
| the local bug-catching ledger (default) | the same rows `corral scorecard` reports | **not recorded** — every row is across all languages | joined from the local adjudication store |
| a pushed warehouse (`--db <dsn>`) | `corral_audits`, the table `corral seal` and `corral verify --db` read | recorded per file | **absent** — adjudication is local and never pushed |

`--lang` against evidence that records no language **refuses**, rather than
returning an empty table that reads as "no model is good at Go".

Two absences are carried honestly rather than filled in. The bug-catching
ledger has no `mutants_graded` column, so a generator's valid share is left
blank there instead of computed as `0 / planted` — 0% valid is a positive
claim about what the generator produced, and nobody measured it. And in the
warehouse, the run key is the **scan**, not the file: a scan that audited
thirty files with one writer is one run's worth of evidence about that writer,
and counting it as thirty would clear any evidence floor on the first scan.

## What is not scored: the goal-deriver

The goal-deriver reads a file and writes the goal the rest of the run works
against. Nothing recorded is attributable to it. Its output is not executed, so
there is no execution-proven outcome to attach; and every downstream number —
mutant yield, kill rate, proven gaps — is jointly produced by the goal *and*
the three seats that consumed it, so crediting a goal-deriver with a good kill
rate would credit it with the generator's and writer's work.

It is therefore reported as:

```
goal-deriver
not scored (goal quality is only visible downstream, via mutant yield)
```

and it never carries a `prefer:` line. This is the honest answer, not a
placeholder awaiting a formula. Making it scorable needs an experiment, not a
metric: hold every other seat fixed, vary only the deriver, and compare the
mutant yield of the runs — which is a controlled A/B, the same shape the test
selection work needed, and a separate piece of work.
