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
   - Lines that occur more than once in the file as whole lines, so the line-bounded anchor is
     ambiguous and the patch applier must refuse them: Survived `374 422`. NoCoverage `593`'s
     line also appears as the tail of more-indented lines, which is not a repeat: it is taken.
   - Other NoCoverage: `26 27 248 267`.
   Each kept mutant is unmodified.
2. `projectRoot` was a host path on the capturing machine; it is replaced with `/work/rea`.
   Nothing reads it.

Everything else (`source`, `testFiles`, `config`, `framework`, `thresholds`, `schemaVersion`) is
as captured.

# stryker-rea-full.json.gz

The FULL REA report the fixture above was trimmed from: all 662 mutants of
`src/domain/analysisErrorProjection.ts`, scrubbed and gzipped.

- **Project, tool, capture:** as for the fixture above (REA `morluto/rea` at commit
  `aa48d494e9726d469a1b9ed917f021d8a1a4c7ea`, MIT-licensed, Copyright (c) 2026 morluto; StrykerJS 10.0.0,
  captured 2026-11-01 on the workstation). The report reproduces REA's source and test files under that licence.
- **Source:** the `json` reporter's output of that run, 1,208,777 bytes, sha256
  `8496b0c19a07efa4a29090eb0156d263b17a3203751cbb0cfecbe117f394d85c`.
- **What was changed**, on 2026-11-05:
  1. `projectRoot` was a host path on the capturing machine; it is replaced with `/work/rea`, as in the
     fixture above.
  2. `config` (the operator's whole Stryker configuration) is removed. The fixture above keeps it; this
     copy does not, because nothing reads it and it is the part of a report most likely to name the
     capturing machine.
  3. The JSON was re-encoded compactly (key order and every other value unchanged): 1,206,812 bytes, sha256
     `4c6f2bf0a8172e012e8b352fbafc46f749dc69fc65070aaf4038acaae69f813f`. Then `gzip -9 -n`: 117,334 bytes,
     sha256 `f7b4204f952df555885c3a023919ff1cc5d4d5783a140833dbc35c06db316977`.
- Neither change touches anything the reader parses, so the golden below is byte-identical whether it is
  computed from the source report or this copy (checked when this copy was made).

# stryker-rea-full.golden.json

The output of `ReadStryker` over the FULL REA report (all 662 mutants of `src/domain/analysisErrorProjection.ts`,
the report from the 2026-11-01 Stryker run, sha256
`8496b0c19a07efa4a29090eb0156d263b17a3203751cbb0cfecbe117f394d85c`), with
budget 40 and seed 20261101, captured 2026-11-05 on unmodified code before Stryker moved behind the `Reader`
interface. It holds each taken mutant's id, parent sha256, span and the sha256 of its search and replace text,
plus `Sources` and `Counts`. `TestStrykerFullReportGolden` reads the committed `stryker-rea-full.json.gz`, so it
runs in CI; `CORRAL_STRYKER_FULL_REPORT` (the absolute path of an uncompressed report) overrides it. `-update`
rewrites the golden.
