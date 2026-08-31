// SPDX-License-Identifier: Elastic-2.0

// Package lang is the language-plugin seam for corral's adversarial audit
// gate. A Plugin owns everything language-specific about grading one
// self-contained source file + its test suite: the jail workspace scaffold,
// the test command, the compile/type-check, the test-file naming convention,
// extension-based detection, a toolchain preflight, and the per-language LLM
// system prompts. Everything else in the gate is language-neutral.
package lang

import "strings"

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
	// docs/design/test-selection.md's "Part B" for the measured mechanism: CPython
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

// TreeEnver is an OPTIONAL plugin capability: the environment one PRIVATE
// TREE of a concurrent workspace pool must run under
// (internal/adequacy.WorkspacePool). A plugin that does not implement it gets
// no tree env at all, which is correct for every language whose toolchain
// neither resolves imports through an absolute path recorded elsewhere nor
// fans out over the whole box on its own.
//
// It is separate from Plugin (rather than another method on it) because it is
// a property of ONE substrate's ONE mode — N copies of a checkout — and every
// plugin would otherwise carry a no-op for it.
//
// tree is that tree's root; cores is the share of the box THIS tree may use
// (the pool divides, it does not multiply: N trees each assuming all cores
// thrash the machine and turn the concurrency probe into a false negative).
// The returned "VAR=value" assignments are applied to every run in that tree,
// BEFORE Plugin.WorkspaceRunEnv's, so a plugin's per-run env can still
// override a tree-derived value.
//
// The two implementations, and why only they:
//
//   - Python (python.go): a tree is a COPY, and an editable install's .pth
//     points at the ORIGINAL checkout. Without the tree's own root on
//     PYTHONPATH the suite imports unmutated source and every mutant
//     "survives" — the false-accusation shape this codebase has killed
//     repeatedly. PYTHONPATH entries precede .pth entries, which is what
//     makes this work at all.
//   - Go (go.go): `go test` builds and runs packages in parallel by itself,
//     so N trees each using every core is N-times oversubscribed.
type TreeEnver interface {
	TreeEnv(tree string, cores int) []string
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

// defaultPreambleLines is how many leading lines PreambleFor contributes for
// a plugin with no Preambler implementation of its own — generous over any
// real import block on purpose: guessing too much only costs a harmless
// extra line count, guessing too little risks losing the file's actual
// header (the package/imports a shard's sliced view needs to still read as
// plausible code).
const defaultPreambleLines = 40

// Preambler is an OPTIONAL plugin extension: a language whose package/import
// header corral can reliably locate returns it verbatim, so a mutant-
// generator shard shown only ITS OWN symbols (see advpool's shard-chunking)
// still has the names its body actually references. Deliberately optional —
// a plugin with no implementation is not wrong, just less precise, and
// PreambleFor's line-count fallback is a guess that is usually right and
// never wrong in a way that breaks anchoring: the preamble is CONTEXT, never
// a SEARCH-anchor target.
type Preambler interface {
	// Preamble returns code's leading package/import block, VERBATIM — a
	// literal prefix of code's own lines, never rewritten or reformatted —
	// so prepending it to a shard's slice cannot alter anchor bytes found
	// elsewhere in the file.
	Preamble(code string) string
}

// PreambleFor returns p's package/import header for code: p.Preamble(code)
// when p implements Preambler, else the file's first defaultPreambleLines
// lines. The one call site outside this package that needs a plugin's
// preamble without knowing whether it implements the optional interface.
func PreambleFor(p Plugin, code string) string {
	if pr, ok := p.(Preambler); ok {
		return pr.Preamble(code)
	}
	lines := strings.Split(code, "\n")
	if len(lines) <= defaultPreambleLines {
		return code
	}
	return strings.Join(lines[:defaultPreambleLines], "\n")
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
	// Base is the operator's own command with its COLLECTION TARGETS
	// removed — the prefix Cmd (and WithAuthoredTest) append to. It exists
	// because appending is not narrowing on a runner that unions positional
	// arguments: `pytest tests/ tests/test_a.py::test_x` collects all of
	// tests/, so a selection appended to the common `-- pytest tests/`
	// shape would run the whole suite while the verdict, the ledger, the
	// warehouse, the attestation and the cache key all said
	// "coverage-context". Options and their values survive; only tokens the
	// evidence run proves are collection targets are dropped. nil means the
	// plugin computed no base (e.g. a zero Selection) and a caller falls
	// back to the command it already had.
	Base     []string
	Cmd      []string
	Tests    []string
	Method   string
	Of       int
	Fallback string
	// Lines, when the evidence carried them, maps each selected test to the
	// ranges of the audited file's lines it executed; Static is the file's
	// lines executed under no test context (import time). Both nil for a
	// whole-suite selection or evidence that did not record lines. They are
	// what ForSpan narrows by.
	Lines  map[string][]LineRange
	Static []LineRange
}

// NarrowableByLine reports whether the Selection's line evidence can actually
// narrow the tests it will run: at least one of the tests that WILL run has
// recorded lines. It is false for evidence that carried no lines at all, and
// — the case that made it a method rather than a `len(Lines) > 0` check at
// each caller — for a selection whose Tests were collapsed to containing
// FILES while Lines stayed keyed by node id. Looking a file path up in a
// node-id map misses every time, which reads as "no test reaches this span"
// when the truth is "this evidence cannot be narrowed".
func (s Selection) NarrowableByLine() bool {
	if len(s.Lines) == 0 || len(s.Tests) == 0 {
		return false
	}
	for _, t := range s.Tests {
		if len(s.Lines[t]) > 0 {
			return true
		}
	}
	return false
}

// LineRange is a closed, 1-based range of source lines.
type LineRange struct{ Start, End int }

// IsZero reports the zero LineRange, which Overlaps treats as never
// overlapping anything.
func (r LineRange) IsZero() bool { return r.Start == 0 && r.End == 0 }

// Overlaps reports whether r and o share at least one line. Neither range
// overlaps anything when either is the zero LineRange.
func (r LineRange) Overlaps(o LineRange) bool {
	return !r.IsZero() && !o.IsZero() && r.Start <= o.End && o.Start <= r.End
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
// note docs/design/test-selection.md
// records the filename-based scoping that inverted a verdict (1.00 → 0.00)
// and why only execution evidence is acceptable here.
type TestSelector interface {
	// Instrument returns the command that produces selection evidence for
	// the whole suite in ONE run, derived from the operator's own testCmd so
	// their markers and flags are honoured. ok=false: this command cannot be
	// instrumented — the caller grades whole-suite, disclosed.
	Instrument(testCmd []string) (cmd []string, ok bool)
	// Select reads that run's output and returns the narrowed command for
	// codePath plus what it selected. It also returns Base — testCmd with
	// the operator's own collection targets removed, resolved against
	// repoRoot — because on a runner that UNIONS positional arguments
	// (pytest does) appending to the raw command narrows nothing at all.
	// testPath is the file's paired test:
	// a codePath ABSENT from the evidence is uncovered only when testPath is
	// present (the suite ran the tests meant to cover it); absent both, the
	// suite may never have run that test, and Select errors — the caller
	// grades whole-suite and discloses the error text.
	Select(evidence []byte, repoRoot, codePath, testPath string, testCmd []string) (Selection, error)
	// WithAuthoredTest returns the command for the POOL pass: the selection
	// plus the pool's authored test at authoredTestPath, so the authored
	// test is collected even though no evidence run ever saw it. When
	// sel.Tests is empty the result runs the authored test alone — built on
	// sel.Base when the Selection carries one, so "alone" is true rather
	// than "alongside whatever the operator's targets collect".
	WithAuthoredTest(sel Selection, testCmd []string, authoredTestPath string) []string
	// ForSpan narrows sel to the tests whose recorded coverage reaches span.
	// It never returns an empty command: the fallbacks all run the file's
	// selection, because corral reports what it ran, not what coverage
	// predicted. Only meaningful when len(sel.Tests) > 0.
	ForSpan(sel Selection, span LineRange) (cmd []string, tests []string, rule string)
}

// SpanRule names why ForSpan chose what it chose.
const (
	SpanRuleLines     = "lines"     // a strict subset reaches the span
	SpanRuleStatic    = "static"    // the span touches an import-time line: the whole file selection
	SpanRuleUnreached = "unreached" // no test reaches the span: the whole file selection runs anyway
	SpanRuleFile      = "file"      // no span, or no line evidence: today's behaviour
)

// FailureParser is an OPTIONAL plugin extension that names the FIRST test the
// runner reported as failing, read out of that runner's own output.
//
// It exists to answer, for a killed mutant, "which test was awake" — a
// question the scorer could never answer because Jail.RunTest only ever
// returned a bool. The answer is recorded (scan_mutants.killed_by) so a later
// reader can see which tests are actually doing the catching, and which parts
// of a suite have never caught anything.
//
// BEST-EFFORT, NEVER GUESSED. The id must be lifted verbatim from the
// output's own summary. An output with no summary — a passing run, a build
// failure, a runner corral does not understand — returns "", and the column
// is stored as NULL. A fabricated or inferred id would name a test that never
// ran, in a record whose whole product is that its claims are checkable.
//
// Deliberately unimplemented for the languages whose runners corral cannot
// parse precisely (ruby, javascript, typescript), for the same reason
// FailureDeselector is: a wrong id is worse than no id.
type FailureParser interface {
	// FirstFailure returns the first failing test's id, or "" when the output
	// names none.
	FirstFailure(output []byte) string
}
