// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/sandbox"
)

func init() { Register(pyPlugin{}) }

// preflightProbeTimeout bounds the `<bin> -m pytest --version`/`<bin>
// --version` probe Preflight runs to confirm pytest is importable. Without
// it a blocking wrapper (previously: only pythonBin()'s own two hardcoded
// names could appear here; now testCmd lets the operator name ANY binary —
// a shim, a version-manager wrapper, anything that ends up on PATH under
// the name they gave) hangs the whole audit before any of --timeout's own
// budget even starts. A plain `--version` probe has no legitimate reason to
// take more than a few seconds.
//
// A var, not a const: preflight_test.go temporarily lowers it to keep the
// timeout-path test fast (a real 10s sleep-and-wait per test run would be a
// slow, silently-accepted tax on every `go test` invocation of this
// package) — no production caller ever mutates it.
var preflightProbeTimeout = 10 * time.Second

// pythonBin resolves the interpreter to invoke: python3 (canonical on the
// Linux hosts corral grades on) when present, else bare python. The bwrap
// jail binds the host /usr, so whatever is on the host PATH is what the jail
// sees — resolving here on the host is valid for the jailed test run too.
func pythonBin() string {
	if _, err := exec.LookPath("python3"); err == nil {
		return "python3"
	}
	return "python"
}

type pyPlugin struct{}

func (pyPlugin) Name() string                { return "python" }
func (pyPlugin) Detect(codePath string) bool { return filepath.Ext(codePath) == ".py" }

// Scaffold is empty: pytest discovers test_*.py in the workspace and the
// module under test is importable from the workspace root.
func (pyPlugin) Scaffold() map[string]string { return map[string]string{} }

func (pyPlugin) TestCmd() []string { return []string{pythonBin(), "-m", "pytest", "-q"} }

// pyCachePrefixEnv redirects py_compile's bytecode output to the sandbox's
// writable /tmp tmpfs. py_compile writes a .pyc into a __pycache__ dir NEXT TO
// each source file; in the container jail the workspace is read-only to the
// container's (different-uid) root — cap-drop=ALL removes CAP_DAC_OVERRIDE, and
// the workspace is world-readable-but-not-writable — so that write fails with
// EACCES and a syntactically VALID test is FALSELY rejected as "does not
// compile" (the whole test-writer role was silently defeated this way on the
// container backend). Both jail backends provide a writable /tmp tmpfs, so
// pointing the bytecode cache there makes the syntax check succeed without
// needing to write into the workspace.
//
// It is set on the CHILD PROCESS via the `env` coreutils command, not as a
// bare "VAR=value" argv element run through a shell: CompileCheck's argv is
// executed by TWO different substrates. The jail (internal/adequacy/jail.go)
// shell-joins argv and runs it under `sh -c`, where a leading VAR=value
// token IS a shell env-assignment idiom — but the workspace substrate
// (internal/adequacy/workspace.go, what the GitHub Action uses) execs argv
// directly via exec.Command(cmdArgv[0], cmdArgv[1:]...), with no shell to
// interpret it; there "PYTHONPYCACHEPREFIX=/tmp/corral-pyc" as argv[0] is
// just a nonexistent filename ("fork/exec ...: no such file or directory"),
// so the compile check never runs at all and every Python audit on that
// substrate loses its adversarial half silently. `env NAME=value <cmd>...`
// is a real executable (present on every host this runs on, load-bearing
// for `#!/usr/bin/env python3` shebangs already) that sets the variable on
// its child process and execs it — identical, substrate-agnostic behavior
// under a direct exec AND under `sh -c` (the jail's shellJoin still quotes
// and joins each element; sh then simply runs `env`, same as any other
// program). Harmless on bwrap (same-uid), where the workspace write already
// worked.
const pyCachePrefixEnv = "PYTHONPYCACHEPREFIX=/tmp/corral-pyc"

// CompileCheck is an offline, stdlib syntax check of both files, with bytecode
// output redirected off the (jail-read-only) workspace — see pyCachePrefixEnv.
// py_compile accepts multiple files on one command line, so this is a
// single-command sequence — see lang.Plugin.CompileCheck's doc comment for
// why the return type is a sequence at all.
func (pyPlugin) CompileCheck(codePath, testPath string) [][]string {
	cmds := [][]string{{"env", pyCachePrefixEnv, pythonBin(), "-m", "py_compile", codePath, testPath}}

	// py_compile validates SYNTAX and nothing else. A mutant that calls a
	// function which does not exist compiles clean, reaches GRADING, fails the
	// suite for the wrong reason, and is scored as KILLED — crediting the
	// developer's tests with catching a mutant that was never valid code. That
	// is the same defect family as scoring a compiler-rejected mutant as
	// caught, and Go's gate (go vet) rejects exactly this class.
	//
	// ruff's F821 is go vet's analogue for undefined names; F401 matches "imported
	// and not used"; F811 catches a redefinition. Measured at ~11ms, so it is
	// free relative to a suite run.
	//
	// OPTIONAL, and deliberately so. corral's Python path otherwise needs only
	// an interpreter, and an absent optional tool must never mark every mutant
	// invalid — the caller treats any non-zero command as a rejection, so a
	// missing binary would fail the whole gate closed on nothing. Included only
	// when it resolves on PATH.
	//
	// CAVEAT worth knowing: this resolves on the HOST, while the command runs
	// in the jail. A ruff installed somewhere the jail does not bind (a user
	// site directory, a virtualenv) will be selected here and then not found
	// there. The jail binds the system paths, so a system-installed ruff is the
	// supported shape.
	//
	// E999 is NOT selected: ruff removed it as a selectable rule, which is why
	// py_compile stays as the syntax gate rather than being replaced by this.
	if ruff, err := exec.LookPath("ruff"); err == nil {
		cmds = append(cmds, []string{ruff, "check", "--no-cache", "--select", "F821,F401,F811", codePath, testPath})
	}
	return cmds
}

// WorkspaceRunEnv closes the __pycache__ staleness hole on the WORKSPACE
// substrate (internal/adequacy/workspace.go), which — unlike the jail — runs
// every baseline/canary/mutant/authored-test invocation against the SAME
// real checkout directory, in place. CPython's default source-based .pyc
// cache is keyed off a source file's (mtime_seconds, size); measured
// reproduction (see the pycache-invalidation branch's investigation): a
// mutant written back to the SAME path, at the SAME byte length, within the
// SAME wall-clock second as the run that populated that path's cache
// silently reads the STALE bytecode instead of recompiling — the mutant
// never actually executes, and a suite that would certainly have caught it
// reads as a "survivor" instead, depressing the measured kill rate.
//
// PYTHONPYCACHEPREFIX redirects both the READ and the WRITE side of that
// cache to dir — verified sufficient ON ITS OWN, with one condition: dir
// MUST be fresh for every call (this method is called fresh by
// WorkspaceRunner before each individual run, per lang.Plugin.WorkspaceRunEnv's
// contract). A dir reused across calls (even one this method itself
// created earlier) still lets a same-second, same-size mutant hit the
// baseline's own entry in that shared dir — measured and confirmed to still
// reproduce the bug. PYTHONDONTWRITEBYTECODE=1 is set too, defense in
// depth: it costs nothing (dir is deleted immediately after this run
// anyway) and it also means a run that somehow escapes cleanup (a killed
// process, a panic before cleanup runs) leaves no cache behind to have
// mattered in the first place. It is NOT, by itself, sufficient — verified:
// it only stops corral WRITING a new stale entry; it does nothing about a
// pre-existing __pycache__ a DEVELOPER's own earlier `pytest` run already
// left next to the source file, which PYTHONDONTWRITEBYTECODE=1 alone would
// still happily READ.
//
// A failed MkdirTemp degrades to PYTHONDONTWRITEBYTECODE=1 only — not a
// hard failure — because refusing to grade over an environment hiccup here
// would be a worse failure mode than "corral creates no new stale cache,
// but a pre-existing one could still theoretically be read this one time";
// see the doc comment above for why that residual is bounded and rare (it
// requires a developer's OWN prior run to have left a same-second,
// same-length cache entry at this exact path, not just corral's own).
func (pyPlugin) WorkspaceRunEnv() (env []string, cleanup func()) {
	dir, err := os.MkdirTemp("", "corral-pyc-run-*")
	if err != nil {
		return []string{"PYTHONDONTWRITEBYTECODE=1"}, func() {}
	}
	return []string{
		"PYTHONDONTWRITEBYTECODE=1",
		"PYTHONPYCACHEPREFIX=" + dir,
	}, func() { _ = os.RemoveAll(dir) }
}

// TestPaths returns pytest-convention candidates for codePath, most specific
// (least likely to collide with a DIFFERENT source file's test) first:
//
//  1. sibling test_foo.py    — same directory, pytest's own preferred prefix.
//  2. sibling foo_test.py    — same directory, the alternate suffix form.
//  3. full-mirror tests/<same full dir>/test_foo.py — keeps the ENTIRE
//     original directory path under tests/, so it can only ever pair with
//     this source file, even if some other top-level package happens to
//     share a subpath.
//  4. leading-segment-stripped tests/<dir minus first component>/test_foo.py
//     — the dominant real-world layout: a single top-level package (or a
//     `src/` layout) collapsed the same way, e.g.
//     `aisuite/agents/artifact_store.py` -> `tests/agents/test_artifact_store.py`.
//     Ranked below the full mirror because two DIFFERENT top-level packages
//     with the same subpath (`pkgA/agents/x.py` and `pkgB/agents/x.py`)
//     would generate the SAME candidate here, which the full mirror cannot.
//  5. flat tests/test_foo.py — no directory context at all, so it is the
//     most likely of the five to accidentally match a different source
//     file's test; tried last, and only generated for a SHALLOW source (dir
//     at most 2 path segments — e.g. `src/flask/views.py`). Beyond that
//     depth the flat form stops being a plausible convention and starts
//     being a collision magnet: on a real repo (flask) a 3-segment source
//     (`examples/javascript/js_example/views.py`) generated the exact same
//     flat candidate (`tests/test_views.py`) as the genuine top-level
//     `src/flask/views.py`, and both "paired" with the same test file. The
//     depth bound is a heuristic tuned to observed layouts — see Enumerate's
//     ambiguous-test demotion for the property that holds unconditionally.
//
// For a shallow codePath (dir has zero or one path segment), several of
// these forms coincide as STRINGS; dedupeCandidates collapses them to one
// entry and attributes it the LEAST specific (highest) Rank among the
// colliding forms — never the rank of whichever form happened to be listed
// first — so that two different sources whose match carries equally little
// real directory evidence always compare as EQUALLY ranked, regardless of
// how each of them individually arrived at that string. See TestCandidate
// and dedupeCandidates for why that distinction is load-bearing.
//
// Ranks: 0 = sibling (both forms), 1 = full mirror, 2 = leading-segment
// stripped, 3 = flat.
func (pyPlugin) TestPaths(codePath string) []TestCandidate {
	dir, base, _ := splitPath(codePath)
	name := "test_" + base + ".py"
	altName := base + "_test.py"

	out := []TestCandidate{
		{Path: joinDir(dir, name), Rank: 0},
		{Path: joinDir(dir, altName), Rank: 0},
		{Path: filepath.Join("tests", dir, name), Rank: 1},
		{Path: filepath.Join("tests", stripFirstSegment(dir), name), Rank: 2},
	}
	if dirDepth(dir) <= 2 {
		out = append(out, TestCandidate{Path: filepath.Join("tests", name), Rank: 3})
	}
	return dedupeCandidates(out)
}

// pytestPreflightProbe derives, from the operator's own test command
// (testCmd), the argv that proves pytest is importable under the EXACT
// interpreter/binary the operator named — never the host's stock
// python3/python guess (see pythonBin's doc comment for why that guess is
// wrong the instant the project lives in a venv: pythonBin() resolves off
// PATH, and an operator-named venv interpreter like .venv/bin/python is
// never on PATH under that name).
//
// Recognizes the same two invocation shapes CoverageCmd already keys off of
// (see that method's doc comment) so the two checks read an operator's
// command the same way:
//
//  1. bare `pytest`/`py.test ...` — the command itself IS the pytest
//     binary; probe it directly with --version.
//  2. `<interp> -m pytest ...` — probe pytest importability under that
//     EXACT interp with --version.
//
// Any other shape (tox, poetry run, a project wrapper script, `python -m
// unittest`, ...) returns ok=false: there is no reliable way to guess what
// "pytest is importable" even means for an opaque wrapper, or whether the
// named module supports --version at all. Preflight still owes those a real
// presence check on testCmd[0] itself — see Preflight below — just not this
// additional probe.
//
// Callers pass testCmd through stripLeadingEnvAssignments first (see
// Preflight): a leading `VAR=value` prefix (`PYTHONPATH=src pytest -q`)
// would otherwise land in testCmd[0] and hide the pytest/interp shape from
// both cases below.
func pytestPreflightProbe(testCmd []string) (probe []string, ok bool) {
	switch {
	case len(testCmd) >= 1 && (testCmd[0] == "pytest" || testCmd[0] == "py.test"):
		return []string{testCmd[0], "--version"}, true
	case len(testCmd) >= 3 && testCmd[1] == "-m" && testCmd[2] == "pytest":
		return []string{testCmd[0], "-m", "pytest", "--version"}, true
	default:
		return nil, false
	}
}

// Preflight fails CLOSED unless the toolchain that will actually run the
// suite has pytest importable (offline). The gate refuses to run rather
// than false-certify.
//
// With no explicit testCmd (certify --local with no override, or any other
// caller that has none), this is BYTE-IDENTICAL to the pre-argv-aware
// behavior: python3 (or python) on PATH, pytest importable under it.
//
// With an explicit testCmd — the operator's own `-- <cmd>` — that command IS
// the assertion of how the suite runs, stronger evidence than this plugin's
// stock guess, and MUST be what gets checked: the command's actual program
// token must be present (exec.LookPath handles both a bare name on PATH and
// an explicit path like .venv/bin/python identically — see toolOnPath), and
// when the shape is a recognized pytest invocation (pytestPreflightProbe),
// pytest's importability is probed under THAT interpreter, not pythonBin()'s
// guess.
//
// testCmd's program token is resolved via firstExecutableToken, not
// testCmd[0] directly: a leading `VAR=value` environment-assignment prefix
// (`-- PYTHONPATH=src pytest -q`, the same idiom pyCachePrefixEnv itself
// uses) is a legitimate, common operator idiom, not the executable, and
// firstExecutableToken skips past it to find the real one — the
// env-assignment case is genuinely FIXED, not merely downgraded.
//
// A shell-compound command (`-- cd sub && .venv/bin/python -m pytest`) is
// different: it names no single executable at all, and firstExecutableToken
// says so (ok=false) rather than guessing. This falls back to
// pyPreflightStockDefault — which is NOT a strictly better outcome for this
// one shape: it demands the HOST's stock python3/pytest be importable,
// which is a DIFFERENT refusal (the venv named later in the compound
// command is invisible to it), reinstating the original venv-not-found
// symptom this whole argv-aware fix exists to remove, just for a shape this
// function has no reliable way to parse a binary out of. That is the
// deliberate, correct trade — guessing wrong from testCmd[0] (treating
// "cd" as the executable) would be worse, a false diagnosis rather than an
// honest, less-precise one — not a case where the fallback avoids failing
// closed altogether.
func (pyPlugin) Preflight(testCmd []string) error {
	if len(testCmd) == 0 {
		return pyPreflightStockDefault()
	}
	bin, ok := firstExecutableToken(testCmd)
	if !ok {
		return pyPreflightStockDefault()
	}
	if err := toolOnPath(bin); err != nil {
		return fmt.Errorf("lang: python plugin preflight — the operator's test command names %q, which is not runnable: %w", bin, err)
	}
	probe, ok := pytestPreflightProbe(stripLeadingEnvAssignments(testCmd))
	if !ok {
		// An unrecognized shape (tox, poetry run, a wrapper script, ...):
		// testCmd[0]'s presence above is the only toolchain fact that can be
		// checked without guessing what the command actually needs.
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), preflightProbeTimeout)
	defer cancel()
	// #nosec G204 -- probe[0] is testCmd[0], the operator's OWN named
	// interpreter/binary from `-- <cmd>` (already presence-checked above via
	// toolOnPath, which rejects anything exec.LookPath can't resolve); the
	// remaining args are the fixed "-m pytest --version"/"--version" probe
	// this method constructs, never operator-controlled beyond that first
	// token. This is the intentional replacement for the old "run the named
	// tool with a fixed probe" pattern the pre-argv-aware version used too.
	// bin is now operator-named (not one of pythonBin()'s two hardcoded
	// choices), so this runs under preflightProbeTimeout — a wrapper that
	// blocks must not hang the whole audit before --timeout's own budget
	// even starts.
	probeCmd := exec.CommandContext(ctx, probe[0], probe[1:]...)
	// GuardProcess (internal/sandbox), not a bare WaitDelay: bin is now
	// OPERATOR-named, someone else's code, which is exactly the case
	// GuardProcess's own doc says it "MUST be applied to every os/exec
	// command corral runs" for. A WaitDelay-only fix bounds the WAIT but
	// leaves two other holes GuardProcess closes in one call: (1) no
	// process-group kill, so a script interpreter's grandchild (e.g.
	// `#!/bin/sh` backgrounding a worker) survives ctx cancellation as a
	// leaked process — confirmed by reproducing exactly that against this
	// probe: a backgrounding wrapper left 3 processes running after
	// "Preflight" returned; (2) the default cmd.Cancel only signals the
	// DIRECT child, so a WaitDelay-only fix still waits its FULL bound
	// again on top of ctx's own timeout (doubling the reported bound)
	// before it forces the pipes closed, rather than killing the whole
	// group promptly the moment ctx expires.
	sandbox.GuardProcess(probeCmd)
	if out, err := probeCmd.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("lang: python plugin preflight — pytest importability probe via %q did not finish within %s: %v",
				bin, preflightProbeTimeout, ctx.Err())
		}
		return fmt.Errorf("lang: python plugin preflight — pytest not importable via %q (%s): %v: %s",
			bin, strings.Join(probe, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// pyPreflightStockDefault is Preflight's fallback when the caller has no
// explicit test command: the same behavior this plugin had before testCmd
// existed (same interpreter resolution, same probe, same error text on a
// genuine import failure) plus one addition — the probe now runs under
// preflightProbeTimeout, so every existing caller with no `-- <cmd>` (or
// none applicable) sees no change EXCEPT that a probe which used to be able
// to hang forever now fails loud after a bounded wait instead.
func pyPreflightStockDefault() error {
	bin := pythonBin()
	if err := toolOnPath(bin); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), preflightProbeTimeout)
	defer cancel()
	// #nosec G204 -- bin is one of two hardcoded interpreter names ("python3" or
	// "python") returned by pythonBin(); the args are constant. No external input.
	// GuardProcess: see the identical comment in Preflight above.
	stockCmd := exec.CommandContext(ctx, bin, "-m", "pytest", "--version")
	sandbox.GuardProcess(stockCmd)
	if out, err := stockCmd.CombinedOutput(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("lang: python plugin preflight — pytest importability probe did not finish within %s: %v", preflightProbeTimeout, ctx.Err())
		}
		return fmt.Errorf("lang: python plugin preflight — pytest not importable (install it on the host): %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (pyPlugin) PromptLang() string { return "Python" }

func (pyPlugin) TestWriterSystem() string {
	return `You are a TEST-WRITER. Given a security control GOAL, a target source file, and its signature surface, write ONE executable pytest test that verifies the code SATISFIES the goal.
- Import the module under test (white-box), using EXACTLY the import given in the task instruction below — never guess a different one.
- It MUST FAIL if the goal is violated — test the goal's boundary (what a weakened implementation would pass that a compliant one must not).
- Standard library plus pytest only. Deterministic, no network.
Return ONLY the raw Python test file content — no prose, no markdown fences.`
}

// ImportPath walks up from codePath's own directory while exists reports a
// package marker (__init__.py) present, dot-joining each package directory
// name it crosses onto the file's own base name — the same resolution
// pytest's own package-aware import mode performs when it collects a test
// file that lives inside a real package (rootdir insertion stops climbing
// at the first ancestor WITHOUT an __init__.py, and everything below that
// point becomes the dotted import). E.g. src/flask/cli.py with
// src/flask/__init__.py present but no src/__init__.py resolves to
// "flask.cli": climbing stops at "src" (the namespace boundary), "src"
// itself is never a package and so never joins the dotted path.
//
// exists == nil means the caller has no real filesystem to consult (e.g. a
// hosted/MCP run with no checkout on disk) — returning ok=false rather than
// silently assuming "no packages here" is the fail-closed half of this fix:
// a wrong assumption ("importable by bare base name") is exactly the bug
// this method exists to stop making.
//
// A file with NO __init__.py anywhere above it (dir has none) climbs zero
// levels and correctly resolves to just its own base name — that really is
// how Python imports a rootless module, so this is not a "could not
// determine" case, it is a genuine (if trivial) determination.
//
// Every directory name crossed while climbing (and the file's own base
// name) is validated with isPythonIdentifier before it joins the dotted
// path: a real repo's directory name is not guaranteed to BE one (a `2fa/`
// package dir, a dashed `my-pkg/`, a dotted `my.pkg/`, one containing a
// space, or one that collides with a Python keyword like `class/` all
// appear in the wild) — joining it anyway would hand the test-writer a
// dotted string that LOOKS like a real import but is a SyntaxError the
// instant it is written (`import 2fa.totp`), which is exactly the class of
// bug this whole fix exists to remove, just re-entered through a directory
// name instead of a missing fact. ok=false there is the honest answer: the
// already-correct ImportNote(_, false) fallback (importlib.util.
// spec_from_file_location, keyed off the file's own path rather than a
// name) is the right advice for a package whose directory isn't a legal
// identifier, since Python itself cannot `import` it by name either.
func (pyPlugin) ImportPath(codePath string, exists func(path string) bool) (string, bool) {
	if exists == nil {
		return "", false
	}
	dir, base, ext := splitPath(codePath)
	if ext != ".py" || !isPythonIdentifier(base) {
		return "", false
	}
	segments := []string{base}
	for dir != "" {
		if !exists(filepath.ToSlash(filepath.Join(dir, "__init__.py"))) {
			break
		}
		seg := filepath.Base(dir)
		if !isPythonIdentifier(seg) {
			return "", false
		}
		segments = append([]string{seg}, segments...)
		parent := filepath.Dir(dir)
		if parent == "." {
			parent = ""
		}
		if parent == dir {
			break // defensive: filepath.Dir is a fixed point at the root ("/", "."); never loop forever.
		}
		dir = parent
	}
	// src/flask/__init__.py resolves segments to ["flask", "__init__"]: that
	// DOES import (Python happily accepts "import flask.__init__"), but as a
	// SECOND, distinct module object from the canonical "flask" — technically
	// working, but not what any human (or a later reviewer of the authored
	// test) would recognize as the real import. Strip a trailing ".__init__"
	// down to its package.
	if n := len(segments); n > 1 && segments[n-1] == "__init__" {
		segments = segments[:n-1]
	}
	return strings.Join(segments, "."), true
}

// pythonKeywords is the reserved-word set that cannot appear as a Python
// identifier — https://docs.python.org/3/reference/lexical_analysis.html#keywords
// (soft keywords like "match"/"case"/"_" are valid identifiers and
// deliberately excluded). A directory or file base name that collides with
// one of these cannot be joined into a dotted import (`import class.foo` is
// a SyntaxError) even though it is a syntactically ordinary path segment.
var pythonKeywords = map[string]bool{
	"False": true, "None": true, "True": true, "and": true, "as": true,
	"assert": true, "async": true, "await": true, "break": true, "class": true,
	"continue": true, "def": true, "del": true, "elif": true, "else": true,
	"except": true, "finally": true, "for": true, "from": true, "global": true,
	"if": true, "import": true, "in": true, "is": true, "lambda": true,
	"nonlocal": true, "not": true, "or": true, "pass": true, "raise": true,
	"return": true, "try": true, "while": true, "with": true, "yield": true,
}

// isPythonIdentifier reports whether s is a legal Python identifier: a
// non-empty ASCII sequence starting with a letter or underscore, continuing
// with letters/digits/underscores, and not a reserved keyword. Deliberately
// ASCII-only and conservative — Python technically permits Unicode
// identifiers, but a false "not an identifier" here only costs an honest
// ok=false (the safe direction: see ImportPath's doc comment), while a false
// "is an identifier" would hand the test-writer a broken import to write.
func isPythonIdentifier(s string) bool {
	if s == "" || pythonKeywords[s] {
		return false
	}
	for i, r := range s {
		switch {
		case r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
			continue
		case r >= '0' && r <= '9' && i > 0:
			continue
		default:
			return false
		}
	}
	return true
}

// ImportNote states the derived import as a FACT for this task, or says
// plainly that it could not be determined — never the withdrawn "assume
// base name" guess TestWriterSystem used to make unconditionally, which is
// exactly what broke on a real package (src/flask/cli.py: "import cli"
// cannot resolve — the module is flask.cli). A wrong instruction is worse
// than an absent one; when ok is false this says so instead of guessing.
func (pyPlugin) ImportNote(importPath string, ok bool) string {
	if ok && importPath != "" {
		return fmt.Sprintf("The correct import for the module under test is %q — write exactly `import %s` (or `from %s import ...`); do NOT import it by its bare file base name unless that IS the full path shown here.\n\n",
			importPath, importPath, importPath)
	}
	return "The correct import path for the module under test could not be determined automatically (this file may live inside a real Python package this task was not given enough context to resolve). Do NOT assume it is importable by its bare file base name — if the source above shows package context (e.g. relative imports, an __init__.py sibling), infer the dotted import from that; otherwise load it via importlib.util.spec_from_file_location keyed off the file's own path rather than guessing a name.\n\n"
}

func (pyPlugin) MutantSystem() string {
	return `You are a MUTATION-TESTING ENGINE. Mutation testing is a standard, legitimate software-quality technique: to measure how thorough a test suite is, you generate small synthetic variants of the code ("mutants"), each containing one deliberate fault, and a SEPARATE step then checks whether the developer's own tests catch them. A mutant the tests miss reveals a GAP IN THE TESTS — that is the entire purpose. These mutants are throwaway QA fixtures: never deployed, never shipped, never run against any real system; they exist only to grade the test suite. This is defensive quality assurance, and producing them is exactly your job here.
Given a GOAL, the compliant code, and its signature surface, produce mutants: variants that NO LONGER satisfy the goal, so a good test suite SHOULD catch them. Each mutant MUST keep the EXACT same public signatures (drop-in importable Python) and must genuinely fail the goal — vary HOW it fails. No no-ops, no syntax errors, no tests.
The output format (a SEARCH/REPLACE edit per mutant) is specified with the task.`
}

func (pyPlugin) SingleTestCmd(testPath, selector string) ([]string, bool) {
	if selector == "" {
		return nil, false
	}
	return []string{"python3", "-m", "pytest", "-q", selector}, true
}

func (pyPlugin) ListTestsCmd(testPath string) ([]string, bool) {
	if testPath == "" {
		return nil, false
	}
	return []string{"python3", "-m", "pytest", "--collect-only", "-q", testPath}, true
}

// CoverageCmd wraps testCmd into ONE `sh -c` script that instruments the run
// with `coverage` (coverage.py) and prints a machine-readable JSON report on
// stdout. It accepts two shapes, CHECKED IN THIS ORDER (order is
// load-bearing — see the note below):
//
//  1. [pytest|py.test, ...args] — a BARE pytest invocation. This is the
//     shape that matters most in practice, not a fallback: action.yml and
//     docs/corral/github-action.md document `test-command: pytest ...` as
//     the canonical GitHub Action example, and cmd/corral/certify_repo.go's
//     testCmd() passes the operator's `-- <cmd>` argv through UNCHANGED —
//     so a real, documented, already-shipped invocation is exactly
//     ["pytest", "-q"], not the TestCmd() shape. Measured: without this
//     case, that invocation got ok=false and silently no pre-flight ran on
//     the one path most operators actually use. "py.test" is pytest's
//     console-script alias; the importable module is always "pytest"
//     either way. This ALSO covers ["pytest", "-m", "not slow"] — pytest's
//     OWN `-m <marker-expression>` flag, a mainstream and
//     action.yml-documented pytest option that happens to share pytest's
//     flag spelling with Python's `-m <module>` flag. This must be checked
//     BEFORE shape 2 below: checking shape 2 first would see
//     testCmd[1]=="-m" and misread the marker expression "not slow" as a
//     MODULE to run, building `pytest -m coverage run -m 'not slow'` — which
//     runs pytest with marker expression "coverage" and positional
//     args "run"/"not slow", never the operator's suite and never coverage
//     at all. (This does fail closed either way — pytest given "run" and
//     "not slow" as bare positional args exits 4, usage error, before any
//     JSON is printed, so ParseCoverage still errors rather than
//     fabricating a false accusation — but it silently no-ops the
//     pre-flight on a mainstream invocation, which checking this shape
//     first avoids entirely.)
//  2. [interpreter, "-m", <module>, ...args] — what pyPlugin.TestCmd() itself
//     returns, e.g. ["python3", "-m", "pytest", "-q"].
//
// Any other shape (e.g. ["poetry", "run", "pytest"] — measured, also
// ok=false) is declined rather than guessed at: there is no reliable way to
// splice `coverage run -m` into an arbitrary wrapper without risking running
// something other than what the operator specified.
//
// Determined empirically against a real repository (pallets/flask, shallow
// clone), not from documentation — see
// .superpowers/sdd/2026-07-29-coverage-preflight/task-2-report.md for the
// full transcript, including a round of adversarial re-verification that
// found and fixed the issues this comment now documents. Three things
// shape the script this builds:
//
//  1. `coverage json -o -` is a SEPARATE command from the instrumented test
//     run (pytest-cov writes its JSON to a file by default; there is no
//     single-invocation pytest-cov flag that puts JSON on stdout). The brief
//     accepts a two-command `sh -c "<cmd1> && <cmd2>"` form for exactly this
//     reason — same shape as Task 1's `go test ...; cat "$f"` split.
//  2. A bare `&&` between the two commands is WRONG for Python: `coverage
//     run -m pytest` exits non-zero whenever ANY test fails, which is the
//     ordinary case for a suite corral is auditing (that's the whole point
//     of running it) — not a tooling failure. `&&` would silently skip the
//     `coverage json` step on that ordinary case and starve the caller of a
//     report entirely.
//  3. But a BARE `;` is ALSO wrong on its own, and this is the part a first
//     pass missed: coverage.py's `[tool.coverage.run] source = [...]`
//     config (real-world, e.g. flask's own pyproject.toml) makes `coverage
//     json` enumerate every file under the configured source tree whether
//     or not it was ever executed — so when the suite never actually ran at
//     all (a broken pytest plugin, a `conftest.py` import crashing before
//     any test file loads, `-p nonexistent-plugin`), `coverage json` still
//     exits 0 and prints a well-formed, syntactically valid report, just
//     with EVERY file's `covered_lines` at 0 and `totals.covered_lines: 0`.
//     Reproduced directly against flask
//     (`coverage run -m pytest -p nonexistent_plugin_xyz`): exit 0, valid
//     JSON, 23 files, all zero. A bare `;` would hand that straight to
//     ParseCoverage indistinguishable from "the suite ran and genuinely
//     touched nothing" — silently reporting "your suite never touches any
//     file in this repo" from what is actually a missing test dependency.
//     The fix keeps `;`'s benefit (still report on ordinary test FAILURES)
//     while refusing to run the json step at all when the test run's own
//     exit code says it didn't get that far: pytest's exit codes are
//     specific (0 ok, 1 tests failed — both are "it ran"; 2 interrupted, 3
//     internal error, 4 usage error, 5 no tests collected — none of those
//     are "it ran"), so `rc=$?; case $rc in 0|1) ;; *) exit "$rc" ;; esac;`
//     runs between the two commands: exit codes 0 and 1 fall through to the
//     json step, everything else re-raises that exit code and skips it,
//     leaving stdout non-JSON — which ParseCoverage already turns into an
//     error, never an empty map. ParseCoverage ALSO checks
//     totals.covered_lines itself (see below) as a backstop that holds
//     regardless of how a report reached it, since a shell-layer check can
//     only guard the one command this method builds.
//
// Every element of testCmd is shell-quoted via shellQuote (shared with
// goPlugin, defined in go.go) — nothing here is interpolated unquoted.
func (pyPlugin) CoverageCmd(testCmd []string) (cmd []string, ok bool) {
	var interp string
	var moduleArgs []string // argv coverage should run via `-m`, e.g. ["pytest", "-q"].

	switch {
	case len(testCmd) >= 1 && (testCmd[0] == "pytest" || testCmd[0] == "py.test"):
		interp = pythonBin()
		moduleArgs = append([]string{"pytest"}, testCmd[1:]...)
	case len(testCmd) >= 3 && testCmd[1] == "-m":
		interp = testCmd[0]
		moduleArgs = testCmd[2:]
	default:
		return nil, false
	}

	quotedInterp := shellQuote(interp)
	quotedModule := make([]string, len(moduleArgs))
	for i, arg := range moduleArgs {
		quotedModule[i] = shellQuote(arg)
	}
	// F3: point coverage.py's own data file at a mktemp'd path via
	// COVERAGE_FILE, cleaned up by an EXIT trap — the same discipline
	// goPlugin.CoverageCmd already uses for `-coverprofile`. Without this,
	// `coverage run` defaults to writing `.coverage` directly into the cwd
	// (the operator's own --repo checkout on the workspace substrate),
	// leaving a real, non-trivial artifact behind after every pre-flight
	// run (475 KB, measured against a real flask run) — an audit tool must
	// not litter the tree it is auditing. `coverage json` is pointed at the
	// SAME COVERAGE_FILE so it reads the data the run just wrote, not
	// whatever (if anything) happens to be sitting at the default path.
	script := `f=$(mktemp) && trap 'rm -f "$f"' EXIT && ` +
		`COVERAGE_FILE="$f" ` + quotedInterp + " -m coverage run -m " + strings.Join(quotedModule, " ") +
		`; rc=$?; case $rc in 0|1) ;; *) exit "$rc" ;; esac; ` +
		`COVERAGE_FILE="$f" ` + quotedInterp + " -m coverage json -o -"
	return []string{"sh", "-c", script}, true
}

// pyCoverageReport is the subset of `coverage json`'s schema (format 3)
// ParseCoverage reads: https://coverage.readthedocs.io/en/latest/cmd.html#json-reporting
// The "meta" and "files" keys are required to exist for input to be accepted
// as a genuine report at all (see ParseCoverage); everything else in the real
// schema (per-line executed_lines/missing_lines, branch coverage,
// functions/classes breakdowns) is real but unused here.
type pyCoverageReport struct {
	Files map[string]struct {
		Summary struct {
			CoveredLines  int `json:"covered_lines"`
			NumStatements int `json:"num_statements"`
		} `json:"summary"`
	} `json:"files"`
	Totals struct {
		CoveredLines int `json:"covered_lines"`
	} `json:"totals"`
}

// ParseCoverage reads the output of the command CoverageCmd builds. Because
// that command is a `sh -c "<instrumented test run>; ...; coverage json -o -"`
// script, stdout carries whatever the wrapped test command itself printed
// (pytest's own dot-progress / summary output) BEFORE the JSON report — the
// same preamble-then-payload shape Task 1 found for Go, except coverage.py's
// JSON output has no leading header token to key off of. Verified against a
// real captured transcript (flask): the report is written as a single line
// with no trailing newline, and it is always the LAST non-blank line of
// stdout — so that is the structural marker ParseCoverage uses to find it.
//
// A file is "executed" if its summary.covered_lines is greater than zero.
// The returned map records that per file the REPORT measured at all — see
// the tri-state note directly above the alignment loop below for why a file
// outside coverage.py's scope is left absent rather than inserted as false.
//
// modulePath is the REPO ROOT (an absolute filesystem path), NOT an
// import-path-style prefix the way Go's module path is — coverage.py's own
// path shape depends on the working directory the test command ran from,
// which corral's harness does not otherwise pin down:
//
//   - Run from the repo root, coverage.py already reports paths relative to
//     the CURRENT DIRECTORY, which coincides with the repo root — those
//     pass through unchanged.
//   - Run from ANY OTHER directory (verified: from flask's own tests/
//     subdirectory, `coverage json` emits absolute paths, e.g.
//     "/…/flask/src/flask/app.py", not "src/flask/app.py") coverage.py
//     switches to absolute paths. An absolute path found UNDER modulePath is
//     relativized to it; an absolute path found OUTSIDE modulePath (a
//     dependency imported from elsewhere, e.g. site-packages) is skipped —
//     it is not this repo's source, so it has no repo-relative form to
//     report and reposcan.Enumerate would never have a candidate for it
//     anyway.
//   - An ABSOLUTE path with modulePath == "" cannot be aligned to anything —
//     guessing would silently fabricate a repo-relative path that might
//     collide with an unrelated file, which is exactly the kind of silent
//     misalignment this method exists to refuse. That combination is
//     therefore an ERROR, not a best-effort pass-through.
//
// modulePath is normalised with filepath.Clean (and "/" handled as its own
// case) before use, and each absolute report path is ALSO filepath.Clean'd
// before the prefix match — an un-Clean'd modulePath (a caller passing
// "/repo/" instead of "/repo") would otherwise make the prefix match
// "/repo//" and silently miss every entry, landing right back in this
// method's own failure mode from a caller-side slip rather than a
// coverage.py quirk; Clean'ing the report path the same way also stops a
// pathological "/repo/../repo-other/x.py" entry from prefix-matching
// "/repo/" as a substring and being relativized to "../repo-other/x.py" (not
// a repo-relative path at all — it would just fail to match any candidate
// downstream, but is folded into the same "outside root, skip" guard rather
// than left as a slightly-wrong path floating around).
//
// The last line failing to parse as JSON, or parsing but missing the "meta",
// "files", or "totals" keys that mark it as a genuine coverage-json report
// (rather than some other JSON a wrapped command happened to print), a
// "files" entry whose covered_lines is not a number, or an absolute path
// with no modulePath to align it against, are all ERRORS — never an empty
// map — for the same reason Task 1's ParseCoverage treats a missing "mode:"
// header as an error: a dropped/misread/misaligned report must not silently
// present as "nothing was executed", which downstream is turned into a
// repo-wide untested-files finding.
//
// totals.covered_lines == 0 is ALSO an error, not the empty-result case —
// this is the one place "well-formed but empty" stops being legitimate.
// Reproduced directly against flask: `coverage run -m pytest -p
// nonexistent_plugin_xyz` (a broken plugin — the suite never runs a single
// test) exits 0 and `coverage json` still prints a syntactically valid
// report, because coverage.py's `[tool.coverage.run] source = [...]` config
// (real, present in flask's own pyproject.toml) makes it enumerate every
// file under the configured source tree whether or not anything executed —
// 23 files, every one at covered_lines: 0, totals.covered_lines: 0. That is
// indistinguishable, file-by-file, from "the suite ran and genuinely touched
// nothing" without this check. "The suite ran and executed zero statements
// total" is not a real state for anything but a suite that never ran at
// all — it does not describe a real, if unlucky, coverage outcome the way a
// single legitimately-untouched FILE can (see the file-level check above,
// which this does not change: fixtureAllZero below still has
// totals.covered_lines: 7525, so its files stay legitimate measured-but-
// unexecuted (present, false) entries, never an error).
//
// A NARROWER form of the same misalignment survives per-entry skipping on
// its own: if totals.covered_lines is positive (the suite genuinely ran and
// executed real code) but EVERY entry with covered_lines > 0 turns out to be
// outside modulePath, the loop below would otherwise return an empty map
// with a nil error — a suite that ran green reported as "touches nothing in
// this repo". Reproduced against a real (non-flask) project: a src-layout
// package installed non-editable (`pip install .`) with
// `[tool.coverage.run] source = ["mypkg2"]` and NO
// `[tool.coverage.paths]` remap traces coverage against the INSTALLED
// site-packages copy, not the repo checkout — every positively-covered file
// is therefore outside any reasonable repo-root modulePath (flask is immune
// only because its own pyproject.toml happens to carry a
// `[tool.coverage.paths] source = ["src", "*/site-packages"]` remap that
// pulls those paths back to "src/..." before `coverage json` ever emits
// them). Per-FILE skipping outside the root is correct (a real dependency
// should not masquerade as a repo file); skipping ALL of them, when at least
// one file genuinely had coverage, is not a legitimate empty result, it is
// a sign modulePath is wrong or the project needs a path remap corral
// cannot see — so that combination is an error too.
func (pyPlugin) ParseCoverage(stdout, modulePath string) (executed map[string]bool, err error) {
	lines := strings.Split(stdout, "\n")
	i := len(lines) - 1
	for i >= 0 && strings.TrimSpace(lines[i]) == "" {
		i--
	}
	if i < 0 {
		return nil, fmt.Errorf("lang: python coverage report is empty")
	}
	payload := lines[i]

	var top map[string]json.RawMessage
	if unmarshalErr := json.Unmarshal([]byte(payload), &top); unmarshalErr != nil {
		return nil, fmt.Errorf("lang: unparseable python coverage report (last line is not JSON): %w", unmarshalErr)
	}
	if _, ok := top["meta"]; !ok {
		return nil, fmt.Errorf("lang: python coverage report has no %q key — not a coverage-json report", "meta")
	}
	if _, ok := top["totals"]; !ok {
		return nil, fmt.Errorf("lang: python coverage report has no %q key — not a coverage-json report", "totals")
	}
	if _, ok := top["files"]; !ok {
		return nil, fmt.Errorf("lang: python coverage report has no %q key — not a coverage-json report", "files")
	}

	var report pyCoverageReport
	if unmarshalErr := json.Unmarshal([]byte(payload), &report); unmarshalErr != nil {
		return nil, fmt.Errorf("lang: unparseable python coverage report: %w", unmarshalErr)
	}
	if report.Totals.CoveredLines == 0 {
		return nil, fmt.Errorf("lang: python coverage report totals.covered_lines is 0 — the suite most likely never ran (e.g. a broken pytest plugin or collection error), not a genuinely-empty result")
	}

	// The returned map is TRI-STATE: every file the report MEASURED gets an
	// entry (true if covered_lines > 0, false if it was in scope and never
	// executed), and a file the report never mentions at all is left ABSENT
	// — never inserted as false. coverage.py's own [tool.coverage.run]
	// source = [...] scoping routinely covers a SUBSET of the repo (flask:
	// 58 files, not the whole tree); treating "outside that scope" the same
	// as "measured and found unexecuted" would turn every file corral never
	// even looked at into a false accusation. Only "present, false" is a
	// real finding — "absent" must stay a silent, non-accusing fact a
	// caller reports as a COUNT, never as a per-file claim.
	//
	// A file with num_statements == 0 gets the SAME absent treatment, for
	// the same reason: coverage.py reports a real, empty file (e.g. a
	// package's zero-byte __init__.py) with covered_lines: 0 alongside
	// every genuinely-untouched file, and the two are indistinguishable
	// from covered_lines alone. A file with nothing measurable cannot be
	// "unexecuted" — there is no statement to have skipped — it can only be
	// uninformative, and importing it (as a bare package init routinely is,
	// with no statement of its own to record) leaves NO trace in
	// covered_lines either. Reproduced directly against a real flask run:
	// tests/test_apps/blueprintapp/apps/__init__.py and
	// tests/test_apps/cliapp/__init__.py are both zero-byte files the suite
	// DOES import (tests/test_blueprints.py, tests/test_cli.py), reported
	// by coverage.py as num_statements: 0, and were WRONGLY surfaced as a
	// "measured and never executed" finding before this check existed. Go
	// needs no equivalent: a file with no statements emits no profile
	// blocks at all and is already absent from goPlugin.ParseCoverage's
	// map by construction — this num_statements guard is Python-only.
	root := normalizePyRepoRoot(modulePath)
	sawPositiveEntry := false
	sawPositiveAligned := false
	executed = make(map[string]bool)
	for path, f := range report.Files {
		if f.Summary.NumStatements == 0 {
			continue
		}
		exec := f.Summary.CoveredLines > 0
		if exec {
			sawPositiveEntry = true
		}

		if filepath.IsAbs(path) && root == "" {
			if !exec {
				// An unexecuted, unaligned absolute path carries no
				// finding either way — there is no repo-relative form to
				// report it under, and "absent" (never measured, from
				// this caller's point of view) is already the honest
				// silence for it. Only a POSITIVE entry with nowhere to
				// align is the alignment failure worth erroring on
				// (see the unconditional case below).
				continue
			}
			return nil, fmt.Errorf("lang: python coverage report contains an absolute path %q but no repo root (modulePath) was given to align it against", path)
		}
		p, ok := alignPyPath(path, root)
		if !ok {
			// Outside the repo root entirely (e.g. a dependency imported
			// from site-packages, or a path that only reaches under the
			// root via a "..\" segment) — not this repo's source, skip
			// rather than report a bogus path (executed OR not: either way
			// it is not a file corral can name).
			continue
		}
		executed[p] = exec
		if exec {
			sawPositiveAligned = true
		}
	}
	if sawPositiveEntry && !sawPositiveAligned {
		return nil, fmt.Errorf("lang: python coverage report has file(s) with covered_lines > 0, but none aligned under repo root %q — likely a wrong modulePath or a coverage.py [tool.coverage.paths] remap corral cannot see, not a genuinely empty result", modulePath)
	}
	return executed, nil
}

// normalizePyRepoRoot cleans modulePath into the exact form ParseCoverage's
// prefix match expects: filepath.Clean (dropping a trailing slash, resolving
// "." / ".." segments, collapsing repeats) then filepath.ToSlash, with "/"
// itself special-cased so the prefix built from it is "/" and not "//". ""
// passes through unchanged (means "no repo root given").
func normalizePyRepoRoot(modulePath string) string {
	if modulePath == "" {
		return ""
	}
	clean := filepath.ToSlash(filepath.Clean(modulePath))
	if clean == "/" {
		return "/"
	}
	return clean
}

func (pyPlugin) ParseTestList(output string) []string {
	var out []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		// pytest -q node ids contain "::"; the summary line ("N tests collected")
		// and blank lines do not.
		if strings.Contains(line, "::") {
			out = append(out, line)
		}
	}
	return out
}

// FailedTests parses pytest's "short test summary info" lines. pytest prints
// one `FAILED <selector> - <error>` line per failure, and the selector is the
// exact `path::test` form `--deselect` accepts. The trailing " - <error>"
// summary must be stripped: passing it through would make the selector match
// nothing and the deselect silently no-op.
func (pyPlugin) FailedTests(output string) []string {
	var out []string
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		rest, ok := strings.CutPrefix(line, "FAILED ")
		if !ok {
			continue
		}
		// The selector runs to the first space; everything after is pytest's
		// own error summary, not part of the id.
		if sel, _, found := strings.Cut(rest, " "); found {
			rest = sel
		}
		if rest = strings.TrimSpace(rest); rest != "" {
			out = append(out, rest)
		}
	}
	return out
}

// DeselectArgs renders one --deselect per selector: pytest takes the flag
// repeatedly and does NOT accept a comma-joined list.
func (pyPlugin) DeselectArgs(selectors []string) []string {
	args := make([]string, 0, len(selectors)*2)
	for _, s := range selectors {
		if s = strings.TrimSpace(s); s != "" {
			args = append(args, "--deselect", s)
		}
	}
	return args
}

// selectionMaxArgv bounds the node-id list Select puts on one command line.
// Past it, Select names the test FILES those ids live in instead — a
// superset, still evidence-derived, and never a whole-suite fallback.
const selectionMaxArgv = 32 * 1024

// Instrument builds the one instrumented run selection evidence comes from.
// Same command-shape rules as CoverageCmd (a `pytest` or `<interp> -m pytest`
// argv; anything else is refused rather than guessed at), but the
// instrumentation is pytest-cov's, because only pytest-cov records
// per-TEST dynamic contexts while keeping the project's own coverage
// configuration (source/omit) in force. `--cov-report=` suppresses the
// terminal report; the JSON is emitted afterwards by coverage itself with
// contexts shown. The data file is a temp path so the project's own
// .coverage is never touched.
func (pyPlugin) Instrument(testCmd []string) (cmd []string, ok bool) {
	var interp string
	var args []string
	switch {
	case len(testCmd) >= 1 && (testCmd[0] == "pytest" || testCmd[0] == "py.test"):
		interp = pythonBin()
		args = append([]string{"pytest"}, testCmd[1:]...)
	case len(testCmd) >= 3 && testCmd[1] == "-m" && testCmd[2] == "pytest":
		interp = testCmd[0]
		args = testCmd[2:]
	default:
		return nil, false
	}
	q := make([]string, len(args))
	for i, a := range args {
		q[i] = shellArg(a)
	}
	qi := shellArg(interp)
	// COVERAGE_CORE=ctrace on BOTH steps: coverage's sysmon core — its
	// default on Python 3.12+ — does not support dynamic contexts. It warns
	// "context data may be incomplete", which under a project's
	// filterwarnings=error fails every test at setup (flask: 985 errors,
	// every file read as uncovered) and otherwise records partial contexts
	// (requests: 11 of the 234 tests that execute adapters.py). The C
	// tracer supports contexts on every supported Python; a build without
	// it fails loudly here and the scan grades whole-suite, disclosed.
	//
	// The suite must PASS (rc 0). CoverageCmd tolerates rc 1 because a
	// failing suite still yields useful file-level coverage; for selection
	// that tolerance is wrong — the tests that fail at setup execute
	// nothing, are never selected, and the narrowed baseline then passes on
	// a suite the whole-suite baseline would refuse (#164).
	//
	// The evidence is reduced INSIDE the run, from the coverage API, to
	// {file: [node ids]} — `coverage json --show-contexts` emits every
	// context of every line and was 411 MB on flask (branch=true); the
	// reduced form of the same run is 331 KB (#165).
	script := `f=$(mktemp) && trap 'rm -f "$f"' EXIT && ` +
		`COVERAGE_CORE=ctrace COVERAGE_FILE="$f" ` + qi + " -m " + strings.Join(q, " ") +
		` --cov --cov-context=test --cov-report= -p no:cacheprovider` +
		`; rc=$?; [ "$rc" -eq 0 ] || exit "$rc"; ` +
		`COVERAGE_CORE=ctrace COVERAGE_FILE="$f" ` + qi + " - <<'PY'\n" + pySelectionReducer + "PY\n"
	return []string{"sh", "-c", script}, true
}

// pySelectionReducer runs after the instrumented suite, in the same shell,
// against the data file the run wrote. It emits ONE compact JSON document:
// per measured file (repo-relative, from the cwd the suite ran in), the
// sorted node ids of every test whose context executed a line of it (with
// pytest-cov's `|setup`/`|run`/`|teardown` phase suffix stripped), the
// closed line ranges each of those tests executed (by index into that
// test list), the ranges executed under no test context at all (import
// time), and the number of distinct tests seen anywhere.
const pySelectionReducer = `import json, os, sys
import coverage
cov = coverage.Coverage(data_file=os.environ["COVERAGE_FILE"])
cov.load()
data = cov.get_data()
root = os.getcwd()

def ranges(lines):
    out = []
    for n in sorted(lines):
        if out and out[-1][1] + 1 == n:
            out[-1][1] = n
        else:
            out.append([n, n])
    return out

files = {}
tests = set()
for path in data.measured_files():
    rel = os.path.relpath(path, root)
    if rel.startswith(".."):
        continue
    by_test = {}
    static = set()
    for lineno, ctxs in data.contexts_by_lineno(path).items():
        for c in ctxs:
            if not c:
                static.add(lineno)
                continue
            by_test.setdefault(c.rsplit("|", 1)[0], set()).add(lineno)
    ids = sorted(by_test)
    tests.update(ids)
    files[rel] = {
        "tests": ids,
        "lines": {str(i): ranges(by_test[t]) for i, t in enumerate(ids)},
        "static": ranges(static),
    }
json.dump({"format": "corral-selection-2", "tests": len(tests), "files": files}, sys.stdout, separators=(",", ":"))
`

// shellArg renders one Instrument argv element for inclusion in its sh -c
// script: bare when it is already safe unquoted (so a plain pytest
// invocation reads naturally, matching what an operator actually typed),
// shellQuote'd only when it contains a character shellArg does not
// recognise as safe (a space, in the common case of a marker expression
// like "not slow").
func shellArg(s string) string {
	if s == "" {
		return shellQuote(s)
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_' || r == '.' || r == '/' || r == '=' || r == ':' || r == ',' || r == '+' || r == '-':
		default:
			return shellQuote(s)
		}
	}
	return s
}

// pyContextReport is the subset of `coverage json --show-contexts` Select
// reads. contexts maps a line number (as a string) to the contexts that
// executed it; pytest-cov names a test context `<nodeid>|<phase>`.
// pySelectionFormat stamps the reducer's output so Select refuses any other
// document — including the full coverage-json it used to parse — by name.
const pySelectionFormat = "corral-selection-2"

// pySelectionEvidence is what pySelectionReducer emits: Tests is the count
// of distinct tests seen across the whole run; Files is decoded separately
// (see pySelectionEvidenceFiles) after Format has been checked.
type pySelectionEvidence struct {
	Format string `json:"format"`
	Tests  int    `json:"tests"`
	// Files is decoded in a second step, after Format has been checked, so
	// a document of another shape is refused BY NAME rather than with an
	// unmarshal error about a field it was never meant to have.
	Files map[string]pySelectionFile `json:"-"`
}

// pySelectionFile is one measured file's entry: the node ids of the tests
// that executed it, each test's executed line ranges (by index into
// Tests), and the ranges executed under no test context (import time).
type pySelectionFile struct {
	Tests  []string            `json:"tests"`
	Lines  map[string][][2]int `json:"lines"`
	Static [][2]int            `json:"static"`
}

type pySelectionEvidenceFiles struct {
	Files map[string]pySelectionFile `json:"files"`
}

// Select narrows testCmd to the tests whose recorded context executed any
// line of codePath. Node ids are sorted so the command — and therefore the
// cache key — is stable for the same evidence.
func (pyPlugin) Select(evidence []byte, repoRoot, codePath, testPath string, testCmd []string) (Selection, error) {
	// The evidence is usually a raw `coverage json` capture: whatever the
	// wrapped test run printed, then the JSON as the LAST line (Instrument's
	// own shape). It may also be the JSON alone, pretty-printed across many
	// lines (a recorded fixture) — try the whole trimmed payload first, and
	// only fall back to the last-line-of-stdout rule on failure.
	var rep pySelectionEvidence
	payload := strings.TrimSpace(string(evidence))
	if payload == "" {
		return Selection{}, fmt.Errorf("lang: python selection evidence is empty")
	}
	if err := json.Unmarshal([]byte(payload), &rep); err != nil {
		payload = lastNonEmptyLine(evidence)
		if payload == "" {
			return Selection{}, fmt.Errorf("lang: python selection evidence is empty")
		}
		if err := json.Unmarshal([]byte(payload), &rep); err != nil {
			return Selection{}, fmt.Errorf("lang: unparseable python selection evidence (last line is not JSON): %w", err)
		}
	}
	if rep.Format != pySelectionFormat {
		return Selection{}, fmt.Errorf("lang: python selection evidence is not a %s document (format %q)", pySelectionFormat, rep.Format)
	}
	var files pySelectionEvidenceFiles
	if err := json.Unmarshal([]byte(payload), &files); err != nil {
		return Selection{}, fmt.Errorf("lang: unparseable %s files: %w", pySelectionFormat, err)
	}
	rep.Files = files.Files
	if rep.Files == nil {
		return Selection{}, fmt.Errorf("lang: python selection evidence has no files — the suite most likely never ran")
	}

	root := normalizePyRepoRoot(repoRoot)
	want := filepath.ToSlash(codePath)
	wantTest := filepath.ToSlash(testPath)
	var mine map[string]bool
	var lines map[string][]LineRange
	var static []LineRange
	sawTest := false
	for path, f := range rep.Files {
		p, ok := alignPyPath(path, root)
		if !ok {
			continue
		}
		if wantTest != "" && p == wantTest {
			sawTest = true
		}
		if p == want {
			mine = map[string]bool{} // measured; executed by these tests, or by none
			for _, id := range f.Tests {
				mine[id] = true
			}
			lines = map[string][]LineRange{}
			for idxStr, rngs := range f.Lines {
				idx, err := strconv.Atoi(idxStr)
				if err != nil || idx < 0 || idx >= len(f.Tests) {
					return Selection{}, fmt.Errorf("lang: %s evidence for %s has a lines index %q out of range for %d tests", pySelectionFormat, path, idxStr, len(f.Tests))
				}
				id := f.Tests[idx]
				for _, r := range rngs {
					lines[id] = append(lines[id], LineRange{Start: r[0], End: r[1]})
				}
			}
			for _, r := range f.Static {
				static = append(static, LineRange{Start: r[0], End: r[1]})
			}
		}
	}
	if mine == nil {
		// Absent from the report. coverage only lists files the suite
		// imported, so absence means no test executed it — PROVIDED the
		// suite actually ran the test meant to cover it. Without that
		// evidence, "uncovered" would accuse a file whose test was simply
		// filtered out or failed to collect.
		if !sawTest {
			return Selection{}, fmt.Errorf("lang: python selection evidence never saw %s or its paired test %q — did the suite run it?", codePath, testPath)
		}
		mine = map[string]bool{}
	}
	sel := Selection{Method: "coverage-context", Of: rep.Tests, Base: stripPyCollectionTargets(testCmd, root), Lines: lines, Static: static}
	if len(mine) == 0 {
		return sel, nil
	}
	ids := make([]string, 0, len(mine))
	for id := range mine {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	ids, collapsed := collapseToFilesIfTooLong(ids)
	if collapsed {
		// The line evidence is keyed by NODE ID, and the collapse just
		// discarded every node id in favour of the containing files. Keeping
		// it would leave ForSpan looking up ids that are no longer in Tests,
		// matching nothing, and labelling every mutant "unreached" — a
		// positive claim that no test reaches those lines, for a file that
		// was not narrowed at all. Evidence in a shape that cannot be
		// narrowed is no line evidence: dropping it reproduces the per-file
		// behaviour byte for byte.
		sel.Lines, sel.Static = nil, nil
	}
	sel.Tests = ids
	sel.Cmd = append(append([]string{}, sel.Base...), ids...)
	return sel, nil
}

// collapseToFilesIfTooLong collapses ids (sorted node ids) to their
// containing files when the sorted argv would exceed selectionMaxArgv —
// still evidence-derived, just a coarser (superset) selection than the
// individual node ids would give. A no-op when ids already fits.
//
// The bool says whether the collapse HAPPENED, because it invalidates
// anything else keyed by node id (see Select).
func collapseToFilesIfTooLong(ids []string) ([]string, bool) {
	if argvLen(ids) <= selectionMaxArgv {
		return ids, false
	}
	files := map[string]bool{}
	for _, id := range ids {
		files[strings.SplitN(id, "::", 2)[0]] = true
	}
	out := make([]string, 0, len(files))
	for f := range files {
		out = append(out, f)
	}
	sort.Strings(out)
	return out, true
}

// ForSpan narrows sel to the tests whose recorded coverage reaches span. See
// TestSelector.ForSpan and SpanRule for the contract.
func (pyPlugin) ForSpan(sel Selection, span LineRange) ([]string, []string, string) {
	file := func(rule string) ([]string, []string, string) {
		return append([]string{}, sel.Cmd...), append([]string{}, sel.Tests...), rule
	}
	// Never "unreached" for evidence that cannot be narrowed at all: a
	// selection whose Tests were collapsed to files still carries Lines
	// keyed by node id, and every lookup below would miss. See
	// Selection.NarrowableByLine.
	if span.IsZero() || !sel.NarrowableByLine() {
		return file(SpanRuleFile)
	}
	for _, s := range sel.Static {
		if s.Overlaps(span) {
			return file(SpanRuleStatic)
		}
	}
	var ids []string
	for _, id := range sel.Tests {
		for _, r := range sel.Lines[id] {
			if r.Overlaps(span) {
				ids = append(ids, id)
				break
			}
		}
	}
	if len(ids) == 0 {
		return file(SpanRuleUnreached)
	}
	sort.Strings(ids)
	ids, _ = collapseToFilesIfTooLong(ids)
	base := sel.Base
	if base == nil {
		// Cmd is Base + Tests by construction. A Selection that does not
		// hold that runs the file selection rather than slicing a command
		// apart on an assumption it just failed.
		if len(sel.Cmd) < len(sel.Tests) {
			return file(SpanRuleFile)
		}
		base = sel.Cmd[:len(sel.Cmd)-len(sel.Tests)]
	}
	return append(append([]string{}, base...), ids...), ids, SpanRuleLines
}

// pyValueOptions are the pytest options that take their value as a SEPARATE
// argv word. The word after one of these is that option's value, never a
// collection target — `pytest --maxfail 3` names no path called "3", and
// stripping it would change the run. A token containing `=` carries its own
// value (`--maxfail=3`) and consumes nothing.
//
// The list is explicit rather than a heuristic ("a bare word after any
// flag") for the reason the whole feature exists: guessing wrong here either
// leaves the suite unnarrowed (silently grading the wrong thing) or eats an
// option's value (changing what the operator asked to run). Options that
// take NO value — -x, -q, -s, -v — are deliberately absent, so the word
// after them is examined like any other.
var pyValueOptions = map[string]bool{
	"-k": true, "-m": true, "-p": true, "-c": true, "-o": true, "-W": true,
	"-n": true, "-r": true,
	"--maxfail": true, "--rootdir": true, "--confcutdir": true,
	"--basetemp": true, "--junitxml": true, "--log-level": true,
	"--log-file": true, "--import-mode": true, "--deselect": true,
	"--ignore": true, "--ignore-glob": true, "--override-ini": true,
	"--tb": true, "--durations": true, "--cov": true, "--cov-config": true,
	"--cov-report": true, "--cov-context": true, "--cov-fail-under": true,
}

// stripPyCollectionTargets returns testCmd with its collection targets
// removed, so appended node ids actually NARROW the run instead of joining a
// union. pytest treats positional arguments as a union of collection roots:
// `pytest tests/ tests/test_a.py::test_x` runs all of tests/. The Action's
// required test-command and corral's own not-collected advice both recommend
// exactly the `pytest tests/` shape, so on the most common invocation nothing
// would be narrowed while every record claimed coverage-context.
//
// A token is a collection target — and only then is it removed — when ALL of:
//
//   - it does not start with `-` (options stay, always);
//   - it is not the separate value of the preceding option (pyValueOptions);
//   - it contains `::` (a node id can be nothing else) OR names a path that
//     exists under repoRoot.
//
// A positional token that is NOT an existing path is LEFT ALONE: it survived
// the evidence run, so pytest accepted it as something other than a missing
// path (an ini-configured value, a plugin's own positional), and removing it
// would change a command that demonstrably works. Index 0 is the program
// itself and is never examined.
//
// repoRoot may be "" (a caller with no checkout — the recorded-evidence and
// hosted paths): existence cannot then be tested, so only `::` tokens are
// recognised, which is the fail-open direction — the run stays exactly as
// wide as it is today rather than losing a target nothing verified.
func stripPyCollectionTargets(testCmd []string, repoRoot string) []string {
	if len(testCmd) == 0 {
		return nil
	}
	out := make([]string, 0, len(testCmd))
	out = append(out, testCmd[0])
	for i := 1; i < len(testCmd); i++ {
		tok := testCmd[i]
		if strings.HasPrefix(tok, "-") {
			out = append(out, tok)
			continue
		}
		prev := testCmd[i-1]
		if strings.HasPrefix(prev, "-") && !strings.Contains(prev, "=") && pyValueOptions[prev] {
			out = append(out, tok)
			continue
		}
		if isPyCollectionTarget(tok, repoRoot) {
			continue
		}
		out = append(out, tok)
	}
	return out
}

// isPyCollectionTarget reports whether tok names tests to collect: a node id
// (`path::selector`), or an existing file/directory under repoRoot. The path
// is resolved against repoRoot, never the process's own cwd, and an absolute
// or escaping token is refused rather than statted — a scan confines every
// read to the checkout it was pointed at.
func isPyCollectionTarget(tok, repoRoot string) bool {
	if tok == "" {
		return false
	}
	if strings.Contains(tok, "::") {
		return true
	}
	if repoRoot == "" || filepath.IsAbs(tok) {
		return false
	}
	rel := filepath.Clean(filepath.FromSlash(tok))
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return false
	}
	_, err := os.Stat(filepath.Join(repoRoot, rel))
	return err == nil
}

// WithAuthoredTest appends the authored test's path so pytest collects it
// alongside the selection; with an empty selection it runs the authored
// test alone — the uncovered case, where the pool's test is the only test.
//
// "Alone" has to mean alone: the uncovered branch builds on sel.Base (the
// operator's command with its collection targets stripped), not on the raw
// testCmd, or `pytest tests/` + the authored path would collect all of
// tests/ and grade a file the evidence says nothing executes against the
// entire suite. sel.Base is nil only for a Selection this plugin did not
// compute, where testCmd is all there is.
func (pyPlugin) WithAuthoredTest(sel Selection, testCmd []string, authoredTestPath string) []string {
	base := sel.Cmd
	if len(sel.Tests) == 0 {
		base = testCmd
		if sel.Base != nil {
			base = sel.Base
		}
	}
	return append(append([]string{}, base...), authoredTestPath)
}

// argvLen approximates the length of a command line carrying args, as one
// space-joined string — the metric selectionMaxArgv bounds.
func argvLen(args []string) int {
	n := 0
	for _, a := range args {
		n += len(a) + 1
	}
	return n
}

func lastNonEmptyLine(b []byte) string {
	lines := strings.Split(string(b), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			return lines[i]
		}
	}
	return ""
}

// alignPyPath is ParseCoverage's absolute-path rule, shared: a relative
// report path is taken as repo-relative; an absolute one is aligned under
// root, and dropped (ok=false) when it lies outside it.
func alignPyPath(path, root string) (string, bool) {
	p := filepath.ToSlash(path)
	if !filepath.IsAbs(path) {
		return p, true
	}
	if root == "" {
		return "", false
	}
	clean := filepath.ToSlash(filepath.Clean(path))
	prefix := root + "/"
	if root == "/" {
		prefix = "/"
	}
	rel, cut := strings.CutPrefix(clean, prefix)
	if !cut || rel == "" || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", false
	}
	return rel, true
}
