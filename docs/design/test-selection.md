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

## Measured (evidence run)

Selection costs one extra instrumented run of the whole suite per scan, and
that run's output is the payload the scan reads back. Measured 2026-08-29 by
running `lang.pyPlugin.Instrument`'s exact `sh -c` script in a venv of each
project's own pinned test dependencies (`requirements-dev.txt` for requests),
on Python 3.14 / coverage 7.16:

| project | suite | tests seen | evidence for the audited file | reduced payload (`corral-selection-2`) | wall time |
|---|---|---|---|---|---|
| pallets/flask | 491 passed | 491 | `src/flask/cli.py` ← 91 tests, 81 static ranges | 1,331,508 bytes | 3.2 s (suite) + 0.41 s (reduce) |
| psf/requests | 620 passed | 620 | `src/requests/adapters.py` ← 234 tests, 36 static ranges | 1,053,331 bytes | 79 s + 0.28 s |

Two facts under those numbers, both learned the expensive way on the first
acceptance run:

- **The tracer is pinned.** coverage.py's `sysmon` core — its default on
  Python 3.12+ — does not support dynamic contexts. It warns "context data may
  be incomplete"; under flask's `filterwarnings = ["error"]` that warning
  failed all 985 tests at setup and every file read as *uncovered*, and on
  requests it recorded 11 of the 234 tests that actually execute
  `adapters.py`. `Instrument` sets `COVERAGE_CORE=ctrace` on both steps.
- **The payload is reduced inside the run.** `coverage json --show-contexts`
  lists every context of every line: on flask (`branch = true`) that is
  **411,563,128 bytes** for the run above. The reducer that now runs after the
  suite, from the coverage API, emits the same facts in a fraction of that,
  as a `corral-selection-2` document that `Select` refuses to confuse with
  anything else. Per file it carries the node ids of the tests that executed
  it, each of those tests' executed LINE RANGES in that file, and the ranges
  executed under no test context at all (import time) — the three facts
  per-mutant selection narrows by. Lines reported at line 0, which coverage
  uses for a module with nothing executable in it, are dropped: line 0 is not
  a source line, and no mutant span can overlap it.

  For scale: the earlier `corral-selection-1` shape, which carried only
  `{file: [node ids]}`, was 331 KB for the same flask run — 1,240× smaller
  than the raw contexts JSON. Carrying every test's line ranges quadruples
  it to the 1.3 MB above, still ~300× smaller than the raw form.

The evidence run must also **pass** (exit 0). A suite whose tests error at
setup executes nothing in those tests, so they are never selected, the
narrowed baseline passes, and a kill rate is reported for a suite the
whole-suite baseline would have refused (#164). Selection is a subset of the
whole suite's guarantees only when the whole suite is green.

The scan bounds the evidence run separately from the coverage pre-flight —
`selectionMaxOutput` (64 MiB, ~50× the measured v2 payload) and
`selectionTimeout` (15 min, ~11× requests) in `cmd/corral/certify_repo.go`.

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

## Appending is not narrowing

pytest treats positional arguments as a UNION of collection roots:
`pytest tests/ tests/test_a.py::test_x` collects all of `tests/`. The GitHub
Action's required `test-command` and corral's own not-collected advice both
recommend exactly the `-- pytest tests/` shape, so on the most common
invocation a selection appended to the operator's command narrowed nothing at
all — while the verdict, the report, the ledger, the warehouse, the
attestation and the cache key every one of them said `coverage-context`.

`Selection.Base` is the operator's command with its collection targets
removed, and everything the selection builds — `Cmd`, the dev pass, the
authored pass — builds on it. A token is a collection target only when it is
not an option, not the separate value of one (an explicit table of the pytest
options that take a separate value), and either contains `::` or names a path
that exists in the checkout. Anything else is left exactly where the operator
put it: it survived the evidence run, so pytest accepted it as something
other than a missing path, and removing it would change a command that
demonstrably works.

## Per mutant

Selection narrows a file to the tests that execute it. Per-mutant selection
narrows each MUTANT to the tests that execute the lines it changed — the
same evidence, read at a finer grain, because a mutant on line 41 is not
answered by a test that only ever ran line 12.

Each mutant carries the span its anchor occupied in the original file, and
`ForSpan` turns that span plus the file's line evidence into one command. It
never returns an empty command: corral reports what it RAN, not what
coverage predicted, so every fallback runs the file's whole selection and
says which fallback it was.

| rule | what it means | what runs |
|---|---|---|
| `lines` | a strict subset of the file's tests reaches the span | that subset |
| `static` | the span touches an import-time line | the file's whole selection |
| `unreached` | no test's recorded lines reach the span | the file's whole selection |
| `file` | no span, or evidence that cannot narrow (no lines; a selection whose node ids overflowed the argv cap and collapsed to test files) | the file's whole selection |

### What is disclosed, and where

A per-mutant run is a DIFFERENT measurement from a per-file one, so it is
disclosed rather than folded in. `TestSelection.Method` becomes
`coverage-lines` (not the file selection's `coverage-context`), which also
keys the verdict cache apart. Beside it:

- **`per_mutant`** — each mutant was graded by its own command.
- **the spread** — `min`/`median`/`max` tests per graded mutant. `234 of 620`
  is then the file's UNION and no mutant's own denominator, and without the
  spread a reader would reasonably take 234 for what every mutant faced. The
  spread is **absent, never `{0,0,0}`**, when nothing measured one — a
  per-mutant run whose every mutant was rejected by the compile gate measured
  no spread, and three zeros would be a range nobody measured.
- **the rule counts** — how many mutants got their command by each rule. The
  spread says how much the narrowing narrowed; this says how much of it was
  narrowing at all. A run reported as `coverage-lines` whose mutants are
  mostly `static` or `unreached` ran the file's whole selection for them.

They travel together, at the grain the grading happened: the verdict, the
report line (`graded by 234 of 620 tests — 3 to 41 per mutant, median 9
(coverage-lines; 4 static, 1 unreached, 2 file)`), `scan_mutants.tests_run`
and `.selection_rule` per mutant, the signed audit statement, and the
warehouse row. Those two ledger columns describe the DEV pass; `proven`
describes the authored pass, which is never narrowed per mutant.

### Residuals

Beyond the design's own two — **dynamic dispatch** (`getattr`, `exec`,
monkeypatching, a metaclass registry: a test can depend on a line it never
executed under coverage, which the static rule catches only for the
import-time case) and **branch coverage not consulted** (`lines`, not arcs:
a mutant that flips a condition runs every test that executes the line —
correct, just not maximally narrow) — two more are known and stated rather
than fixed:

- **Non-function-scoped fixtures and lazy imports attribute shared lines to
  ONE test.** A session-, module- or class-scoped fixture's lines execute
  under the FIRST test's `|setup` context only; a module imported lazily
  inside a test records its module-level lines under that test rather than
  as static. A mutant on such a line is then graded by one test when it
  affects the whole suite. The direction is **false-SURVIVOR** — the safe
  side, over-reporting gaps rather than crediting the suite — and it is
  partly self-disclosing, since the mutant's row reads `tests_run 1`. No fix
  in this wave: the context string cannot reveal a fixture's scope, and
  treating every `|setup` line as static would erase the win on exactly the
  fixture-heavy files selection helps most. A heuristic (a line executed
  ONLY under the `|setup` context of exactly one test is static) is
  ledgered, to be justified by measurement rather than shipped blind.
- **A per-mutant subset is never validated on unmutated code.** The baseline
  runs the FILE's selection, so a subset with an order or fixture dependency
  could fail on clean code and be scored as a kill. The direction here is
  the other one — **false-KILL**, over-crediting the suite. The acceptance
  run's per-mutant versus whole-suite comparison on `adapters.py` is the
  measurement that would show it: a divergence beyond the generator's own
  swing would justify a compliant re-check per distinct subset, which is the
  follow-up rather than this wave's work.

### Measured

Run 2026-08-30, same setup as the table above (all `gemini-3.6-flash`,
`--critic-model off`, `--substrate workspace`, 24 cores), against the per-file
numbers from #163's acceptance the day before:

| target | mode | graded by | per mutant (min–max, median) | kill rate | survivors | proven | wall |
|---|---|---|---|---|---|---|---|
| `requests/adapters.py` | **per mutant** | 234 of 620 | **1–234, median 158** | 0.55 | 18 | 18 | 79m32s |
| same file, same session | whole suite (control) | 620 | — | 0.50 | 20 | 15 | 86m24s |
| same file, previous day | per file (#163) | 234 of 620 | — | 0.53 | 18 | 17 | 79m07s |
| `flask/cli.py` | **per mutant** | 91 of 491 | **1–91, median 14** | 0.50 | 20 | 0 `[TEST UNSOUND]` | 4m52s |
| same file, previous day | per file (#163) | 91 of 491 | — | 0.50 | 20 | 0 `[TEST UNSOUND]` | 4m09s |

What that says, plainly:

- **The measurement is faithful.** Per-mutant, per-file and whole-suite agree
  within the generator's own run-to-run swing on `adapters.py` (0.55 / 0.53 /
  0.50), and the two soundness residuals above did not show themselves — a
  false-kill drift would have pushed the per-mutant rate up, not held it.
- **The narrowing is real and the wall clock did not move.** On
  `adapters.py` the median mutant is reached by 158 of the 234 tests: the
  mutants concentrate in `HTTPAdapter.send`, which almost every test
  exercises, so "only the tests that reach the span" is most of the file's
  tests. On `cli.py` the median is 14 of 91 — a 6× narrowing — on a suite
  that takes three seconds, where the run is dominated by the writer's
  attempts. Neither file is the shape per-mutant selection pays off on: that
  shape is a *slow* suite whose mutants land in *cold* functions, and it is
  not measured here.
- **The authored pass is now the cost.** On `adapters.py`, 18 survivors were
  each proven against the full per-file selection plus writer retries; that
  pass is unchanged by design and is a large share of the 79 minutes. The
  authored test is named explicitly on the command, so narrowing the *rest*
  of that command to the survivor's own span is sound and is the next step
  — not taken here, because it is a separate measurement change.

Every row above was produced with the disclosure in place: the
`1–234, median 158` is the report line's own text, and the same numbers are
in `scan_mutants`, the attestation and the warehouse row for those scans.

### The authored pass proves alone

The acceptance table above put the authored pass at the top of the cost:
each survivor of the dev pass was proven against the file's full selection
plus the authored test, with the writer's retries on top. But the mutants in
that pass are *survivors* — no selected dev test killed them — so re-running
those tests cannot kill them either. The only test that can is the authored
one.

So, whenever the run has a selector, the authored pass grades each survivor
with **the authored test alone** (`base + authored path`); the compliance
baseline and the canary keep the shared command, because they ask whether
the authored test is real, not whether it kills. This is cheaper (one test
per survivor instead of the file's selection) and stricter: a dev test that
flaked during the authored pass used to count as the authored test proving a
gap. Under this rule a proven count can only be the authored test's own.

That is a change in what `proven` means — it can only go *down* — so it is
`VerdictGeneration` "4", the verdict carries `authored_alone`, the report
line appends `proven by the authored test alone`, and the attestation signs
`provenByAuthoredAlone`. Runs with no selector (`--whole-suite`, `--local`,
Ruby, `node:test`) prove the old way and say nothing new.

## Part B — not built

A per-worker tree on the workspace substrate, letting concurrent scoring runs
avoid the CPython `__pycache__` staleness hazard (documented in
`internal/lang/lang.go`) without a fresh workspace per mutant, is designed
but not implemented, along with the concurrency-safety probe that would prove
it closes the hazard. Nothing in this document depends on it landing.
