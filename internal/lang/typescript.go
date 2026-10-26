// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/pdbethke/corralai/internal/pairing"
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
		// The project's own compiler first (Preflight's order, ruling
		// R26), else tsc on PATH. On the workspace substrate PATH is the
		// host's, which has no node_modules/.bin on it.
		`if [ -x node_modules/.bin/tsc ]; then t="./node_modules/.bin/tsc"; elif [ -f node_modules/typescript/bin/tsc ]; then t="node ./node_modules/typescript/bin/tsc"; else t=tsc; fi; ` +
		// The project's target and lib, and nothing else of its tsconfig
		// (ruling R27). es2022 alone rejected REA's toSorted (es2023) in
		// code the audit never touched, at baseline, which switched the
		// gate off for the whole run. --showConfig is the project's own
		// compiler resolving its own tsconfig, extends included; file
		// selection stays ours, so the check stays scoped. Where the
		// project sets no target (or no lib), es2022 is dropped and the
		// project's compiler default applies, as it does in the project's
		// own build: es2022 is the single-file scaffold's choice, never a
		// project's. (tsc 7 resolves an unparseable tsconfig to {} with
		// exit 0, so that case lands here too; a failing --showConfig still
		// fails the check by name.) Older tsc spells a lib
		// "lib.es2023.d.ts", the form compilerOptions.lib does not accept,
		// hence the strip.
		`if [ -f tsconfig.json ]; then ` +
		`if ! $t --showConfig -p tsconfig.json > "$d/project.json" 2>&1; then echo "corral: the project's tsc could not resolve tsconfig.json (tsc --showConfig):"; cat "$d/project.json"; rm -rf "$d"; exit 2; fi; ` +
		`node -e 'const fs=require("fs");const [p,o]=process.argv.slice(1);const c=JSON.parse(fs.readFileSync(o,"utf8"));const r=JSON.parse(fs.readFileSync(p,"utf8")).compilerOptions||{};delete c.compilerOptions.target;if(r.target!==undefined)c.compilerOptions.target=r.target;if(Array.isArray(r.lib))c.compilerOptions.lib=r.lib.map(l=>String(l).replace(/^lib\./,"").replace(/\.d\.ts$/,""));fs.writeFileSync(o,JSON.stringify(c))' "$d/project.json" "$d/corral-tsc.json" || { echo "corral: could not read the target and lib that tsc --showConfig resolved from tsconfig.json"; rm -rf "$d"; exit 2; }; ` +
		`fi; ` +
		`$t -p "$d/corral-tsc.json"; rc=$?; rm -rf "$d"; exit $rc`
	return [][]string{{"sh", "-c", script}}
}

// TestRules is jsFamilyRules with the `.ts` suffix — see there for the
// ordering rationale (sibling .test/.spec, __tests__/, then a
// leading-segment-stripped parallel test/ or tests/ tree).
func (tsPlugin) TestRules() []pairing.Rule { return jsFamilyRules(".ts") }

// HarnessFiles mirrors jsPlugin's: the same runners, the same files.
func (tsPlugin) HarnessFiles() []string { return jsPlugin{}.HarnessFiles() }

// Preflight requires BOTH the test runtime AND tsc (TS genuinely needs the
// compiler; unlike JS this is a hard dependency, preflighted fail-closed).
// The runtime check honors the operator's own test command's binary when one
// is given (e.g. a node version manager's shim not on PATH under "node"),
// else falls back to the stock "node" — see preflightBin and
// Plugin.Preflight's doc comment. tsc is checked UNCONDITIONALLY either way:
// CompileCheck always invokes it directly, never via testCmd, so there is no
// operator-supplied command to defer to for it.
//
// The compiler is the PROJECT's own when it has one (B7, ruling R26): its
// node_modules/.bin/tsc, or the typescript package's bin/tsc, ahead of a tsc
// on PATH, on every substrate — the same order CompileCheck's script uses at
// run time, so the check and the compile cannot disagree about which tsc. A
// project-local TypeScript with no global one (REA) was refused here before
// its first mutant.
func (tsPlugin) Preflight(repoRoot string, testCmd []string) error {
	if err := toolOnPath(preflightBin(testCmd, "node")); err != nil {
		return err
	}
	var looked []string
	if repoRoot != "" {
		for _, rel := range projectTscPaths {
			p := filepath.Join(repoRoot, filepath.FromSlash(rel))
			if fi, err := os.Stat(p); err == nil && fi.Mode().IsRegular() {
				return nil
			}
			looked = append(looked, p)
		}
	}
	if err := toolOnPath("tsc"); err != nil {
		if len(looked) == 0 {
			return err
		}
		return fmt.Errorf("lang: no TypeScript compiler: not the project's (%s) and not on PATH: %w", strings.Join(looked, ", "), err)
	}
	return nil
}

// projectTscPaths are where a project's own TypeScript compiler lives,
// repository-relative, in the order Preflight and CompileCheck try them:
// npm's bin link, then the package's own entry script (run with node, for a
// tree installed without bin links).
var projectTscPaths = []string{"node_modules/.bin/tsc", "node_modules/typescript/bin/tsc"}

func (tsPlugin) PromptLang() string { return "TypeScript" }

func (tsPlugin) TestWriterSystem() string {
	return `You are a TEST-WRITER. Given a security control GOAL, a target source file, and its signature surface, write ONE executable Node.js test in TypeScript using the builtin node:test runner that verifies the code SATISFIES the goal.
- Start with ` + "`import { test } from 'node:test';`" + ` and ` + "`import assert from 'node:assert';`" + `, and import the target with its EXPLICIT .ts extension (e.g. ` + "`import { quote } from './pricing.ts';`" + ` — the explicit extension is required).
- Fully typed; it MUST FAIL if the goal is violated — test the goal's boundary (what a weakened implementation would pass that a compliant one must not).
- Builtin modules only (node:test, node:assert). No external packages, deterministic, no network.
Return ONLY the raw TypeScript test file content — no prose, no markdown fences.`
}

func (tsPlugin) MutantSystem() string {
	return mutantSystem("exports, signatures, and types (a drop-in replacement that type-checks)", "type/syntax errors")
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

// TestNamesInSource lists the file's tests by their describe/it titles
// (jsTestNames, shared by JavaScript and TypeScript).
func (tsPlugin) TestNamesInSource(_, src string) []string { return jsTestNames(src) }

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

// CoverageCounts reads the same reduced report jsPlugin produces.
func (tsPlugin) CoverageCounts(stdout, modulePath string) (map[string]LineCounts, error) {
	return jsPlugin{}.CoverageCounts(stdout, modulePath)
}

// LineComment is the typescript line-comment marker.
func (tsPlugin) LineComment() string { return "//" }

// DependencyDirs is the Node dependency tree, shared with javascript.
func (tsPlugin) DependencyDirs() []DepDir { return jsFamilyDependencyDirs() }
