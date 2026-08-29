<!-- SPDX-License-Identifier: Elastic-2.0 -->

# Test selection: which tests grade a file, and why the record says

**Status:** shipped, 2026-08-29. Coverage-guided selection is the default for
Python; `--scope-tests` is removed.

## The cost expression, and today's measurement

Scoring runs the operator's test command once per mutant, so an audit costs
roughly *mutants × that command's runtime*. That multiplier is the whole
economics of the tool: it decides whether an audit takes a minute or an
afternoon, and it decides it per file, because every file re-runs whatever
command is chosen for it.

corral's own self-audit is the worked example. Run `33226112930` audited
`cmd/corral/certify_repo.go`: 96 minutes of dev scoring on a 4-vCPU hosted
runner, roughly 2.3 minutes per mutant, and 74 seconds for the same mutants
locally on 24 cores. Kill rate 0.72, 28 of 39 mutants killed, 11 survivors.
The gap between 96 minutes and 74 seconds is entirely the runner's core
count — the *number* of mutants and the *shape* of the suite were unchanged.
That number motivates selection: if a file's own tests run in a fraction of
the whole suite's time, grading it against only those tests divides the
multiplier by the same fraction, for free, with no change to what gets
proven.

## Why selection is by execution evidence only

The obvious shortcut is a filename convention: pair `foo.py` with
`test_foo.py` and run only that file's test. It was tried, and it inverted a
verdict.

| file | whole-suite kill rate | paired-file kill rate | whole-suite time | paired-file time |
|---|---|---|---|---|
| `flask/cli.py` | 0.65 | 0.68 | 5m20s | 2m46s |
| `requests/adapters.py` | **1.00** | **0.00** | 11m | 58s |

`flask/cli.py` looks like the promise of the shortcut: nearly the same
number, a fraction of the time. `requests/adapters.py` is why the shortcut is
not shipped: its real tests live in `test_requests.py`, not in the
conventionally-paired `test_adapters.py`, so scoping to the pair reported
every mutant as a survivor in a file whose whole suite kills all of them. A
filename rule cannot see that — it does not read what a test imports or
calls, it reads what a test is *named*. Coverage does: it observes, per
mutant run, which tests actually executed the file, and that observation is
immune to naming convention by construction.

So selection is keyed on **execution evidence** — coverage contexts, or a
harness's own module graph — never on a filename pattern, no matter how
common the pattern is in practice.

## The locked decisions

| decision | value |
|---|---|
| default | on — a project with a working selector uses it without an opt-in flag |
| opt-out | `--whole-suite` grades every file against the full suite, deliberately |
| disclosure | every verdict, report line, and ledger row names which measurement produced it |
| verdict generation | bumped to `"2"` — a cached verdict from before this change cannot be reused unnoticed |
| uncovered files | kill rate is **withheld**, not reported as zero; the pool still writes and proves tests against the file, and it fails `--min-kill-rate` |
| `--scope-tests` | removed — its paired-file scoping is the inversion above, and it offered no disclosure of which measurement it was |

The report line makes the measurement explicit either way:

```
graded by 14 of 1,431 tests (coverage-context)
graded by the whole suite (no selector for ruby)
```

and an uncovered file prints its own marker rather than a number:

```
[UNCOVERED — no test executes this file]
```

## Per-language evidence

| language | evidence today | mechanism | status |
|---|---|---|---|
| Python | coverage contexts | `pytest-cov`, one instrumented run of the whole suite, per-test contexts read back per file | shipped |
| JavaScript / TypeScript | module graph | the harness's own dependency graph from source file to spec file | next, not built |
| Go | package dependencies | the package graph already used for compile-checking | next, not built |
| Ruby | none | no evidence source identified yet | whole-suite, disclosed |

A language without a selector, a project without the required tool
(`pytest-cov` absent, say), or a run whose evidence cannot be read all fall
back to the whole suite, and the fallback reason is recorded verbatim —
`no selector for ruby`, or the read error — rather than silently grading
against nothing.

## The authored-test consequence

The pool's own test — the one written to prove a survivor is a real gap —
never appears in the coverage evidence, because no run has executed it yet
when selection happens. `WithAuthoredTest` appends it explicitly to whatever
the selection already chose, so it is always collected alongside the
selected tests; for an uncovered file, where selection chose nothing, the
authored test runs alone rather than dragging in the whole suite it was
never covered by.

## Part B — not built

A per-worker tree on the workspace substrate, letting concurrent scoring runs
avoid the CPython `__pycache__` staleness hazard (documented in
`internal/lang/lang.go`) without a fresh workspace per mutant, is designed
but not implemented, along with the concurrency-safety probe that would prove
it closes the hazard. Nothing in this document depends on it landing.
