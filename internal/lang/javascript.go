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
func (jsPlugin) Preflight(repoRoot string, testCmd []string) error {
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
// corralCoverageCounts for why that boundary matters.
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
// TRANSFORMED CODE THROUGH ITS MAP. A runner that evaluates through node:vm
// (vitest, jest) runs transformed code under the original file's URL. When
// corral's preload (jsCoveragePreload) recorded that code's inline source
// map, matched by the file's path and the script's exact length, each range's
// start and last character are mapped through node:module's SourceMap onto
// the ORIGINAL file, and the range is painted there; the counting below is
// then the same as for code run as written. A function with any range whose
// ends map to no original position, to two files, or backwards, is left out
// whole, never guessed, and a mapped source outside the working directory or
// under node_modules is not counted. The original file must be the text the
// map describes: the map's sourcesContent, when it carries one (vitest's
// do), must equal the file on disk, and a map without it must not point past
// the file; otherwise the file is DISCLOSED as uncounted (changed since it
// ran, or the map names another file).
//
// Without a recorded map, a file whose ranges do not fit the source on disk
// (transformed code, or a file changed or deleted since it ran) cannot be
// counted either. It is DISCLOSED on a
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
const lines=new Map(),uncounted=new Map(),sources=new Map(),loaded=new Set();
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
// RELATIVE TO THE WORKING DIRECTORY — see the Ruby reducer for why an
// absolute path is only ever right on one substrate. Outside cwd, or under
// node_modules, is a dependency or the runtime, never a candidate: null.
function keyOf(p){
  if(p.includes('/node_modules/'))return null;
  const rel=path.relative(process.cwd(),p);
  if(rel===''||rel==='..'||rel.startsWith('..'+path.sep)||path.isAbsolute(rel))return null;
  return rel.split(path.sep).join('/');
}
function toPath(f){
  if(f.startsWith('file://')){try{return url.fileURLToPath(f)}catch(e){return null}}
  return path.isAbsolute(f)?f:null;
}
// The source maps corral's preload recorded for code a runner evaluated
// through node:vm (corral_maps.cjs), by the evaluated file's path.
const maps=new Map();
let mapFiles=[];
try{mapFiles=fs.readdirSync(path.join(dir,'..','maps'))}catch(e){}
mapFiles.sort();
for(const f of mapFiles){
  let text='';
  try{text=fs.readFileSync(path.join(dir,'..','maps',f),'utf8')}catch(e){continue}
  for(const ln of text.split('\n')){
    if(!ln)continue;
    let r;
    try{r=JSON.parse(ln)}catch(e){continue}
    const p=typeof r.filename==='string'?toPath(r.filename):null;
    if(p===null)continue;
    if(!maps.has(p))maps.set(p,[]);
    maps.get(p).push(r);
  }
}
// count paints one file's source with V8 ranges whose offsets are positions
// in THAT text, and adds its lines to the totals.
function count(key,src,fns){
  const n=src.length;
  const named=fns.filter(fn=>fn.functionName);
  const scope=named.length?named:fns;
  const all=[];
  for(const fn of fns)for(const r of (fn.ranges||[]))all.push(r);
  // Paint outer ranges first so an inner range overrides the one around it.
  all.sort((a,b)=>a.startOffset-b.startOffset||b.endOffset-a.endOffset);
  const hot=new Uint8Array(n),inScope=new Uint8Array(n);
  for(const r of all){const e=Math.min(r.endOffset,n),v=r.count>0?1:0;for(let i=r.startOffset;i<e;i++)hot[i]=v}
  for(const fn of scope)for(const r of (fn.ranges||[])){const e=Math.min(r.endOffset,n);for(let i=r.startOffset;i<e;i++)inScope[i]=1}
  let st=lines.get(key);
  if(!st){
    let c=1;for(let i=0;i<n;i++)if(src.charCodeAt(i)===10)c++;
    st={exe:new Uint8Array(c),ran:new Uint8Array(c)};
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
// lineSpans is each line of text, by ECMAScript's line terminators (\n, \r,
// \r\n, U+2028, U+2029), as its start offset and the offset of its end
// (where its terminator begins): the lines a source map counts, original
// side and generated side alike. A file read by \n alone puts every line
// after a literal U+2028 out of step with its map.
function lineSpans(text){
  const starts=[0],ends=[];
  for(let i=0;i<text.length;i++){
    const c=text.charCodeAt(i);
    if(c===13&&text.charCodeAt(i+1)===10){ends.push(i);starts.push(i+2);i++;continue}
    if(c===10||c===13||c===0x2028||c===0x2029){ends.push(i);starts.push(i+1)}
  }
  ends.push(text.length);
  return {starts,ends};
}
function lineAt(starts,off){let lo=0,hi=starts.length-1;while(lo<hi){const m=(lo+hi+1)>>1;if(starts[m]<=off)lo=m;else hi=m-1}return lo}
// countMapped counts a script evaluated through node:vm by the original
// files its recorded source map names: each range's start and last
// character are mapped through node:module's SourceMap to an original line
// and column, and the range is painted onto that ORIGINAL file. A range
// whose ends map to no original position, to two different files, or
// backwards, is left out: never guessed.
function countMapped(p,rec,fns){
  let sm;
  try{sm=new (require('module').SourceMap)(JSON.parse(rec.map))}catch(e){return 'its recorded source map could not be read'}
  const gen=Array.isArray(rec.lineStarts)?rec.lineStarts:[];
  let sourceRoot='';
  try{const pl=JSON.parse(rec.map);if(typeof pl.sourceRoot==='string')sourceRoot=pl.sourceRoot}catch(e){}
  const origStarts=new Map();
  // A source this map cannot be trusted to describe, and why: its
  // sourcesContent is not the file on disk, or (with no sourcesContent) it
  // points past the file. Such a file is uncounted, never painted.
  const bad=new Map();
  let payload={};
  try{payload=JSON.parse(rec.map)}catch(e){}
  const contents=new Map();
  if(Array.isArray(payload.sources)&&Array.isArray(payload.sourcesContent)){
    payload.sources.forEach((s,i)=>{const r=resolve(s);if(r!==null&&typeof payload.sourcesContent[i]==='string')contents.set(r,payload.sourcesContent[i])});
  }
  function described(src,text){
    if(!contents.has(src))return true;
    let c=contents.get(src);
    if(c.charCodeAt(0)===0xFEFF)c=c.slice(1);
    if(c===text)return true;
    bad.set(src,'the file on disk is not the text its source map was made from (changed since it ran, or the map names another file)');
    return false;
  }
  function resolve(s){
    if(typeof s!=='string'||s==='')return null;
    if(s.startsWith('file://'))return toPath(s);
    if(path.isAbsolute(s))return s;
    return path.resolve(path.dirname(p),sourceRoot,s);
  }
  function orig(off){
    if(gen.length===0||off<0||off>=rec.length)return null;
    const line=lineAt(gen,off);
    let col=off-gen[line];
    if(line===0)col+=rec.columnOffset||0;
    const gl=line+(rec.lineOffset||0);
    if(col<0||gl<0)return null;
    const e=sm.findEntry(gl,col);
    if(!e||e.generatedLine!==gl||typeof e.originalLine!=='number'||typeof e.originalColumn!=='number')return null;
    const src=resolve(e.originalSource);
    if(src===null)return null;
    const text=source(src);
    if(text===null)return null;
    if(bad.has(src)||!described(src,text))return null;
    if(!origStarts.has(src))origStarts.set(src,lineSpans(text));
    const {starts:ls,ends}=origStarts.get(src);
    const past=()=>{bad.set(src,'its source map points past the file on disk (changed since it ran, or the map names another file)');return null};
    if(e.originalLine<0||e.originalLine>=ls.length)return past();
    const lineEnd=ends[e.originalLine];
    if(ls[e.originalLine]+e.originalColumn>lineEnd)return past();
    // Within one mapping segment, generated and original text advance
    // together (a segment is one token or a run copied as is), so the
    // position is the segment's original start plus how far into the
    // segment it lies, kept on the segment's original line.
    const at=Math.min(ls[e.originalLine]+e.originalColumn+(col-e.generatedColumn),lineEnd);
    return {src,at};
  }
  const bySrc=new Map();
  for(const fn of fns){
    const per=new Map();
    let whole=true;
    for(const r of (fn.ranges||[])){
      const a=r.endOffset>r.startOffset?orig(r.startOffset):null,b=a?orig(r.endOffset-1):null;
      const ok=a&&b&&a.src===b.src&&b.at>=a.at;
      // A function with ANY range that maps nowhere is left out whole. When
      // it is its own range, it is code the runner added (its module
      // wrapper, export getters), and a block of it left alone could read
      // as a cold line; when it is an inner block, keeping the outer range
      // would paint that block's lines as executed.
      if(!ok){whole=false;break}
      if(!per.has(a.src))per.set(a.src,[]);
      per.get(a.src).push({startOffset:a.at,endOffset:b.at+1,count:r.count});
    }
    if(!whole)continue;
    for(const [src,ranges] of per){
      if(!bySrc.has(src))bySrc.set(src,[]);
      bySrc.get(src).push({functionName:fn.functionName,ranges});
    }
  }
  for(const [src,why] of bad){
    const key=keyOf(src);
    if(key!==null)uncounted.set(key,why);
  }
  for(const [src,mapped] of bySrc){
    const key=keyOf(src);
    if(key!==null&&!bad.has(src))count(key,source(src),mapped);
  }
  if(bad.has(p))return '';
  return bySrc.has(p)?'':'its recorded source map maps none of its executed ranges onto it';
}
for(const f of files){
  if(!f.endsWith('.json'))continue;
  let j;
  try{j=JSON.parse(fs.readFileSync(path.join(dir,f),'utf8'))}catch(e){continue}
  for(const s of (j.result||[])){
    if(typeof s.url!=='string'||!s.url.startsWith('file://'))continue;
    let p;
    try{p=url.fileURLToPath(s.url)}catch(e){continue}
    const key=keyOf(p);
    if(key===null)continue;
    const fns=s.functions||[];
    let script=-1;
    for(const fn of fns){const r=(fn.ranges||[])[0];if(r&&!fn.functionName&&r.startOffset===0&&r.endOffset>script)script=r.endOffset}
    // TRANSFORMED CODE WITH A RECORDED MAP: a runner that evaluates through
    // node:vm (vitest, jest) ran this text, whose inline source map corral's
    // preload recorded, matched by path and by the script's exact length.
    // LOADED: the script-level range ran, so the module's own top level
    // executed — whether or not any function of it did.
    const top=fns.some(fn=>!fn.functionName&&(fn.ranges||[]).some(r=>r.startOffset===0&&r.endOffset===script&&r.count>0));
    const rec=(maps.get(p)||[]).find(r=>r.length===script);
    if(rec){
      const why=countMapped(p,rec,fns);
      if(why)uncounted.set(key,why);
      if(top)loaded.add(key);
      continue;
    }
    const src=source(p);
    if(src===null){uncounted.set(key,'the source could not be read');continue}
    const n=src.length;
    // THE SCRIPT RANGE MUST SPAN THE FILE ON DISK, exactly: the length of
    // the source, or that plus the sourceURL comment Node's TypeScript
    // stripping appends. Anything else means V8 ran different text under
    // this URL (a runner that transforms first, with no map corral
    // recorded), and its offsets mean nothing in this file even when they
    // happen to land inside it.
    const stripped=/\.[cm]?ts$/.test(p)?n+('\n\n//# sourceURL='+s.url).length:-1;
    if(script!==n&&script!==stripped){
      uncounted.set(key,script<0
        ?'V8 reported no script-level range for it'
        :'V8 ran '+script+' characters under its URL and the file on disk has '+n+' (transformed code, or a file changed since it ran)');
      continue;
    }
    const all=[];
    for(const fn of fns)for(const r of (fn.ranges||[]))all.push(r);
    if(all.some(r=>!(r.startOffset>=0)||r.startOffset>script||!(r.endOffset>=r.startOffset))){
      uncounted.set(key,'its coverage ranges do not fit the source on disk (transformed code, or a file changed since it ran)');
      continue;
    }
    count(key,src,fns);
    if(top)loaded.add(key);
  }
}
const sum=a=>a.reduce((t,v)=>t+v,0);
const out=['` + jsCoverageHeaderV2 + `'];
const keys=[...new Set([...lines.keys(),...uncounted.keys()])].sort();
for(const k of keys){
  const st=lines.get(k);
  // NO EXECUTABLE LINE, NO ENTRY: a file V8 measured with nothing in it to
  // run (whitespace only) is not measurable, and "0 0" would read as
  // measured-and-never-executed, accusing it of a gap it cannot have.
  // A file ANY process could not count is uncounted, whatever another
  // counted: the process that could not count it may be the one it ran in,
  // and a count from the others would read as a measured zero.
  if(uncounted.has(k))out.push('# uncounted '+k+': '+uncounted.get(k));
  else{const exe=sum(st.exe);if(exe>0){out.push(sum(st.ran)+' '+exe+' '+k);if(loaded.has(k))out.push('# loaded '+k)}}
}
process.stdout.write(out.join('\n')+'\n');
`

// jsCoveragePreload is the file CoverageCmd loads into every Node process of
// the run (NODE_OPTIONS --require): it records the inline source map of code
// a runner evaluates through node:vm, so the reducer can count TRANSFORMED
// code against the original file. vitest runs every module of the project
// through vm.runInThisContext (vitest 5.0.3, dist/module-evaluator.js), and
// Node keeps no source map for code compiled that way, even under
// --enable-source-maps (measured on node 22.23.4: the coverage document's
// source-map-cache held only files Node loaded itself), so without this
// every vitest-run file is uncounted.
//
// The same pattern as Ruby's RUBYOPT preload and PHP's auto_prepend_file,
// and it adds nothing to the audited repository. It must never change what
// the suite does: vm.runInThisContext, vm.compileFunction and the vm.Script
// constructor are wrapped only to READ their arguments. The original is then
// called with the original arguments and `this` (new.target for Script), and
// its result or exception is returned untouched. Every function the
// recording calls is captured at load, so a suite's later spy on fs (or
// JSON, Buffer, a String method) never sees corral's write. Options are read
// through their property descriptors, so a getter runs exactly as often as
// vm runs it; a getter-defined option leaves that evaluation unrecorded. The
// wrappers keep the originals' name and length, String() of each reads as
// the original's source, and instances of vm.Script keep their prototype,
// with prototype.constructor === vm.Script. The behaviour-preservation test
// (testdata/coverage/vmprobe) runs every one of these observations with and
// without the preload. Recording failures are swallowed, and it prints
// nothing.
//
// What a suite CAN still observe: one more stack frame inside these calls;
// Function.prototype.toString.call() of the three (it reads the wrapper or
// the Proxy); a Proxy passed as options sees a getOwnPropertyDescriptor
// trap; an fs mock at the binding level (below the fs module) sees the
// write; each mapped evaluation costs one synchronous append; and
// module.syncBuiltinESMExports() also syncs into ESM any CJS builtin export
// an earlier --require patched without syncing. An operator --permission
// policy that cannot read $d makes every Node process fail to load it, which
// fails each unit visibly.
//
// Each process appends one JSON line per evaluated code string that carries
// a base64 inline map — its filename, length, line/column offsets, line
// lengths and the decoded map — to its own file under $d/maps.
const jsCoveragePreload = `'use strict';
(function(){
const fs=require('fs'),path=require('path'),vm=require('vm'),mod=require('module');
// Every function the recording path calls is taken NOW, at load, so a suite
// that later spies on or stubs fs, JSON, Buffer or a String method never
// sees corral's call.
const call=Function.prototype.call.bind(Function.prototype.call);
const appendFileSync=fs.appendFileSync,mkdirSync=fs.mkdirSync,join=path.join;
const stringify=JSON.stringify,bufferFrom=Buffer.from,isInteger=Number.isInteger;
const apply=Reflect.apply,construct=Reflect.construct,reflectGet=Reflect.get,getOwn=Reflect.getOwnPropertyDescriptor,define=Reflect.defineProperty;
const fnToString=Function.prototype.toString;
const lastIndexOf=String.prototype.lastIndexOf,slice=String.prototype.slice,charCodeAt=String.prototype.charCodeAt,exec=RegExp.prototype.exec,toStr=Buffer.prototype.toString;
const dir=join(__dirname,'maps');
const marker='//# sourceMappingURL=data:application/json';
const base64=/^[^,\s]*;base64,([A-Za-z0-9+/=]+)/;
let file=null;
function target(){
  if(file===null){
    let tid=0;
    try{tid=require('worker_threads').threadId}catch(e){}
    call(mkdirSync,fs,dir,{recursive:true});
    file=join(dir,process.pid+'-'+tid+'.jsonl');
  }
  return file;
}
// An option is read through its property descriptor, never through a
// getter: a getter would run once more than vm runs it. A getter-defined
// option (or one inherited) leaves that evaluation unrecorded.
function own(options,key){
  if(options===null||typeof options!=='object')return undefined;
  const d=getOwn(options,key);
  return d&&!d.get&&!d.set?d.value:undefined;
}
// lineStarts is where each line of code starts, by ECMAScript's line
// terminators (\n, \r, \r\n, U+2028, U+2029): the lines a source map's
// generated positions count.
function lineStarts(code){
  const out=[0];
  for(let i=0;i<code.length;i++){
    const c=call(charCodeAt,code,i);
    if(c===13&&call(charCodeAt,code,i+1)===10)continue;
    if(c===10||c===13||c===0x2028||c===0x2029)out[out.length]=i+1;
  }
  return out;
}
function record(kind,code,options){
  try{
    if(typeof code!=='string')return;
    const at=call(lastIndexOf,code,marker);
    if(at<0)return;
    const m=call(exec,base64,call(slice,code,at+marker.length));
    if(!m)return;
    const filename=typeof options==='string'?options:own(options,'filename');
    if(typeof filename!=='string'||filename==='')return;
    const lo=own(options,'lineOffset'),co=own(options,'columnOffset');
    // vm's own reading of an absent offset is 0.
    appendRecord({kind,filename,length:code.length,lineOffset:isInteger(lo)?lo:0,columnOffset:isInteger(co)?co:0,lineStarts:lineStarts(code),map:call(toStr,bufferFrom(m[1],'base64'),'utf8')});
  }catch(e){}
}
function appendRecord(r){call(appendFileSync,fs,target(),stringify(r)+'\n')}
// A wrapper carries the original's name and length, and String() of it
// reads as the original's source.
function same(w,orig){
  for(const k of ['name','length'])define(w,k,getOwn(orig,k));
  define(w,'toString',{value:fnToString.bind(orig),writable:true,configurable:true});
  return w;
}
const runInThisContext=vm.runInThisContext;
vm.runInThisContext=same(function(code,options){
  record('script',code,options);
  return apply(runInThisContext,this,arguments);
},runInThisContext);
const compileFunction=vm.compileFunction;
vm.compileFunction=same(function(code,params,options){
  record('function',code,options);
  return apply(compileFunction,this,arguments);
},compileFunction);
// vm.Script stays the same class to its instances: the Proxy constructs the
// original with new.target untouched, and the prototype's constructor names
// the Proxy, which is what vm.Script now is.
const Script=vm.Script;
const toSource=fnToString.bind(Script);
const ScriptProxy=new Proxy(Script,{
  construct(t,args,nt){
    record('script',args[0],args[1]);
    return construct(t,args,nt===ScriptProxy?Script:nt);
  },
  get(t,k,recv){return k==='toString'?toSource:reflectGet(t,k,recv)}
});
define(Script.prototype,'constructor',{value:ScriptProxy,writable:true,configurable:true,enumerable:false});
vm.Script=ScriptProxy;
mod.syncBuiltinESMExports();
})();
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
	setup := `cat > "$d/corral_reduce.js" <<'CORRAL_JS_EOF'` + "\n" + jsCoverageReduce + "CORRAL_JS_EOF\n" +
		`cat > "$d/corral_maps.cjs" <<'CORRAL_JS_EOF'` + "\n" + jsCoveragePreload + "CORRAL_JS_EOF\n"
	// The preload is APPENDED to the operator's NODE_OPTIONS, never in
	// place of it: a suite that needs its own flags keeps them.
	env := `NODE_V8_COVERAGE="$d/cov" NODE_OPTIONS="${NODE_OPTIONS:+$NODE_OPTIONS }--require \"$d/corral_maps.cjs\""`
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
