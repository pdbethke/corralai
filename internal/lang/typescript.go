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
		// No `types:["node"]`: that would force tsc to resolve @types/node, which
		// is NOT present in the ephemeral jail workspace (and a globally-installed
		// @types/node is off tsc's default typeRoots) — so the type-check would
		// spuriously fail with TS2688. Instead we ship a tiny self-contained
		// ambient declaration for the only node builtins an audit test uses
		// (node:test, node:assert), so the type-check is zero-infra AND still
		// catches real type errors in the code under review.
		"tsconfig.json": `{"compilerOptions":{"module":"nodenext","moduleResolution":"nodenext","target":"es2022","noEmit":true,"skipLibCheck":true,"strict":true,"allowImportingTsExtensions":true}}` + "\n",
		// Permissive on purpose: a generated test may reach for any node:test
		// export (describe/it/before/mock/…) or import assert by default OR by
		// name. Declaring the surface as `any` lets every well-formed test
		// type-check, while tsc still catches real type errors in the CODE under
		// review (that's the point of the check). A too-narrow shim made the
		// test-writer thrash for 11 minutes on tsc rejections — don't do that.
		"corral-env.d.ts": `declare module "node:test" {
  export const test: any;
  export const describe: any;
  export const it: any;
  export const suite: any;
  export const before: any;
  export const after: any;
  export const beforeEach: any;
  export const afterEach: any;
  export const mock: any;
  const _default: any;
  export default _default;
}
declare module "node:assert" {
  const _assert: any;
  export default _assert;
  export const ok: any;
  export const equal: any;
  export const strictEqual: any;
  export const deepEqual: any;
  export const deepStrictEqual: any;
  export const notStrictEqual: any;
  export const throws: any;
  export const rejects: any;
  export const match: any;
  export const fail: any;
  export const ifError: any;
}
`,
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
The output format (a SEARCH/REPLACE edit per mutant) is specified with the task.`
}

func (tsPlugin) SingleTestCmd(testPath, selector string) ([]string, bool) { return nil, false }
