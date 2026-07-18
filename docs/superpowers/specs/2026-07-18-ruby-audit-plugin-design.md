# Ruby Audit Plugin — Design

**Date:** 2026-07-18
**Status:** Approved for planning
**Author:** Peter Bethke (+ Claude)

## Problem

The `internal/lang` plugin seam (shipped 2026-07-17, merge 06f4571) made corral's
adversarial audit gate language-agnostic: Go and Python (pytest) are live, and the spec
explicitly anticipated "JS/Ruby/C become one plugin registration each." This adds **Ruby**
as the third language, grading with **both minitest and RSpec** (auto-detected).

Nothing in the seam changes: `advpool`, `brain`, `testgen`, and the CLI already resolve
any registered plugin by the code file's extension. This is a new plugin + its in-jail
test + provisioning — the Ruby analogue of the Python plugin's Tasks 2/6/7.

## Scope

- `internal/lang/ruby.go` — rubyPlugin, registered under `"ruby"`, detects `.rb`.
- `internal/lang/ruby_test.go` — unit tests.
- `internal/adequacy/score_ruby_test.go` — hermetic in-jail kill+survive test (skips when
  the toolchain/jail is unavailable).
- CI + brain-host provisioning of `ruby` (+ the `rspec` gem for the RSpec path) and docs.

**Out of scope:** JS/C plugins; container-backed jail; tree-sitter structural mutation.

## Global Constraints

- No change to the seam or to the Go/Python plugins. Existing suites pass unchanged.
- Fail closed: unknown language / failed preflight refuses the run — never certifies. A
  jail command that fails (e.g. an RSpec suite when the `rspec` gem is absent) never reads
  as a pass (`sandbox.RunGuarded`, unchanged).
- Offline grading: the jail runs with network off. `ruby` + minitest ship together (zero
  gems); the `rspec` gem, when needed, must be host-present (no `gem install` at grade time).
- No new external Go dependencies.

## The detect-both mechanism

The pool grades one code file + one dev-test file, writing the dev test to a synthetic
path (`TestPath(codePath)`), which erases any framework signal in the original filename.
So framework selection is **by test-file content, at jail-run time**, exploiting the fact
that the jail executes the test command via `sh -c` (`internal/sandbox/isolator_linux.go`
appends `sh -c <command>`).

- `Detect(codePath)` → `.rb`.
- `Scaffold()` → `{}` (empty; a Ruby test `require_relative`s its sibling module).
- `TestPath("pkg/foo.rb")` → `"pkg/foo_test.rb"` (neutral name; content decides framework).
- `TestCmd()` → a single `sh` dispatch line (joined by the jail into `sh -c`):
  ```sh
  t="$(ls *_test.rb *_spec.rb test_*.rb spec_*.rb 2>/dev/null | head -n1)"; \
  [ -z "$t" ] && { echo "no ruby test file"; exit 1; }; \
  if grep -Eq "require ['\"](rspec|spec_helper)|RSpec[.:]" "$t"; then exec rspec "$t"; else exec ruby "$t"; fi
  ```
  minitest test files self-run via `require 'minitest/autorun'` (`ruby <file>`); RSpec
  files are dispatched to `rspec <file>`.
- `CompileCheck(codePath, testPath)` → `ruby -c <codePath> && ruby -c <testPath>` (each
  `ruby -c` is a one-file syntax check; the `&&` is honored by the `sh -c` execution).
- `Preflight()` → `ruby` on PATH (minitest is bundled). It deliberately does NOT require
  `rspec`: a run only needs rspec if the *dev's* suite is RSpec, and a missing gem then
  surfaces as a jail-command failure (fail-closed, never a false pass) — a per-plugin
  preflight cannot know a per-run framework choice.
- `PromptLang()` → `"Ruby"`.
- `TestWriterSystem()` → instructs the model to write **one minitest test** (stdlib, no
  gem): `require 'minitest/autorun'`, `require_relative` the target, a `Minitest::Test`
  subclass, boundary-testing the goal, deterministic, no network, raw Ruby only. The
  pool's own authored test is thus always runnable without the rspec gem; the *dev's*
  suite is still graded in whatever framework it is written in.
- `MutantSystem()` → the standard mutation-testing framing, Ruby-flavored: complete
  drop-in Ruby files, same public method signatures, genuinely goal-violating, no syntax
  errors, no tests, in the `===MUTATION_N===` format.

## Signature extraction

`repoindex.ExtractSignatures(rs.Code, rs.Lang)` (fixed in M-1) already routes `"ruby"` to
the tree-sitter Ruby extractor (`internal/repoindex/lang.go` has the `.rb`→`ruby` case and
a `"ruby"` branch; the brain builds `CGO_ENABLED=1`). So Ruby runs get real signature hints
— no code change needed.

## Testing

- **Ruby unit** (`ruby_test.go`): `Detect`, `TestPath`, `CompileCheck` argv, `TestCmd`
  contains the rspec/minitest dispatch, `Preflight` requires only `ruby`, `PromptLang`.
- **Ruby in-jail** (`score_ruby_test.go`): a tiny Ruby module + a minitest thorough suite
  (kills all mutants) and a gappy suite (leaves a survivor), through the real jail. The
  survivor mutant must survive the gappy suite (mirror the Python lesson: use an
  always-true-style mutant the gappy assertion misses but the thorough one catches). Skips
  cleanly when `ruby`/jail are unavailable (this dev host's bwrap userns is blocked).
- Manual out-of-band validation with real `ruby` (as done for Python) before merge.

## Provisioning + rollout

1. CI `validate` job: install `ruby` + the `rspec` gem (non-fatal, like the pytest step).
2. Brain host operator step (documented): `ssh hetzner 'apt-get install -y ruby && gem install rspec --user-install'` (or the host's package manager); a missing `ruby` ⇒ Ruby
   runs fail closed.
3. Docs: README/ROADMAP note Go + Python + Ruby; SKILL.md updates the plugin list.
4. Follow-on: a live Ruby audit recording; JS/C plugins.
