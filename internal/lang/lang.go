// SPDX-License-Identifier: Elastic-2.0

// Package lang is the language-plugin seam for corral's adversarial audit
// gate. A Plugin owns everything language-specific about grading one
// self-contained source file + its test suite: the jail workspace scaffold,
// the test command, the compile/type-check, the test-file naming convention,
// extension-based detection, a toolchain preflight, and the per-language LLM
// system prompts. Everything else in the gate is language-neutral.
package lang

// TestCandidate is one plausible test-file location for a source file,
// carrying its evidentiary Rank: how much real directory context the
// candidate's own construction encodes, NOT its position in the returned
// slice. Rank is what a cross-source ambiguity check (reposcan's
// demoteAmbiguousPairings) compares — a plain slice index would conflate "how
// specific is this match" with "how many earlier candidates happened to
// collapse onto the same string for THIS source", which are different
// things: a zero-directory-evidence match (e.g. Python's flat
// tests/test_foo.py) can arise from the mirror, stripped, OR flat form
// depending on how shallow the source is, and MUST rank identically
// (as the least specific of whichever forms produced it) no matter which one
// it was, or two equally-uninformative matches from different-depth sources
// would never tie and the safer "demote both" outcome would never fire. See
// dedupeCandidates, which enforces exactly that attribution when candidates
// collapse.
//
// Rank is comparable ACROSS plugins (all start at 0 = sibling, the most
// specific a plugin can offer) but is otherwise plugin-defined; reposcan
// only ever compares ranks between candidates for the SAME resolved
// TestPath, never across different paths.
type TestCandidate struct {
	Path string
	Rank int
}

// Plugin is everything the audit gate needs to grade one self-contained
// source file + its test suite in a given language.
type Plugin interface {
	Name() string                // "go", "python"
	Detect(codePath string) bool // by file extension
	Scaffold() map[string]string // base workspace files (go.mod / none)
	TestCmd() []string           // default recursive test command
	// CompileCheck returns a SEQUENCE of commands that together syntax/type
	// check the authored test: run each in order, stop at the first
	// non-zero exit (i.e. treat the sequence as chained by `&&`), and
	// report an overall pass only if every command exits 0. Most plugins
	// return exactly one command (their checker can look at both files —
	// or the whole workspace — in a single invocation). Plugins whose
	// checker only accepts ONE file per invocation (ruby -c, node --check)
	// return one command per file instead of trying to splice `&&` into a
	// single argv element: the caller may run this sequence via a bare
	// exec.Command with no shell at all (the workspace substrate —
	// internal/adequacy/workspace.go — execs argv directly), so nothing
	// here may depend on shell interpretation of `&&`, `;`, or any other
	// control operator. An empty sequence is NEVER valid: every registered
	// plugin has at least one real check to run, and a caller (see
	// advpool.JailValidator.CompileTest) treats an empty sequence as an
	// ERROR, not as "nothing to check, therefore compiles" — a validation
	// gate that silently reports "compiles" without invoking a single
	// command is exactly the failure class this type exists to prevent.
	CompileCheck(codePath, testPath string) [][]string
	// TestPaths returns the plausible test-file locations for codePath,
	// ordered most specific (least likely to accidentally match a DIFFERENT
	// source file's test) first, each carrying its evidentiary Rank (see
	// TestCandidate). A caller that wants "the" conventional test path —
	// e.g. to name a freshly-authored test — uses TestPaths(codePath)[0].Path,
	// which is always the same sibling convention the old single-valued
	// TestPath used to return. A caller PAIRING against an existing repo
	// (reposcan) walks the whole list and takes the first entry that exists
	// on disk, using its Rank (not its position) to resolve cross-source
	// collisions.
	TestPaths(codePath string) []TestCandidate
	// Preflight checks the toolchain the jail is about to grade with — fail
	// CLOSED, never a guess. testCmd, when non-empty, is the OPERATOR's own
	// `-- <cmd>` (or advpool run spec TestCmd) argv: it is an assertion of
	// exactly how the suite runs, stronger evidence than any stock guess
	// this plugin could make about the host's default toolchain (e.g.
	// python3/python on PATH), and MUST be what gets checked — a project
	// living in a venv, a bundler-managed Ruby, or a locally-installed
	// node_modules toolchain is invisible to the stock guess but named
	// exactly by testCmd. testCmd == nil (or empty) means the caller has no
	// explicit command (e.g. certify --local with no --code test command
	// override); Preflight then falls back to its prior stock-toolchain
	// check, UNCHANGED from before this parameter existed.
	Preflight(testCmd []string) error
	PromptLang() string       // human label, for verdict metadata + logs
	TestWriterSystem() string // language-specific test-writer system prompt
	MutantSystem() string     // language-specific mutant-generator system prompt
	// ImportPath derives the importable module/package path for codePath —
	// the fact a white-box test needs to reference the code under test
	// correctly once it is no longer guaranteed to sit at a workspace root
	// (a real repo's file lives inside a real package tree, e.g.
	// src/flask/cli.py imports as flask.cli, not cli). It is PURE: codePath
	// and exists are the only inputs, exists is consulted (never a real
	// filesystem touched directly) to test whether a directory contains a
	// package marker (e.g. Python's __init__.py), so a caller can exercise
	// this with a fake exists function — no I/O, no jail, no live repo
	// needed. exists may be nil when the caller has no filesystem context
	// at all (e.g. a hosted/MCP run with no checkout on disk); a plugin
	// that needs exists to answer honestly MUST return ok=false rather than
	// guess when exists is nil, per ImportNote's fail-closed contract.
	//
	// ok=false also covers every language whose own test-authoring
	// convention already resolves correctly regardless of nesting — Go's
	// same-package white-box convention and JS/TS/Ruby's same-directory
	// relative import/require all need no correction, because the authored
	// test is always placed in the SAME directory as the code under test
	// (see roles.renderTestWriterWithRepair's shared "named" fact) — only
	// Python's `import <name>` is a NAMESPACE lookup rather than a
	// same-directory reference, so only pyPlugin.ImportPath ever computes a
	// real, non-trivial value. See each plugin's own ImportPath for why.
	ImportPath(codePath string, exists func(path string) bool) (string, bool)
	// ImportNote turns an ImportPath(codePath, exists) result into the
	// per-task instruction text telling the test-writer how to import the
	// code under test — "" when this language needs no such note (every
	// plugin except python; see ImportPath's doc comment for why). Called
	// with ok=false (importPath=="") this MUST say the import path could
	// not be determined, rather than silently asserting the (possibly
	// wrong) base-name convention — the whole point of this seam existing
	// (see internal/advpool/roles.go's renderTestWriterWithRepair).
	ImportNote(importPath string, ok bool) string
	// SingleTestCmd yields a command that runs exactly the one test named by
	// selector in testPath. ok=false when the language can't yet target a
	// single test — callers must treat that as "no auto-signal", never a pass.
	SingleTestCmd(testPath, selector string) (cmd []string, ok bool)
	// ListTestsCmd yields a command that ENUMERATES the individual tests in
	// testPath. ok=false when the language can't list tests yet — callers must
	// then skip the matrix, never assume an empty suite.
	ListTestsCmd(testPath string) (cmd []string, ok bool)
	// ParseTestList extracts SingleTestCmd-compatible selectors from the output
	// of ListTestsCmd, in emission order. Pure.
	ParseTestList(output string) []string
	// WorkspaceRunEnv returns extra "VAR=value" environment assignments to
	// apply to ONE scoring run (a single baseline, canary, mutant, or
	// authored-test invocation) on the WORKSPACE substrate — where the
	// runner mutates a REAL checkout in place across many runs, rather than
	// materializing a fresh, disposable temp directory per run the way the
	// bwrap jail's writeWorkspace does (internal/adequacy/jail.go). cleanup
	// releases whatever that env's values needed (e.g. a temp directory);
	// the caller MUST invoke it once that single run has finished, win or
	// lose.
	//
	// THE CALLER MUST INVOKE THIS FRESH BEFORE **EVERY** RUN, never once
	// for a whole audit and reused: internal/adequacy.WorkspaceRunner does
	// exactly that (once per applyRunRestore call). A value computed once
	// and shared across the baseline and its mutants does NOT close the
	// hole this method exists for — see python.go's implementation and
	// docs/superpowers (gitignored) for the measured mechanism: CPython
	// keys a persistent .pyc cache off a source file's (mtime_seconds,
	// size), and the workspace substrate can rewrite a mutant to the exact
	// same path with the exact same size within the exact same wall-clock
	// second as the run that populated that cache — a stale-cache HIT that
	// silently skips re-executing the mutated code, reading as a phantom
	// "survivor" no matter how obviously wrong the mutant is. A cache
	// directory shared across calls (even a freshly-created one) still
	// lets a later same-second, same-size mutant hit the entry an earlier
	// call in the SAME audit left there; only a directory that is fresh
	// PER RUN closes it.
	//
	// Most plugins have nothing to add here (nil, a no-op cleanup): the
	// jail substrate is immune by construction (a fresh MkdirTemp workspace
	// per run means there is never a pre-existing cache entry to alias
	// against), and only a language whose toolchain keeps a PERSISTENT,
	// content-addressed-by-mtime-and-size cache next to the source it
	// compiles is exposed on the workspace substrate at all — currently
	// only Python's own __pycache__. Ruby's compiler has no such cache by
	// default. JS/TS bundlers and `ts-node` DO cache compiled output
	// keyed off source metadata in some configurations, but closing that
	// is out of scope here — see typescript.go's own note.
	WorkspaceRunEnv() (env []string, cleanup func())
}

var registry = map[string]Plugin{}

// Register adds a plugin to the registry. Called from plugin files' init().
func Register(p Plugin) { registry[p.Name()] = p }

// ByName resolves a plugin by its language name. Fail-closed: (nil,false)
// for anything not registered.
func ByName(name string) (Plugin, bool) {
	p, ok := registry[name]
	return p, ok
}

// Detect resolves a plugin by the code file's extension. Fail-closed:
// (nil,false) if no registered plugin claims the path.
func Detect(codePath string) (Plugin, bool) {
	for _, p := range registry {
		if p.Detect(codePath) {
			return p, true
		}
	}
	return nil, false
}

// FailureDeselector is an OPTIONAL plugin extension for salvaging a partially
// broken authored test. The compliant check is all-or-nothing per FILE: if any
// test in the authored file fails against the unmutated code, the whole file
// is discarded and nothing is scored.
//
// Measured cost of that, on the first authored test corral ever retained
// (gemini-3.6-flash on pallets/flask, 2026-07-31): 13 tests, TEN PASSED, and
// all 13 were thrown away because 3 carried wrong API assumptions.
//
// Asking the model to repair itself is one answer and depends on the model
// being able to. Deselecting the failures and scoring with the remainder does
// not depend on the model at all — it is arithmetic over the runner's own
// output.
//
// Deliberately OPTIONAL, and deliberately unimplemented for languages whose
// runners corral cannot parse failures from precisely. A wrong selector would
// deselect the wrong test and silently narrow the exam, which is worse than
// not salvaging: the run would look healthier while proving less.
type FailureDeselector interface {
	// FailedTests extracts the runner's own failing-test selectors from its
	// output. Empty when nothing failed or nothing could be parsed.
	FailedTests(output string) []string
	// DeselectArgs renders those selectors as arguments appended to the
	// project's own test command. Empty for an empty selector list — never a
	// bare flag with no argument.
	DeselectArgs(selectors []string) []string
}

// Selection is what one TestSelector.Select call decided for one source
// file: the tests that EXECUTED it, as evidence from a run, and the narrowed
// command that runs just those.
//
// Empty Tests with Method set means the evidence ran and no test executed
// the file — a finding (the file is uncovered), not a failure. Fallback is
// non-empty exactly when the whole suite must run instead, and says why; a
// caller must surface it — a whole-suite grade under selection is a
// different measurement and the record must say which one it is.
type Selection struct {
	Cmd      []string
	Tests    []string
	Method   string
	Of       int
	Fallback string
}

// TestSelector is an OPTIONAL plugin extension that narrows the project's
// own test command to the tests that EXECUTE a given source file, using
// evidence from a run — coverage contexts or the harness's module graph —
// never a filename convention.
//
// It exists for the cost model: scoring runs the command once per mutant,
// so an audit costs O(mutants × runtime of that command). It is also a
// MEASUREMENT change, deliberately: "do the tests FOR THIS FILE catch the
// bug?" is the question a per-file kill rate claims to answer. The design
// note docs/superpowers/specs/2026-08-28-coverage-guided-selection-and-concurrent-scoring-design.md
// records the filename-based scoping that inverted a verdict (1.00 → 0.00)
// and why only execution evidence is acceptable here.
type TestSelector interface {
	// Instrument returns the command that produces selection evidence for
	// the whole suite in ONE run, derived from the operator's own testCmd so
	// their markers and flags are honoured. ok=false: this command cannot be
	// instrumented — the caller grades whole-suite, disclosed.
	Instrument(testCmd []string) (cmd []string, ok bool)
	// Select reads that run's output and returns the narrowed command for
	// codePath plus what it selected. testPath is the file's paired test:
	// a codePath ABSENT from the evidence is uncovered only when testPath is
	// present (the suite ran the tests meant to cover it); absent both, the
	// suite may never have run that test, and Select errors — the caller
	// grades whole-suite and discloses the error text.
	Select(evidence []byte, repoRoot, codePath, testPath string, testCmd []string) (Selection, error)
	// WithAuthoredTest returns the command for the POOL pass: the selection
	// plus the pool's authored test at authoredTestPath, so the authored
	// test is collected even though no evidence run ever saw it. When
	// sel.Tests is empty the result runs the authored test alone.
	WithAuthoredTest(sel Selection, testCmd []string, authoredTestPath string) []string
}
