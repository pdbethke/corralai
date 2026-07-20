// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"path/filepath"
	"strings"
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

// CompileCheck syntax-checks BOTH files with `ruby -c` (offline); the `&&` is
// honored because the jail executes the joined command under `sh -c`.
func (rubyPlugin) CompileCheck(codePath, testPath string) []string {
	return []string{"ruby", "-c", codePath, "&&", "ruby", "-c", testPath}
}

// TestPath is framework-neutral: pkg/foo.rb -> pkg/foo_test.rb (content, not the
// name, selects minitest vs rspec at run time).
func (rubyPlugin) TestPath(codePath string) string {
	ext := filepath.Ext(codePath)
	base := strings.TrimSuffix(codePath, ext)
	dir := filepath.Dir(codePath)
	if dir == "." {
		return base + "_test.rb"
	}
	return filepath.Join(dir, filepath.Base(base)+"_test.rb")
}

// Preflight requires only `ruby` (minitest is bundled). It deliberately does
// NOT require the rspec gem: only a run whose DEV suite is RSpec needs it, and a
// missing gem then fails the jail command — fail-closed, never a false pass.
func (rubyPlugin) Preflight() error { return toolOnPath("ruby") }

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

func (rubyPlugin) SingleTestCmd(testPath, selector string) ([]string, bool) { return nil, false }

func (rubyPlugin) ListTestsCmd(string) ([]string, bool) { return nil, false }

func (rubyPlugin) ParseTestList(string) []string { return nil }
