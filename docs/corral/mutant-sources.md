# Mutant sources: grading a mutation tool's mutants

Corral's mutants normally come from one place: a model seat (`--mutant-model`)
that reads a file and its goals and plants goal-violating faults. A **tool
source** is a second place. An established mutation tool plants mutants
mechanically, with fixed operators, and writes a report of which ones survived
your tests. `--mutants-from` hands corral that report, and corral does with the
tool's mutants what it does with a model's: grades each one itself, in its jail,
with your own test command; hands each confirmed survivor to the test-writer;
and counts it a proven missing test only when a compiling test passes on the
original and fails on the mutant. None of the mutation tools writes the test.

## The flag

```bash
corral certify --repo . --top 3 \
  --writer-model <m> --mutant-model <m> --derive-model <m> --critic-model <m> \
  --mutants-from stryker:reports/mutation/mutation.json \
  -- <your test command>
```

- `--mutants-from <tool>:<report path>` is on `certify --repo` and `certify --local`. It is
  repeatable, once per tool, and refused with `--mutants`, which already pins the exam.
- With a `--mutant-model`, the tool's mutants join the model's on the same exam.
- `--mutant-model off` is legal **only** with `--mutants-from`, and then the tool's
  mutants are the whole exam. An omitted `--mutant-model` is still refused.
- `--tool-sample-seed` fixes the sample drawn when a file has more tool mutants than
  its mutant budget. Absent, a seed is drawn and printed, so a recorded run can be replayed.

Corral does not run the tool. You, or your CI, run it and corral reads the report it
wrote, so no third-party tool executes in the audited checkout.

## What corral takes from a report, and what it refuses

- Only StrykerJS reports are accepted. Other Stryker variants are refused until their
  column units have been checked.
- The report's `source` must be byte-identical (sha256) to the audited file. A stale
  report means wrong mutants, so that file's tool mutants are refused and the scan line says so.
- Only `Survived` and `NoCoverage` mutants are taken. The rest (killed, compile errors and so on) are counted, not re-graded.
- The taken mutants are capped by the file's mutant budget: a seeded sample, distinct lines first.
  Beside a `--mutant-model` the cap is the same one the model is asked for, so a file's exam can
  be up to twice its budget. The scan line and the `mutants:` line say both shares and the sum
  (`N model + T tool, an exam of up to N+T`).
- A line that occurs more than once in the file cannot be anchored to one place, so its
  mutants are dropped and counted. On REA's report that was 29 of 234 survivors.
- A file containing a lone CR, U+2028 or U+2029 is refused, because the tool's line and
  column counts and corral's disagree on those.
- A mutant's id, operator and status, and the report's Stryker version, are recorded only
  when each is a plain token (letters, digits and `._@+-`, at most 64 characters). Any other
  value is recorded as `not-a-plain-token` (an id as `not-a-plain-token-<digest>`), and the
  mutant is still graded.

## The tool's status is a claim, never the outcome

Corral never records the tool's `Survived` or `NoCoverage` as its own result. Each
mutant's row carries `Source`, `SourceTool`, `SourceOperator` and `SourceStatus`.
`SourceStatus` is what the tool said, recorded as a declared claim. `SourceTool` and
`SourceOperator` are read from the same report, which anyone can edit, so they are
declared too. `Source` (model or tool) and `Outcome` are what corral measured. A mutant the tool called `Survived` or `NoCoverage` can be killed
by corral's run. A mutant the tool called killed is counted and never re-graded.

`GeneratorModel` is set only on model mutants, and the mutant-generator's leaderboard
credit counts only the mutants it planted.

The run's one scan row carries `ToolMutants`, an array with one entry per audited file:
its counts, the seed, the report (`Report`, as `sha256:<hex>`) and any refusal. The report
is named on the record by tool and sha256, never by the path you typed, because that path
often sits under a home directory and the ledger is public. The same holds for a file's
`Detail` when the report could not be read for it. Because the source is recorded per
mutant, a per-source statistic (gap yield, waste, how often a mutant the tool reported
surviving or uncovered also survives corral's run) is a query over signed entries. The
fields and their tiers are in [record-fields.md](record-fields.md).

The verdict cache key includes the tool, the report's sha256 and the seed, so a
different report or sample is a different exam.

## The measured comparison (2026-11-01, one file)

This is the first data point, measured on 2026-11-01. It is one file at one commit.
Corral's side is public ledger entry 163 on the `corral/ledger` branch
(`scans/20261101T194309Z-aa48d494e972-eb54ee028a79.json.gz`). The Stryker run is not on the ledger.

- File: REA `src/domain/analysisErrorProjection.ts` at aa48d494e, a vitest config narrowed to REA's `domain` project.
- Stryker 10.0.0 with per-test coverage and the TypeScript checker, run from scratch configs
  (`stryker.corral-compare.config.mjs`, `vitest.corral-compare.config.ts`) with
  `NODE_OPTIONS=--max-old-space-size=16384`. REA depends on TypeScript 7.0.2, and Stryker's
  TypeScript checker could not use TypeScript 7's API, so REA's own
  `scripts/prepare-reverse-tools-mutation.mjs` points Stryker at a bundled TypeScript 6; that
  script was used. It took three setup failures to get there: TypeScript 7's API, the checker
  running out of memory, and related tests pulling in a non-domain CLI test. This is not REA's
  own unmodified Stryker setup.

| | Stryker | corral (model mutants) |
|---|---|---|
| Mutants | 662 (340 did not compile) | 39 graded |
| Survived | 159 survived, plus 75 no coverage | 21 |
| Score | 27.3% mutation score | 0.46 kill rate (0.32-0.61) |
| Tests that catch them | writes no tests | 17 proven |
| Wall time | 63 min | 2h03m, including a 1h49m test-writing phase |

How corral's 17 proven gaps line up with Stryker's mutants:

- 14 sit on lines where Stryker also has a surviving or untested mutant.
- 3 sit on lines where Stryker killed every compiling mutant: L153, L335-338 and L367-368.
- In the other direction, 150 Stryker survivors sit on 91 lines that corral never mutated.

Caveats, all of them load-bearing:

- Sharing a line is not sharing a fault. Line overlap does not show that the two sources found the same bug.
- It is one file at one commit.
- The 150 Stryker survivors are untriaged. Some are real gaps and some are equivalent
  mutants that change nothing observable. Running corral's writer over them is how that gets sorted out, and it has not been run.
- The two columns differ in kind (mutants, scores and wall time are not like for like).

> **Correction (2026-11-02): Stryker's column understates Stryker.** StrykerJS 10.0.0's
> vitest runner joins a test's describe and test names with a space when it builds the
> per-test `-t` filter, and vitest 5 joins them with ` > `. So under per-test coverage, a
> mutant covered only by tests inside a `describe` ran zero tests and was reported
> Survived: 72 of the 159 Survived mutants above ran none. This was already known upstream
> and is fixed in StrykerJS 10.1.0 ([stryker-js#6210](https://github.com/stryker-mutator/stryker-js/issues/6210)).
> REA pins 10.0.0. With 10.1.1 and the same configs, the same 662 mutants give 161 killed,
> 86 survived, 75 no coverage and 340 compile errors: a **50.0%** mutation score, not
> 27.3%, in 1h05m. 73 mutants moved from Survived to Killed and nothing else moved. The
> three lines where corral proved a gap and Stryker killed every compiling mutant are
> unchanged under 10.1.1, because the defect only ever turned kills into survivals.
> The table above is left as it was measured.

## The first `--mutants-from` run (2026-11-04, one file)

Corral's side is public ledger entry 167 on the `corral/ledger` branch
(`scans/20261105T153215Z-aa48d494e972-62f5a6da58de.json.gz`). It is the same file at the
same commit, with the model's mutants and Stryker's on one exam:
`--mutants-from stryker:<the 10.0.0 report above> --tool-sample-seed 20261101`. Every seat
ran on a coding-agent subscription: the goal deriver and mutant generator `claude-code:claude-sonnet-5-5`,
the writer `codex` and the critic `claude-code:claude-haiku-5-5`.

- Intake: 40 of Stryker's 234 Survived and NoCoverage mutants were taken. 194 were dropped:
  29 whose line occurs more than once, and 165 over the file's budget. The 88 Stryker
  killed were counted and not re-graded.
- Graded: 71 distinct mutants, 39 from the model and 32 from Stryker. Kill rate **0.42**
  (30 of 71, 95% 0.31-0.54). Every kill was confirmed by running the blamed test alone.
- Survivors: 41. **30 were proven** by authored tests that pass together on the unmutated
  code: 12 on the model's mutants and 18 on Stryker's. Of the other 11, 3 were tried and
  missed, and 8 writer seats produced no test that graded.
- Stryker's 18 proven mutants: 10 that Stryker reported NoCoverage and 8 it reported
  Survived. Under Stryker 10.1.1 all 18 keep the same status.
- Stryker reported 20 of its 32 graded mutants as Survived. Corral's run killed 10 of them,
  and StrykerJS 10.1.1 kills those same 10, so on every one of the 32 corral's outcome
  agrees with 10.1.1. The disagreement was the 10.0.0 runner defect in the correction above.
- Time: 4h11m, of which 3h42m was test writing. Tokens: 8.4M in (6.8M cached), 69.8k out.

An earlier run of the same command, before kills were confirmed, credited 7 kills to one
slow REA test. Run by hand, that test catches none of the 7 mutants: it passes on the mutated and
the unmutated code alike, so those kills were false. That run is not on the ledger. It is the reason
a kill now counts only once the failure repeats (see the CHANGELOG). In the run above, that test
killed nothing.

The caveats on the first comparison apply here too: it is one file at one commit, and the
Stryker survivors outside the 40 taken are still untriaged.

## What has and has not been executed

Executed: unit tests, and end-to-end `certify --repo` runs on the workspace substrate
with a Go fixture and a hand-made report in Stryker's schema, for the tool-only and
both-sources shapes. The real `certify --local` CLI has been exercised only on its refusals.

A real `certify --repo` run against REA with `--mutants-from` has been executed (ledger
entry 167, above).

> **Correction (2026-11-05):** this section said that no real run against REA with
> `--mutants-from` had been executed, and that no number from one would appear here
> until one existed. One now exists, and its numbers are above.

Not yet executed: a real `certify --local` run with `--mutants-from`, and any tool other
than StrykerJS.

## Tools

| Tool | Status |
|---|---|
| StrykerJS | accepted (`stryker:`) |
| Stryker.NET, Stryker4s | planned, unverified |
| Infection | planned, unverified |
| cargo-mutants | planned, unverified |
| Gremlins | planned, unverified |
| mutmut, Cosmic Ray | planned, unverified |
| Ruby `mutant` | planned, unverified |
| Muter | planned, unverified |

"Planned" is a list of intentions. Corral has read none of these tools' report formats, and no support is claimed.
