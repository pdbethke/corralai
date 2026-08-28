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
func (jsPlugin) CompileCheck(codePath, testPath string) [][]string {
	cmds := [][]string{
		{"node", "--check", codePath},
		{"node", "--check", testPath},
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
func jsUndefCheck(codePath, testPath string) []string {
	var haveConfig bool
	for _, c := range jsLintConfigs {
		if _, err := os.Stat(c); err == nil {
			haveConfig = true
			break
		}
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
