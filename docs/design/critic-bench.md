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

## 6. The same planted files on local models, with the schema enforced (2026-10-06)

From this run on, the typed critic sends its schema through the provider's
constrained-output field (Ollama's `format`). Modes `typed` and `seat` only
(`-modes typed,seat`); the loop was measured in section 5. 3 runs.

### qwen3.6:35b-a3b

| fixture | mode | right | missed | false | flags on unplanted tests | incomplete runs | errors | distinct answers | tests judged (fewest of runs / in file) | calls | input tokens | output tokens |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| reposcan/selection.go (planted) | typed | 8 | 1 | 0 | 1 | 0 | 0 | 3 | 2 / 20 | 3 | 26013 | 1149 |
| reposcan/selection.go (planted) | seat | 5 | 4 | 0 | 3 | 0 | 0 | 3 | 3 / 20 | 3 | 26013 | 3063 |
| reposcan/cachekey.go (planted) | typed | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 3 / 17 | 3 | 19437 | 878 |
| reposcan/cachekey.go (planted) | seat | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 3 / 17 | 3 | 19437 | 816 |
| attest/attest.go (planted) | typed | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 3 / 12 | 3 | 15432 | 938 |
| attest/attest.go (planted) | seat | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 3 / 12 | 3 | 15432 | 901 |
| reposcan/report.go (planted) | typed | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 3 / 17 | 3 | 43536 | 921 |
| reposcan/report.go (planted) | seat | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 3 / 17 | 3 | 43536 | 954 |
| egress/scan.go (planted) | typed | 8 | 1 | 0 | 2 | 0 | 0 | 3 | 2 / 15 | 3 | 27240 | 985 |
| egress/scan.go (planted) | seat | 7 | 2 | 0 | 1 | 0 | 0 | 2 | 2 / 15 | 3 | 27240 | 811 |
| models/models.go (planted) | typed | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 3 / 12 | 3 | 24777 | 991 |
| models/models.go (planted) | seat | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 3 / 12 | 3 | 24777 | 1105 |
| adequacy/jail.go (planted) | typed | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 3 / 17 | 3 | 36987 | 913 |
| adequacy/jail.go (planted) | seat | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 3 / 17 | 3 | 36987 | 962 |
| test | typed | seat |
|---|---|---|
| TestSelectionEvidenceEmptyDocumentFallsBack | 0 | 1 |
| TestSelectionEvidenceEmptyOutputIsNotRan | 1 | 0 |
| TestSelectionEvidenceEmptyOutputNamesMissingPytestCov | 3 | 2 |
| TestSelectionEvidenceEmptyOutputWithoutDetailedContractIsStillNotRan | 0 | 1 |
| TestSelectionEvidenceNoSelectorIsWholeSuiteDisclosed | 2 | 2 |
| TestSelectionEvidencePathologicalDocumentFallsBack | 0 | 1 |
| TestSourceRootsForDerivesDotForRootLevelSources | 3 | 1 |
| test | typed | seat |
|---|---|---|
| TestCacheKeyIsUnambiguous | 3 | 3 |
| TestCacheKeySeparatesSubstrates | 3 | 3 |
| TestCacheKeyStableForIdenticalInputs | 3 | 3 |
| test | typed | seat |
|---|---|---|
| TestKeyFilePerm | 3 | 3 |
| TestLoadOrCreateKeyFromFile | 3 | 3 |
| TestLoadOrCreateKeyPersistFailureIsLoud | 3 | 3 |
| test | typed | seat |
|---|---|---|
| TestAggregateMarksPoolTestUnsoundFiles | 3 | 3 |
| TestAggregateNeverReportsMoreAuditedThanCandidates | 3 | 3 |
| TestAggregateScoresOverAuditedSurfaceOnly | 3 | 3 |
| test | typed | seat |
|---|---|---|
| TestScanText_CatchesDashDashAdjacencyVariant | 1 | 1 |
| TestScanText_CatchesPlusPlusContentSpoof | 3 | 3 |
| TestScanText_IgnoresFileHeaderAndContext | 3 | 3 |
| TestScanText_RealFileHeaderNotScannedAsContent | 1 | 0 |
| TestScan_MissingFileSkippedNotFatal | 2 | 1 |
| test | typed | seat |
|---|---|---|
| TestLoadNoRegistryIsNotAnError | 3 | 3 |
| TestLookupUnknownAliasIsNotAnError | 3 | 3 |
| TestStrictModeIsOffByDefaultAndReadFromTheDocument | 3 | 3 |
| test | typed | seat |
|---|---|---|
| TestJailAdapterExitMapping | 3 | 3 |
| TestJailAdapterNilBackendErrors | 3 | 3 |
| TestShellJoinQuotesMetacharacters | 3 | 3 |

### qwen2.5-coder:7b

| fixture | mode | right | missed | false | flags on unplanted tests | incomplete runs | errors | distinct answers | tests judged (fewest of runs / in file) | calls | input tokens | output tokens |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| reposcan/selection.go (planted) | typed | 5 | 4 | 0 | 29 | 1 | 0 | 3 | 14 / 20 | 4 | 24618 | 5132 |
| reposcan/selection.go (planted) | seat | 6 | 3 | 0 | 36 | 0 | 0 | 1 | 14 / 20 | 3 | 24618 | 3256 |
| reposcan/cachekey.go (planted) | typed | 7 | 2 | 0 | 2 | 0 | 0 | 2 | 3 / 17 | 3 | 18033 | 706 |
| reposcan/cachekey.go (planted) | seat | 7 | 2 | 0 | 2 | 0 | 0 | 2 | 3 / 17 | 3 | 18033 | 706 |
| attest/attest.go (planted) | typed | 6 | 3 | 0 | 0 | 0 | 0 | 1 | 2 / 12 | 3 | 14574 | 454 |
| attest/attest.go (planted) | seat | 5 | 4 | 0 | 1 | 0 | 0 | 3 | 1 / 12 | 3 | 14574 | 517 |
| reposcan/report.go (planted) | typed | 9 | 0 | 0 | 42 | 0 | 0 | 1 | 17 / 17 | 3 | 41772 | 4075 |
| reposcan/report.go (planted) | seat | 9 | 0 | 0 | 42 | 0 | 0 | 1 | 17 / 17 | 3 | 41772 | 4143 |
| egress/scan.go (planted) | typed | 3 | 6 | 0 | 0 | 0 | 0 | 1 | 1 / 15 | 3 | 25653 | 253 |
| egress/scan.go (planted) | seat | 4 | 5 | 0 | 3 | 0 | 0 | 3 | 1 / 15 | 3 | 25653 | 575 |
| models/models.go (planted) | typed | 4 | 5 | 0 | 1 | 0 | 0 | 3 | 1 / 12 | 3 | 23325 | 470 |
| models/models.go (planted) | seat | 6 | 3 | 0 | 0 | 0 | 0 | 1 | 2 / 12 | 3 | 23325 | 484 |
| adequacy/jail.go (planted) | typed | 7 | 2 | 0 | 31 | 0 | 0 | 2 | 4 / 17 | 3 | 35205 | 3092 |
| adequacy/jail.go (planted) | seat | 9 | 0 | 0 | 42 | 0 | 0 | 1 | 17 / 17 | 3 | 35205 | 4107 |
| test | typed | seat |
|---|---|---|
| TestCollectSelectionEvidenceRefusesUnparseableOutput | 2 | 3 |
| TestCollectSelectionEvidenceThreadsSourceRootsIntoInstrument | 2 | 3 |
| TestCollectSelectionEvidenceWithNoSourcePathsFallsBackToBareCov | 2 | 3 |
| TestSelectionEvidenceEmptyDocumentFallsBack | 1 | 0 |
| TestSelectionEvidenceEmptyOutputIsNotRan | 1 | 0 |
| TestSelectionEvidenceEmptyOutputNamesMissingPytestCov | 1 | 0 |
| TestSelectionEvidenceEmptyOutputWithoutDetailedContractIsStillNotRan | 1 | 0 |
| TestSelectionEvidenceForNarrowsFromRecordedEvidence | 2 | 3 |
| TestSelectionEvidenceGoodDocumentIsUnchanged | 1 | 0 |
| TestSelectionEvidenceInstrumentRefusalIsDisclosed | 2 | 3 |
| TestSelectionEvidenceNoSelectorIsWholeSuiteDisclosed | 2 | 3 |
| TestSelectionEvidencePathologicalDocumentFallsBack | 1 | 0 |
| TestSelectionEvidenceRunFailureIsDisclosedPerFile | 2 | 3 |
| TestSelectionEvidenceZeroValueIsDisclosedWholeSuite | 2 | 3 |
| TestSourceRootsForDedupsAndSorts | 2 | 3 |
| TestSourceRootsForDerivesDotForRootLevelSources | 2 | 3 |
| TestSourceRootsForDerivesFlatPackage | 2 | 3 |
| TestSourceRootsForDerivesSrcLayout | 2 | 3 |
| TestSourceRootsForEmptyWhenNothingQualifies | 2 | 3 |
| TestSourceRootsForIgnoresOtherLanguages | 2 | 3 |
| test | typed | seat |
|---|---|---|
| TestCacheKeyIsUnambiguous | 3 | 3 |
| TestCacheKeySeparatesSubstrates | 1 | 1 |
| TestCacheKeyStableForIdenticalInputs | 3 | 3 |
| TestDigestDirIsUnambiguous | 2 | 2 |
| test | typed | seat |
|---|---|---|
| TestKeyFilePerm | 3 | 3 |
| TestLoadOrCreateKeyFromFile | 3 | 1 |
| TestLoadOrCreateKeyPersistFailureIsLoud | 0 | 1 |
| TestLoadOrCreateKeyPersistFailureReadOnlyDir | 0 | 1 |
| test | typed | seat |
|---|---|---|
| TestAggregateBooksPrepFailed | 3 | 3 |
| TestAggregateBooksUngoaledFromExclusionsNotSubtraction | 3 | 3 |
| TestAggregateCarriesProvenMissedThrough | 3 | 3 |
| TestAggregateCarriesUngradableDetailThrough | 3 | 3 |
| TestAggregateCountsCacheHits | 3 | 3 |
| TestAggregateCountsUngoaledCandidatesInTheDenominator | 3 | 3 |
| TestAggregateDoesNotFoldNotSelectedIntoUngradable | 3 | 3 |
| TestAggregateExcludesTimedOutFilesFromProvenMissedRollup | 3 | 3 |
| TestAggregateFoldsDeriveFailedIntoUngradable | 3 | 3 |
| TestAggregateFoldsSourceTooLargeIntoUngradable | 3 | 3 |
| TestAggregateMarksPoolTestUnsoundFiles | 3 | 3 |
| TestAggregateMarksTestWriterFailedFiles | 3 | 3 |
| TestAggregateMarksTimedOutFiles | 3 | 3 |
| TestAggregateNeverReportsMoreAuditedThanCandidates | 3 | 3 |
| TestAggregateNothingAuditedIsNotZeroScore | 3 | 3 |
| TestAggregateRanksWeakestFirst | 3 | 3 |
| TestAggregateScoresOverAuditedSurfaceOnly | 3 | 3 |
| test | typed | seat |
|---|---|---|
| TestScanText_CatchesDashDashAdjacencyVariant | 0 | 2 |
| TestScanText_CatchesPlusPlusContentSpoof | 3 | 3 |
| TestScanText_IgnoresFileHeaderAndContext | 0 | 1 |
| TestScanText_RealFileHeaderNotScannedAsContent | 0 | 1 |
| test | typed | seat |
|---|---|---|
| TestLoadNoRegistryIsNotAnError | 3 | 3 |
| TestLoadRefusesMissingEnvFile | 1 | 0 |
| TestLookupUnknownAliasIsNotAnError | 1 | 3 |
| test | typed | seat |
|---|---|---|
| TestEnvWithDepBinPaths | 2 | 3 |
| TestEnvWithDepBinPathsIgnoresNonDepBinds | 2 | 3 |
| TestJailAdapterBwrapWorkspaceStaysLockedDown | 2 | 3 |
| TestJailAdapterContainerBackendCanCompilePythonInWorkspace | 2 | 3 |
| TestJailAdapterContainerBackendCanReadOwnWorkspace | 2 | 3 |
| TestJailAdapterExitMapping | 2 | 3 |
| TestJailAdapterNilBackendErrors | 2 | 3 |
| TestJailAdapterTimeoutNeverReadsAsPassed | 2 | 3 |
| TestJailAdapterWritesFilesIntoWorkspace | 2 | 3 |
| TestJailRefusesSymlinkedDepBind | 2 | 3 |
| TestJailResolvesDepBindsToWorkspaceTarget | 2 | 3 |
| TestJailWithMaxOutputSetsSandboxOption | 2 | 3 |
| TestJailWithoutMaxOutputLeavesSandboxDefault | 2 | 3 |
| TestShellJoinQuotesMetacharacters | 3 | 3 |
| TestShellSplitMatchesFieldsOnSimpleCommands | 3 | 3 |
| TestShellSplitRoundTripsShellJoin | 3 | 3 |
| TestShellSplitUnterminatedQuoteDoesNotLoseInput | 3 | 3 |

Totals over 63 planted tests per mode: 35B typed 61, seat 57, with 3 and 4
flags on tests nobody planted; 7B typed 41, seat 46, with 105 and 126. The
35B returned no unparseable answer; the 7B one, on the largest file. The
seat never retried, so its difference from typed alone is variance.

Under the schema the 35B listed only the tests it flagged (2 or 3 of 12 to
20 per file), not every test, so "a verdict for every test" no longer held,
though its flags were right. There is no unconstrained 35B run on the
planted files, so apart from parsing this does not show what the schema
changed in its accuracy.

## 7. The same planted files on gemini-3.8-flash, with the schema enforced (2026-10-06)

Gemini's OpenAI-compatible endpoint takes the schema as `response_format`
with `strict: true`. Modes `typed` and `seat`, 3 runs.

| fixture | mode | right | missed | false | flags on unplanted tests | incomplete runs | errors | distinct answers | tests judged (fewest of runs / in file) | calls | input tokens | output tokens |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| reposcan/selection.go (planted) | typed | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 20 / 20 | 3 | 27879 | 4898 |
| reposcan/selection.go (planted) | seat | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 20 / 20 | 3 | 27879 | 5115 |
| reposcan/cachekey.go (planted) | typed | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 17 / 17 | 3 | 20829 | 3617 |
| reposcan/cachekey.go (planted) | seat | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 17 / 17 | 3 | 20829 | 3193 |
| attest/attest.go (planted) | typed | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 12 / 12 | 3 | 16299 | 2523 |
| attest/attest.go (planted) | seat | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 12 / 12 | 3 | 16299 | 2802 |
| reposcan/report.go (planted) | typed | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 17 / 17 | 3 | 44667 | 3382 |
| reposcan/report.go (planted) | seat | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 17 / 17 | 3 | 44667 | 3394 |
| egress/scan.go (planted) | typed | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 15 / 15 | 3 | 28770 | 3828 |
| egress/scan.go (planted) | seat | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 15 / 15 | 3 | 28770 | 4232 |
| models/models.go (planted) | typed | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 12 / 12 | 3 | 26304 | 2225 |
| models/models.go (planted) | seat | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 12 / 12 | 3 | 26304 | 2811 |
| adequacy/jail.go (planted) | typed | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 17 / 17 | 3 | 39510 | 4255 |
| adequacy/jail.go (planted) | seat | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 17 / 17 | 3 | 39510 | 4769 |
| test | typed | seat |
|---|---|---|
| TestSelectionEvidenceEmptyOutputNamesMissingPytestCov | 3 | 3 |
| TestSelectionEvidenceNoSelectorIsWholeSuiteDisclosed | 3 | 3 |
| TestSourceRootsForDerivesDotForRootLevelSources | 3 | 3 |
| test | typed | seat |
|---|---|---|
| TestCacheKeyIsUnambiguous | 3 | 3 |
| TestCacheKeySeparatesSubstrates | 3 | 3 |
| TestCacheKeyStableForIdenticalInputs | 3 | 3 |
| test | typed | seat |
|---|---|---|
| TestKeyFilePerm | 3 | 3 |
| TestLoadOrCreateKeyFromFile | 3 | 3 |
| TestLoadOrCreateKeyPersistFailureIsLoud | 3 | 3 |
| test | typed | seat |
|---|---|---|
| TestAggregateMarksPoolTestUnsoundFiles | 3 | 3 |
| TestAggregateNeverReportsMoreAuditedThanCandidates | 3 | 3 |
| TestAggregateScoresOverAuditedSurfaceOnly | 3 | 3 |
| test | typed | seat |
|---|---|---|
| TestScanText_CatchesPlusPlusContentSpoof | 3 | 3 |
| TestScanText_IgnoresFileHeaderAndContext | 3 | 3 |
| TestScan_MissingFileSkippedNotFatal | 3 | 3 |
| test | typed | seat |
|---|---|---|
| TestLoadNoRegistryIsNotAnError | 3 | 3 |
| TestLookupUnknownAliasIsNotAnError | 3 | 3 |
| TestStrictModeIsOffByDefaultAndReadFromTheDocument | 3 | 3 |
| test | typed | seat |
|---|---|---|
| TestJailAdapterExitMapping | 3 | 3 |
| TestJailAdapterNilBackendErrors | 3 | 3 |
| TestShellJoinQuotesMetacharacters | 3 | 3 |

Both modes caught all 63 planted tests, flagged no other test, gave the same
answer on every run, and judged every test in every file: unlike the 35B in
section 6, Gemini kept listing the sound tests under the schema. Compared
with section 5 (the same files, no schema), the typed call alone went from
60 to 63; section 5 already put a 3-test gap between typed and seat down to
run-to-run variance, so this is not evidence the schema improved accuracy.
Input was identical (204,258 tokens per mode); output was 24,728 (typed) and
26,316 (seat).

## 8. The same planted files with the test list, schema enforced (2026-10-06)

From this run on, the critic is handed the file's tests by name
(`lang.Plugin.TestNamesInSource`) and the schema requires a verdict for each
(`agentworker.CriticTestListBlock`, the keyed answer). Modes `typed` and
`seat`, 3 runs.

### gemini-3.8-flash

| fixture | mode | right | missed | false | flags on unplanted tests | incomplete runs | errors | distinct answers | tests judged (fewest of runs / in file) | calls | input tokens | output tokens |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| reposcan/selection.go (planted) | typed | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 20 / 20 | 3 | 28626 | 3053 |
| reposcan/selection.go (planted) | seat | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 20 / 20 | 3 | 28626 | 3044 |
| reposcan/cachekey.go (planted) | typed | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 17 / 17 | 3 | 21369 | 2225 |
| reposcan/cachekey.go (planted) | seat | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 17 / 17 | 3 | 21369 | 1950 |
| attest/attest.go (planted) | typed | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 12 / 12 | 3 | 16662 | 1307 |
| attest/attest.go (planted) | seat | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 12 / 12 | 3 | 16662 | 1104 |
| reposcan/report.go (planted) | typed | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 17 / 17 | 3 | 45279 | 2602 |
| reposcan/report.go (planted) | seat | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 17 / 17 | 3 | 45279 | 2625 |
| egress/scan.go (planted) | typed | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 15 / 15 | 3 | 29343 | 2086 |
| egress/scan.go (planted) | seat | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 15 / 15 | 3 | 29343 | 2310 |
| models/models.go (planted) | typed | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 12 / 12 | 3 | 26691 | 1586 |
| models/models.go (planted) | seat | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 12 / 12 | 3 | 26691 | 1764 |
| adequacy/jail.go (planted) | typed | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 17 / 17 | 3 | 40137 | 2526 |
| adequacy/jail.go (planted) | seat | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 17 / 17 | 3 | 40137 | 2627 |
| test | typed | seat |
|---|---|---|
| TestSelectionEvidenceEmptyOutputNamesMissingPytestCov | 3 | 3 |
| TestSelectionEvidenceNoSelectorIsWholeSuiteDisclosed | 3 | 3 |
| TestSourceRootsForDerivesDotForRootLevelSources | 3 | 3 |
| test | typed | seat |
|---|---|---|
| TestCacheKeyIsUnambiguous | 3 | 3 |
| TestCacheKeySeparatesSubstrates | 3 | 3 |
| TestCacheKeyStableForIdenticalInputs | 3 | 3 |
| test | typed | seat |
|---|---|---|
| TestKeyFilePerm | 3 | 3 |
| TestLoadOrCreateKeyFromFile | 3 | 3 |
| TestLoadOrCreateKeyPersistFailureIsLoud | 3 | 3 |
| test | typed | seat |
|---|---|---|
| TestAggregateMarksPoolTestUnsoundFiles | 3 | 3 |
| TestAggregateNeverReportsMoreAuditedThanCandidates | 3 | 3 |
| TestAggregateScoresOverAuditedSurfaceOnly | 3 | 3 |
| test | typed | seat |
|---|---|---|
| TestScanText_CatchesPlusPlusContentSpoof | 3 | 3 |
| TestScanText_IgnoresFileHeaderAndContext | 3 | 3 |
| TestScan_MissingFileSkippedNotFatal | 3 | 3 |
| test | typed | seat |
|---|---|---|
| TestLoadNoRegistryIsNotAnError | 3 | 3 |
| TestLookupUnknownAliasIsNotAnError | 3 | 3 |
| TestStrictModeIsOffByDefaultAndReadFromTheDocument | 3 | 3 |
| test | typed | seat |
|---|---|---|
| TestJailAdapterExitMapping | 3 | 3 |
| TestJailAdapterNilBackendErrors | 3 | 3 |
| TestShellJoinQuotesMetacharacters | 3 | 3 |

### qwen3.6:35b-a3b

| fixture | mode | right | missed | false | flags on unplanted tests | incomplete runs | errors | distinct answers | tests judged (fewest of runs / in file) | calls | input tokens | output tokens |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| reposcan/selection.go (planted) | typed | 2 | 7 | 0 | 0 | 0 | 0 | 2 | 20 / 20 | 3 | 26817 | 4638 |
| reposcan/selection.go (planted) | seat | 4 | 5 | 0 | 3 | 0 | 0 | 2 | 20 / 20 | 3 | 26817 | 4860 |
| reposcan/cachekey.go (planted) | typed | 5 | 4 | 0 | 1 | 0 | 0 | 2 | 17 / 17 | 3 | 19980 | 3186 |
| reposcan/cachekey.go (planted) | seat | 7 | 2 | 0 | 1 | 0 | 0 | 3 | 17 / 17 | 3 | 19980 | 3354 |
| attest/attest.go (planted) | typed | 8 | 1 | 0 | 4 | 0 | 0 | 3 | 12 / 12 | 3 | 15795 | 2321 |
| attest/attest.go (planted) | seat | 8 | 1 | 0 | 0 | 0 | 0 | 2 | 12 / 12 | 3 | 15795 | 2100 |
| reposcan/report.go (planted) | typed | 9 | 0 | 0 | 0 | 0 | 0 | 1 | 17 / 17 | 3 | 44154 | 3150 |
| reposcan/report.go (planted) | seat | 8 | 1 | 0 | 0 | 0 | 0 | 2 | 17 / 17 | 3 | 44154 | 3162 |
| egress/scan.go (planted) | typed | 5 | 4 | 0 | 5 | 0 | 0 | 3 | 15 / 15 | 3 | 27807 | 3460 |
| egress/scan.go (planted) | seat | 5 | 4 | 0 | 17 | 0 | 0 | 3 | 15 / 15 | 3 | 27807 | 3951 |
| models/models.go (planted) | typed | 9 | 0 | 0 | 2 | 0 | 0 | 2 | 12 / 12 | 3 | 25179 | 2477 |
| models/models.go (planted) | seat | 8 | 1 | 0 | 0 | 0 | 0 | 2 | 12 / 12 | 3 | 25179 | 2815 |
| adequacy/jail.go (planted) | typed | 9 | 0 | 0 | 2 | 0 | 0 | 2 | 17 / 17 | 3 | 37641 | 4071 |
| adequacy/jail.go (planted) | seat | 9 | 0 | 0 | 3 | 0 | 0 | 3 | 17 / 17 | 3 | 37641 | 3824 |
| test | typed | seat |
|---|---|---|
| TestCollectSelectionEvidenceRefusesUnparseableOutput | 0 | 1 |
| TestCollectSelectionEvidenceThreadsSourceRootsIntoInstrument | 0 | 1 |
| TestSelectionEvidenceEmptyOutputWithoutDetailedContractIsStillNotRan | 0 | 1 |
| TestSelectionEvidenceNoSelectorIsWholeSuiteDisclosed | 0 | 1 |
| TestSourceRootsForDerivesDotForRootLevelSources | 2 | 3 |
| test | typed | seat |
|---|---|---|
| TestCacheKeyChangesWhenAnyFieldChanges | 1 | 1 |
| TestCacheKeyIsUnambiguous | 1 | 2 |
| TestCacheKeySeparatesSubstrates | 1 | 2 |
| TestCacheKeyStableForIdenticalInputs | 3 | 3 |
| test | typed | seat |
|---|---|---|
| TestKeyFilePerm | 3 | 3 |
| TestLoadOrCreateKeyFromFile | 3 | 3 |
| TestLoadOrCreateKeyFromSeed | 3 | 0 |
| TestLoadOrCreateKeyPersistFailureIsLoud | 2 | 2 |
| TestLoadOrCreateKeyPersistFailureReadOnlyDir | 1 | 0 |
| test | typed | seat |
|---|---|---|
| TestAggregateMarksPoolTestUnsoundFiles | 3 | 3 |
| TestAggregateNeverReportsMoreAuditedThanCandidates | 3 | 2 |
| TestAggregateScoresOverAuditedSurfaceOnly | 3 | 3 |
| test | typed | seat |
|---|---|---|
| TestGovulnEnv | 3 | 3 |
| TestScanSecrets_LongLineNotSilentlyAborted | 2 | 2 |
| TestScanSecrets_OverSizeFileSurfaced | 0 | 1 |
| TestScanText_CatchesDashDashAdjacencyVariant | 0 | 3 |
| TestScanText_CatchesHistoryOnlySecret | 0 | 1 |
| TestScanText_CatchesPlusPlusContentSpoof | 2 | 3 |
| TestScanText_IgnoresFileHeaderAndContext | 2 | 1 |
| TestScanText_LongLineSurfacesUnscannedRemainder | 0 | 1 |
| TestScanText_RealFileHeaderNotScannedAsContent | 0 | 1 |
| TestScan_CleanChangeSetPasses | 0 | 1 |
| TestScan_LicenseAdvisory | 0 | 1 |
| TestScan_MissingFileSkippedNotFatal | 1 | 1 |
| TestScan_OnlyScansChangedFiles | 0 | 1 |
| TestScan_PlantedAWSKeyBlocks | 0 | 1 |
| TestScan_PlantedPrivateKeyBlocks | 0 | 1 |
| test | typed | seat |
|---|---|---|
| TestAuditedRepoCannotPickItsAuditors | 1 | 0 |
| TestLoadFromRepoFile | 1 | 0 |
| TestLoadNoRegistryIsNotAnError | 3 | 3 |
| TestLookupUnknownAliasIsNotAnError | 3 | 2 |
| TestStrictModeIsOffByDefaultAndReadFromTheDocument | 3 | 3 |
| test | typed | seat |
|---|---|---|
| TestEnvWithDepBinPaths | 1 | 2 |
| TestEnvWithDepBinPathsIgnoresNonDepBinds | 1 | 0 |
| TestJailAdapterExitMapping | 3 | 3 |
| TestJailAdapterNilBackendErrors | 3 | 3 |
| TestJailWithoutMaxOutputLeavesSandboxDefault | 0 | 1 |
| TestShellJoinQuotesMetacharacters | 3 | 3 |

**Gemini:** 63 of 63 in both modes, no other flags, the same answer every
run, every test judged, as in section 7, on 15,385 output tokens for the
typed call against 24,728 there, because a keyed answer does not repeat each
test's name, file and selector. The list added 3,849 input tokens per mode.

**35B:** every test in every file was judged, on every run, where in section
6 it judged 2 or 3 per file; no answer failed to parse and none was retried.
But it caught 47 (typed) and 49 (seat) of 63, against 61 and 57 in section
6, and flagged 14 and 24 tests nobody planted, against 3 and 4, with less
agreement between runs. Made to rule on every test, it makes more wrong calls
in both directions. With 3 runs per mode this is a measured drop, not an
explained one; the likeliest reading is that a forced verdict on every test
works against the critic's own "flag only if certain" rule.

## What this does not show

- Two models answered the cloud runs and the local runs: gemini-3.8-flash and
  qwen3.6:35b-a3b (plus the 7B). Other models are unmeasured. The schema has
  been measured on one cloud model, gemini-3.8-flash.
- Planting makes one kind of vacuous test, a test whose checks were deleted.
  Tautologies, self-comparisons and dead branches appear only in the
  hand-written fixtures.
- No price is quoted: the typed critic spends less input and more output,
  and the cost ratio depends on the provider's per-token prices.
