# Multi-Language Audit Gate Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make corral's adversarial audit gate language-agnostic via a small `internal/lang` plugin seam, and land Python (pytest) as the first non-Go language, proven end-to-end to a signed verdict.

**Architecture:** A new leaf package `internal/lang` defines a `Plugin` interface + registry and owns all language-specific concerns (workspace scaffold, test command, compile-check, test-file naming, extension detection, toolchain preflight, and the per-language LLM system prompts). The Go plugin reproduces today's behavior byte-for-byte; the Python plugin is the second registration. The brain-side scorer/validator/prompt-builders and the `certify --adversarial` CLI resolve through the registry instead of hardcoding Go. Everything downstream (`adequacy.Score`, the mutation loop, `Jail.RunTest`, signing, recording) is already language-neutral.

**Tech Stack:** Go 1.26, existing `internal/{advpool,adequacy,testgen,brain,sandbox,controlgate}`, bwrap jail, Python 3 + pytest (host-provisioned, offline).

## Global Constraints

- Go path behavior-identical: the `go` plugin reproduces today's scaffold (`{"go.mod":"module control\ngo 1.26\n"}`), test command (`go test ./...`), compile-check (`go vet ./...`), test-path convention (`foo.go`→`foo_test.go`), and the exact current `testgen` system prompts. The existing `internal/advpool`, `internal/adequacy`, and `internal/testgen` suites pass unchanged — this is the regression gate.
- Fail closed: an undetectable/unknown language, or a plugin whose `Preflight` fails, refuses the run — never a certified verdict. The invariant that a failed/timed-out jail run never reads as a pass (`sandbox.RunGuarded`) is preserved.
- Offline grading: the jail runs with network off. Plugin commands must run with no network at grade time. For Python: `pytest` is host-present; grading only shells `python -m py_compile` and `python -m pytest`.
- No new external Go dependencies.
- Spec refinement (vs `docs/superpowers/specs/2026-07-17-multilanguage-audit-gate-design.md`): the spec's single `PromptLang() string` hook is realized as three hooks — `PromptLang()` (label, for verdict metadata/logs) plus `TestWriterSystem()` and `MutantSystem()` (the full per-language system prompts), because the Go-specific guidance is several lines, not one word.

---

### Task 1: `internal/lang` package — interface, registry, and the `go` plugin

**Files:**
- Create: `internal/lang/lang.go` (interface + registry)
- Create: `internal/lang/go.go` (goPlugin, owns the Go system prompts)
- Create: `internal/lang/lang_test.go`
- Create: `internal/lang/go_test.go`

**Interfaces:**
- Produces:
  - `type Plugin interface { Name() string; Detect(codePath string) bool; Scaffold() map[string]string; TestCmd() []string; CompileCheck(codePath, testPath string) []string; TestPath(codePath string) string; Preflight() error; PromptLang() string; TestWriterSystem() string; MutantSystem() string }`
  - `func Register(p Plugin)` ; `func ByName(name string) (Plugin, bool)` ; `func Detect(codePath string) (Plugin, bool)`
  - goPlugin registered under name `"go"`.

- [ ] **Step 1: Write the failing test** — `internal/lang/lang_test.go`

```go
package lang

import "testing"

func TestRegistryByNameAndDetect(t *testing.T) {
	p, ok := ByName("go")
	if !ok {
		t.Fatal("go plugin not registered")
	}
	if p.Name() != "go" {
		t.Fatalf("Name() = %q, want go", p.Name())
	}
	d, ok := Detect("internal/auth/login.go")
	if !ok || d.Name() != "go" {
		t.Fatalf("Detect(.go) = %v,%v; want go,true", d, ok)
	}
	if _, ok := ByName("cobol"); ok {
		t.Fatal("ByName(cobol) must be false — fail closed")
	}
	if _, ok := Detect("x.cobol"); ok {
		t.Fatal("Detect(.cobol) must be false — fail closed")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/lang/`
Expected: FAIL (package/symbols do not exist).

- [ ] **Step 3: Write `internal/lang/lang.go`**

```go
// SPDX-License-Identifier: Elastic-2.0

// Package lang is the language-plugin seam for corral's adversarial audit
// gate. A Plugin owns everything language-specific about grading one
// self-contained source file + its test suite: the jail workspace scaffold,
// the test command, the compile/type-check, the test-file naming convention,
// extension-based detection, a toolchain preflight, and the per-language LLM
// system prompts. Everything else in the gate is language-neutral.
package lang

// Plugin is everything the audit gate needs to grade one self-contained
// source file + its test suite in a given language.
type Plugin interface {
	Name() string                                    // "go", "python"
	Detect(codePath string) bool                     // by file extension
	Scaffold() map[string]string                     // base workspace files (go.mod / none)
	TestCmd() []string                               // default recursive test command
	CompileCheck(codePath, testPath string) []string // syntax/type check for the authored test
	TestPath(codePath string) string                 // sibling test path per convention
	Preflight() error                                // toolchain present? nil ok, else fail CLOSED
	PromptLang() string                              // human label, for verdict metadata + logs
	TestWriterSystem() string                        // language-specific test-writer system prompt
	MutantSystem() string                            // language-specific mutant-generator system prompt
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
```

- [ ] **Step 4: Write `internal/lang/go.go`** — moves the exact Go system-prompt text out of `internal/testgen` (byte-identical; Task 3 makes testgen take these as a parameter).

```go
// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"path/filepath"
	"strings"

	"github.com/pdbethke/corralai/internal/controlgate"
)

func init() { Register(goPlugin{}) }

type goPlugin struct{}

func (goPlugin) Name() string                { return "go" }
func (goPlugin) Detect(codePath string) bool { return filepath.Ext(codePath) == ".go" }

func (goPlugin) Scaffold() map[string]string {
	base, _, _ := controlgate.LangScaffold("go")
	return base
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
Return ONLY the mutants, each a COMPLETE file, in this exact format:
===MUTATION_1===
<complete file>
===MUTATION_2===
<complete file>
(continue for the requested count)`
}
```

- [ ] **Step 5: Write `internal/lang/preflight.go`** (shared helper)

```go
// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"fmt"
	"os/exec"
)

// toolOnPath reports a fail-closed error if the named executable is not on
// PATH — the toolchain a plugin needs to grade in the jail.
func toolOnPath(name string) error {
	if _, err := exec.LookPath(name); err != nil {
		return fmt.Errorf("lang: required tool %q not found on PATH: %w", name, err)
	}
	return nil
}
```

- [ ] **Step 6: Write `internal/lang/go_test.go`** — pins the Go plugin's values to today's behavior.

```go
package lang

import (
	"reflect"
	"testing"
)

func TestGoPluginMatchesLegacyBehavior(t *testing.T) {
	p, _ := ByName("go")
	if got := p.Scaffold(); !reflect.DeepEqual(got, map[string]string{"go.mod": "module control\ngo 1.26\n"}) {
		t.Fatalf("Scaffold() = %v", got)
	}
	if got := p.TestCmd(); !reflect.DeepEqual(got, []string{"go", "test", "./..."}) {
		t.Fatalf("TestCmd() = %v", got)
	}
	if got := p.CompileCheck("a/b.go", "a/b_test.go"); !reflect.DeepEqual(got, []string{"go", "vet", "./..."}) {
		t.Fatalf("CompileCheck() = %v", got)
	}
	for in, want := range map[string]string{
		"login.go":            "login_test.go",
		"internal/auth/x.go":  "internal/auth/x_test.go",
	} {
		if got := p.TestPath(in); got != want {
			t.Fatalf("TestPath(%q) = %q, want %q", in, got, want)
		}
	}
	if p.PromptLang() != "Go" {
		t.Fatalf("PromptLang() = %q", p.PromptLang())
	}
}
```

- [ ] **Step 7: Run to verify pass**

Run: `go test ./internal/lang/`
Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/lang/
git commit -m "feat(lang): language-plugin seam + go plugin (behavior-identical)"
```

---

### Task 2: The `python` plugin

**Files:**
- Create: `internal/lang/python.go`
- Create: `internal/lang/python_test.go`

**Interfaces:**
- Consumes: `Plugin`, `Register`, `toolOnPath` (Task 1).
- Produces: pyPlugin registered under name `"python"`, `Detect` on `.py`.

- [ ] **Step 1: Write the failing test** — `internal/lang/python_test.go`

```go
package lang

import (
	"reflect"
	"strings"
	"testing"
)

func TestPythonPlugin(t *testing.T) {
	p, ok := ByName("python")
	if !ok {
		t.Fatal("python plugin not registered")
	}
	if !p.Detect("app/pricing.py") || p.Detect("app/pricing.go") {
		t.Fatal("Detect must match .py only")
	}
	if got := p.TestPath("app/pricing.py"); got != "app/test_pricing.py" {
		t.Fatalf("TestPath = %q, want app/test_pricing.py", got)
	}
	if got := p.TestPath("pricing.py"); got != "test_pricing.py" {
		t.Fatalf("TestPath = %q, want test_pricing.py", got)
	}
	if got := p.CompileCheck("pricing.py", "test_pricing.py"); !reflect.DeepEqual(got,
		[]string{"python", "-m", "py_compile", "pricing.py", "test_pricing.py"}) {
		t.Fatalf("CompileCheck = %v", got)
	}
	if got := p.TestCmd(); !reflect.DeepEqual(got, []string{"python", "-m", "pytest", "-q"}) {
		t.Fatalf("TestCmd = %v", got)
	}
	if len(p.Scaffold()) != 0 {
		t.Fatalf("Scaffold must be empty for python, got %v", p.Scaffold())
	}
	if !strings.Contains(p.TestWriterSystem(), "pytest") || !strings.Contains(p.MutantSystem(), "mutant") {
		t.Fatal("python system prompts must be language-appropriate")
	}
	if p.PromptLang() != "Python" {
		t.Fatalf("PromptLang = %q", p.PromptLang())
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/lang/ -run TestPythonPlugin`
Expected: FAIL (python plugin not registered).

- [ ] **Step 3: Write `internal/lang/python.go`**

```go
// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

func init() { Register(pyPlugin{}) }

type pyPlugin struct{}

func (pyPlugin) Name() string                { return "python" }
func (pyPlugin) Detect(codePath string) bool { return filepath.Ext(codePath) == ".py" }

// Scaffold is empty: pytest discovers test_*.py in the workspace and the
// module under test is importable from the workspace root.
func (pyPlugin) Scaffold() map[string]string { return map[string]string{} }

func (pyPlugin) TestCmd() []string { return []string{"python", "-m", "pytest", "-q"} }

// CompileCheck is an offline, stdlib syntax check of both files.
func (pyPlugin) CompileCheck(codePath, testPath string) []string {
	return []string{"python", "-m", "py_compile", codePath, testPath}
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

// Preflight fails CLOSED unless python3 is on PATH AND pytest is importable
// (offline). The gate refuses to run rather than false-certify.
func (pyPlugin) Preflight() error {
	if err := toolOnPath("python"); err != nil {
		return err
	}
	if out, err := exec.Command("python", "-m", "pytest", "--version").CombinedOutput(); err != nil {
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
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/lang/ -run TestPythonPlugin`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/lang/python.go internal/lang/python_test.go
git commit -m "feat(lang): python/pytest plugin (fail-closed preflight)"
```

---

### Task 3: Parameterize `testgen` prompts by system string (Go byte-identical)

The two `testgen` system prompts move to the `lang` plugins (Task 1). `testgen`'s prompt builders now take the system string as an argument. All current Go callers pass the go plugin's system prompt, so behavior is byte-identical.

**Files:**
- Modify: `internal/testgen/testgen.go` (remove the two consts; `WriteTestPrompt`/`GenerateMutantsPrompt`/`WriteTest`/`GenerateMutants` take `system string`)
- Modify: `internal/testgen/testgen_test.go` (`TestWriteTestPromptUnchanged`, `TestGenerateMutantsPromptUnchanged` pass the go system prompt)
- Modify: `internal/authoring/authoring.go` (pass go system prompts)
- Modify: `internal/advpool/roles.go` (pass system prompts — resolved from `rs.Lang` in Task 4; for this task pass the go plugin's, keeping behavior identical)

**Interfaces:**
- Consumes: `lang.ByName` (Task 1).
- Produces:
  - `func WriteTestPrompt(system, goal, code string, sigs []repoindex.Signature) (sys, user string)`
  - `func GenerateMutantsPrompt(system, goal, code string, sigs []repoindex.Signature, n int) (sys, user string)`
  - `func WriteTest(ctx, m LLM, system, goal, code string, sigs []repoindex.Signature) (string, error)`
  - `func GenerateMutants(ctx, m LLM, system, goal, code string, sigs []repoindex.Signature, n int) ([]adequacy.Mutant, error)`

- [ ] **Step 1: Update the byte-identical tests first** — edit `internal/testgen/testgen_test.go` so `TestWriteTestPromptUnchanged` / `TestGenerateMutantsPromptUnchanged` obtain the system prompt from the plugin and pass it through:

```go
import golang "github.com/pdbethke/corralai/internal/lang"

// in TestWriteTestPromptUnchanged:
goP, _ := golang.ByName("go")
sys, user := WriteTestPrompt(goP.TestWriterSystem(), goal, code, sigs)
// ...assert sys == goP.TestWriterSystem() and the same `user` bytes as before.

// in TestGenerateMutantsPromptUnchanged:
goP, _ := golang.ByName("go")
sys, user := GenerateMutantsPrompt(goP.MutantSystem(), goal, code, sigs, 3)
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/testgen/`
Expected: FAIL to compile (signatures don't take `system` yet).

- [ ] **Step 3: Edit `internal/testgen/testgen.go`** — delete the `writeTestSystem` and `genMutantsSystem` consts and thread `system`:

```go
func WriteTestPrompt(system, goal, code string, sigs []repoindex.Signature) (sys, user string) {
	return system, buildUser(goal, code, sigs, "")
}

func GenerateMutantsPrompt(system, goal, code string, sigs []repoindex.Signature, n int) (sys, user string) {
	instr := fmt.Sprintf("Produce exactly %d distinct mutations.", n)
	return system, buildUser(goal, code, sigs, instr)
}

func WriteTest(ctx context.Context, m LLM, system, goal, code string, sigs []repoindex.Signature) (string, error) {
	sysP, usr := WriteTestPrompt(system, goal, code, sigs)
	resp, err := m.Ask(ctx, sysP, usr)
	if err != nil {
		return "", err
	}
	return ParseTestOutput(resp), nil
}

func GenerateMutants(ctx context.Context, m LLM, system, goal, code string, sigs []repoindex.Signature, n int) ([]adequacy.Mutant, error) {
	sysP, usr := GenerateMutantsPrompt(system, goal, code, sigs, n)
	resp, err := m.Ask(ctx, sysP, usr)
	if err != nil {
		return nil, err
	}
	return ParseMutantsOutput(resp)
}
```

- [ ] **Step 4: Update `internal/authoring/authoring.go`** — it is Go-only (control gate), so pass the go plugin's system prompts:

```go
import golang "github.com/pdbethke/corralai/internal/lang"

// near the top of Author(), after resolving sigs:
goP, _ := golang.ByName("go")
test, err := testgen.WriteTest(ctx, m, goP.TestWriterSystem(), req.Goal, req.Code, sigs)
// ...
mutants, err := testgen.GenerateMutants(ctx, m, goP.MutantSystem(), req.Goal, req.Code, sigs, req.NMutants)
```

- [ ] **Step 5: Update `internal/advpool/roles.go`** — pass the go plugin's system prompts for now (Task 4 swaps to `rs.Lang`-resolved):

```go
import golang "github.com/pdbethke/corralai/internal/lang"

func renderMutantGenerator(rs RunSpec, sigs []repoindex.Signature, _ []adequacy.Mutant) string {
	goP, _ := golang.ByName("go")
	system, user := testgen.GenerateMutantsPrompt(goP.MutantSystem(), rs.Goal, rs.Code, sigs, rs.NMutants)
	return joinPrompt(system, user)
}

func renderTestWriter(rs RunSpec, sigs []repoindex.Signature, survivors []adequacy.Mutant) string {
	goal := rs.Goal
	if len(survivors) > 0 { /* unchanged survivor-augmentation block */ }
	goP, _ := golang.ByName("go")
	system, user := testgen.WriteTestPrompt(goP.TestWriterSystem(), goal, rs.Code, sigs)
	return joinPrompt(system, user)
}
```

- [ ] **Step 6: Run the full affected suites**

Run: `go test ./internal/testgen/ ./internal/authoring/ ./internal/advpool/ ./internal/controlgate/`
Expected: PASS — prompts byte-identical for Go, everything compiles.

- [ ] **Step 7: Commit**

```bash
git add internal/testgen/ internal/authoring/authoring.go internal/advpool/roles.go
git commit -m "refactor(testgen): system prompt is a parameter; go prompts sourced from lang plugin"
```

---

### Task 4: Wire the brain + advpool seam through the registry

Add `Lang` to the run spec; resolve the plugin at the brain-side use sites; run `Preflight` before grading (fail closed). Empty `Lang` defaults to `"go"` so all existing paths and tests are unaffected.

**Files:**
- Modify: `internal/advpool/run.go` (`RunSpec` gains `Lang string`)
- Modify: `internal/advpool/roles.go` (render fns resolve plugin from `rs.Lang`)
- Modify: `internal/brain/advpool.go` (`AdvPoolRunSpec.Lang`; `advPoolBase(codePath)`, `advPoolTestPath`, `CompileTest`, `Score` resolve the plugin by codePath; `StartRun` runs `Preflight`)
- Test: `internal/advpool/roles_test.go`, `internal/brain/advpool_test.go`

**Interfaces:**
- Consumes: `lang.ByName`, `lang.Detect`, `Plugin.{Scaffold,TestCmd,CompileCheck,TestPath,Preflight,TestWriterSystem,MutantSystem}`.
- Produces: `RunSpec.Lang`, `AdvPoolRunSpec.Lang`; a package-level helper `func pluginFor(codePath string) (lang.Plugin, error)` in `internal/brain/advpool.go` (fail-closed).

- [ ] **Step 1: Write the failing test** — `internal/brain/advpool_test.go` (add):

```go
func TestPluginForFailsClosedOnUnknownExt(t *testing.T) {
	if _, err := pluginFor("weird.cobol"); err == nil {
		t.Fatal("pluginFor(.cobol) must error — fail closed")
	}
	p, err := pluginFor("internal/sqlguard/sqlguard.go")
	if err != nil || p.Name() != "go" {
		t.Fatalf("pluginFor(.go) = %v,%v; want go,nil", p, err)
	}
}

func TestAdvPoolBaseGoUnchanged(t *testing.T) {
	base, cmd := advPoolBase("x/y.go")
	if base["go.mod"] == "" || cmd[0] != "go" {
		t.Fatalf("go base/cmd regressed: %v %v", base, cmd)
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/brain/ -run 'TestPluginFor|TestAdvPoolBaseGo'`
Expected: FAIL (`pluginFor` undefined; `advPoolBase` takes no arg).

- [ ] **Step 3: Add `Lang` to `internal/advpool/run.go`**

```go
type RunSpec struct {
	Repo        string
	Commit      string
	Goal        string
	CodePath    string
	Code        string
	DevTestPath string
	DevTestCode string
	TestCmd     string
	NMutants    int
	Lang        string // "" defaults to "go" at render time (back-compat)
}
```

- [ ] **Step 4: Resolve the plugin in `internal/advpool/roles.go`** — replace the fixed `golang.ByName("go")` from Task 3 with a helper that honors `rs.Lang`:

```go
// langFor resolves the run's plugin, defaulting to go for back-compat when
// Lang is unset. Falls back to go if an unknown name slips through (the
// brain has already preflighted; this keeps rendering total).
func langFor(rs RunSpec) golang.Plugin {
	if p, ok := golang.ByName(rs.Lang); ok {
		return p
	}
	p, _ := golang.ByName("go")
	return p
}

func renderMutantGenerator(rs RunSpec, sigs []repoindex.Signature, _ []adequacy.Mutant) string {
	p := langFor(rs)
	system, user := testgen.GenerateMutantsPrompt(p.MutantSystem(), rs.Goal, rs.Code, sigs, rs.NMutants)
	return joinPrompt(system, user)
}
// renderTestWriter: same, using p.TestWriterSystem()
```

- [ ] **Step 5: Edit `internal/brain/advpool.go`** — add `pluginFor`, make `advPoolBase` take the codePath, route CompileTest/TestPath/Score through the plugin, thread `Lang`, and preflight in `StartRun`:

```go
import golang "github.com/pdbethke/corralai/internal/lang"

// pluginFor resolves the language plugin from the code file's extension,
// fail-closed on an unknown language (the gate never grades what it cannot run).
func pluginFor(codePath string) (golang.Plugin, error) {
	p, ok := golang.Detect(codePath)
	if !ok {
		return nil, fmt.Errorf("advpool: no language plugin for %q — refusing to grade", codePath)
	}
	return p, nil
}

// advPoolBase now resolves per code file. Unknown ext falls back to go's
// scaffold ONLY for the empty-testCmd default path; callers that can fail
// (StartRun) preflight first.
func advPoolBase(codePath string) (base map[string]string, testCmd []string) {
	if p, err := pluginFor(codePath); err == nil {
		return p.Scaffold(), p.TestCmd()
	}
	b, _, _ := controlgate.LangScaffold("go")
	return b, []string{"go", "test", "./..."}
}

func advPoolTestPath(codePath string) string {
	if p, err := pluginFor(codePath); err == nil {
		return p.TestPath(codePath)
	}
	// legacy go fallback (kept identical to the prior implementation)
	ext := filepath.Ext(codePath)
	base := strings.TrimSuffix(codePath, ext)
	dir := filepath.Dir(codePath)
	if dir == "." {
		return base + "_test.go"
	}
	return filepath.Join(dir, filepath.Base(base)+"_test.go")
}
```

In `advpoolScorer.Score` change `base, defaultCmd := advPoolBase()` to `base, defaultCmd := advPoolBase(codePath)`.

In `advpoolValidator.CompileTest`, replace the hardcoded `[]string{"go","vet","./..."}` with the plugin's check:

```go
func (v advpoolValidator) CompileTest(ctx context.Context, codePath, code, test string) error {
	p, err := pluginFor(codePath)
	if err != nil {
		return err
	}
	base := p.Scaffold()
	ws := make(map[string]string, len(base)+2)
	for k, val := range base {
		ws[k] = val
	}
	ws[codePath] = code
	ws[advPoolTestPath(codePath)] = test
	compiles, err := v.jail.RunTest(ctx, ws, p.CompileCheck(codePath, advPoolTestPath(codePath)))
	if err != nil {
		return fmt.Errorf("advpool: compile-verify test: %w", err)
	}
	if !compiles {
		return fmt.Errorf("advpool: test does not compile")
	}
	return nil
}
```

- [ ] **Step 6: Thread `Lang` on `AdvPoolRunSpec` + preflight in `StartRun`** (`internal/brain/advpool.go`):

```go
// in AdvPoolRunSpec struct:
Lang string `json:"lang,omitempty" jsonschema:"source language of the code under review (default: inferred from code_path extension, e.g. go, python)"`

// in AdvPoolRuntime.StartRun, before creating the run:
langName := in.Lang
if langName == "" {
	if p, err := pluginFor(in.CodePath); err == nil {
		langName = p.Name()
	}
}
p, ok := golang.ByName(langName)
if !ok {
	return 0, fmt.Errorf("advpool: unknown language %q for %q — refusing", langName, in.CodePath)
}
if err := p.Preflight(); err != nil {
	return 0, fmt.Errorf("advpool: language toolchain unavailable — refusing to run: %w", err)
}
// set rs.Lang = p.Name() on the advpool.RunSpec built below
```

- [ ] **Step 7: Surface the language on the signed verdict (provenance).** Add a `Lang string` field to `advpool.Verdict` (in `internal/advpool/driver.go`), populate it from `run.rs.Lang` where the terminal `Verdict` is built, and add a matching `Lang string \`json:"Lang"\`` to the CLI's `advVerdict` decode struct in `cmd/corral/certify_adversarial.go` so the rendered verdict can print `language: <lang>`. Because the signer digests the marshaled `Verdict`, this new field is automatically covered by the tamper-evident `output_digest` — no signer change needed. Add one assertion to an existing driver verdict test that a run with `Lang:"go"` produces `Verdict.Lang == "go"`.

- [ ] **Step 8: Run the regression + new tests**

Run: `go test ./internal/advpool/ ./internal/brain/ ./internal/adequacy/`
Expected: PASS (Go path identical; new fail-closed + Verdict.Lang tests green).

- [ ] **Step 9: Commit**

```bash
git add internal/advpool/ internal/brain/advpool.go internal/brain/advpool_test.go cmd/corral/certify_adversarial.go
git commit -m "feat(advpool): resolve language plugin per run; preflight fail-closed; Lang on spec + verdict"
```

---

### Task 5: CLI — detect language and send `Lang`

**Files:**
- Modify: `cmd/corral/certify_adversarial.go`
- Test: `cmd/corral/certify_adversarial_test.go`

**Interfaces:**
- Consumes: `lang.Detect`, `lang.ByName`, `Plugin.TestPath`, `advStartSpec.Lang` (add the field mirroring the brain's `AdvPoolRunSpec.Lang`).
- Produces: `--lang` override flag; language inferred from `--code` extension; sibling test path derived from the plugin.

- [ ] **Step 1: Write the failing test** — `cmd/corral/certify_adversarial_test.go` (add): a case asserting that a `--code foo.py` invocation (with a fake `advPoolClient`) sends `spec.Lang == "python"` and `spec.DevTestPath == "test_foo.py"`, and that `--code foo.xyz` exits 2 with an "unknown language" message. Follow the existing test's fake-client pattern in this file.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./cmd/corral/ -run Adversarial`
Expected: FAIL.

- [ ] **Step 3: Edit `cmd/corral/certify_adversarial.go`** — add the `--lang` flag, resolve the plugin, and default the test path through it:

```go
langFlag := fs.String("lang", "", "source language (default: inferred from --code extension)")
// after codePath is known and validated:
var plug goent.Plugin // import goent "github.com/pdbethke/corralai/internal/lang"
if strings.TrimSpace(*langFlag) != "" {
	p, ok := goent.ByName(strings.TrimSpace(*langFlag))
	if !ok {
		fmt.Fprintf(stderr, "corral certify --adversarial: unknown --lang %q\n", *langFlag)
		return 2
	}
	plug = p
} else {
	p, ok := goent.Detect(*codePath)
	if !ok {
		fmt.Fprintf(stderr, "corral certify --adversarial: unknown language for --code %s (pass --lang)\n", *codePath)
		return 2
	}
	plug = p
}
// derive the sibling test path via the plugin when --test is empty:
tp := strings.TrimSpace(*testPath)
if tp == "" {
	tp = plug.TestPath(*codePath)
}
// add Lang to the spec:
spec := advStartSpec{ /* existing fields */, Lang: plug.Name() }
```

Add `Lang string \`json:"lang"\`` to `advStartSpec` and pass it in `mcpAdvClient.StartRun`'s `args` map as `"lang": spec.Lang`. Delete the old `siblingTestPath(*codePath)` call (now the plugin's job); leave `siblingTestPath` only if other callers use it, else remove it.

- [ ] **Step 4: Run to verify pass**

Run: `go test ./cmd/corral/ -run Adversarial`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/corral/certify_adversarial.go cmd/corral/certify_adversarial_test.go
git commit -m "feat(cli): certify --adversarial infers language from --code (+ --lang override)"
```

---

### Task 6: Python-in-jail proof — hermetic `adequacy.Score` test

Prove the whole grading loop works for Python through the bwrap jail with real pytest: a thorough suite kills all mutants; a gappy suite leaves survivors.

**Files:**
- Create: `internal/adequacy/score_python_test.go`

**Interfaces:**
- Consumes: `adequacy.Score`, `adequacy.NewJail`, `sandbox.Resolve`, `lang.ByName("python")`.

- [ ] **Step 1: Write the test** — skips cleanly when python3/pytest or a bwrap backend is unavailable (so local dev is not blocked; CI provisions them):

```go
package adequacy_test

import (
	"context"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	golang "github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/sandbox"
)

func TestScorePythonKillsAndSurvives(t *testing.T) {
	py, _ := golang.ByName("python")
	if err := py.Preflight(); err != nil {
		t.Skipf("python/pytest not available: %v", err)
	}
	backend, err := sandbox.Resolve(sandbox.Config{Backend: "bwrap"})
	if err != nil {
		t.Skipf("no bwrap backend: %v", err)
	}
	jail := adequacy.NewJail(backend, 60*time.Second)

	code := "def is_even(n):\n    return n % 2 == 0\n"
	mutants := []adequacy.Mutant{
		{ID: "m1", Code: "def is_even(n):\n    return n % 2 == 1\n"}, // inverted — a real test kills it
	}

	// Thorough suite: kills the mutant.
	thorough := "from evenmod import is_even\ndef test_even():\n    assert is_even(2)\n    assert not is_even(3)\n"
	rep, err := adequacy.Score(context.Background(), jail,
		map[string]string{py.TestPath("evenmod.py"): thorough}, "evenmod.py", code, mutants, py.TestCmd())
	if err != nil {
		t.Fatalf("score(thorough): %v", err)
	}
	if rep.KillRate() != 1.0 {
		t.Fatalf("thorough suite should kill all mutants, got kill rate %v", rep.KillRate())
	}

	// Gappy suite: only checks the true case, so the inverted mutant survives.
	gappy := "from evenmod import is_even\ndef test_even():\n    assert is_even(2)\n"
	rep2, err := adequacy.Score(context.Background(), jail,
		map[string]string{py.TestPath("evenmod.py"): gappy}, "evenmod.py", code, mutants, py.TestCmd())
	if err != nil {
		t.Fatalf("score(gappy): %v", err)
	}
	if len(rep2.Survived) == 0 {
		t.Fatal("gappy suite must leave a survivor")
	}
}
```

> Note the test writes `evenmod.py` (the code) at `codePath` and `test_evenmod.py` alongside it; `Score` writes `code` at `codePath` and the test at the map key. Confirm `adequacy.Score` writes the code file at `codePath` into the same workspace dir (it does — it is how the Go path works); if the import fails because the workspace root is not on `sys.path`, prepend `import sys, os; sys.path.insert(0, os.path.dirname(__file__))` to the test strings.

- [ ] **Step 2: Run it** (on a host with python3+pytest+bwrap)

Run: `go test ./internal/adequacy/ -run Python -v`
Expected: PASS (or SKIP where the toolchain is absent).

- [ ] **Step 3: Commit**

```bash
git add internal/adequacy/score_python_test.go
git commit -m "test(adequacy): python-in-jail grading (kill + survive) via pytest"
```

---

### Task 7: Provision the toolchain + document

**Files:**
- Modify: `.github/workflows/deploy.yml` (the `validate` job installs python3 + pytest before `go test`)
- Modify: `README.md` and `docs/` (state that the audit gate now supports Go and Python; Python needs pytest on the brain host)
- Modify: `/home/pdbethke/.claude/skills/corralai/SKILL.md` (note the `internal/lang` seam + host pytest requirement)

- [ ] **Step 1: Add a provisioning step to the `validate` job** in `.github/workflows/deploy.yml`, before the test step:

```yaml
      - name: Provision Python test toolchain (audit-gate: python plugin)
        run: |
          python3 -m pip install --quiet pytest
          python3 -m pytest --version
```

- [ ] **Step 2: Install pytest on the brain host** (operator step — document it; do not script a prod change here):

```
ssh hetzner 'python3 -m pip install --user pytest && python3 -m pytest --version'
```

Record in the deploy notes that a brain host missing pytest will **fail closed** on Python runs (never false-certify) — the desired behavior.

- [ ] **Step 3: Update `README.md` / `docs/`** — one line under the audit-gate description: "Certify-by-execution supports Go and Python (pytest); the language is inferred from the file extension. Other languages are a plugin each (`internal/lang`)."

- [ ] **Step 4: Commit**

```bash
git add .github/workflows/deploy.yml README.md docs/ /home/pdbethke/.claude/skills/corralai/SKILL.md
git commit -m "chore: provision pytest in CI + document the multi-language audit gate"
```

---

## Rollout (post-merge, operational — not plan tasks)

1. Merge behind the existing `CORRALAI_ADVERSARIAL_POOL` gate (off by default); Go behavior unchanged.
2. Install pytest on the brain host.
3. Capture a real Python `certify --adversarial` audit on the live herd → signed record → publish to the gallery beside the Go one (reuse `scratchpad/selfaudit-run.sh`, pointing `--code` at a `.py` target).
4. Field note: "The herd learns a second language."
5. Follow-ons (separate specs): JS/Ruby/C plugins; container-backed per-language jail; tree-sitter AST structural mutation.
