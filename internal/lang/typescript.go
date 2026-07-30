// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"path/filepath"
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

// TestPaths mirrors jsPlugin.TestPaths with the `.ts` suffix — see there for
// the ordering rationale (sibling .test/.spec, __tests__/, then a
// leading-segment-stripped parallel test/ or tests/ tree).
func (tsPlugin) TestPaths(codePath string) []TestCandidate {
	dir, base, _ := splitPath(codePath)
	sub := stripFirstSegment(dir)
	testName := base + ".test.ts"
	specName := base + ".spec.ts"

	out := []TestCandidate{
		{Path: joinDir(dir, testName), Rank: 0},
		{Path: joinDir(dir, specName), Rank: 0},
		{Path: filepath.Join(dir, "__tests__", testName), Rank: 1},
		{Path: filepath.Join("test", sub, testName), Rank: 2},
		{Path: filepath.Join("tests", sub, testName), Rank: 2},
	}
	return dedupeCandidates(out)
}

// Preflight requires BOTH the test runtime AND tsc (TS genuinely needs the
// compiler; unlike JS this is a hard dependency, preflighted fail-closed).
// The runtime check honors the operator's own test command's binary when one
// is given (e.g. a node version manager's shim not on PATH under "node"),
// else falls back to the stock "node" — see preflightBin and
// Plugin.Preflight's doc comment. tsc is checked UNCONDITIONALLY either way:
// CompileCheck always invokes it directly, never via testCmd, so there is no
// operator-supplied command to defer to for it.
func (tsPlugin) Preflight(testCmd []string) error {
	if err := toolOnPath(preflightBin(testCmd, "node")); err != nil {
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

// ImportPath is always ok=false: the `import ... from './pricing.ts'` form
// is relative to the TEST FILE's own directory, and roles.go always places
// the authored test in the SAME directory as the code under test — that
// reference already resolves correctly no matter how deep the source sits
// in the tree, so there is nothing here to correct (see
// lang.Plugin.ImportPath's doc comment for the general rule this follows).
func (tsPlugin) ImportPath(string, func(string) bool) (string, bool) { return "", false }

// ImportNote is always "": see ImportPath — TS's relative import needs no
// per-task correction.
func (tsPlugin) ImportNote(string, bool) string { return "" }

func (tsPlugin) SingleTestCmd(testPath, selector string) ([]string, bool) { return nil, false }

func (tsPlugin) ListTestsCmd(string) ([]string, bool) { return nil, false }

func (tsPlugin) ParseTestList(string) []string { return nil }
