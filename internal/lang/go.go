// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pdbethke/corralai/internal/pairing"
)

func init() { Register(goPlugin{}) }

type goPlugin struct{}

func (goPlugin) Name() string                { return "go" }
func (goPlugin) Detect(codePath string) bool { return filepath.Ext(codePath) == ".go" }

func (goPlugin) Scaffold() map[string]string {
	return map[string]string{"go.mod": "module control\ngo 1.26\n"}
}

func (goPlugin) TestCmd() []string { return []string{"go", "test", "./..."} }

// CompileCheck type-checks the authored test. It scopes to the audited file's
// OWN package (./dir/...) rather than the whole module (./...): a single-file
// audit inside a monorepo must not drag in unrelated packages — above all cgo
// deps elsewhere in the tree (e.g. tree-sitter bindings) whose C headers
// `go mod vendor` prunes, which would fail the compile-check for a reason that
// has nothing to do with the authored test. This matches the (package-scoped)
// command the scorer actually runs. A bare filename (single-file mode) has no
// package dir, so it falls back to ./... over the one-package scaffold.
// `go vet` already type-checks every .go file (source and test) in the
// target package in one invocation, so this is a single-command sequence —
// see lang.Plugin.CompileCheck's doc comment for why the return type is a
// sequence at all.
func (goPlugin) CompileCheck(codePath, _ string) [][]string {
	// `go build`, not `go vet`. vet runs analyzers `go test` never does —
	// assign (`a = a`), unreachable, unusedresult, lostcancel, appends,
	// shift — and those are exactly the statement-deletion, early-return and
	// self-assignment shapes a mutant generator emits. A mutant vet rejects
	// but the toolchain BUILDS is a runnable mutant; calling it invalid
	// takes it out of the denominator and inflates the rate. Measured: two
	// mutants, one killed, one a genuine survivor vet rejected — KillRate
	// 1.00 with the gate, 0.50 without. The question this gate asks is "does
	// it build", so that is the command it runs; the test binary is built
	// too, because a mutant can break only the test's view of the package.
	dir := filepath.ToSlash(filepath.Dir(codePath))
	pkg := "./..."
	if !(dir == "." || dir == "" || dir == "/") {
		pkg = "./" + dir + "/..."
	}
	return [][]string{{"go", "test", "-count=1", "-run", "^$", pkg}}
}

// TestRules mirrors the prior advPoolTestPath: same base name, `_test.go`
// suffix, same directory. Go's convention IS the sibling file — there is no
// parallel-tree or flat convention to add, so this is a single rule, and its
// search roots are just pairing.DefaultRoot.
func (goPlugin) TestRules() []pairing.Rule {
	return []pairing.Rule{{Shape: pairing.Sibling, Name: "{base}_test.go"}}
}

// Preflight checks the operator's own test command's binary (e.g. a
// `gotestsum` wrapper named after `--`) when one is given, else the stock
// "go" — see preflightBin and Plugin.Preflight's doc comment.
func (goPlugin) Preflight(testCmd []string) error {
	return toolOnPath(preflightBin(testCmd, "go"))
}

func (goPlugin) PromptLang() string { return "Go" }

// TestWriterSystem is the EXACT string previously named writeTestSystem in
// internal/testgen/testgen.go — moved here unchanged so the Go prompt stays
// byte-identical.
func (goPlugin) TestWriterSystem() string {
	return `You are a TEST-WRITER. Given a security control GOAL, a target source file, and its signature surface, write ONE executable Go test that verifies the code SATISFIES the goal.
- Same package as the target (white-box).
- It MUST compile against the target and MUST FAIL if the goal is violated — test the goal's boundary (what a weakened implementation would pass that a compliant one must not).
- Standard library "testing" only. Deterministic, no network.
Return ONLY the raw Go test file content — no prose, no markdown fences.`
}

// Preamble returns the package clause and the import block immediately
// following it (a bare `import "x"` or a grouped `import (...)`), verbatim —
// the header a mutant-generator shard's sliced view needs so mutated code
// that references an import still reads as a real Go file, without pulling
// in the file's actual declarations. Line-based, not AST-based: this package
// already has a full tree-sitter parse available at the signature-extraction
// call site, but a shard's preamble is cheap enough not to need one, and a
// plain scan is trivially correct for the shape every real Go file's header
// takes (comments, one package clause, then imports).
func (goPlugin) Preamble(code string) string {
	lines := strings.Split(code, "\n")
	end := 0
	inImportGroup := false
	for i, l := range lines {
		t := strings.TrimSpace(l)
		switch {
		case inImportGroup:
			end = i + 1
			if t == ")" {
				inImportGroup = false
			}
		case t == "" || strings.HasPrefix(t, "//"):
			end = i + 1
		case strings.HasPrefix(t, "package "):
			end = i + 1
		case strings.HasPrefix(t, "import ("):
			inImportGroup = true
			end = i + 1
		case strings.HasPrefix(t, "import "):
			end = i + 1
		default:
			return strings.Join(lines[:end], "\n")
		}
	}
	return strings.Join(lines[:end], "\n")
}

// MutantSystem is the shared mutant-generator prompt (mutantSystem) with Go's
// two differences filled in; it was once genMutantsSystem in internal/testgen.
func (goPlugin) MutantSystem() string {
	return mutantSystem("signature and package (a drop-in replacement that compiles)", "compile errors")
}

// ImportPath is always ok=false: Go's convention is white-box SAME PACKAGE
// (see TestWriterSystem), never an import statement at all — there is
// nothing for this method to derive. See lang.Plugin.ImportPath's doc
// comment for the general rule this follows.
func (goPlugin) ImportPath(string, func(string) bool) (string, bool) { return "", false }

// ImportNote is always "": see ImportPath — Go's white-box convention needs
// no per-task import correction.
func (goPlugin) ImportNote(string, bool) string { return "" }

func goSingleTestCmd(testPath, selector string) ([]string, bool) {
	if selector == "" {
		return nil, false
	}
	return []string{"go", "test", "-run", "^" + selector + "$", "./..."}, true
}

func goListTestsCmd(testPath string) ([]string, bool) {
	return []string{"go", "test", "-list", ".*", "./..."}, true
}

// goCoverageReduceScript is the second half of the pipeline that
// goPlugin.CoverageCmd builds. The first half wraps a `go test` command (see
// goTestShaped for exactly which shapes) so a coverage profile ends up on
// stdout after ONE run. `-coverprofile=/dev/stdout` was the obvious first
// attempt but is NOT safe: verified against corral's own suite, it
// interleaves and corrupts the profile whenever more than one package's
// test binary flushes to the shared fd (`go test ./... -p 1
// -coverprofile=/dev/stdout` still corrupted — the race is between the `go
// test` parent's own summary line and a child binary's profile flush, not
// package parallelism). Writing to a real temporary file and `cat`-ing it
// afterward, inside one `sh -c` invocation (so callers still see a single
// command and a single stdout stream), was verified clean. See
// .superpowers/sdd/2026-07-29-coverage-preflight/task-1-report.md for the
// transcript.
//
// -coverpkg=./... is REQUIRED, not cosmetic. Go's default (no -coverpkg)
// instruments each test binary for ONLY the package it directly tests — a
// package with no _test.go files of its own gets a synthetic all-zero
// coverage line, even when its exported functions are called constantly by
// OTHER packages' tests through a shared interface or package-level var.
// Verified on gin (task 5 foreign-repo sweep): plain `go test -coverprofile
// ./...` reported codec/json/json.go as "measured, never executed", but
// errors_test.go (package gin, root) calls json.API.Marshal — which
// dispatches to json.go's own jsonApi.Marshal at runtime under the default
// build tags — every run. That is a false accusation of the exact kind
// this feature exists to refuse: with -coverpkg=./... added, the same file
// shows real (non-zero) execution counts, sourced from the root package's
// test binary.
//
// The cost of -coverpkg=./... is real, and by itself unaffordable: every
// test binary now instruments every package the pattern resolves to that it
// actually imports, so the RAW profile carries one block set per (test
// binary × imported covered package) — roughly quadratic in package count,
// not linear in repo size. Measured on the task-5 review round: gin (7
// packages) 90 KB -> 558 KB; prometheus/client_golang 236 KB -> 3.5 MB;
// corralai's own tree (~90 packages) 848 KB -> 53 MB; grpc-go (~180
// packages) 1.7 MB -> 253 MB. A raw profile that size blew straight through
// the pre-flight's own output cap on any substrate that didn't enforce one
// (see adequacy.WithWorkspaceMaxOutput) and would have made the pre-flight
// simply never complete on corral's own repo or anything grpc-go-sized —
// converting "works, with a false positive" into "never works" for a whole
// size class, which is worse.
//
// goCoverageReduceScript closes that from the source side, inside the same
// `sh -c` invocation, before the profile ever reaches stdout: it collapses
// the raw profile to exactly ONE line per file, discarding the per-block,
// per-test-binary duplication entirely — a file needs only ONE bit to
// answer "did any block, in any test binary, ever execute" (present-true),
// vs "every block, in every test binary that measured it, stayed at zero"
// (present-false, the real finding) — see ParseCoverage's tri-state
// contract, which this reduction preserves exactly, just pre-aggregated.
// corral's own 53 MB raw profile reduces to ~15 KB (measured). This is the ONLY
// piece of the coverage pipeline that moves into the shell: the reduction
// is one comparison (a block's count > 0) applied once per file, not a
// relocation of ParseCoverage's fail-closed guards, which stay in tested Go
// exactly as before — the "mode:" header requirement, the malformed-line
// error, and the empty-profile-is-not-an-error case are all still enforced
// by ParseCoverage itself, on whatever this script hands it (see the
// script's own pass-through-unchanged branch for anything that doesn't
// match the expected block shape, so a genuinely malformed line still
// reaches ParseCoverage byte-for-byte and still trips its validation).
const goCoverageReduceScript = `awk '` +
	// Line 1 is always "mode: <mode>" for a well-formed profile — pass it
	// through untouched, so the parser's own header check still runs
	// against the real thing, not a reduction artifact.
	`NR==1{print;next} ` +
	// A real block line is exactly 3 whitespace-separated fields:
	// "<path>:<range> <numStmt> <count>". Anything else (garbled output, a
	// truncated line) is passed through UNCHANGED rather than reduced —
	// the parser sees the exact original bytes and still errors on it,
	// the same as if this script did not exist.
	`NF!=3{print;next} ` +
	`{c=index($1,":"); if(c<=1||$3!~/^[0-9]+$/||$2!~/^[0-9]+$/){print;next} ` +
	// A block is identified by file+position: -coverpkg=./... repeats the
	// SAME block once per test binary that imports it, each with its own
	// hit count. Its statements count ONCE, and it ran if ANY occurrence
	// did, so keep the largest hit per block.
	`p=substr($1,1,c-1); if(!(p in seen)){seen[p]=1; ord[++n]=p} if(!($1 in stm)){stm[$1]=$2+0; fil[$1]=p} ` +
	`if($3+0>hit[$1]+0){hit[$1]=$3+0}} ` +
	// One synthetic line per distinct file, "<path>:0,<any> <executable>
	// <executed>": executable is the sum of numStmt over the file's
	// distinct blocks, executed the sum over those that ran. The range
	// "0,<any>" cannot occur in a real profile (lines start at 1) so the
	// parser tells a reduced line from a raw one; <any> is 1 when ANY block
	// ran, which stays true for a block with zero statements (an empty
	// function body) that executed without adding to the executed sum — the
	// file's executed/not-executed bit must not depend on the sums. A file
	// measured and never executed still gets a line (executed 0, any 0);
	// only a file with ZERO lines in the whole raw profile is absent.
	`END{for(k in fil){p=fil[k]; e[p]+=stm[k]; if(hit[k]+0>0){x[p]+=stm[k]; a[p]=1}} ` +
	`for(i=1;i<=n;i++){p=ord[i]; print p":0,"(p in a?1:0)" "e[p]" "x[p]+0}}' "$f"`

// CoverageCmd appends -coverpkg/-coverprofile to testCmd, so it claims only a
// command those flags can reach: see goTestShaped. Any other command is
// declined (ok=false) rather than wrapped, because a wrapped command that
// never forwards the flags exits non-zero with no profile.
func (goPlugin) CoverageCmd(testCmd []string) (cmd []string, ok bool) {
	if !goTestShaped(testCmd) {
		return nil, false
	}
	quoted := make([]string, len(testCmd))
	for i, arg := range testCmd {
		quoted[i] = shellQuote(arg)
	}
	// `;` + an explicit rc check between the test run and the reduction, NOT
	// `&&`. This is the same treatment pyPlugin.CoverageCmd has carried since
	// it was written, and Go was the asymmetry: with `&&`, `go test`'s exit 1
	// on an ORDINARY failing test — the single most likely state of a suite
	// corral is auditing, and the whole reason the pre-flight is interesting
	// — short-circuited the reduction, so no profile ever reached stdout and
	// the pre-flight reported `coverage report unparseable: no "mode:"
	// header`. That blamed the report for a red suite, and silently no-opped
	// on exactly the weak-suite repos this feature targets. `go test
	// -coverprofile` writes a complete, usable profile whether the tests pass
	// or fail, so discarding it on failure threw away the answer.
	//
	// Only 0 and 1 fall through: those are "the suite ran" (all passed / some
	// failed). Anything else — a bad flag (2), a signal — re-raises that exit
	// code and skips the reduction, leaving stdout non-profile, which
	// ParseCoverage already turns into an error rather than an empty map. The
	// fail-closed direction is preserved; only the red-suite case changes.
	script := `f=$(mktemp) && trap 'rm -f "$f"' EXIT && ` +
		strings.Join(quoted, " ") + ` -coverpkg=./... -coverprofile="$f"` +
		`; rc=$?; case $rc in 0|1) ;; *) exit "$rc" ;; esac; ` +
		goCoverageReduceScript
	return []string{"sh", "-c", script}, true
}

// shellQuote wraps s in single quotes for safe use inside a POSIX sh -c
// script, escaping any embedded single quote.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// ParseCoverage reads the output of the command CoverageCmd builds: whatever
// preamble the wrapped test command itself prints (e.g. `go test`'s own
// "ok  pkg  0.01s  coverage: NN%" summary line, which — per the verified
// invocation above — lands on stdout BEFORE the profile), then the profile
// itself: a "mode: <mode>" header line, followed by one line per covered
// block —
//
//	<import-path>/<file>:<startLine>.<col>,<endLine>.<col> <numStmt> <count>
//
// Lines before the mode header are preamble and are ignored outright — they
// are the wrapped command's own output, not coverage data, so there is
// nothing to validate about their shape. A file is "executed" if ANY of its
// blocks has count > 0. modulePath, when non-empty, is stripped as a
// "<modulePath>/" prefix to yield a repo-relative path.
//
// Once the mode header has been seen, every subsequent non-blank line MUST
// match the block shape above, or this returns an error — never silently
// drops it, because a dropped block line would just as silently shrink the
// executed set, exactly the falsely-empty-coverage outcome this type exists
// to prevent. For the same reason, input with no recognizable "mode:" header
// anywhere is itself an error rather than an empty map: a genuinely-empty
// instrumented run still emits that header, so its absence means the
// command's output was not a coverage profile at all (e.g. it failed before
// ever running `go test`), which is a fact callers must see, not paper over.
//
// The returned map is TRI-STATE, not a plain "was it seen" set: a file gets
// an entry (true or false) for every file the profile MEASURED at all —
// true if any of its blocks has count > 0, false if every block it has is
// count == 0. A file this profile never mentions at all — outside the
// package(s) `go test` actually built — is left ABSENT from the map
// entirely. Conflating "measured and found unexecuted" with "never
// measured" would turn every file outside the instrumented package set into
// an accusation ("your suite never touches this") about a file the run
// never looked at; a caller must be able to tell the two apart, and only
// "present, false" is a real finding. Once a file is recorded true (any
// block executed), a later count-0 block for the SAME file must not
// overwrite it back to false.
func (goPlugin) ParseCoverage(stdout, modulePath string) (executed map[string]bool, err error) {
	tally, err := parseGoProfile(stdout, modulePath)
	if err != nil {
		return nil, err
	}
	executed = make(map[string]bool, len(tally))
	for p, t := range tally {
		executed[p] = t.hit
	}
	return executed, nil
}

// goFileTally is what one profile says about one file: the statements its
// distinct blocks hold, the statements of those that ran, and whether any
// block ran at all (true for an executed block with zero statements too).
type goFileTally struct {
	executed, executable int
	hit                  bool
}

// goBlock is a block's statement count and its largest hit across the
// profile's repeats of it.
type goBlock struct {
	stmts, hit int
}

// parseGoProfile is the one reader of the profile (raw or reduced) behind
// both ParseCoverage and CoverageCounts, so the two cannot disagree on what
// a line means or which line is malformed. Every non-blank line after the
// mode header must be a block line or the whole report is an error, never a
// silently shrunken one. Raw blocks are deduplicated by file+position taking
// the largest hit, because -coverpkg repeats a block per test binary; a
// reduced line (range "0,<any>", see goCoverageReduceScript) is already
// aggregated and added as it stands.
func parseGoProfile(stdout, modulePath string) (map[string]*goFileTally, error) {
	tally := make(map[string]*goFileTally)
	blocks := make(map[string]*goBlock)
	owner := make(map[string]string)
	sawMode := false
	for i, line := range strings.Split(stdout, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if !sawMode {
			if strings.HasPrefix(trimmed, "mode:") {
				sawMode = true
			}
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) != 3 {
			return nil, fmt.Errorf("lang: unparseable coverage line %d: %q", i+1, line)
		}
		count, convErr := strconv.Atoi(fields[2])
		if convErr != nil {
			return nil, fmt.Errorf("lang: unparseable coverage count on line %d: %q", i+1, line)
		}
		stmts, convErr := strconv.Atoi(fields[1])
		if convErr != nil || stmts < 0 || count < 0 {
			return nil, fmt.Errorf("lang: unparseable coverage statement count on line %d: %q", i+1, line)
		}
		colon := strings.Index(fields[0], ":")
		if colon <= 0 {
			return nil, fmt.Errorf("lang: unparseable coverage block on line %d: %q", i+1, line)
		}
		path := fields[0][:colon]
		if modulePath != "" {
			path = strings.TrimPrefix(path, modulePath+"/")
		}
		t := tally[path]
		if t == nil {
			t = &goFileTally{}
			tally[path] = t
		}
		if rng := fields[0][colon+1:]; strings.HasPrefix(rng, "0,") {
			t.executable += stmts
			t.executed += count
			t.hit = t.hit || rng == "0,1" || count > 0
			continue
		}
		b := blocks[fields[0]]
		if b == nil {
			b = &goBlock{stmts: stmts}
			blocks[fields[0]] = b
			owner[fields[0]] = path
		}
		b.hit = max(b.hit, count)
	}
	if !sawMode {
		return nil, fmt.Errorf("lang: coverage report has no \"mode:\" header (got %d bytes)", len(stdout))
	}
	for key, b := range blocks {
		t := tally[owner[key]]
		t.executable += b.stmts
		if b.hit > 0 {
			t.executed += b.stmts
			t.hit = true
		}
	}
	return tally, nil
}

// WorkspaceRunEnv is a no-op: the Go build/test cache (GOCACHE) is
// content-addressed (a hash of the source, not its mtime+size), so a mutant
// that changes a byte the compiler sees necessarily changes its cache key —
// there is no analog of python.go's __pycache__ staleness hole here. See
// lang.Plugin.WorkspaceRunEnv's doc comment.
func (goPlugin) WorkspaceRunEnv() (env []string, cleanup func()) { return nil, func() {} }

// TreeEnv divides the box between the pool's trees instead of letting each
// one assume all of it. `go test` already builds and runs packages in
// parallel (GOMAXPROCS for the test binary, -p for the build), so N trees
// each taking every core is N-times oversubscribed: the machine thrashes,
// every suite gets slower, and the concurrency probe — whose whole job is to
// decide whether the suite PASSES under N — can fail on contention alone and
// downgrade a perfectly safe suite to one tree.
//
// cores is this tree's share, already divided by the pool; a degenerate share
// floors at 1 (GOMAXPROCS=0 is rejected outright by the runtime, and -p=0
// means "unlimited" — exactly the oversubscription this exists to prevent).
//
// GOFLAGS APPENDS to whatever the operator already set rather than replacing
// it: GOFLAGS is a single space-separated variable, so assigning it outright
// silently drops a `-mod=vendor` or a `-tags=integration` the project's suite
// needs — the mutant would then be graded by a build the operator never runs,
// or by no build at all. -p is placed LAST so it wins if their GOFLAGS
// happens to set one too (later flags override earlier ones), which is the
// one value this tree is not free to give up.
//
// -trimpath is also appended: the Go build cache is keyed in part on the
// absolute path baked into each object, so the SAME module built from two
// different tree directories misses the cache on the second copy — measured
// on this box at 5.05s cold vs 2.42s warm. -trimpath drops that path from the
// cache key, so every tree after the first hits the shared cache (2.42s),
// at the cost of one full rebuild per machine the first time (22s).
func (goPlugin) TreeEnv(tree string, cores int) []string {
	if cores < 1 {
		cores = 1
	}
	n := strconv.Itoa(cores)
	flags := "-trimpath -p=" + n
	if existing := strings.TrimSpace(os.Getenv("GOFLAGS")); existing != "" {
		flags = existing + " " + flags
	}
	return []string{"GOMAXPROCS=" + n, "GOFLAGS=" + flags}
}

// TestNamesInSource parses the file with go/parser and keeps exactly what
// `go test` would run: top-level funcs named Test plus a name that does not
// begin with a lower-case letter, taking one *testing.T. TestMain takes a
// *testing.M and is excluded by that rule. Unparseable source lists nothing.
func (goPlugin) TestNamesInSource(testPath, src string) []string {
	f, err := parser.ParseFile(token.NewFileSet(), testPath, src, 0)
	if err != nil {
		return nil
	}
	// The name the file imports "testing" under: usually "testing", an alias
	// if it renames it, "." for a dot import (then the type is a bare T).
	tname := ""
	for _, im := range f.Imports {
		if im.Path.Value == `"testing"` {
			tname = "testing"
			if im.Name != nil {
				tname = im.Name.Name
			}
		}
	}
	if tname == "" || tname == "_" {
		return nil
	}
	var out []string
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || !isGoTestName(fn.Name.Name) || fn.Type.Params == nil || len(fn.Type.Params.List) != 1 {
			continue
		}
		p := fn.Type.Params.List[0]
		star, ok := p.Type.(*ast.StarExpr)
		if !ok || len(p.Names) > 1 {
			continue
		}
		switch x := star.X.(type) {
		case *ast.SelectorExpr:
			if pkg, ok := x.X.(*ast.Ident); ok && x.Sel.Name == "T" && pkg.Name == tname {
				out = append(out, fn.Name.Name)
			}
		case *ast.Ident:
			if tname == "." && x.Name == "T" {
				out = append(out, fn.Name.Name)
			}
		}
	}
	return out
}

// isGoTestName is the go tool's rule: "Test", then either nothing or a rune
// that is not a lower-case letter.
func isGoTestName(name string) bool {
	if !strings.HasPrefix(name, "Test") {
		return false
	}
	rest := name[len("Test"):]
	if rest == "" {
		return true
	}
	r, _ := utf8.DecodeRuneInString(rest)
	return !unicode.IsLower(r)
}

func goParseTestList(output string) []string {
	var out []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		// go test -list prints one identifier per line; keep only Test* funcs
		// (drop Example*/Benchmark*/Fuzz* and the "ok  pkg  0.00s" / PASS trailer,
		// which contain whitespace or don't start with "Test").
		if strings.HasPrefix(line, "Test") && !strings.ContainsAny(line, " \t") {
			out = append(out, line)
		}
	}
	return out
}

// goFirstFailure names the first test `go test` reported as failing. The
// runner prints one `--- FAIL: <name> (<elapsed>)` line per failing test,
// indented one level per subtest depth; the name is taken VERBATIM, so a
// subtest comes back as `TestA/sub` — the form `go test -run` accepts.
//
// The FIRST such line in the stream is the answer, whether that turns out to
// be a parent or one of its subtests: which of the two `go test` flushes
// first is a property of the runner, not a judgement this parser is entitled
// to make.
//
// Output with no `--- FAIL:` line at all — a passing package, or one that
// failed to BUILD — names nothing. No test ran, so no test can be blamed.
func goFirstFailure(output []byte) string {
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "--- FAIL: ")
		if !ok {
			continue
		}
		if name, _, found := strings.Cut(rest, " "); found {
			rest = name
		}
		if rest = strings.TrimSpace(rest); rest != "" {
			return rest
		}
	}
	return ""
}

// isGoBinary reports whether argv0 names the go command: base name go or
// go.exe. A versioned toolchain wrapper from golang.org/dl (`go1.26.9`) is
// NOT recognised, so a command run through one gets no -failfast and no
// coverage instrumentation — unchanged behaviour, never a wrong flag.
func isGoBinary(argv0 string) bool {
	base := filepath.Base(argv0)
	return base == "go" || base == "go.exe"
}

// goTestShaped reports whether testCmd hands trailing flags to `go test`, so
// the -coverpkg/-coverprofile CoverageCmd appends actually reach it: `go ...
// test ...` (argv[0] passes isGoBinary and the first non-flag argument, after
// skipping -C's value, is test), or `gotestsum ... -- ...`, which forwards
// everything after -- to go test. Measured 2026-10-11: `make test`, bare
// `gotestsum` and `gotestsum --format x` exit 2/3 without writing a profile,
// so claiming them made every mixed-language pre-flight decline as
// ambiguous and every pure-Go one fail its run.
//
// A command containing -args (or --args) is refused in either shape: go test
// hands everything after it to the test binary, so the appended flags would
// never reach go test. Measured 2026-10-11 on go1.26.9: `go test ./... -args
// -v -coverprofile=f` exits 1 and writes no profile.
func goTestShaped(testCmd []string) bool {
	if len(testCmd) == 0 {
		return false
	}
	if slices.Contains(testCmd, "-args") || slices.Contains(testCmd, "--args") {
		return false
	}
	if isGoBinary(testCmd[0]) {
		for i := 1; i < len(testCmd); i++ {
			a := testCmd[i]
			if a == "-C" {
				i++
				continue
			}
			if strings.HasPrefix(a, "-") {
				continue
			}
			return a == "test"
		}
		return false
	}
	if filepath.Base(testCmd[0]) == "gotestsum" {
		return slices.Contains(testCmd[1:], "--")
	}
	return false
}

// LineComment is the go line-comment marker.
func (goPlugin) LineComment() string { return "//" }

// Vendor: a Go module not already carrying vendor/ is vendored with
// `go mod vendor`, so the offline jail resolves its external modules from
// the tree instead of the operator's module cache. Done is the vendor/
// directory itself — the same check this made before the step had a
// done-marker — so a tree that carries one is used as it is.
func (goPlugin) Vendor(repoDir string) (VendorStep, bool) {
	if _, err := os.Stat(filepath.Join(repoDir, "go.mod")); err != nil {
		return VendorStep{}, false
	}
	return StepUnlessDone(repoDir, VendorStep{Argv: []string{"go", "mod", "vendor"}, Done: "vendor"})
}

// DependencyDirs is Go's module vendor directory (`go mod vendor`).
//
// "vendor" covers TWO languages under the one basename: it named Go's own
// module vendor dir first, and Composer's PHP dependency tree lands at the
// exact same repo-relative name (see the php plugin).
func (goPlugin) DependencyDirs() []DepDir { return []DepDir{{Name: "vendor"}} }

// CoverageCounts reads the same profile as ParseCoverage into statement
// counts: Executable is the sum of numStmt over a file's distinct blocks and
// Executed the sum over those that ran in any test binary. A statement is
// Go's unit of coverage, so these are statement counts under the line-count
// name the other languages share. ParseCoverage is NOT derived from
// Executed>0 here: a block with zero statements (an empty function body)
// that ran makes its file executed without adding to Executed, and that
// file read executed before counts existed.
func (goPlugin) CoverageCounts(stdout, modulePath string) (map[string]LineCounts, error) {
	tally, err := parseGoProfile(stdout, modulePath)
	if err != nil {
		return nil, err
	}
	counts := make(map[string]LineCounts, len(tally))
	for p, t := range tally {
		counts[p] = LineCounts{Executed: t.executed, Executable: t.executable}
	}
	return counts, nil
}
