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

// CompileCheck is a REAL type-check, SCOPED to the audited file and the test
// under consideration — not the whole project.
//
// It used to run project-mode (`tsc --noEmit -p tsconfig.json`). In single-file
// mode that was fine: the only tsconfig present was our own scaffold. Against a
// real repository (--repo-dir) the PROJECT's tsconfig governs, so the check
// type-checks every file the project includes — and any pre-existing error
// anywhere in the repo fails it. Observed on a real TypeScript project whose
// unrelated modules import workspace-sibling packages that were not installed:
// the test-writer authored a perfectly good test, the gate reported "does not
// compile" three times over errors in files the audit never touched, and every
// survivor was reported unproven. The run still graded and looked healthy.
//
// This is the same defect that was fixed for Go by scoping `go vet` to the
// audited package instead of ./... — a compile gate must answer "does THIS test
// compile", never "is this entire repository clean".
//
// It does NOT name files on the command line either — see the body for why
// that broke the single-file scaffold on tsc 5 and every repo on tsc 6.
func (tsPlugin) CompileCheck(codePath, testPath string) [][]string {
	// A PROJECT FILE OF OUR OWN, written to a temp directory for the
	// duration of the check — never files named on the command line, and
	// never the project's tsconfig. Files on the command line made tsc
	// ignore EVERY tsconfig, including the scaffold's, so the shim the
	// scaffold ships (corral-env.d.ts, declaring node:test / node:assert)
	// was never loaded: every compliant single-file workspace failed the
	// gate with TS2307, the gate reported itself DISABLED, and a mutant
	// returning 'yes' from a boolean function was scored KILLED instead of
	// invalid. Under TypeScript 6 the same shape fails harder — TS5112
	// "tsconfig.json is present but will not be loaded if files are
	// specified" — in every repo that has a tsconfig, which is every repo.
	//
	// The temp project lists exactly the audited file, the test, and the
	// shim when present, with the options the scaffold assumes, so the
	// check stays scoped (imports are still followed) and the project's own
	// tsconfig — with its file selection and its pre-existing errors — is
	// never consulted. Written under $TMPDIR, so nothing lands in the tree
	// the workspace runner restores; removed on every exit path.
	files := []string{codePath}
	if testPath != "" && testPath != codePath {
		files = append(files, testPath)
	}
	var quoted []string
	for _, f := range files {
		quoted = append(quoted, `\"$PWD/`+f+`\"`)
	}
	script := `d=$(mktemp -d) || exit 2; ` +
		`extra=""; if [ -f corral-env.d.ts ]; then extra=",\"$PWD/corral-env.d.ts\""; fi; ` +
		`printf '%s' "{\"compilerOptions\":{\"noEmit\":true,\"skipLibCheck\":true,\"target\":\"es2022\",\"module\":\"nodenext\",\"moduleResolution\":\"nodenext\",\"strict\":true,\"allowImportingTsExtensions\":true},\"files\":[` +
		strings.Join(quoted, ",") + `$extra]}" > "$d/corral-tsc.json"; ` +
		`tsc -p "$d/corral-tsc.json"; rc=$?; rm -rf "$d"; exit $rc`
	return [][]string{{"sh", "-c", script}}
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

// TestRoots names TS's additional conventional test roots — mirrors
// jsPlugin.TestRoots, see there for rationale.
// HarnessFiles mirrors jsPlugin's: the same runners, the same files.
func (tsPlugin) HarnessFiles() []string { return jsPlugin{}.HarnessFiles() }

func (tsPlugin) TestRoots() []string { return []string{"__tests__", "test", "tests"} }

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

// WorkspaceRunEnv is a no-op. TestCmd uses Node's native `--experimental-
// strip-types`, an in-memory transform with no persistent on-disk cache
// keyed off source metadata — so THIS plugin's own scoring path has no
// analog of python.go's __pycache__ hole. Out of scope, not ruled out in
// general: `ts-node` (not used by TestCmd here, but a plausible operator
// `-- <cmd>` override) has its own transpile cache that CAN be keyed off
// file mtime/size in some configurations — a project that overrides TestCmd
// to run through ts-node on the workspace substrate could plausibly hit the
// same class of bug this method exists to close for Python. See
// lang.Plugin.WorkspaceRunEnv's doc comment.
func (tsPlugin) WorkspaceRunEnv() (env []string, cleanup func()) { return nil, func() {} }

// FailFastArgs is the JS runners' `--bail`; see jsFailFastArgs. The stock
// `node --experimental-strip-types --test` command has no such flag and is
// unchanged.
func (tsPlugin) FailFastArgs(testCmd []string) ([]string, bool) {
	return jsFailFastArgs(testCmd)
}

// COVERAGE FOR TYPESCRIPT — the JavaScript mechanism, unchanged.
//
// TypeScript delegates to jsPlugin because the instrument is the RUNTIME, not
// the language: NODE_V8_COVERAGE records whatever Node actually executed, and
// Node 22 strips types natively (process.features.typescript === "strip"), so
// a .ts file run by `node --test` reports under its OWN .ts path. Verified on
// a fixture — the reduced report named lib/calc.ts, not a transpiled artifact.
//
// THE LIMIT IS WORTH STATING because it is not visible from the report: this
// holds when the runner executes the .ts source. A toolchain that compiles to
// dist/ first and runs the JAVASCRIPT is measured accurately too — but the
// paths it reports are the compiled ones, and no source map is consulted here,
// so a caller pairing those against .ts sources will find nothing rather than
// something wrong. Absent, not false: the same tri-state discipline the rest
// of this parser keeps.
func (tsPlugin) CoverageCmd(testCmd []string) (cmd []string, ok bool) {
	return jsPlugin{}.CoverageCmd(testCmd)
}

// ParseCoverage reads the same reduced report jsPlugin produces.
func (tsPlugin) ParseCoverage(stdout, modulePath string) (executed map[string]bool, err error) {
	return jsPlugin{}.ParseCoverage(stdout, modulePath)
}
