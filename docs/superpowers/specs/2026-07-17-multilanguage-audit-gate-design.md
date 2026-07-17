# Multi-Language Audit Gate — Design

**Date:** 2026-07-17
**Status:** Approved for planning
**Author:** Peter Bethke (+ Claude)

## Problem

corral's adversarial audit gate certifies a change **by execution**: a decorrelated
herd plants fault-mutants in the code under review and proves the change's own test
suite kills them, then signs the verdict ("Nemo iudex in causa sua"). Today that gate
is **Go-only**. The frontier agents already read and write Python, JavaScript, Ruby,
and C fluently — the blocker is not cognition, it is the deterministic execution
harness: the jail scaffold, the test command, and the compile-check are all hardcoded
to Go tooling, and the three LLM system prompts hardcode "Go."

This design makes the gate **language-agnostic** via a small plugin seam, and lands
**Python (pytest)** as the first non-Go language, proven end-to-end to a real signed
verdict. JavaScript, Ruby, and C then become one plugin registration each.

## Scope (v1)

- The `internal/lang` plugin seam.
- Two plugins: `go` (behavior-identical to today) and `python` (new, pytest).
- Wiring through `advpool`, `testgen`, and the `certify --adversarial` CLI.
- A real Python self/target audit producing a signed record.
- CI + brain-host provisioning of `python3` + `pytest`.

**Out of scope (explicit follow-ons):** JS/Ruby/C plugins; a container-backed
per-language jail (sandbox already has a `container` backend); tree-sitter AST-based
structural mutation (the multi-language parser already in `internal/repoindex`).

## Global Constraints

- **Go path behavior-identical.** The `go` plugin must reproduce today's scaffold,
  test command (`go test ./...`), compile-check (`go vet ./...`), and test-path
  convention (`foo.go`→`foo_test.go`) exactly. Existing `advpool`/`adequacy` tests
  pass unchanged — this is the regression gate.
- **Fail closed.** An undetectable/unknown language, or a plugin whose `Preflight`
  fails (host lacks the toolchain), must **refuse the run** — never produce a
  certified verdict. The existing invariant that a failed/timed-out jail run never
  reads as a pass (`sandbox.RunGuarded`) is preserved and now covers Python.
- **Offline grading.** The jail runs with network **off**. All plugin commands must
  be runnable with no network at grade time: for Python that means `pytest` is
  **host-present** (installed once, offline), and grading only ever shells
  `python -m py_compile` and `python -m pytest` — never `pip install`.
- **No new external Go dependencies.**

## Architecture

A new **leaf** package `internal/lang` with no dependency on `advpool`, `brain`, or
`testgen`, so all of those may import it without cycles.

```go
// internal/lang/lang.go
package lang

// Plugin is everything the audit gate needs to grade one self-contained
// source file + its test suite in a given language.
type Plugin interface {
    Name() string                                  // "go", "python"
    Detect(codePath string) bool                   // by file extension
    Scaffold() map[string]string                   // base workspace files (go.mod / none)
    TestCmd() []string                             // default recursive test command
    CompileCheck(codePath, testPath string) []string // syntax/type check for the authored test
    TestPath(codePath string) string               // sibling test path per convention
    Preflight() error                              // toolchain present? nil ok, else fail CLOSED
    PromptLang() string                            // human language label injected into LLM prompts
}

// Registry resolves plugins by name and by path detection. Fail-closed:
// Resolve/Detect return (nil,false)/error for anything not registered.
func Register(p Plugin)
func ByName(name string) (Plugin, bool)
func Detect(codePath string) (Plugin, bool) // first plugin whose Detect matches
```

### The `go` plugin (`internal/lang/go.go`)

Reproduces current behavior, delegating the `go.mod` string to the existing
`controlgate.LangScaffold("go")` so it is not duplicated:

- `Scaffold()` → `{"go.mod": "module control\ngo 1.26\n"}`
- `TestCmd()` → `["go","test","./..."]` (recursive — the current `advPoolBase` default)
- `CompileCheck(_, _)` → `["go","vet","./..."]` (args ignored — go vet is recursive)
- `TestPath("a/b.go")` → `"a/b_test.go"` (current `advPoolTestPath`)
- `Preflight()` → `go` on PATH
- `PromptLang()` → `"Go"`

### The `python` plugin (`internal/lang/python.go`)

- `Detect()` → `.py`
- `Scaffold()` → `{}` (pytest discovers `test_*.py`; the code file is importable from
  the workspace root)
- `TestCmd()` → `["python","-m","pytest","-q"]`
- `CompileCheck()` → `["python","-m","py_compile", "<codePath>", "<testPath>"]`
  (syntax check for both the code and the authored test, offline, stdlib)
- `TestPath("pkg/foo.py")` → `"pkg/test_foo.py"`
- `Preflight()` → `python3` on PATH **and** `pytest` importable
  (`python -m pytest --version` exits 0); otherwise a clear operator error.
- `PromptLang()` → `"Python"`

## Wire points (the only Go-specific code that moves)

1. `internal/brain/advpool.go`
   - `advPoolBase()` → resolve the run's plugin, return `plugin.Scaffold()` +
     `plugin.TestCmd()`.
   - `advpoolValidator.CompileTest` → `plugin.CompileCheck(codePath, testPath)`.
   - `advPoolTestPath(codePath)` → `plugin.TestPath(codePath)`.
   - Resolve the plugin from the run's `Lang` at the top of the run; run
     `plugin.Preflight()` before any grading and **abort the run on error**.
2. `internal/brain/advpool.go` `AdvPoolRunSpec` and `internal/advpool` `RunState`:
   add a `Lang string` field (threaded end to end, surfaced on the signed verdict's
   `ModelsByRole`-adjacent metadata as `lang`).
3. `internal/testgen/testgen.go`: the `writeTestSystem`, mutant-generator, and critic
   system prompts take the plugin's `PromptLang()` and the test-path convention,
   replacing the three hardcoded "Go" references. The prompt still instructs "return
   only raw <lang> source, no fences."
4. `cmd/corral/certify_adversarial.go`: detect the language from `--code` (registry
   `Detect`), add a `--lang` override, derive the default `--test` sibling via the
   plugin, and send `Lang` on the spec. Unknown language → usage error (exit 2).

## Data flow

```
corral certify --adversarial --code foo.py --goal "…" -- python -m pytest
  └─ CLI: lang.Detect("foo.py") → pyPlugin; test = pyPlugin.TestPath → test_foo.py
  └─ spec{Lang:"python", CodePath, Code, DevTestPath, DevTestCode, TestCmd}
        │ start_adversarial_run (MCP)
        ▼
  brain: p,_ := lang.ByName(spec.Lang); if err := p.Preflight(); err != nil → REFUSE
        │  scorer  → p.Scaffold() + p.TestCmd()
        │  validator→ p.CompileCheck(codePath, testPath)
        │  prompts → p.PromptLang()
        ▼
  adequacy.Score → jail RunTest(files, ["python","-m","pytest","-q"])  (network off)
        ▼
  verdict signed by the identical certify chain (certifyBuild) → record + recording
```

## Error handling

| Condition | Behavior |
|---|---|
| `--code` extension matches no plugin | CLI usage error, exit 2, no run created |
| `spec.Lang` not registered | `start_adversarial_run` errors; no run created |
| `Preflight()` fails (no python3/pytest) | run refuses with operator message; **no certified verdict** |
| Authored test fails `CompileCheck` | test rejected (same path as Go's `go vet` failure) |
| Jail run fails/timeouts | never reads as pass (`sandbox.RunGuarded`, unchanged) |

## Testing strategy

- **Go regression:** table-assert `goPlugin` returns today's exact Scaffold/TestCmd/
  CompileCheck/TestPath; the existing `internal/advpool` and `internal/adequacy`
  suites pass unchanged.
- **Python unit:** `Detect`, `TestPath`, `CompileCheck` argv, `Preflight` (present vs
  simulated-absent), registry resolution + fail-closed on unknown.
- **Python in-jail (hermetic):** an `adequacy.Score` test with a tiny Python module
  and two suites — a thorough one (all mutants killed → kill_rate 1.0) and a gappy one
  (survivors > 0) — proving both kill and survive work through pytest in the bwrap
  jail. Gated on `python3`+`pytest` being present (skips with a clear message if not,
  so local dev without pytest is not blocked, but CI — which provisions them — runs
  it).
- **e2e:** a real `certify --adversarial` run against a Python target on the live
  herd, producing a real signed record (the part-2 hero recording).
- **Provisioning:** the self-hosted `validate` runner and the brain host install
  `python3` + `pytest` offline; documented in the deploy notes.

## Rollout

1. Land `internal/lang` + both plugins + wiring behind the existing
   `CORRALAI_ADVERSARIAL_POOL` gate (already off by default). Go behavior unchanged.
2. Provision python3+pytest on CI + brain host.
3. Capture the Python audit recording; publish to the gallery beside the Go one.
4. Field note: "The herd learns a second language."
5. Follow-ons: JS/Ruby/C plugins; container-backed jail; tree-sitter structural
   mutation.
