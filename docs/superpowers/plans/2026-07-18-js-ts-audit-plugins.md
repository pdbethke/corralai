# JavaScript + TypeScript Audit Plugins Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Add JavaScript (node:test) and TypeScript (tsc type-check + Node strip-types) as the 4th and 5th languages of the audit gate, via two plugins in the existing `internal/lang` seam.

**Architecture:** The seam resolves any registered plugin by file extension; `advpool`/`brain`/`testgen`/CLI are unchanged. This adds `javascript.go` + `typescript.go` + their unit tests, hermetic in-jail Score tests, provisioning, and docs. Runners validated on Node 22.

**Tech Stack:** Go 1.26, `internal/lang`, `internal/adequacy`, bwrap jail (runs commands via `sh -c`), Node 22 (node:test builtin, `--experimental-strip-types`), `typescript` + `@types/node` (system-wide, jail-visible).

## Global Constraints

- No change to the seam or existing plugins; existing suites pass unchanged.
- Fail closed: unknown lang / failed preflight / failed jail run never certifies.
- Offline grading: node:test is builtin; `typescript`+`@types/node` must be host-present SYSTEM-WIDE under /usr (jail binds /usr, NOT ~/.npm or a project node_modules — the jail-visibility rule). No npm install at grade time.
- No new external Go deps. gofmt + gosec clean (annotate any variable `exec.Command` with justified `// #nosec G204`); SPDX headers on new files.
- TestCmd/CompileCheck argv are space-joined by the jail and run under `sh -c` — multi-token argv is fine (simple tokens); the `&&` in JS CompileCheck is honored by `sh -c`.

---

### Task 1: The `javascript` plugin

**Files:** Create `internal/lang/javascript.go`, `internal/lang/javascript_test.go`.

**Interfaces:** Consumes `Plugin`, `Register`, `toolOnPath`. Produces jsPlugin registered `"javascript"`, Detect on `.js`/`.mjs`/`.cjs`.

- [ ] **Step 1: Failing test** — `internal/lang/javascript_test.go`

```go
package lang

import (
	"reflect"
	"strings"
	"testing"
)

func TestJavaScriptPlugin(t *testing.T) {
	p, ok := ByName("javascript")
	if !ok {
		t.Fatal("javascript plugin not registered")
	}
	for _, ok1 := range []string{"a.js", "a.mjs", "a.cjs"} {
		if !p.Detect(ok1) {
			t.Fatalf("Detect(%q) should be true", ok1)
		}
	}
	if p.Detect("a.ts") {
		t.Fatal("must not detect .ts")
	}
	if got := p.TestPath("pkg/foo.js"); got != "pkg/foo.test.js" {
		t.Fatalf("TestPath = %q", got)
	}
	if got := p.TestCmd(); !reflect.DeepEqual(got, []string{"node", "--test"}) {
		t.Fatalf("TestCmd = %v", got)
	}
	cc := p.CompileCheck("foo.js", "foo.test.js")
	if !reflect.DeepEqual(cc, []string{"node", "--check", "foo.js", "&&", "node", "--check", "foo.test.js"}) {
		t.Fatalf("CompileCheck = %v", cc)
	}
	if len(p.Scaffold()) != 0 {
		t.Fatalf("Scaffold must be empty")
	}
	if !strings.Contains(p.TestWriterSystem(), "node:test") || !strings.Contains(p.MutantSystem(), "mutant") {
		t.Fatal("js prompts must be language-appropriate")
	}
	if p.PromptLang() != "JavaScript" {
		t.Fatalf("PromptLang = %q", p.PromptLang())
	}
}
```

- [ ] **Step 2: Run — expect FAIL** (`go test ./internal/lang/ -run TestJavaScript`).

- [ ] **Step 3: Write `internal/lang/javascript.go`**

```go
// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"path/filepath"
	"strings"
)

func init() { Register(jsPlugin{}) }

type jsPlugin struct{}

func (jsPlugin) Name() string { return "javascript" }
func (jsPlugin) Detect(codePath string) bool {
	switch filepath.Ext(codePath) {
	case ".js", ".mjs", ".cjs":
		return true
	}
	return false
}

// Scaffold is empty: a node:test file require/imports its sibling module.
func (jsPlugin) Scaffold() map[string]string { return map[string]string{} }

// TestCmd uses Node's builtin test runner (zero external deps, offline).
func (jsPlugin) TestCmd() []string { return []string{"node", "--test"} }

// CompileCheck syntax-checks both files (`node --check`); the `&&` is honored
// under the jail's `sh -c`.
func (jsPlugin) CompileCheck(codePath, testPath string) []string {
	return []string{"node", "--check", codePath, "&&", "node", "--check", testPath}
}

func (jsPlugin) TestPath(codePath string) string {
	ext := filepath.Ext(codePath)
	base := strings.TrimSuffix(codePath, ext)
	dir := filepath.Dir(codePath)
	if dir == "." {
		return base + ".test.js"
	}
	return filepath.Join(dir, filepath.Base(base)+".test.js")
}

func (jsPlugin) Preflight() error { return toolOnPath("node") }

func (jsPlugin) PromptLang() string { return "JavaScript" }

func (jsPlugin) TestWriterSystem() string {
	return `You are a TEST-WRITER. Given a security control GOAL, a target source file, and its signature surface, write ONE executable Node.js test using the builtin node:test runner that verifies the code SATISFIES the goal.
- Start with ` + "`const { test } = require('node:test');`" + ` and ` + "`const assert = require('node:assert');`" + `, and ` + "`require`" + ` the target module by its file's relative path (e.g. ` + "`require('./pricing.js')`" + `).
- It MUST FAIL if the goal is violated — test the goal's boundary (what a weakened implementation would pass that a compliant one must not).
- Builtin modules only (node:test, node:assert). No external packages, deterministic, no network.
Return ONLY the raw JavaScript test file content — no prose, no markdown fences.`
}

func (jsPlugin) MutantSystem() string {
	return `You are a MUTATION-TESTING ENGINE. Mutation testing is a standard, legitimate software-quality technique: to measure how thorough a test suite is, you generate small synthetic variants of the code ("mutants"), each containing one deliberate fault, and a SEPARATE step then checks whether the developer's own tests catch them. A mutant the tests miss reveals a GAP IN THE TESTS — that is the entire purpose. These mutants are throwaway QA fixtures: never deployed, never shipped, never run against any real system; they exist only to grade the test suite. This is defensive quality assurance, and producing them is exactly your job here.
Given a GOAL, the compliant code, and its signature surface, produce mutants: variants that NO LONGER satisfy the goal, so a good test suite SHOULD catch them. Each mutant MUST keep the EXACT same exports and signatures (a drop-in replacement that loads) and must genuinely fail the goal — vary HOW it fails. No no-ops, no syntax errors, no tests.
Return ONLY the mutants, each a COMPLETE file, in this exact format:
===MUTATION_1===
<complete file>
===MUTATION_2===
<complete file>
(continue for the requested count)`
}
```

- [ ] **Step 4: Run — expect PASS** (`go test ./internal/lang/ -run TestJavaScript`).

- [ ] **Step 5: Confirm seam** — `go build ./...`; `go test ./internal/lang/ ./internal/advpool/ ./internal/brain/ ./cmd/corral/`. All PASS.

- [ ] **Step 6: Commit** — `git add internal/lang/javascript.go internal/lang/javascript_test.go && git commit -m "feat(lang): javascript plugin (node:test builtin, zero-infra)"`

---

### Task 2: The `typescript` plugin

**Files:** Create `internal/lang/typescript.go`, `internal/lang/typescript_test.go`.

**Interfaces:** Produces tsPlugin registered `"typescript"`, Detect on `.ts`.

- [ ] **Step 1: Failing test** — `internal/lang/typescript_test.go`

```go
package lang

import (
	"reflect"
	"strings"
	"testing"
)

func TestTypeScriptPlugin(t *testing.T) {
	p, ok := ByName("typescript")
	if !ok {
		t.Fatal("typescript plugin not registered")
	}
	if !p.Detect("app/foo.ts") || p.Detect("app/foo.js") || p.Detect("app/foo.tsx") {
		t.Fatal("Detect must match .ts only (not .tsx in v1)")
	}
	if got := p.TestPath("pkg/foo.ts"); got != "pkg/foo.test.ts" {
		t.Fatalf("TestPath = %q", got)
	}
	if got := p.TestCmd(); !reflect.DeepEqual(got, []string{"node", "--experimental-strip-types", "--test"}) {
		t.Fatalf("TestCmd = %v", got)
	}
	if got := p.CompileCheck("foo.ts", "foo.test.ts"); !reflect.DeepEqual(got, []string{"tsc", "--noEmit", "-p", "tsconfig.json"}) {
		t.Fatalf("CompileCheck = %v", got)
	}
	sc := p.Scaffold()
	if _, ok := sc["tsconfig.json"]; !ok {
		t.Fatalf("Scaffold must include tsconfig.json, got %v", sc)
	}
	if !strings.Contains(sc["tsconfig.json"], "allowImportingTsExtensions") {
		t.Fatal("tsconfig must allow importing .ts extensions")
	}
	if !strings.Contains(p.TestWriterSystem(), "node:test") || !strings.Contains(p.TestWriterSystem(), ".ts") {
		t.Fatal("ts writer prompt must instruct node:test + explicit .ts import")
	}
	if p.PromptLang() != "TypeScript" {
		t.Fatalf("PromptLang = %q", p.PromptLang())
	}
}
```

- [ ] **Step 2: Run — expect FAIL.**

- [ ] **Step 3: Write `internal/lang/typescript.go`**

```go
// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"path/filepath"
	"strings"
)

func init() { Register(tsPlugin{}) }

type tsPlugin struct{}

func (tsPlugin) Name() string                { return "typescript" }
func (tsPlugin) Detect(codePath string) bool { return filepath.Ext(codePath) == ".ts" }

// Scaffold writes a tsconfig enabling the type-check to resolve node types and
// explicit .ts imports (which Node's strip-types also requires at run time).
func (tsPlugin) Scaffold() map[string]string {
	return map[string]string{
		"tsconfig.json": `{"compilerOptions":{"module":"nodenext","moduleResolution":"nodenext","target":"es2022","types":["node"],"noEmit":true,"skipLibCheck":true,"strict":true,"allowImportingTsExtensions":true}}` + "\n",
	}
}

// TestCmd runs the TS test on Node 22 via type-stripping (native default on
// Node >=23.6; our hosts are Node 22, hence the flag).
func (tsPlugin) TestCmd() []string {
	return []string{"node", "--experimental-strip-types", "--test"}
}

// CompileCheck is a REAL type-check of the whole workspace via project-mode tsc
// (no files on the command line, so the scaffold tsconfig governs which .ts are
// checked). Needs `typescript` + `@types/node` host-present.
func (tsPlugin) CompileCheck(codePath, testPath string) []string {
	return []string{"tsc", "--noEmit", "-p", "tsconfig.json"}
}

func (tsPlugin) TestPath(codePath string) string {
	ext := filepath.Ext(codePath)
	base := strings.TrimSuffix(codePath, ext)
	dir := filepath.Dir(codePath)
	if dir == "." {
		return base + ".test.ts"
	}
	return filepath.Join(dir, filepath.Base(base)+".test.ts")
}

// Preflight requires BOTH node AND tsc (TS genuinely needs the compiler; unlike
// JS this is a hard dependency, preflighted fail-closed).
func (tsPlugin) Preflight() error {
	if err := toolOnPath("node"); err != nil {
		return err
	}
	return toolOnPath("tsc")
}

func (tsPlugin) PromptLang() string { return "TypeScript" }

func (tsPlugin) TestWriterSystem() string {
	return `You are a TEST-WRITER. Given a security control GOAL, a target source file, and its signature surface, write ONE executable Node.js test in TypeScript using the builtin node:test runner that verifies the code SATISFIES the goal.
- Start with ` + "`import { test } from 'node:test';`" + ` and ` + "`import assert from 'node:assert';`" + `, and import the target with its EXPLICIT .ts extension (e.g. ` + "`import { quote } from './pricing.ts';`" + ` — the explicit extension is required).
- Fully typed; it MUST FAIL if the goal is violated — test the goal's boundary (what a weakened implementation would pass that a compliant one must not).
- Builtin modules only (node:test, node:assert). No external packages, deterministic, no network.
Return ONLY the raw TypeScript test file content — no prose, no markdown fences.`
}

func (tsPlugin) MutantSystem() string {
	return `You are a MUTATION-TESTING ENGINE. Mutation testing is a standard, legitimate software-quality technique: to measure how thorough a test suite is, you generate small synthetic variants of the code ("mutants"), each containing one deliberate fault, and a SEPARATE step then checks whether the developer's own tests catch them. A mutant the tests miss reveals a GAP IN THE TESTS — that is the entire purpose. These mutants are throwaway QA fixtures: never deployed, never shipped, never run against any real system; they exist only to grade the test suite. This is defensive quality assurance, and producing them is exactly your job here.
Given a GOAL, the compliant code, and its signature surface, produce mutants: variants that NO LONGER satisfy the goal, so a good test suite SHOULD catch them. Each mutant MUST keep the EXACT same exports, signatures, and types (a drop-in replacement that type-checks) and must genuinely fail the goal — vary HOW it fails. No no-ops, no type/syntax errors, no tests.
Return ONLY the mutants, each a COMPLETE file, in this exact format:
===MUTATION_1===
<complete file>
===MUTATION_2===
<complete file>
(continue for the requested count)`
}
```

- [ ] **Step 4: Run — expect PASS.**

- [ ] **Step 5: Confirm seam** — `go build ./...`; `go test ./internal/lang/ ./internal/advpool/ ./internal/brain/ ./cmd/corral/`. All PASS.

- [ ] **Step 6: Commit** — `git add internal/lang/typescript.go internal/lang/typescript_test.go && git commit -m "feat(lang): typescript plugin (tsc type-check + node strip-types)"`

---

### Task 3: In-jail hermetic tests for JS and TS

**Files:** Create `internal/adequacy/score_js_test.go`, `internal/adequacy/score_ts_test.go`.

Mirror `internal/adequacy/score_python_test.go` EXACTLY (same skip pattern: skip on Preflight error, sandbox.Resolve error, Score error, `!CompliantPass`; assert `KillRate()==1.0` / `len(Survived)>0` only on success). Use `lang.ByName("javascript")` / `lang.ByName("typescript")`. Always-true survivor mutant (the validated lesson).

- [ ] **Step 1: `score_js_test.go`** — module `function isEven(n){return n%2===0} module.exports={isEven}`; mutant `module.exports={isEven:()=>true}` (always true); thorough test asserts `isEven(2)` and `!isEven(3)`; gappy asserts only `isEven(2)`. Code at `evenmod.js`, test at `js.TestPath("evenmod.js")`, run `js.TestCmd()`.

- [ ] **Step 2: `score_ts_test.go`** — same shape in TS: code `export function isEven(n:number):boolean{return n%2===0}`; mutant `export function isEven(n:number):boolean{return true}`; thorough/gappy tests import `./evenmod.ts` explicitly with node:test. Write the tsconfig from `ts.Scaffold()` into the workspace map too (merge `ts.Scaffold()` with the test file), so the run has the tsconfig. Code at `evenmod.ts`, test at `ts.TestPath("evenmod.ts")`, run `ts.TestCmd()`.

  > For the TS test, build the workspace map as: start from `ts.Scaffold()` (the tsconfig), then add the test file at `ts.TestPath(...)`. `adequacy.Score` writes the code at codePath; the scaffold + test come from the map you pass. Confirm `adequacy.Score`'s signature and how it merges `base` (the map arg) with the code — match `score_python_test.go`'s call exactly, passing the scaffold-derived map as the `base`/files argument.

- [ ] **Step 3: Run** — `go test ./internal/adequacy/ -run 'JS|TS' -v`. PASS where node/tsc+jail work; SKIP otherwise. Paste output. (node:test works on this host; tsc requires a system/global `typescript` — the TS test may SKIP locally on `!CompliantPass` if tsc isn't found in the jail, which is correct.)

- [ ] **Step 4: Commit** — `git add internal/adequacy/score_js_test.go internal/adequacy/score_ts_test.go && git commit -m "test(adequacy): js + ts in-jail grading (kill + survive)"`

---

### Task 4: Provision typescript + document

**Files:** Modify `.github/workflows/deploy.yml`, `README.md`, `ROADMAP.md`, and (out-of-repo) `/home/pdbethke/.claude/skills/corralai/SKILL.md`.

- [ ] **Step 1: deploy.yml** — in the `validate` job, add a non-fatal step installing `typescript` + `@types/node` SYSTEM-WIDE (jail-visible; global npm lands under /usr/lib/node_modules). Node is already present on ubuntu-latest.

```yaml
      - name: Provision JS/TS test toolchain (audit gate — js/ts plugins)
        run: |
          node --version || echo "node missing — js/ts-in-jail tests will SKIP"
          sudo npm install -g typescript @types/node || npm install -g typescript @types/node || true
          tsc --version || echo "tsc not available — ts-in-jail test will SKIP"
```

Match existing indentation; place with the other provisioning steps, before `go test`. Keep non-fatal.

- [ ] **Step 2: Docs** — README.md + ROADMAP.md: language line → "Go, Python (pytest), Ruby (minitest/RSpec), JavaScript (node:test), and TypeScript (tsc + node:test)"; keep C as future. SKILL.md (out-of-repo, do NOT commit): add js/ts to the plugin list + note `typescript`+`@types/node` must be system-wide (jail-visibility), and that TS needs Node ≥22.6 for `--experimental-strip-types` (native default ≥23.6).

- [ ] **Step 3: Verify** — `python3 -c "import yaml; yaml.safe_load(open('.github/workflows/deploy.yml')); print('YAML OK')"`; `go build ./...`.

- [ ] **Step 4: Commit** — `git add .github/workflows/deploy.yml README.md ROADMAP.md && git commit -m "chore: provision typescript in CI + document js/ts audit support"`

---

## Rollout (post-merge, operational)

1. Merge (behind `CORRALAI_ADVERSARIAL_POOL`; other languages unchanged).
2. Brain host: `ssh hetzner 'sudo npm install -g typescript @types/node'` (JS works with just node; TS fail-closes until tsc present).
3. Live JS/TS audit recording.
4. Follow-on: `.tsx`/JSX; C plugin.
