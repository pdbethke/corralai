<!-- SPDX-License-Identifier: Elastic-2.0 -->
# The critic bench: a tool loop against one typed decision

**Status: measured (2026-10-05 and 2026-10-06), and the result shipped in
v1.0.0-rc.16.** The test-critic used to work through a tool-calling loop. It
now answers one typed question per test (`sound`, `vacuous` or `dead_check`)
in a single call, retried once on an answer that does not parse, with the
loop as the fallback (`agentworker.runCritic`). This note holds the
measurements behind that change, exactly as `scripts/criticbench` printed
them, so every number quoted elsewhere traces to a committed file.

## How to read the tables

- **loop** is the old critic (`agentworker.RunCriticLoop`). **typed** is one
  typed call with no retry (`agentworker.RunCriticTyped`). **seat** is what
  certify runs now (`agentworker.RunRole`: typed, retried once, loop as
  fallback). The first two local runs predate the seat mode.
- **right / missed / false** are summed over runs, against an answer key
  that is checked by execution, never asserted:
  - the three hand-written fixtures run their tests against real and broken
    code (`TestFixtureAnswerKeysAreExecuted`);
  - a **planted** file has `k` tests made vacuous by deleting every call
    through which they can fail, each proven by re-parsing (no failure call
    left) and by compiling and running it with `go test -overlay`
    (`plantVacuous`). Only planted tests are keyed: a flag on any other test
    is counted apart, never as right or false.
- A **real** file benched as it stands has no key; only a flag naming a test
  that is not in the file counts as false there.
- Counts are raw, never percentages, at these sizes.

To reproduce a run:

```sh
go run ./scripts/criticbench -model gemini-3.8-flash -runs 5
go run ./scripts/criticbench -model gemini-3.8-flash -runs 3 -files <src.go,...>
go run ./scripts/criticbench -model gemini-3.8-flash -runs 3 -plant 3 -files <src.go,...>
go run ./scripts/criticbench -plant 3 -show <dir> -files <src.go,...>   # write the planted files, call no model
```

## 1. Hand-written fixtures, qwen2.5-coder:7b (local), 5 runs

| fixture | mode | right | missed | false | incomplete runs | errors | distinct answers | calls | input tokens | output tokens |
|---|---|---|---|---|---|---|---|---|---|---|
| wallet | loop | 2 | 8 | 0 | 0 | 0 | 2 | 11 | 14417 | 959 |
| wallet | typed | 6 | 4 | 0 | 2 | 0 | 2 | 5 | 4135 | 1723 |
| parse | loop | 0 | 0 | 0 | 0 | 0 | 1 | 5 | 6395 | 2675 |
| parse | typed | 0 | 0 | 0 | 0 | 0 | 1 | 5 | 4350 | 2012 |
| rates | loop | 1 | 34 | 0 | 0 | 0 | 2 | 7 | 9178 | 2503 |
| rates | typed | 34 | 1 | 0 | 0 | 0 | 2 | 5 | 4350 | 2318 |

The loop's 3 of 45 mostly measures that this model barely calls tools: it
answered in prose and filed nothing. It does not show the loop's design is
worse.

## 2. Hand-written fixtures, qwen3.6:35b-a3b (local, tool-capable), 5 runs

| fixture | mode | right | missed | false | incomplete runs | errors | distinct answers | calls | input tokens | output tokens |
|---|---|---|---|---|---|---|---|---|---|---|
| wallet | loop | 5 | 5 | 0 | 0 | 0 | 1 | 17 | 26661 | 3236 |
| wallet | typed | 10 | 0 | 0 | 0 | 0 | 1 | 5 | 4385 | 1939 |
| parse | loop | 0 | 0 | 2 | 1 | 0 | 2 | 18 | 31121 | 4430 |
| parse | typed | 0 | 0 | 0 | 0 | 0 | 1 | 5 | 4625 | 2188 |
| rates | loop | 35 | 0 | 0 | 0 | 0 | 1 | 10 | 17411 | 6769 |
| rates | typed | 35 | 0 | 0 | 0 | 0 | 1 | 5 | 4635 | 3319 |

## 3. Hand-written fixtures, gemini-3.8-flash, 5 runs

| fixture | mode | right | missed | false | incomplete runs | errors | distinct answers | calls | input tokens | output tokens |
|---|---|---|---|---|---|---|---|---|---|---|
| wallet | loop | 10 | 0 | 0 | 0 | 0 | 1 | 15 | 19996 | 1552 |
| wallet | typed | 10 | 0 | 0 | 0 | 0 | 1 | 5 | 4570 | 1286 |
| wallet | seat | 10 | 0 | 0 | 0 | 0 | 1 | 5 | 4570 | 1431 |
| parse | loop | 0 | 0 | 0 | 0 | 0 | 1 | 5 | 6655 | 138 |
| parse | typed | 0 | 0 | 0 | 0 | 0 | 1 | 5 | 4820 | 1587 |
| parse | seat | 0 | 0 | 0 | 0 | 0 | 1 | 5 | 4820 | 1749 |
| rates | loop | 17 | 18 | 0 | 0 | 0 | 2 | 22 | 31375 | 2481 |
| rates | typed | 35 | 0 | 0 | 0 | 0 | 1 | 5 | 4820 | 2930 |
| rates | seat | 35 | 0 | 0 | 0 | 0 | 1 | 5 | 4820 | 2702 |

## 4. Five corral files as they stand, gemini-3.8-flash, 3 runs

Files: `internal/reposcan/selection.go`, `internal/reposcan/cachekey.go`,
`internal/adequacy/workspace.go`, `internal/queue/findings.go`,
`internal/attest/attest.go`.

| fixture | mode | right | missed | false | incomplete runs | errors | distinct answers | calls | input tokens | output tokens |
|---|---|---|---|---|---|---|---|---|---|---|
| reposcan/selection.go | loop | no key | no key | 0 | 0 | 0 | 1 | 6 | 58749 | 300 |
| reposcan/selection.go | typed | no key | no key | 0 | 0 | 0 | 1 | 3 | 28188 | 4546 |
| reposcan/selection.go | seat | no key | no key | 0 | 0 | 0 | 1 | 3 | 28188 | 4068 |
| reposcan/cachekey.go | loop | no key | no key | 0 | 0 | 0 | 1 | 6 | 44275 | 285 |
| reposcan/cachekey.go | typed | no key | no key | 0 | 0 | 0 | 1 | 3 | 20952 | 4015 |
| reposcan/cachekey.go | seat | no key | no key | 0 | 0 | 0 | 1 | 3 | 20952 | 3558 |
| adequacy/workspace.go | loop | no key | no key | 0 | 0 | 0 | 1 | 6 | 97031 | 252 |
| adequacy/workspace.go | typed | no key | no key | 0 | 1 | 0 | 1 | 3 | 47334 | 3387 |
| adequacy/workspace.go | seat | no key | no key | 0 | 0 | 0 | 1 | 3 | 47334 | 5646 |
| queue/findings.go | loop | no key | no key | 0 | 0 | 0 | 1 | 4 | 29291 | 146 |
| queue/findings.go | typed | no key | no key | 0 | 0 | 0 | 1 | 3 | 20826 | 2202 |
| queue/findings.go | seat | no key | no key | 0 | 0 | 0 | 1 | 3 | 20826 | 2005 |
| attest/attest.go | loop | no key | no key | 0 | 0 | 0 | 1 | 6 | 35529 | 292 |
| attest/attest.go | typed | no key | no key | 0 | 0 | 0 | 1 | 3 | 16581 | 2865 |
| attest/attest.go | seat | no key | no key | 0 | 0 | 0 | 1 | 3 | 16581 | 2252 |

No mode flagged any test, so this run says nothing about accuracy. It does
show the typed call answering files of 11 to 24 tests in one call. The typed
call alone failed to parse once in fifteen, on the largest file.

## 5. Seven corral files with 3 vacuous tests planted in each, gemini-3.8-flash, 3 runs

Files: `internal/reposcan/selection.go`, `internal/reposcan/cachekey.go`,
`internal/attest/attest.go`, `internal/reposcan/report.go`,
`internal/egress/scan.go`, `internal/models/models.go`,
`internal/adequacy/jail.go`. (`internal/queue/findings.go` and
`internal/adequacy/workspace.go` could not supply three plantable tests:
most of their tests fail through helpers they hand `t` to.)

| fixture | mode | right | missed | false | flags on unplanted tests | incomplete runs | errors | distinct answers | tests judged (fewest of runs / in file) | calls | input tokens | output tokens |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| reposcan/selection.go (planted) | loop | 7 | 2 | 0 | 0 | 0 | 0 | 2 | n/a | 12 | 116893 | 1636 |
| reposcan/selection.go (planted) | typed | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 20 / 20 | 3 | 27879 | 4544 |
| reposcan/selection.go (planted) | seat | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 20 / 20 | 3 | 27879 | 5053 |
| reposcan/cachekey.go (planted) | loop | 5 | 4 | 0 | 0 | 0 | 0 | 2 | n/a | 11 | 81247 | 1333 |
| reposcan/cachekey.go (planted) | typed | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 17 / 17 | 3 | 20829 | 4119 |
| reposcan/cachekey.go (planted) | seat | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 17 / 17 | 3 | 20829 | 4190 |
| attest/attest.go (planted) | loop | 3 | 6 | 0 | 0 | 0 | 0 | 1 | n/a | 9 | 52721 | 1023 |
| attest/attest.go (planted) | typed | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 12 / 12 | 3 | 16299 | 2505 |
| attest/attest.go (planted) | seat | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 12 / 12 | 3 | 16299 | 3161 |
| reposcan/report.go (planted) | loop | 6 | 0 | 0 | 0 | 0 | 1 | 1 | n/a | 12 | 168777 | 1526 |
| reposcan/report.go (planted) | typed | 7 | 2 | 0 | 0 | 0 | 0 | 3 | 17 / 17 | 3 | 44667 | 3925 |
| reposcan/report.go (planted) | seat | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 17 / 17 | 3 | 44667 | 3869 |
| egress/scan.go (planted) | loop | 3 | 6 | 0 | 0 | 0 | 0 | 1 | n/a | 9 | 90134 | 993 |
| egress/scan.go (planted) | typed | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 15 / 15 | 3 | 28770 | 3044 |
| egress/scan.go (planted) | seat | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 15 / 15 | 3 | 28770 | 3052 |
| models/models.go (planted) | loop | 4 | 5 | 0 | 0 | 0 | 0 | 2 | n/a | 10 | 92010 | 1260 |
| models/models.go (planted) | typed | 8 | 1 | 0 | 0 | 0 | 0 | 2 | 12 / 12 | 3 | 26304 | 2280 |
| models/models.go (planted) | seat | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 12 / 12 | 3 | 26304 | 2560 |
| adequacy/jail.go (planted) | loop | 8 | 1 | 0 | 0 | 0 | 0 | 2 | n/a | 12 | 164081 | 2146 |
| adequacy/jail.go (planted) | typed | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 17 / 17 | 3 | 39510 | 3445 |
| adequacy/jail.go (planted) | seat | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 17 / 17 | 3 | 39510 | 3880 |
| test | loop | typed | seat |
|---|---|---|---|
| TestSelectionEvidenceEmptyOutputNamesMissingPytestCov | 3 | 3 | 3 |
| TestSelectionEvidenceNoSelectorIsWholeSuiteDisclosed | 3 | 3 | 3 |
| TestSourceRootsForDerivesDotForRootLevelSources | 1 | 3 | 3 |
| test | loop | typed | seat |
|---|---|---|---|
| TestCacheKeyIsUnambiguous | 2 | 3 | 3 |
| TestCacheKeySeparatesSubstrates | 0 | 3 | 3 |
| TestCacheKeyStableForIdenticalInputs | 3 | 3 | 3 |
| test | loop | typed | seat |
|---|---|---|---|
| TestKeyFilePerm | 0 | 3 | 3 |
| TestLoadOrCreateKeyFromFile | 3 | 3 | 3 |
| TestLoadOrCreateKeyPersistFailureIsLoud | 0 | 3 | 3 |
| test | loop | typed | seat |
|---|---|---|---|
| TestAggregateMarksPoolTestUnsoundFiles | 2 | 2 | 3 |
| TestAggregateNeverReportsMoreAuditedThanCandidates | 2 | 2 | 3 |
| TestAggregateScoresOverAuditedSurfaceOnly | 2 | 3 | 3 |
| test | loop | typed | seat |
|---|---|---|---|
| TestScanText_CatchesPlusPlusContentSpoof | 0 | 3 | 3 |
| TestScanText_IgnoresFileHeaderAndContext | 0 | 3 | 3 |
| TestScan_MissingFileSkippedNotFatal | 3 | 3 | 3 |
| test | loop | typed | seat |
|---|---|---|---|
| TestLoadNoRegistryIsNotAnError | 3 | 3 | 3 |
| TestLookupUnknownAliasIsNotAnError | 1 | 2 | 3 |
| TestStrictModeIsOffByDefaultAndReadFromTheDocument | 0 | 3 | 3 |
| test | loop | typed | seat |
|---|---|---|---|
| TestJailAdapterExitMapping | 3 | 3 | 3 |
| TestJailAdapterNilBackendErrors | 3 | 3 | 3 |
| TestShellJoinQuotesMetacharacters | 2 | 3 | 3 |

### Flagged tests on real files (runs out of 3 that flagged each)

#### reposcan/selection.go (planted)

| test | loop | typed | seat |
|---|---|---|---|
| TestSelectionEvidenceEmptyOutputNamesMissingPytestCov | 3 | 3 | 3 |
| TestSelectionEvidenceNoSelectorIsWholeSuiteDisclosed | 3 | 3 | 3 |
| TestSourceRootsForDerivesDotForRootLevelSources | 1 | 3 | 3 |

#### reposcan/cachekey.go (planted)

| test | loop | typed | seat |
|---|---|---|---|
| TestCacheKeyIsUnambiguous | 2 | 3 | 3 |
| TestCacheKeySeparatesSubstrates | 0 | 3 | 3 |
| TestCacheKeyStableForIdenticalInputs | 3 | 3 | 3 |

#### attest/attest.go (planted)

| test | loop | typed | seat |
|---|---|---|---|
| TestKeyFilePerm | 0 | 3 | 3 |
| TestLoadOrCreateKeyFromFile | 3 | 3 | 3 |
| TestLoadOrCreateKeyPersistFailureIsLoud | 0 | 3 | 3 |

#### reposcan/report.go (planted)

| test | loop | typed | seat |
|---|---|---|---|
| TestAggregateMarksPoolTestUnsoundFiles | 2 | 2 | 3 |
| TestAggregateNeverReportsMoreAuditedThanCandidates | 2 | 2 | 3 |
| TestAggregateScoresOverAuditedSurfaceOnly | 2 | 3 | 3 |

#### egress/scan.go (planted)

| test | loop | typed | seat |
|---|---|---|---|
| TestScanText_CatchesPlusPlusContentSpoof | 0 | 3 | 3 |
| TestScanText_IgnoresFileHeaderAndContext | 0 | 3 | 3 |
| TestScan_MissingFileSkippedNotFatal | 3 | 3 | 3 |

#### models/models.go (planted)

| test | loop | typed | seat |
|---|---|---|---|
| TestLoadNoRegistryIsNotAnError | 3 | 3 | 3 |
| TestLookupUnknownAliasIsNotAnError | 1 | 2 | 3 |
| TestStrictModeIsOffByDefaultAndReadFromTheDocument | 0 | 3 | 3 |

#### adequacy/jail.go (planted)

| test | loop | typed | seat |
|---|---|---|---|
| TestJailAdapterExitMapping | 3 | 3 | 3 |
| TestJailAdapterNilBackendErrors | 3 | 3 | 3 |
| TestShellJoinQuotesMetacharacters | 2 | 3 | 3 |

Totals over the 63 planted tests per mode: seat 63, typed 60, loop 36; no
mode flagged a test that was not planted. The seat never retried in this
run, so its lead over the typed call alone is run-to-run variance, not the
retry. Input: loop 765,863 tokens, typed 204,258. Output: loop 9,917, typed
23,862, because a typed answer gives a verdict for every test. One loop run
errored, and the bench did not record why.

## What this does not show

- Two models answered the cloud runs and the local runs: gemini-3.8-flash and
  qwen3.6:35b-a3b (plus the 7B). Other models are unmeasured.
- Planting makes one kind of vacuous test, a test whose checks were deleted.
  Tautologies, self-comparisons and dead branches appear only in the
  hand-written fixtures.
- No price is quoted: the typed critic spends less input and more output,
  and the cost ratio depends on the provider's per-token prices.
