# JavaScript + TypeScript Audit Plugins — Design

**Date:** 2026-07-18
**Status:** Approved for planning
**Author:** Peter Bethke (+ Claude)

## Problem

The `internal/lang` plugin seam grades Go, Python (pytest), and Ruby (minitest/RSpec).
This adds **JavaScript** and **TypeScript** — the highest-reach languages — as two more
plugins. The seam is unchanged; `advpool`/`brain`/`testgen`/CLI already resolve any
registered plugin by file extension.

## Validated mechanics (Node 22.23 local, Node 22.22 brain host)

- JS: `node --test` (node:test builtin) runs `*.test.js`; `node --check` syntax-checks.
  Zero-infra, offline, no node_modules.
- TS: `node --experimental-strip-types --test foo.test.ts` runs a TS test that imports a
  sibling `.ts` module (validated: a two-file TS example passed). Native strip-types is
  default in Node ≥23.6 but our hosts are Node 22, so the `--experimental-strip-types`
  flag (Node ≥22.6) is required. Type-CHECKING is separate (`tsc --noEmit`, needs the
  `typescript` package + `@types/node`).

## Scope

- `internal/lang/javascript.go` — jsPlugin (`.js`, `.mjs`, `.cjs`), node:test.
- `internal/lang/typescript.go` — tsPlugin (`.ts`), strip-types run + tsc type-check.
- Unit tests for both; hermetic in-jail Score tests for both.
- CI + host provisioning: Node (already present), `typescript` + `@types/node` system-wide.
- Docs.

**Out of scope:** `.tsx`/JSX/React; bundler/monorepo setups; C plugin.

## Global Constraints

- No change to the seam or existing plugins; all suites pass unchanged.
- Fail closed: unknown lang / failed preflight / failed jail run never certifies.
- Offline grading (network off in jail): node:test is builtin (zero-infra); `typescript`
  + `@types/node` must be host-present SYSTEM-WIDE (jail binds /usr, not ~/.npm or a
  project node_modules — the jail-visibility rule). No `npm install` at grade time.
- No new external Go dependencies. gofmt + gosec clean; SPDX headers.

## The JavaScript plugin

- `Detect` → `.js`, `.mjs`, `.cjs`.
- `Scaffold()` → `{}` (a JS test `require`s/`import`s its sibling module).
- `TestCmd()` → `["node","--test"]` (node:test discovers `*.test.js` / files with tests
  in the workspace).
- `CompileCheck(codePath, testPath)` → `["node","--check",codePath,"&&","node","--check",testPath]`
  (syntax check both; `&&` honored under the jail's `sh -c`).
- `TestPath("pkg/foo.js")` → `"pkg/foo.test.js"`.
- `Preflight()` → `node` on PATH (fail-closed).
- `PromptLang()` → `"JavaScript"`.
- `TestWriterSystem()` → write ONE `node:test` test: `const {test} = require('node:test')`,
  `const assert = require('node:assert')`, `require('./<base>.js')` the target; boundary-test
  the goal; deterministic, no network, no external deps; raw JS only.
- `MutantSystem()` → standard mutation framing, JS-flavored (complete drop-in `.js` files,
  same exported signatures, genuinely goal-violating, no syntax errors, no tests).

## The TypeScript plugin

- `Detect` → `.ts` (NOT `.tsx` in v1).
- `Scaffold()` → a minimal `tsconfig.json` enabling the type-check to resolve node types
  and modern modules:
  ```json
  {"compilerOptions":{"module":"nodenext","moduleResolution":"nodenext","target":"es2022","types":["node"],"noEmit":true,"skipLibCheck":true,"strict":true,"allowImportingTsExtensions":true}}
  ```
  (`allowImportingTsExtensions` + `noEmit` lets the test import `./foo.ts` explicitly, which
  is what Node's strip-types also wants.)
- `TestCmd()` → `["node","--experimental-strip-types","--test"]` (runs `*.test.ts` on Node
  22 via type-stripping).
- `CompileCheck(codePath, testPath)` → `["tsc","--noEmit","-p","tsconfig.json"]` — a real
  type-check of the whole workspace (the code + the authored/dev test) against the scaffold
  tsconfig. Needs `typescript` + `@types/node` host-present (system-wide). A type error
  fails the check → the authored test is rejected (same gate as Go's `go vet`).
- `TestPath("pkg/foo.ts")` → `"pkg/foo.test.ts"`.
- `Preflight()` → BOTH `node` AND `tsc` on PATH (TS genuinely needs the compiler; unlike
  JS, this is a hard requirement, so it is preflighted — fail-closed if tsc is absent).
- `PromptLang()` → `"TypeScript"`.
- `TestWriterSystem()` → write ONE `node:test` test in TypeScript: `import {test} from
  'node:test'`, `import assert from 'node:assert'`, and `import { … } from './<base>.ts'`
  with the EXPLICIT `.ts` extension (required by strip-types + the tsconfig); typed,
  boundary-test the goal; deterministic, no network, no external deps; raw TS only.
- `MutantSystem()` → standard framing, TS-flavored (complete drop-in `.ts` files, same
  exported signatures + types, genuinely goal-violating, no type/syntax errors, no tests).

### TS import-extension caveat

Node strip-types + the tsconfig require test imports to name the module with its explicit
`.ts` extension (`from './foo.ts'`). The pool's own authored test is prompted to do this,
so it always runs. A *dev's* TS suite that imports extensionless (`from './foo'`) may fail
to run under strip-types → that run fails closed (never a false pass) rather than silently
mis-grading. This is an accepted v1 limitation, documented; a future pass can add a
resolution shim.

## Signature extraction

`repoindex.ExtractSignatures(rs.Code, rs.Lang)` — confirm `internal/repoindex/lang.go` maps
`.js`→`javascript`/`.ts`→`typescript` (tree-sitter has both). If a language name is
unmapped, extraction returns `ErrUnsupportedLang` (non-fatal, `sigs=nil`) — the run still
grades from the full code. The plan verifies the mapping and, if the plugin `Name()` differs
from repoindex's expected key, threads the right key.

## Testing

- Unit (both plugins): Detect (all extensions), TestPath, CompileCheck argv, TestCmd,
  Preflight (js: node only; ts: node + tsc), PromptLang, prompt content.
- In-jail hermetic (both): a tiny module + thorough suite (kills all mutants) + gappy suite
  (leaves a survivor), through the real jail, using an always-true-style survivor mutant
  (the validated lesson). Skips cleanly when node/tsc/jail are unavailable. Manual
  out-of-band validation with real node/tsc before merge (node:test + strip-types already
  validated; tsc validated once installed).
- Go regression: existing suites unchanged; the 5-plugin registry stays green.

## Provisioning + rollout

1. CI `validate` job: ensure Node present (it is on ubuntu-latest); install `typescript` +
   `@types/node` SYSTEM-WIDE (jail-visible): `sudo npm install -g typescript @types/node`
   (global lands under /usr/lib/node_modules — jail-visible), non-fatal.
2. Brain host operator step (documented): `ssh hetzner 'sudo npm install -g typescript
   @types/node'`; a missing tsc ⇒ TS runs fail closed (JS still works with just node).
3. Docs: README/ROADMAP/SKILL note Go + Python + Ruby + JS + TS.
4. Follow-on: `.tsx`/JSX; a live JS/TS audit recording; C plugin.
