// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os/exec"
	"path/filepath"

	"github.com/pdbethke/corralai/internal/pairing"
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
	_, haveConfig := nearestAncestorWith(filepath.Dir(codePath), jsLintConfigs, 8)
	if !haveConfig {
		return nil
	}
	if bin, err := exec.LookPath("oxlint"); err == nil {
		return []string{bin, "--deny", "no-undef", codePath, testPath}
	}
	return nil
}

// jsFamilyRules is the ONE declaration of the common Node/JS test-file
// conventions, shared by JavaScript and TypeScript (which differ only in the
// literal suffix ext, ".js" or ".ts"), most specific first. Every form uses
// that literal suffix regardless of the source file's own extension
// (.js/.mjs/.cjs) — that mirrors the prior single-path behavior, which always
// emitted `.test.js`.
//
//  1. sibling foo.test.js         — same directory as the source.
//  2. sibling foo.spec.js         — same directory, alternate suffix.
//  3. __tests__/foo.test.js       — same directory, in a `__tests__` folder
//     beside the source (a common Jest/CRA convention).
//  4. test/<subpath>/foo.test.js  — parallel tree, leading directory
//     replaced by `test` (mirrors the `src/` -> `test/` layout).
//  5. tests/<subpath>/foo.test.js — the `tests` (plural) spelling of (4).
//
// The beside-dir and parallel-tree rules also name JS/TS's search roots
// (beyond pairing.DefaultRoot "tests"): a same-directory __tests__ folder,
// and the singular test/ spelling many Node projects use.
func jsFamilyRules(ext string) []pairing.Rule {
	return []pairing.Rule{
		{Shape: pairing.Sibling, Name: "{base}.test" + ext},
		{Shape: pairing.Sibling, Name: "{base}.spec" + ext},
		{Shape: pairing.BesideDir, Name: "{base}.test" + ext, Dir: "__tests__", Rank: 1},
		{Shape: pairing.ParallelTree, Name: "{base}.test" + ext, Dir: "test", Rank: 2},
		{Shape: pairing.ParallelTree, Name: "{base}.test" + ext, Dir: "tests", Rank: 2},
	}
}

// TestRules is jsFamilyRules with the `.js` suffix.
func (jsPlugin) TestRules() []pairing.Rule { return jsFamilyRules(".js") }

// HarnessFiles names what jest/vitest/mocha read before any test.
func (jsPlugin) HarnessFiles() []string {
	return []string{"jest.config.", "jest.setup.", "vitest.config.", "vitest.setup.", ".mocharc.", "package.json"}
}

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
	return mutantSystem("exports and signatures (a drop-in replacement that loads)", "syntax errors")
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

// TestNamesInSource lists the file's tests by their describe/it titles
// (jsTestNames, shared by JavaScript and TypeScript).
func (jsPlugin) TestNamesInSource(_, src string) []string { return jsTestNames(src) }

// WorkspaceRunEnv is a no-op. TestCmd runs Node's builtin test runner
// directly against source (no separate persistent compile-cache step), so
// there is no analog of python.go's __pycache__ hole in THIS plugin's own
// scoring path today. This is NOT a claim that no JS/TS toolchain anywhere
// has a same class of bug — a bundler or `ts-node`'s own transpile cache
// keyed off file metadata could plausibly exhibit it — only that jsPlugin's
// stock TestCmd doesn't route through one; see lang.Plugin.WorkspaceRunEnv's
// doc comment and typescript.go's own note for the closer case.
func (jsPlugin) WorkspaceRunEnv() (env []string, cleanup func()) { return nil, func() {} }

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
//
// LINE COUNTS FROM V8 RANGES. V8 reports, per function, a list of
// [startOffset, endOffset) ranges with an execution count, where a nested
// range overrides the one around it. The reducer reads the file from disk,
// paints every source position with its innermost range's count, and then
// counts lines: a line is EXECUTABLE if a non-blank character on it lies in a
// measured function, and EXECUTED if one of those characters ran.
//
// "Measured function" is the named-function rule the boolean always used,
// kept because the counts must agree with it (ParseCoverage is derived from
// them). The unnamed top-level wrapper runs merely because something
// REQUIRED the file, so counting its lines would mark every imported-but-unused
// module as executed — measured directly on a fixture: a required, never-called
// module reports its wrapper hot and every named function cold. So when a file
// has named functions, only lines inside them are counted; a file with none (a
// constants module, a side-effecting script) can only be judged by its
// wrapper, and every line of it is counted.
//
// What the count is not: V8 gives positions, not statements, so a comment or a
// lone brace inside a function body counts as a line. That is the same for
// every unit run over a file, which is what the count is for — ranking units
// against each other on one file — and it is why counts are never compared
// across languages.
//
// A file whose ranges do not fit the source on disk (a runner that executes
// TRANSFORMED code under the original file's URL, as vitest does, or a file
// changed or deleted since it ran) cannot be counted. It is DISCLOSED on a
// "# uncounted <path>: <reason>" line and left out of the counts, never given
// a number. The test is the SCRIPT-LEVEL range, which spans whatever text V8
// compiled: it must equal the file's length on disk, or that length plus the
// sourceURL comment Node's TypeScript stripping appends (stripping keeps
// every position, so the file's own offsets stay true). A range that merely
// starts inside the file proves nothing — shifted offsets of transformed code
// usually do. Because ParseCoverage is derived from these counts, such a file
// is ABSENT from Preflight's map too (never measured), rather than given a
// boolean read off offsets into some other text. What remains undetectable is
// a transform that preserves the file's exact length.
//
// Output is sorted by path so one run's report is byte-stable, which is what
// lets the captured fixture reports be checked against a live run.
const jsCoverageReduce = `const fs=require('fs'),path=require('path'),url=require('url');
const dir=process.argv[2];
const lines=new Map(),uncounted=new Map(),sources=new Map();
let files=[];
try{files=fs.readdirSync(dir)}catch(e){}
files.sort();
function blank(c){return c===32||c===9||c===13||c===11||c===12||c===0xA0||c===0xFEFF}
function source(p){
  if(!sources.has(p)){
    let s=null;
    try{s=fs.readFileSync(p,'utf8')}catch(e){s=null}
    // Node strips a byte-order mark before compiling, so V8's offsets start after it.
    if(s!==null&&s.charCodeAt(0)===0xFEFF)s=s.slice(1);
    sources.set(p,s);
  }
  return sources.get(p);
}
for(const f of files){
  if(!f.endsWith('.json'))continue;
  let j;
  try{j=JSON.parse(fs.readFileSync(path.join(dir,f),'utf8'))}catch(e){continue}
  for(const s of (j.result||[])){
    if(typeof s.url!=='string'||!s.url.startsWith('file://'))continue;
    let p;
    try{p=url.fileURLToPath(s.url)}catch(e){continue}
    // node_modules is a dependency, never a candidate for audit.
    if(p.includes('/node_modules/'))continue;
    // RELATIVE TO THE WORKING DIRECTORY — see the Ruby reducer for why an
    // absolute path is only ever right on one substrate. Outside cwd is a
    // dependency or the runtime, never a candidate.
    const rel=path.relative(process.cwd(),p);
    if(rel===''||rel==='..'||rel.startsWith('..'+path.sep)||path.isAbsolute(rel))continue;
    const key=rel.split(path.sep).join('/');
    const fns=s.functions||[];
    const named=fns.filter(fn=>fn.functionName);
    const scope=named.length?named:fns;
    const src=source(p);
    if(src===null){uncounted.set(key,'the source could not be read');continue}
    const n=src.length;
    const all=[];
    for(const fn of fns)for(const r of (fn.ranges||[]))all.push(r);
    // THE SCRIPT RANGE MUST SPAN THE FILE ON DISK, exactly: the length of
    // the source, or that plus the sourceURL comment Node's TypeScript
    // stripping appends. Anything else means V8 ran different text under
    // this URL (a runner that transforms first), and its offsets mean
    // nothing in this file even when they happen to land inside it.
    let script=-1;
    for(const fn of fns){const r=(fn.ranges||[])[0];if(r&&!fn.functionName&&r.startOffset===0&&r.endOffset>script)script=r.endOffset}
    const stripped=/\.[cm]?ts$/.test(p)?n+('\n\n//# sourceURL='+s.url).length:-1;
    if(script!==n&&script!==stripped){
      uncounted.set(key,script<0
        ?'V8 reported no script-level range for it'
        :'V8 ran '+script+' characters under its URL and the file on disk has '+n+' (transformed code, or a file changed since it ran)');
      continue;
    }
    if(all.some(r=>!(r.startOffset>=0)||r.startOffset>script||!(r.endOffset>=r.startOffset))){
      uncounted.set(key,'its coverage ranges do not fit the source on disk (transformed code, or a file changed since it ran)');
      continue;
    }
    // Paint outer ranges first so an inner range overrides the one around it.
    all.sort((a,b)=>a.startOffset-b.startOffset||b.endOffset-a.endOffset);
    const hot=new Uint8Array(n),inScope=new Uint8Array(n);
    for(const r of all){const e=Math.min(r.endOffset,n),v=r.count>0?1:0;for(let i=r.startOffset;i<e;i++)hot[i]=v}
    for(const fn of scope)for(const r of (fn.ranges||[])){const e=Math.min(r.endOffset,n);for(let i=r.startOffset;i<e;i++)inScope[i]=1}
    let st=lines.get(key);
    if(!st){
      let count=1;for(let i=0;i<n;i++)if(src.charCodeAt(i)===10)count++;
      st={exe:new Uint8Array(count),ran:new Uint8Array(count)};
      lines.set(key,st);
    }
    // Several processes (test workers) report the same file: a line executed
    // by ANY of them is executed. Never downgrade a hit.
    let ln=0;
    for(let i=0;i<n;i++){
      const c=src.charCodeAt(i);
      if(c===10){ln++;continue}
      if(blank(c)||!inScope[i])continue;
      st.exe[ln]=1;
      if(hot[i])st.ran[ln]=1;
    }
  }
}
const sum=a=>a.reduce((t,v)=>t+v,0);
const out=['` + jsCoverageHeaderV2 + `'];
const keys=[...new Set([...lines.keys(),...uncounted.keys()])].sort();
for(const k of keys){
  const st=lines.get(k);
  if(st)out.push(sum(st.ran)+' '+sum(st.exe)+' '+k);
  else out.push('# uncounted '+k+': '+uncounted.get(k));
}
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
	if !coverageRunnerNamed(testCmd, coverageClaims("javascript")) {
		return nil, false
	}
	setup := `cat > "$d/corral_reduce.js" <<'CORRAL_JS_EOF'` + "\n" + jsCoverageReduce + "CORRAL_JS_EOF\n"
	env := `NODE_V8_COVERAGE="$d/cov"`
	return coverageRunAndReduce(setup, env, testCmd, `node "$d/corral_reduce.js" "$d/cov"`), true
}

// ParseCoverage is derived from CoverageCounts: a file is executed when any
// of its executable lines ran. The tri-state is CoverageCounts' own.
func (p jsPlugin) ParseCoverage(stdout, modulePath string) (executed map[string]bool, err error) {
	return executedFromCounts(p.CoverageCounts(stdout, modulePath))
}

// CoverageCounts reads the version-2 report jsCoverageReduce writes — see the
// reducer for what an executable line is in V8's terms, and
// corralCoverageCounts for the grammar.
func (jsPlugin) CoverageCounts(stdout, modulePath string) (map[string]LineCounts, error) {
	return corralCoverageCounts(stdout, jsCoverageHeaderV2, "javascript", modulePath)
}

// LineComment is the javascript line-comment marker.
func (jsPlugin) LineComment() string { return "//" }

// jsFamilyDependencyDirs is the ONE declaration of the Node dependency tree,
// shared by the javascript and typescript plugins.
func jsFamilyDependencyDirs() []DepDir { return []DepDir{{Name: "node_modules"}} }

// DependencyDirs is the Node dependency tree.
func (jsPlugin) DependencyDirs() []DepDir { return jsFamilyDependencyDirs() }
