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
	Name() string                                    // "go", "python"
	Detect(codePath string) bool                     // by file extension
	Scaffold() map[string]string                     // base workspace files (go.mod / none)
	TestCmd() []string                               // default recursive test command
	CompileCheck(codePath, testPath string) []string // syntax/type check for the authored test
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
