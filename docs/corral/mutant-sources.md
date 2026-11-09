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

- `--mutants-from <tool>[@<version>]:<report path>` is on `certify --repo` and `certify --local`. It is
  repeatable, once per tool, and refused with `--mutants`, which already pins the exam.
- The `@<version>` is optional. The report names its own version, and a version you declare
  that contradicts it is refused. An absent version is recorded absent, never filled in.
- With a `--mutant-model`, the tool's mutants join the model's on the same exam.
- `--mutant-model off` is legal **only** with `--mutants-from`, and then the tool's
  mutants are the whole exam. An omitted `--mutant-model` is still refused.
- `--tool-sample-seed` fixes the sample drawn when a file has more tool mutants than
  its mutant budget. Absent, a seed is drawn and printed, so a recorded run can be replayed.

Corral does not run the tool. You, or your CI, run it and corral reads the report it
wrote, so no third-party tool executes in the audited checkout.

## Several tools in one run

`--mutants-from` may be given once per tool, and every tool's mutants join one exam. Each
tool is capped by the file's mutant budget separately, so a file's exam can hold up to that
budget from each tool (plus the model's). The scan, `mutants:` and verdict lines name each
tool's share and their sum. A run with one tool has the same verdict cache key it had before
several tools were possible. Phase 1 adds no tool: StrykerJS is still the only one accepted,
so "several tools" is exercised by unit tests and not by a real run with two different tools.

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
- Each file's account records how the report was tied to the audited file (`Match`) and the
  column unit applied (`Units`). For Stryker these are `embedded-source` (the report embeds
  the file's source, and corral compares its sha256) and `utf16`. A refused file records
  neither, because nothing was tied. The scan line for a counted file ends in `— match: <how>`.
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

### Every reported mutant is recorded

Each file's `ToolMutantCounts.Mutants` holds every mutant the report has for the file, in the
report's order, whatever became of it: graded, dropped (and why), or not taken under the tool's
status. Per mutant: id (and whether the tool gave it or corral derived it), operator, status,
the tool's span, corral's byte span into the audited file, the sha256 of the replacement text
for a mutant corral took, `static`, `tests_completed`, `killed_by` and `covered_by` as the
tool's test ids, `covered_by_count`, duration, and `fate`. The tool's claims (status, spans,
which tests killed or covered) are declared; the byte span, replacement hash and fate are
corral's. Test names are held once per file in `ToolMutantCounts.Tests` (id to name), only for
ids some recorded mutant refers to. A name is recorded as a digest of it when it is over 256
bytes, is not valid UTF-8, holds a newline, tab or other control character, starts with `/` or
`~`, or contains any of: an email-like token; two slashes around a segment with no space in it
(`/x/`, anywhere, so `/home/u/...`, `file:///...`, `` `/x/y` ``, `*/x/y` and `foo/x/y` are all
caught); `~/`; a backslash pair (`\\server\share`, a UNC path); `file:` in any case; or a drive
letter (`C:\`, `C:/`) not inside a word. Known shapes it still lets through: a one-segment
absolute path with no second slash (`reads /etc`) and a path written with single backslashes
(`dir\file`). It errs toward digesting, so some harmless names (`rel/path/to/x`, a URL with a
path) become digests. A digest is `not-a-plain-name-` and the first 16 hex characters of the
name's sha256, unsalted: it tells two names apart and keeps the same name the same, but anyone
who can guess a name can confirm it by hashing their guess.

Each account also records the report's own `schemaVersion` and its mutation-score `thresholds`
(`high`, `low`, `break`) as declared values; a `null` break is recorded absent.

Report fields not recorded, each for a stated reason (some are parsed and dropped, the rest are
never parsed):

The report's `files.language` and `framework.dependencies` ARE recorded, as declared values on the
file's account (`Language`, `Dependencies`): the dependencies, such as the TypeScript checker and
TypeScript itself, decide which of the tool's mutants come out as compile errors. Package names pass
the same host-identifier filter as test names, and versions are held as written only when they are
plain tokens.

> **Correction (2026-11-05):** an earlier version of this page listed both as not recorded. The
> founder ruled that they be recorded, under "all the data possible makes it into the ledger".

| Stryker field | Why it is not recorded |
|---|---|
| `statusReason` | the tool's free text: compiler output and assertion diffs that name values and host paths |
| `description` | free text the tool writes about a mutant |
| `projectRoot` | a host path on the machine that ran the tool |
| `config` | the operator's whole configuration, paths and commands |
| `framework.branding` | the tool's logo and homepage; describes no run |
| `testFiles.source` | the test files' whole source text: the developer's code, and free text no filter makes safe |

A test fails on any parsed field that is neither recorded nor on this list
(`TestEveryParsedFieldIsRecordedOrExcluded`), and on any key of the committed real reports
(the trimmed fixture and the full REA report under `internal/mutantsources/testdata/`) that is
neither parsed nor on it (`TestEveryFixtureKeyIsParsedOrExcluded`).

### The size bound

A file's `Mutants` list is bounded to 1<<20 bytes of JSON. Over the bound, every `covered_by`
list gives way to its count first, and if that is still over, mutants are cut (the ones corral
took or dropped are kept before those it never took). What was done is stated on the record in
`MutantsTruncated`, for example `covered_by lists replaced by counts; <n> of <m> mutants recorded`.
Measured on REA's full 662-mutant report (the 10.0.0 report above): the list encodes to
292,956 bytes raw and 25,006 gzipped, so that file fits whole (`TestFullReportRecordSize`
re-measures these). Measured earlier, on 2026-11-05, before test names were moved to a per-file
table: 1,801,228 raw and 47,960 gzipped. The measure is `json.Marshal` of the
list alone, so it is approximate against the signed entry's bytes, which add the rest of the row
and the `Tests` names.

The verdict cache key includes the tool, the report's sha256 and the seed, so a
different report or sample is a different exam.

A file whose verdict is served from the cache ran no audit, so its tool accounts (one per tool)
are the ones recorded on the ledger entry that earned the verdict. An entry written before
`Mutants`, `Match` and `Units` were recorded has none of them, so a verdict served from such an
entry carries an account without them.

## The measured comparison (2026-11-01, one file)

This is the first data point, measured on 2026-11-01. It is one file at one commit.
Corral's side is public ledger entry 163 on the `corral/ledger` branch (archived as the tag `ledger-archive/2026-11-09` when the record restarted at a checkpoint)
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

Corral's side is public ledger entry 167 on the `corral/ledger` branch (archived as the tag `ledger-archive/2026-11-09` when the record restarted at a checkpoint)
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

Executed for the reader seam (2026-11-05): `TestStrykerFullReportGolden`, run against the full
REA 662-mutant report, passes on the branch that put Stryker behind the general reader
interface, so a Stryker report is read to the same mutants as before. A `certify --repo
--dry-run --mutants-from` on a scratch copy of REA did not reach tool intake: it printed the
tool sample seed and the audit plan and nothing about the tool's mutants, so no run of the
new per-mutant record (`Match`, `Units`, `Mutants`, `Tests`) on the CLI has been executed;
it is covered by unit tests and the golden above.

Not yet executed: a real `certify --local` run with `--mutants-from`, a real run that records
the per-mutant account, a real run with two tools, and any tool other than StrykerJS.

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

The reader is now a general interface with a registry, with Stryker behind it. Phase 1 adds no tool; StrykerJS is still the only accepted one. "Planned" is a list of intentions. Corral has read none of these tools' report formats, and no support is claimed.
