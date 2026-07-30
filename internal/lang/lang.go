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
	Preflight() error         // toolchain present? nil ok, else fail CLOSED
	PromptLang() string       // human label, for verdict metadata + logs
	TestWriterSystem() string // language-specific test-writer system prompt
	MutantSystem() string     // language-specific mutant-generator system prompt
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
