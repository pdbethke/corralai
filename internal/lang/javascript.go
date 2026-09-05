// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"os/exec"
	"path/filepath"
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

// CompileCheck syntax-checks both files (`node --check`). `node --check`
// only ever checks a SINGLE file per invocation, so this returns a
// TWO-command sequence — codePath then testPath, run in order, stopping at
// the first failure — rather than trying to splice a `&&` into one argv
// element. A `&&`-joined single element only ever worked because the jail
// substrate shell-joins argv and runs it under `sh -c`; the workspace
// substrate execs argv directly with no shell, where `&&` would just be
// handed to `node --check` as a literal (nonexistent) filename — see
// lang.Plugin.CompileCheck's doc comment and python.go's pyCachePrefixEnv
// comment for the identical class of bug this avoids.
//
// `node --check` is SYNTAX only, so a mutant calling an undefined function
// at module scope, or importing a name that does not exist, read as KILLED
// (every test importing the file fails) — breakage any suite catches, which
// Go's and Python's gates mark INVALID instead. The load command imports the
// file the way a test would, dynamically so CommonJS and ESM both resolve,
// and is probed on compliant code first like every gate command.
func (jsPlugin) CompileCheck(codePath, testPath string) [][]string {
	cmds := [][]string{
		{"node", "--check", codePath},
		{"node", "--check", testPath},
		{"node", "-e", "import(require('path').resolve(process.argv[1])).catch(e => { console.error(String(e && e.message || e)); process.exit(1) })", "--", codePath},
	}
	if undef := jsUndefCheck(codePath, testPath); undef != nil {
		cmds = append(cmds, undef)
	}
	return cmds
}

// jsLintConfigs are the files a project uses to declare which globals exist.
var jsLintConfigs = []string{
	".oxlintrc.json", ".oxlintrc",
	"eslint.config.js", "eslint.config.mjs", "eslint.config.cjs",
	".eslintrc.json", ".eslintrc.js", ".eslintrc.cjs", ".eslintrc",
}

// jsUndefCheck returns an undefined-name check for the JS gate, or nil.
//
// `node --check` validates SYNTAX only. A mutant calling a function that does
// not exist passes it, reaches GRADING, fails the suite for the wrong reason,
// and is scored as KILLED — crediting the developer's tests with catching a
// mutant that was never valid code. Go's `go vet` rejects exactly this class,
// and Python's gate now does too via ruff's F821.
//
// TWO conditions, and the second is the interesting one.
//
// The linter must be on PATH — an absent optional tool must never fail the
// gate closed on nothing, since the caller treats any non-zero command as a
// rejection.
//
// And the PROJECT must declare its own environment. `no-undef` has to know
// which globals exist: `require` and `module` in CommonJS, `window` in a
// browser, `describe` under a test runner. Measured on an ordinary CommonJS
// file, oxlint with no configuration reports `require` and `module` as
// undefined — so a guessed environment would reject VALID mutants, drop them
// from the denominator, and silently shrink the measurement. corral therefore
// uses the project's own declaration or does not run the check at all. That
// deliberately trades coverage for correctness: repos without a lint config
// keep the weaker syntax-only gate rather than an unsound stronger one.
//
// Unlike Python's ruff, this cannot be run configuration-free, which is why
// the two languages are gated differently.
//
// The lint config is looked for in the REPOSITORY — the directory the code
// path is under, walking up — not in corral's own working directory, which
// is where os.Stat(c) looked: any --repo-dir run from elsewhere silently
// lost the gate.
func jsUndefCheck(codePath, testPath string) []string {
	var haveConfig bool
	dir := filepath.Dir(codePath)
	for depth := 0; depth < 8 && !haveConfig; depth++ {
		for _, c := range jsLintConfigs {
			if _, err := os.Stat(filepath.Join(dir, c)); err == nil {
				haveConfig = true
				break
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if !haveConfig {
		return nil
	}
	if bin, err := exec.LookPath("oxlint"); err == nil {
		return []string{bin, "--deny", "no-undef", codePath, testPath}
	}
	return nil
}

// TestPaths covers the common Node/JS test-file conventions, most specific
// first. All forms use a literal `.js` suffix regardless of the source
// file's own extension (.js/.mjs/.cjs) — that mirrors the prior single-path
// behavior, which always emitted `.test.js`.
//
//  1. sibling foo.test.js         — same directory as the source.
//  2. sibling foo.spec.js         — same directory, alternate suffix.
//  3. __tests__/foo.test.js       — same directory, in a `__tests__` folder
//     beside the source (a common Jest/CRA convention).
//  4. test/<subpath>/foo.test.js  — parallel tree, leading directory
//     replaced by `test` (mirrors the `src/` -> `test/` layout).
//  5. tests/<subpath>/foo.test.js — the `tests` (plural) spelling of (4).
func (jsPlugin) TestPaths(codePath string) []TestCandidate {
	dir, base, _ := splitPath(codePath)
	sub := stripFirstSegment(dir)
	testName := base + ".test.js"
	specName := base + ".spec.js"

	out := []TestCandidate{
		{Path: joinDir(dir, testName), Rank: 0},
		{Path: joinDir(dir, specName), Rank: 0},
		{Path: filepath.Join(dir, "__tests__", testName), Rank: 1},
		{Path: filepath.Join("test", sub, testName), Rank: 2},
		{Path: filepath.Join("tests", sub, testName), Rank: 2},
	}
	return dedupeCandidates(out)
}

// TestRoots names JS/TS's additional conventional test roots (beyond
// reposcan's generic "tests" default): a same-directory __tests__ folder,
// and the singular test/ spelling many Node projects use.
// HarnessFiles names what jest/vitest/mocha read before any test.
func (jsPlugin) HarnessFiles() []string {
	return []string{"jest.config.", "jest.setup.", "vitest.config.", "vitest.setup.", ".mocharc.", "package.json"}
}

func (jsPlugin) TestRoots() []string { return []string{"__tests__", "test", "tests"} }

// Preflight checks the operator's own test command's binary (e.g. a project
// script or a node version manager's shim not on PATH under "node") when one
// is given, else the stock "node" — see preflightBin and Plugin.Preflight's
// doc comment.
func (jsPlugin) Preflight(testCmd []string) error {
	return toolOnPath(preflightBin(testCmd, "node"))
}

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
The output format (a SEARCH/REPLACE edit per mutant) is specified with the task.`
}

// ImportPath is always ok=false: node:test's `require('./pricing.js')` is a
// relative path off the TEST FILE's own directory, and roles.go always
// places the authored test in the SAME directory as the code under test —
// that reference already resolves correctly no matter how deep the source
// sits in the tree, so there is nothing here to correct (see
// lang.Plugin.ImportPath's doc comment for the general rule this follows).
func (jsPlugin) ImportPath(string, func(string) bool) (string, bool) { return "", false }

// ImportNote is always "": see ImportPath — JS's relative require needs no
// per-task correction.
func (jsPlugin) ImportNote(string, bool) string { return "" }

func (jsPlugin) SingleTestCmd(testPath, selector string) ([]string, bool) { return nil, false }

func (jsPlugin) ListTestsCmd(string) ([]string, bool) { return nil, false }

func (jsPlugin) ParseTestList(string) []string { return nil }

// WorkspaceRunEnv is a no-op. TestCmd runs Node's builtin test runner
// directly against source (no separate persistent compile-cache step), so
// there is no analog of python.go's __pycache__ hole in THIS plugin's own
// scoring path today. This is NOT a claim that no JS/TS toolchain anywhere
// has a same class of bug — a bundler or `ts-node`'s own transpile cache
// keyed off file metadata could plausibly exhibit it — only that jsPlugin's
// stock TestCmd doesn't route through one; see lang.Plugin.WorkspaceRunEnv's
// doc comment and typescript.go's own note for the closer case.
func (jsPlugin) WorkspaceRunEnv() (env []string, cleanup func()) { return nil, func() {} }

// FailFastArgs is `--bail` for the two JS runners that take it and corral is
// sure of (jest, mocha). `node --test` — this plugin's own stock command — has
// no such flag, so it returns ok=false and is simply unchanged; vitest is left
// out deliberately (its bail spelling has changed across major versions, and
// an unrecognised flag would make every mutant exit non-zero and read as a
// kill). See lang.FailFaster.
func (jsPlugin) FailFastArgs(testCmd []string) ([]string, bool) {
	return jsFailFastArgs(testCmd)
}

// jsFailFastArgs is shared by the javascript and typescript plugins: the
// runners are the same, and a second copy would be free to drift.
func jsFailFastArgs(testCmd []string) ([]string, bool) {
	if len(testCmd) == 0 || cmdIsShellWrapped(testCmd) {
		return nil, false
	}
	if cmdHasWord(testCmd, "jest") || cmdHasWord(testCmd, "mocha") {
		return []string{"--bail"}, true
	}
	return nil, false
}

// COVERAGE FOR JAVASCRIPT, WITH NOTHING TO INSTALL.
//
// NODE_V8_COVERAGE is built into Node, so this needs no c8, no nyc, no istanbul
// and no edit to the audited project — which matters, because the pre-flight
// runs against a STRANGER'S repository and must not ask it to add a dev
// dependency before corral will look at it. It is also runner-agnostic for the
// same reason the Ruby path is: the variable is inherited by child processes,
// so `node --test`, jest's workers, vitest, mocha and a bare `npm test` are all
// instrumented by the same mechanism, with no per-runner plugin.
//
// V8 writes raw range data — megabytes for an ordinary project — so the
// reduction runs IN NODE and only one line per file crosses to stdout. See
// corralCoverageReport for why that boundary matters.
const jsCoverageReduce = `const fs=require('fs'),path=require('path');
const dir=process.argv[2];
const state=new Map();
let files=[];
try{files=fs.readdirSync(dir)}catch(e){}
for(const f of files){
  if(!f.endsWith('.json'))continue;
  let j;
  try{j=JSON.parse(fs.readFileSync(path.join(dir,f),'utf8'))}catch(e){continue}
  for(const s of (j.result||[])){
    if(typeof s.url!=='string'||!s.url.startsWith('file://'))continue;
    let p;
    try{p=require('url').fileURLToPath(s.url)}catch(e){continue}
    // node_modules is a dependency, never a candidate for audit. Dropping it
    // here keeps the report small; the parser would drop it later anyway only
    // if it fell outside the repo root, which a vendored dep does not.
    if(p.includes('/node_modules/'))continue;
    const fns=s.functions||[];
    // A NAMED function that ran is the signal. The unnamed top-level wrapper
    // runs merely because something REQUIRED the file, so counting it would
    // mark every imported-but-unused module as executed — measured directly
    // on a fixture: a required, never-called module reports its wrapper hot
    // and every named function cold. A file with no named functions at all
    // (a constants module, a side-effecting script) can only be judged by
    // its wrapper, so it falls back to that.
    // RELATIVE TO THE WORKING DIRECTORY — see the Ruby reducer for why an
    // absolute path is only ever right on one substrate. Outside cwd is a
    // dependency or the runtime, never a candidate.
    const rel=path.relative(process.cwd(),p);
    if(rel===''||rel==='..'||rel.startsWith('..'+path.sep)||path.isAbsolute(rel))continue;
    p=rel.split(path.sep).join('/');
    const named=fns.filter(fn=>fn.functionName);
    const hit=named.length
      ? named.some(fn=>(fn.ranges||[]).some(r=>r.count>0))
      : fns.some(fn=>(fn.ranges||[]).some(r=>r.count>0));
    // Several processes (test workers) report the same file. Executed by ANY
    // of them is executed; never downgrade a true to a false.
    state.set(p,(state.get(p)||false)||hit);
  }
}
const out=['` + jsCoverageHeader + `'];
for(const [p,hit] of state)out.push((hit?'1 ':'0 ')+p);
process.stdout.write(out.join('\n')+'\n');
`

// CoverageCmd wraps a JavaScript test command in V8 coverage instrumentation.
//
// The accepted argv[0] set is the ways a Node suite is actually launched. It
// is a allow-list rather than "anything", because CoverageCmd's ok=false is
// also what certify_repo.go's language disambiguation reads to decide which
// language an operator's `--` command belongs to: returning true for every
// command would make JavaScript match a pytest invocation.
func (jsPlugin) CoverageCmd(testCmd []string) (cmd []string, ok bool) {
	if len(testCmd) == 0 {
		return nil, false
	}
	if !coverageRunnerNamed(testCmd, []string{"node", "npm", "npx", "yarn", "pnpm", "jest", "vitest", "mocha", "tap", "ava", "jasmine"}) {
		return nil, false
	}
	setup := `cat > "$d/corral_reduce.js" <<'CORRAL_JS_EOF'` + "\n" + jsCoverageReduce + "CORRAL_JS_EOF\n"
	env := `NODE_V8_COVERAGE="$d/cov"`
	return coverageRunAndReduce(setup, env, testCmd, `node "$d/corral_reduce.js" "$d/cov"`), true
}

// ParseCoverage reads the reduced report jsCoverageReduce writes. The grammar
// and its tri-state are shared with Ruby — see corralCoverageReport.
func (jsPlugin) ParseCoverage(stdout, modulePath string) (executed map[string]bool, err error) {
	return corralCoverageReport(stdout, jsCoverageHeader, "javascript", modulePath)
}
