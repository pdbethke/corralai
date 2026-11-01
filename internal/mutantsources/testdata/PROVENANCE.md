# stryker-rea-analysisErrorProjection.json

A real StrykerJS mutation report, trimmed. It is the fixture `stryker_test.go` reads.

- **Project:** REA, `morluto/rea` at commit `aa48d494e9726d469a1b9ed917f021d8a1a4c7ea`.
  REA is MIT-licensed (Copyright (c) 2026 morluto); the file's source is reproduced here under
  that licence.
- **Audited file:** `src/domain/analysisErrorProjection.ts`, sha256
  `a670809bcfcdd001fe1deb8462433fea700c502dbe0cd20e6e33e27a746b3fa7`. The report's `source` is
  that file byte for byte; the test asserts the hash, so the byte-identity check it exercises is
  made against the real file and not a stand-in.
- **Tool:** StrykerJS 10.0.0 (`@stryker-mutator/vitest-runner`, `@stryker-mutator/typescript-checker`,
  TypeScript 7.0.2), `coverageAnalysis: perTest`, `vitest.related: true`, mutating that one file.
- **Captured:** 2026-11-01, on the workstation, as the `json` reporter's output of a full
  Stryker run (662 mutants: 340 CompileError, 88 Killed, 159 Survived, 75 NoCoverage).

## What was changed from the captured report

1. The file's `mutants` array was cut from 662 to 31, chosen by id so that every status in
   the report is present and the cases the reader must handle are all real:
   - Killed `1 3 7 12`; CompileError `0 2 5 519`.
   - Survived single-line `11 19 20`, and the seven on line 578 (`512`-`518`) plus NoCoverage
     `520 521` on the same line, so the line-spreading sample has a crowded line to spread from.
   - Multi-line locations: Survived `117 282 426`, NoCoverage `607`.
   - Anchors that occur more than once in the file (so the patch applier must refuse them):
     Survived `374 422`, NoCoverage `593`.
   - Other NoCoverage: `26 27 248 267`.
   Each kept mutant is unmodified.
2. `projectRoot` was a host path on the capturing machine; it is replaced with `/work/rea`.
   Nothing reads it.

Everything else (`source`, `testFiles`, `config`, `framework`, `thresholds`, `schemaVersion`) is
as captured.
