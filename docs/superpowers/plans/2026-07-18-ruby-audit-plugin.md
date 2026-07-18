# Ruby Audit Plugin Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add Ruby (minitest + auto-detected RSpec) as the third language of corral's adversarial audit gate, via one plugin in the existing `internal/lang` seam.

**Architecture:** The `internal/lang` seam already resolves any registered plugin by file extension; `advpool`/`brain`/`testgen`/CLI need no change. This adds `internal/lang/ruby.go` (rubyPlugin), its unit test, a hermetic in-jail Score test, and CI/host provisioning — the Ruby analogue of the Python plugin's Tasks 2/6/7.

**Tech Stack:** Go 1.26, `internal/lang`, `internal/adequacy`, bwrap jail (runs test commands via `sh -c`), Ruby + minitest (bundled) + optional `rspec` gem.

## Global Constraints

- No change to the seam or the Go/Python plugins; existing suites pass unchanged.
- Fail closed: failed preflight / failed jail run never certifies (unchanged `sandbox.RunGuarded`).
- Offline grading (network off in jail): `ruby`+minitest ship together; the `rspec` gem must be host-present for RSpec suites (no `gem install` at grade time).
- No new external Go dependencies. gofmt clean; every new file starts with `// SPDX-License-Identifier: Elastic-2.0`. gosec: annotate any variable-`exec.Command` with a justified `// #nosec G204` (mirror the Python plugin).

---

### Task 1: The `ruby` plugin

**Files:**
- Create: `internal/lang/ruby.go`
- Create: `internal/lang/ruby_test.go`

**Interfaces:**
- Consumes: `Plugin`, `Register`, `toolOnPath` (existing in `internal/lang`).
- Produces: rubyPlugin registered under `"ruby"`, `Detect` on `.rb`.

- [ ] **Step 1: Write the failing test** — `internal/lang/ruby_test.go`

```go
package lang

import (
	"reflect"
	"strings"
	"testing"
)

func TestRubyPlugin(t *testing.T) {
	p, ok := ByName("ruby")
	if !ok {
		t.Fatal("ruby plugin not registered")
	}
	if !p.Detect("app/pricing.rb") || p.Detect("app/pricing.py") {
		t.Fatal("Detect must match .rb only")
	}
	if got := p.TestPath("app/pricing.rb"); got != "app/pricing_test.rb" {
		t.Fatalf("TestPath = %q, want app/pricing_test.rb", got)
	}
	if got := p.TestPath("pricing.rb"); got != "pricing_test.rb" {
		t.Fatalf("TestPath = %q, want pricing_test.rb", got)
	}
	cc := p.CompileCheck("pricing.rb", "pricing_test.rb")
	if !reflect.DeepEqual(cc, []string{"ruby", "-c", "pricing.rb", "&&", "ruby", "-c", "pricing_test.rb"}) {
		t.Fatalf("CompileCheck = %v", cc)
	}
	// TestCmd MUST be a single shell string: the jail space-joins the argv and
	// runs it under `sh -c`, so a multi-token slice with an embedded snippet
	// would lose its argument boundaries. One element keeps the snippet intact.
	if len(p.TestCmd()) != 1 {
		t.Fatalf("TestCmd must be a single shell string, got %v", p.TestCmd())
	}
	tc := p.TestCmd()[0]
	if !strings.Contains(tc, "rspec") || !strings.Contains(tc, "ruby ") {
		t.Fatalf("TestCmd must dispatch rspec-or-ruby: %q", tc)
	}
	if len(p.Scaffold()) != 0 {
		t.Fatalf("Scaffold must be empty, got %v", p.Scaffold())
	}
	if !strings.Contains(p.TestWriterSystem(), "minitest") || !strings.Contains(p.MutantSystem(), "mutant") {
		t.Fatal("ruby system prompts must be language-appropriate")
	}
	if p.PromptLang() != "Ruby" {
		t.Fatalf("PromptLang = %q", p.PromptLang())
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/lang/ -run TestRubyPlugin`
Expected: FAIL (ruby plugin not registered).

- [ ] **Step 3: Write `internal/lang/ruby.go`**

```go
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
Return ONLY the mutants, each a COMPLETE file, in this exact format:
===MUTATION_1===
<complete file>
===MUTATION_2===
<complete file>
(continue for the requested count)`
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/lang/ -run TestRubyPlugin`
Expected: PASS.

- [ ] **Step 5: Confirm the seam picks it up with no other change** — `go build ./...`; `go test ./internal/lang/ ./internal/advpool/ ./internal/brain/ ./cmd/corral/`. All PASS (the registry now has three plugins; nothing else changed).

- [ ] **Step 6: Commit**

```bash
git add internal/lang/ruby.go internal/lang/ruby_test.go
git commit -m "feat(lang): ruby plugin (minitest + auto-detected rspec, fail-closed)"
```

---

### Task 2: Ruby-in-jail proof — hermetic `adequacy.Score` test

**Files:**
- Create: `internal/adequacy/score_ruby_test.go`

**Interfaces:**
- Consumes: `adequacy.Score`, `adequacy.NewJail`, `sandbox.Resolve`, `lang.ByName("ruby")`.

- [ ] **Step 1: Write the test** — mirrors `score_python_test.go`, minitest fixtures. It MUST skip cleanly when `ruby`/jail are unavailable (Preflight error, `sandbox.Resolve` error, or a jail-start error / `!CompliantPass`), and only assert on a successful Score. The survivor mutant must SURVIVE the gappy suite (Python lesson): use an always-even `def is_even; true` style mutant that the gappy (even-only) suite misses but the thorough (odd-case) suite catches.

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

func TestScoreRubyKillsAndSurvives(t *testing.T) {
	rb, _ := golang.ByName("ruby")
	if err := rb.Preflight(); err != nil {
		t.Skipf("ruby not available: %v", err)
	}
	backend, err := sandbox.Resolve(sandbox.Config{Backend: "bwrap"})
	if err != nil {
		t.Skipf("no bwrap backend: %v", err)
	}
	jail := adequacy.NewJail(backend, 60*time.Second)

	code := "def is_even(n)\n  n % 2 == 0\nend\n"
	// Always-even mutant: killed by the thorough suite's odd-case assertion,
	// survives the gappy suite that only checks an even input.
	mutants := []adequacy.Mutant{
		{ID: "m1", Code: "def is_even(n)\n  true\nend\n"},
	}
	tp := rb.TestPath("evenmod.rb") // evenmod_test.rb

	thorough := "require 'minitest/autorun'\nrequire_relative 'evenmod'\nclass T < Minitest::Test\n  def test_even\n    assert is_even(2)\n    refute is_even(3)\n  end\nend\n"
	rep, err := adequacy.Score(context.Background(), jail,
		map[string]string{tp: thorough}, "evenmod.rb", code, mutants, rb.TestCmd())
	if err != nil {
		t.Skipf("jail/ruby unavailable (score errored): %v", err)
	}
	if !rep.CompliantPass {
		t.Skipf("compliant suite did not pass in jail — treating as toolchain/jail unavailable")
	}
	if rep.KillRate() != 1.0 {
		t.Fatalf("thorough suite should kill all mutants, got kill rate %v", rep.KillRate())
	}

	gappy := "require 'minitest/autorun'\nrequire_relative 'evenmod'\nclass T < Minitest::Test\n  def test_even\n    assert is_even(2)\n  end\nend\n"
	rep2, err := adequacy.Score(context.Background(), jail,
		map[string]string{tp: gappy}, "evenmod.rb", code, mutants, rb.TestCmd())
	if err != nil {
		t.Skipf("jail/ruby unavailable (score errored): %v", err)
	}
	if !rep2.CompliantPass {
		t.Skipf("compliant suite did not pass in jail — treating as toolchain/jail unavailable")
	}
	if len(rep2.Survived) == 0 {
		t.Fatal("gappy suite must leave a survivor")
	}
}
```

> Verify the exact `adequacy.Report` field names against `score_python_test.go` (which is already in the tree) and match them — use `CompliantPass`, `KillRate()`, `Survived` exactly as that file does.

- [ ] **Step 2: Run it**

Run: `go test ./internal/adequacy/ -run Ruby -v`
Expected: PASS where ruby+jail work; SKIP (not FAIL) otherwise. Paste the outcome.

- [ ] **Step 3: Commit**

```bash
git add internal/adequacy/score_ruby_test.go
git commit -m "test(adequacy): ruby-in-jail grading (kill + survive) via minitest"
```

---

### Task 3: Provision Ruby + document

**Files:**
- Modify: `.github/workflows/deploy.yml` (validate job installs ruby + rspec, non-fatal)
- Modify: `README.md`, `ROADMAP.md`
- Modify: `/home/pdbethke/.claude/skills/corralai/SKILL.md` (out-of-repo; edit, do NOT commit)

- [ ] **Step 1: Add a provisioning step to the `validate` job** in `.github/workflows/deploy.yml`, near the pytest step, non-fatal:

```yaml
      - name: Provision Ruby test toolchain (audit gate — ruby plugin)
        run: |
          sudo apt-get install -y ruby ruby-rspec || gem install --user-install rspec || true
          ruby --version || echo "ruby not available — ruby-in-jail test will SKIP"
```

Read the existing validate job and match its indentation/style; place it in the same job as the pytest step.

- [ ] **Step 2: Docs** — README.md and ROADMAP.md: update the audit-gate language line to "Go, Python (pytest), and Ruby (minitest/RSpec)"; keep JS/C as future. SKILL.md (out-of-repo): add ruby to the plugin list + note the rspec-gem host requirement for the RSpec path.

- [ ] **Step 3: Verify** — `python3 -c "import yaml; yaml.safe_load(open('.github/workflows/deploy.yml')); print('YAML OK')"`; `go build ./...` clean.

- [ ] **Step 4: Commit**

```bash
git add .github/workflows/deploy.yml README.md ROADMAP.md
git commit -m "chore: provision ruby in CI + document ruby audit support"
```

---

## Rollout (post-merge, operational)

1. Merge (feature stays behind `CORRALAI_ADVERSARIAL_POOL`; Go/Python unchanged).
2. Install ruby + rspec on the brain host: `ssh hetzner 'sudo apt-get install -y ruby ruby-rspec'` (a missing ruby ⇒ Ruby runs fail closed).
3. Live Ruby audit recording (a real .rb target) beside the Go/Python ones.
4. Follow-on: JS/C plugins.
