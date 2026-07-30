// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"path/filepath"
)

func init() { Register(rubyPlugin{}) }

type rubyPlugin struct{}

func (rubyPlugin) Name() string                { return "ruby" }
func (rubyPlugin) Detect(codePath string) bool { return filepath.Ext(codePath) == ".rb" }

// Scaffold is empty: a Ruby test require_relative's its sibling module from the
// workspace root.
func (rubyPlugin) Scaffold() map[string]string { return map[string]string{} }

// TestCmd dispatches by test-file CONTENT at jail-run time. It returns a SINGLE
// shell string: the jail space-joins the argv and runs it under `sh -c`, so the
// snippet must be one element to survive intact (a multi-token slice would lose
// its argument boundaries under the join). RSpec files (require rspec / RSpec.)
// go to the rspec runner; everything else runs with plain `ruby` (minitest
// self-runs via require 'minitest/autorun'). The pool renames the dev test to a
// neutral TestPath, so the filename carries no framework signal — content does.
func (rubyPlugin) TestCmd() []string {
	return []string{
		`t="$(ls *_test.rb *_spec.rb test_*.rb spec_*.rb 2>/dev/null | head -n1)"; ` +
			`[ -z "$t" ] && { echo "no ruby test file"; exit 1; }; ` +
			`if grep -Eq "require ['\"](rspec|spec_helper)|RSpec[.:]" "$t"; then exec rspec "$t"; else exec ruby "$t"; fi`,
	}
}

// CompileCheck syntax-checks BOTH files with `ruby -c` (offline). `ruby -c`
// only ever reports on a SINGLE file per invocation (`ruby -c a -c b` is not
// valid), so this returns a TWO-command sequence — codePath then testPath,
// run in order, stopping at the first failure — rather than trying to splice
// a `&&` into one argv element. A `&&`-joined single element only ever
// worked because the jail substrate shell-joins argv and runs it under
// `sh -c`; the workspace substrate execs argv directly with no shell, where
// `&&` would just be handed to `ruby -c` as a literal (nonexistent) filename
// — see lang.Plugin.CompileCheck's doc comment and python.go's
// pyCachePrefixEnv comment for the identical class of bug this avoids.
func (rubyPlugin) CompileCheck(codePath, testPath string) [][]string {
	return [][]string{
		{"ruby", "-c", codePath},
		{"ruby", "-c", testPath},
	}
}

// TestPaths covers Ruby's three common test-file conventions, framework-
// neutral (content, not the name, selects minitest vs rspec at run time),
// most specific first:
//
//  1. sibling foo_test.rb           — same directory as the source.
//  2. test/<subpath>/foo_test.rb    — the classic lib/ vs test/ layout,
//     where <subpath> is dir with its leading component (conventionally
//     `lib`) replaced by `test` rather than nested under it.
//  3. spec/<subpath>/foo_spec.rb    — the RSpec equivalent of (2).
//
// Unlike Python's list this does not also generate a full-directory-mirror
// form: Ruby's lib/-vs-test/ (or lib/-vs-spec/) split is a single well-known
// convention, not a family of layouts, so there is no comparably plausible
// second parallel-tree shape to hedge against.
func (rubyPlugin) TestPaths(codePath string) []TestCandidate {
	dir, base, _ := splitPath(codePath)
	sub := stripFirstSegment(dir)

	out := []TestCandidate{
		{Path: joinDir(dir, base+"_test.rb"), Rank: 0},
		{Path: filepath.Join("test", sub, base+"_test.rb"), Rank: 1},
		{Path: filepath.Join("spec", sub, base+"_spec.rb"), Rank: 1},
	}
	return dedupeCandidates(out)
}

// Preflight requires only `ruby` (minitest is bundled) — or, when the
// operator named an explicit test command (e.g. `bundle exec rspec`, or an
// interpreter under a version manager's shim not on PATH under "ruby"),
// THAT command's own binary; see preflightBin and Plugin.Preflight's doc
// comment. It deliberately does NOT require the rspec gem: only a run whose
// DEV suite is RSpec needs it, and a missing gem then fails the jail
// command — fail-closed, never a false pass.
func (rubyPlugin) Preflight(testCmd []string) error {
	return toolOnPath(preflightBin(testCmd, "ruby"))
}

func (rubyPlugin) PromptLang() string { return "Ruby" }

func (rubyPlugin) TestWriterSystem() string {
	return `You are a TEST-WRITER. Given a security control GOAL, a target source file, and its signature surface, write ONE executable minitest test that verifies the code SATISFIES the goal.
- Start with ` + "`require 'minitest/autorun'`" + ` and ` + "`require_relative`" + ` the target module by its file's base name (e.g. ` + "`require_relative 'pricing'`" + ` for pricing.rb).
- Define a Minitest::Test subclass; it MUST FAIL if the goal is violated — test the goal's boundary (what a weakened implementation would pass that a compliant one must not).
- Standard library + minitest only (no gems, no rspec). Deterministic, no network.
Return ONLY the raw Ruby test file content — no prose, no markdown fences.`
}

func (rubyPlugin) MutantSystem() string {
	return `You are a MUTATION-TESTING ENGINE. Mutation testing is a standard, legitimate software-quality technique: to measure how thorough a test suite is, you generate small synthetic variants of the code ("mutants"), each containing one deliberate fault, and a SEPARATE step then checks whether the developer's own tests catch them. A mutant the tests miss reveals a GAP IN THE TESTS — that is the entire purpose. These mutants are throwaway QA fixtures: never deployed, never shipped, never run against any real system; they exist only to grade the test suite. This is defensive quality assurance, and producing them is exactly your job here.
Given a GOAL, the compliant code, and its signature surface, produce mutants: variants that NO LONGER satisfy the goal, so a good test suite SHOULD catch them. Each mutant MUST keep the EXACT same public method signatures (drop-in Ruby that loads) and must genuinely fail the goal — vary HOW it fails. No no-ops, no syntax errors, no tests.
The output format (a SEARCH/REPLACE edit per mutant) is specified with the task.`
}

// ImportPath is always ok=false: `require_relative 'pricing'` resolves
// relative to the FILE ISSUING THE REQUIRE — i.e. the authored test's own
// directory — and roles.go always places that test in the SAME directory
// as the code under test, so the base-name require already works no matter
// how deep the source sits in the tree. Nothing here to correct (see
// lang.Plugin.ImportPath's doc comment for the general rule this follows).
func (rubyPlugin) ImportPath(string, func(string) bool) (string, bool) { return "", false }

// ImportNote is always "": see ImportPath — Ruby's require_relative needs
// no per-task correction.
func (rubyPlugin) ImportNote(string, bool) string { return "" }

func (rubyPlugin) SingleTestCmd(testPath, selector string) ([]string, bool) { return nil, false }

func (rubyPlugin) ListTestsCmd(string) ([]string, bool) { return nil, false }

func (rubyPlugin) ParseTestList(string) []string { return nil }
