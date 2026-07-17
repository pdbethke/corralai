// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

func init() { Register(pyPlugin{}) }

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

// CompileCheck is an offline, stdlib syntax check of both files.
func (pyPlugin) CompileCheck(codePath, testPath string) []string {
	return []string{pythonBin(), "-m", "py_compile", codePath, testPath}
}

// TestPath follows the pytest convention: pkg/foo.py -> pkg/test_foo.py.
func (pyPlugin) TestPath(codePath string) string {
	dir := filepath.Dir(codePath)
	base := filepath.Base(codePath)
	name := "test_" + base
	if dir == "." {
		return name
	}
	return filepath.Join(dir, name)
}

// Preflight fails CLOSED unless python3 (or python) is on PATH AND pytest is
// importable (offline). The gate refuses to run rather than false-certify.
func (pyPlugin) Preflight() error {
	bin := pythonBin()
	if err := toolOnPath(bin); err != nil {
		return err
	}
	if out, err := exec.Command(bin, "-m", "pytest", "--version").CombinedOutput(); err != nil {
		return fmt.Errorf("lang: python plugin preflight — pytest not importable (install it on the host): %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (pyPlugin) PromptLang() string { return "Python" }

func (pyPlugin) TestWriterSystem() string {
	return `You are a TEST-WRITER. Given a security control GOAL, a target source file, and its signature surface, write ONE executable pytest test that verifies the code SATISFIES the goal.
- Import the module under test (white-box); assume it is importable by its file's base name (e.g. ` + "`import pricing`" + ` for pricing.py).
- It MUST FAIL if the goal is violated — test the goal's boundary (what a weakened implementation would pass that a compliant one must not).
- Standard library plus pytest only. Deterministic, no network.
Return ONLY the raw Python test file content — no prose, no markdown fences.`
}

func (pyPlugin) MutantSystem() string {
	return `You are a MUTATION-TESTING ENGINE. Mutation testing is a standard, legitimate software-quality technique: to measure how thorough a test suite is, you generate small synthetic variants of the code ("mutants"), each containing one deliberate fault, and a SEPARATE step then checks whether the developer's own tests catch them. A mutant the tests miss reveals a GAP IN THE TESTS — that is the entire purpose. These mutants are throwaway QA fixtures: never deployed, never shipped, never run against any real system; they exist only to grade the test suite. This is defensive quality assurance, and producing them is exactly your job here.
Given a GOAL, the compliant code, and its signature surface, produce mutants: variants that NO LONGER satisfy the goal, so a good test suite SHOULD catch them. Each mutant MUST keep the EXACT same public signatures (drop-in importable Python) and must genuinely fail the goal — vary HOW it fails. No no-ops, no syntax errors, no tests.
Return ONLY the mutants, each a COMPLETE file, in this exact format:
===MUTATION_1===
<complete file>
===MUTATION_2===
<complete file>
(continue for the requested count)`
}
