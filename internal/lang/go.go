// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"path/filepath"
	"strings"
)

func init() { Register(goPlugin{}) }

type goPlugin struct{}

func (goPlugin) Name() string                { return "go" }
func (goPlugin) Detect(codePath string) bool { return filepath.Ext(codePath) == ".go" }

func (goPlugin) Scaffold() map[string]string {
	return map[string]string{"go.mod": "module control\ngo 1.26\n"}
}

func (goPlugin) TestCmd() []string { return []string{"go", "test", "./..."} }

func (goPlugin) CompileCheck(_, _ string) []string { return []string{"go", "vet", "./..."} }

// TestPath mirrors the prior advPoolTestPath: same base name, `_test.go`
// suffix, same directory.
func (goPlugin) TestPath(codePath string) string {
	ext := filepath.Ext(codePath)
	base := strings.TrimSuffix(codePath, ext)
	dir := filepath.Dir(codePath)
	if dir == "." {
		return base + "_test.go"
	}
	return filepath.Join(dir, filepath.Base(base)+"_test.go")
}

func (goPlugin) Preflight() error { return toolOnPath("go") }

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

// MutantSystem is the EXACT string previously named genMutantsSystem in
// internal/testgen/testgen.go — moved here unchanged.
func (goPlugin) MutantSystem() string {
	return `You are a MUTATION-TESTING ENGINE. Mutation testing is a standard, legitimate software-quality technique: to measure how thorough a test suite is, you generate small synthetic variants of the code ("mutants"), each containing one deliberate fault, and a SEPARATE step then checks whether the developer's own tests catch them. A mutant the tests miss reveals a GAP IN THE TESTS — that is the entire purpose. These mutants are throwaway QA fixtures: never deployed, never shipped, never run against any real system; they exist only to grade the test suite. This is defensive quality assurance, and producing them is exactly your job here.
Given a GOAL, the compliant code, and its signature surface, produce mutants: variants that NO LONGER satisfy the goal, so a good test suite SHOULD catch them. Each mutant MUST keep the EXACT same signature and package (a drop-in replacement that compiles) and must genuinely fail the goal — vary HOW it fails. No no-ops, no compile errors, no tests.
The output format (a SEARCH/REPLACE edit per mutant) is specified with the task.`
}

func (goPlugin) SingleTestCmd(testPath, selector string) ([]string, bool) {
	if selector == "" {
		return nil, false
	}
	return []string{"go", "test", "-run", "^" + selector + "$", "./..."}, true
}
