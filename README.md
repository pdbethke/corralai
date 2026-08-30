# Corral — prove your tests would catch it

[![CI](https://github.com/pdbethke/corralai/actions/workflows/deploy.yml/badge.svg?branch=main)](https://github.com/pdbethke/corralai/actions/workflows/deploy.yml)
[![License: Elastic 2.0](https://img.shields.io/badge/license-Elastic--2.0-e8a838)](LICENSE)
[![docs](https://img.shields.io/badge/docs-corralai.dev-2f6f4e)](https://corralai.dev/docs/getting-started/)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/pdbethke/corralai/badge)](https://securityscorecards.dev/viewer/?uri=github.com/pdbethke/corralai)

Corral breaks your code on purpose and checks whether your tests notice. It plants faults that violate a stated guarantee, runs **your own suite** against each one in a sandbox, and reports how many it killed — measured by execution, never taken on a model's word.

***Nemo iudex in causa sua*** — no one may be judge in their own cause. The one who
wrote the code doesn't get to certify it: the verdict is **measured by execution**, by
a **decorrelated** party, behind a **human gate**. That maxim isn't a slogan here — it's
the constraint everything below is built on. ([why it's the whole design](https://corralai.dev/field-notes/nemo-iudex/))

> **An audit for software change.** Certify a change **by execution, not opinion**:
> run the check in a jail, measure the result yourself, sign a tamper-evident
> record, and gate the merge. Across any model (local 7B to frontier), behind real
> fences, human-gated, every run recorded and replayable.

In the age of AI, the thing that wrote the code tends to be the thing that grades
it — the model writes the code and says it's good, or writes the tests for its own
code and reports that they pass. That's the author reading his own verdict into the
record, and authors are kind to themselves. Corral is built the other way: the party
that did the work never certifies the work, nothing is taken on a model's say-so, and
the one number that means anything is the one no model was allowed to author — it's
what happened when the check actually ran, in a sandbox.

## `corral certify --local` — the whole thing in one command

The fastest way to see it is `--local`: a complete adversarial audit, in-process, off
your own key, no server.

```bash
go install github.com/pdbethke/corralai/cmd/corral@latest
export ANTHROPIC_API_KEY=sk-ant-...     # or OPENAI_/GEMINI_/OPENROUTER_API_KEY

corral certify --local \
  --code path/to/your/file.py \
  --goal "what this code must guarantee" \
  --writer-model claude-sonnet-5 \
  --mutant-model claude-sonnet-5 \
  --critic-model claude-haiku-4-5 \
  --out verdict.json \
  -- python -m pytest
```

**Those model names are an example, not a default — corral has none.** Every seat
is yours to name, from whichever provider you have a key for; the models above are
simply what *we* run. The one rule is that the **critic must differ from the
writer** — the run refuses to start where they collapse onto one model. That rule
enforces *distinctness*, which is necessary and, on our own evidence, not
sufficient: two frontier models from different labs still shared three quarters
of their blind spots ([Jaccard 0.750 over 13
survivors](https://corralai.dev/field-notes/both-models-missed-the-same-nine/)).
Distinctness is a property rather than a vendor, so any two different models
satisfy the *rule* — how much independence that actually buys is a separate
question, and corral measures it rather than assuming it. `--critic-model off`
drops the critic entirely (it's advisory and never gates the verdict). A run with an
unnamed seat is refused, and the refusal tells you which provider credentials it can
actually see.

### See it work first: `corral demo`

One command, no setup beyond a provider key. It writes a small Go package with a
five-clause password rule and a test that checks only two of them, then audits it
with the real `certify --local`:

```bash
corral demo --writer-model <model> --mutant-model <model> --critic-model <model>
```

You need a Go toolchain — you installed corral with one — and one key. No venv, no
database, no fixtures, nothing of yours to configure. It leaves the project on disk
so you can read the test and see what it never asserts.

Then point it at your own code, and run `corral doctor` first: the environment stops
an audit far more often than the tests do.

It asks the question you can never answer honestly about your own code — *do my tests
actually test anything, or do they just pass?* — and answers it by execution:

- A **mutant-generator** seeds real, goal-violating bugs into your code.
- Your **own test suite** is run against every one of them, **in a jail** — the
  fraction it catches (the *kill-rate*) is the adequacy score, measured, never a
  self-report.
- A **test-writer** authors a compiling test that kills whatever your suite missed,
  correcting itself when its test doesn't compile rather than blindly repeating. A
  surviving mutant is *disclosed, unadjudicated* — a real gap, or an equivalent
  mutant no test can catch, your call. Only a survivor a compiling test actually
  kills is a **proven** gap. Zero proven gaps means *nothing was proven this run* —
  never *your tests are fine*. ([why that distinction is the whole
  point](#when-corral-proves-a-gap-and-when-it-proves-nothing))
- A **test-critic** — always a *different* model, enforced — reads your
  suite cold and flags vacuous, designed-to-pass tests. Its opinion is carried as
  **unverified advice; it never gates the verdict.**

You get a signed verdict — `certified` or `needs-review` — printed and written to a
local, tamper-evident ledger. `--out` also writes it as a self-contained file you
re-check anytime, offline:

```bash
corral certify verify verdict.json --pubkey "$(corral certify pubkey)" --allow-unanchored
```

`--allow-unanchored` is deliberate: a record minted off *your own* key, with no
server, was never submitted to a public witness, so it's "signed by you, not
third-party attested" — a weaker claim, and verify makes you say so out loud.
Records the **brain** signs are stronger: it anchors every one to a public,
append-only transparency log (**Sigstore Rekor**, `CORRALAI_REKOR_URL`) at signing
time and carries the inclusion proof *inside* the record, so `corral certify verify`
checks it **offline** against the TUF-rooted Rekor key — no round trip to the brain or
the log — and tampering is detectable even by someone who doesn't trust the brain. A
witness outage degrades honestly (`anchored=false`, never a fabricated proof); verify
then refuses unless you pass `--allow-unanchored`.

Go, Python, Ruby, JavaScript and TypeScript — the language is inferred from
`--code`'s extension, each a plugin in `internal/lang`. The authored test is
written in **your project's own harness** (it is shown your existing test file
and told to match it), so vitest, jest, pytest, minitest and RSpec all work
without configuration.

**Where each language's support actually stands.** A tool that argues for
execution over self-report should not claim a language on the strength of a
passing unit test, so here is what has actually been run, against what:

| language | evidence | verdict |
|---|---|---|
| **Go** | this repo's own entry point, audited by the GitHub Action on a real commit | 40 faults planted, 10 killed, **0.25**, 1 gap proven |
| **Python** | 6 whole-repo scans of [Flask](https://github.com/pallets/flask) | 10 files audited, **48 gaps proven by execution** |
| **TypeScript** | a private SDK, and [vercel/ms](https://github.com/vercel/ms) | 0.79 / **0.94**, 3 and 2 gaps proven |
| **JavaScript** | [vercel/ms](https://github.com/vercel/ms) under jest | **CERTIFIED**, 33 of 35 killed, 2 gaps proven |
| **Ruby** | [minitest](https://github.com/minitest/minitest) itself | 36 of 40 killed, **0.90** |

Go and Python are exercised hardest — Go continuously in CI, Python across
repeated whole-repo scans. **TypeScript, JavaScript and Ruby each rest on a
single third-party repository**, which is enough to show the plugin works and
is not evidence about the ecosystem. Treat them accordingly.

One number in that table deserves its own warning: kill rate moves **run to
run on the same file with the same suite**, because the faults are generated
afresh each time. Measured swing on one file: 0.55 to 0.80. A single run is
evidence of specific gaps, **not a grade** — never quote one as a score.

C is next.

> **Whole-repo scanning is not equally strong across those languages.** `certify
> --local` audits any single file you name, in any of them — you give it the path,
> so nothing has to be discovered. `certify --repo` must first *find* the files, by
> pairing each source file with its test using naming conventions, and that pairing
> is much better at some ecosystems than others. Measured on real repos:
> `rubocop/rubocop` **736** candidates, `gin-gonic/gin` **29**, `pallets/flask`
> **9** — and `expressjs/express` **zero**, because common JavaScript layouts don't
> match the conventions corral knows.
>
> **Read those as fractions, not totals.** flask's 9 candidates come out of **236
> files walked** — 153 aren't a language corral reads, 27 are themselves tests, and
> 47 are source files whose tests convention could not pair. So a default
> `certify --repo` on flask audits a handful of files, not a repository. Every
> exclusion is reported with a machine-stable reason (`corral certify --repo
> --dry-run --json` gives you the whole inventory for free, no key and no money),
> so you can always see exactly what was and wasn't looked at — but an audit that
> accounts honestly for its blind spots still has them. Widening that fraction is
> the main thing standing between this and "audit your repo", and the `--tests` map
> below is the current lever. **If a `--repo` scan of your JS/TS project
> reports `0 candidates`, that's a limitation of corral's pairing, not a verdict on
> your tests** — and there are two ways out. Point corral at the files directly with
> `--local --code <path> --test <path>`, which never uses pairing at all; or hand
> `certify --repo` a **`--tests` map** (JSON, source path → test path) and let it scan
> the repo normally. Express is the worked example: convention finds nothing, because
> `lib/response.js` is covered by `test/res.send.js`, `res.json.js` and others and no
> filename rule derives `response → res` — but a six-line map takes it from **0 to 6
> auditable files**. A mapping to a file that doesn't exist is refused rather than
> silently falling back, so a typo is visible. Both the `express` zero AND the mapped
> result are pinned in CI so neither can be quietly papered over; see [the foreign-repo
> sweep](#the-gate--for-a-repo-and-for-a-control-owner) below.

One key can satisfy the distinctness rule on its own — name two different models
from the same provider (Sonnet writing and mutating, Haiku critiquing, say) — though
two models from one lab are the WEAKEST form of it and share the most lineage. Naming
`--critic-model gemini-3.6-flash` plus `GEMINI_API_KEY` (or `GOOGLE_API_KEY`) routes
the critic to Gemini via the OpenAI-compatible Google endpoint, a real cross-vendor
critic, while the writer and mutant-generator stay on whatever you named — a missing
key fails the run closed rather than silently falling back.

Cross-vendor routing is **one-directional by design**: it applies to the critic, and
only when you have *not* pinned `MODEL_BACKEND`. Setting `MODEL_BACKEND` explicitly
means "every role on this endpoint," so pointing a whole run at one vendor and then
naming a critic from a different one sends that model to the wrong endpoint. For a
deliberately single-vendor run, use **`--critic-model off`**: the critic is advisory
and never gates the verdict, so dropping it changes nothing about the
execution-proven result — it only removes the second opinion. (Useful when a vendor
offers just one model you're willing to run, since the critic must otherwise differ
from the writer.) Full walkthrough of a real verdict: **[the "first audit"
guide](https://corralai.dev/docs/first-audit/)**.

### Before you spend a run: `corral doctor`

An audit costs real money and real minutes, and it is almost always the
*environment* that stops one — the sandbox won't start, the toolchain is invisible
inside it, the key for the model you assigned is missing, the file has no paired
test. Discovered one at a time, each of those costs another run, and most of them
cost money to learn. `doctor` checks
them all up front for **free** — no model is ever called — in the order the audit
itself would hit them, so the first `FAIL` is the first thing to fix:

```bash
corral doctor --code path/to/your/file.py \
  --writer-model claude-sonnet-5 --mutant-model claude-sonnet-5 \
  -- python -m pytest
```

```
  [ok  ] sandbox starts
  [ok  ] toolchain reachable inside the sandbox
  [FAIL] credential for mutant-generator (claude-sonnet-5)
         agentbackend: ForModel: model "claude-sonnet-5" needs an Anthropic key — set ANTHROPIC_API_KEY

1 check(s) failed — fix these before spending a run.
```

Every argument is optional and each one unlocks more checks: `--code`/`--test` add
the test-pairing check, a test command after `--` adds the in-sandbox toolchain
check, and `--mutant-model`/`--writer-model`/`--critic-model` check the credential
for exactly the models you plan to route to. It exits non-zero if anything failed.

Two things it deliberately does **not** check, because both need a real seeded
workspace: whether your suite passes on *unmutated* code inside the sandbox — the
most common way an audit dies — and whether a multi-file project needs
`--repo-dir` (it does; the bare `--code` form seeds only that file and its test).
`certify --local` reports the first as `COULD-NOT-GRADE`, with the runner's own
output.

### When corral proves a gap, and when it proves nothing

**A compiling test that passes is not evidence of anything.** It may simply never
have been collected by your test runner — a project that confines discovery to a
test root, like flask's `testpaths = ["tests"]`, will not run a file written
elsewhere. A tool that counted that as a clean result would report *your tests are
fine* on the strength of a test nobody ran.

So corral writes its authored test into the directory your paired test already lives
in, and then *proves* the run reached it: it plants deliberately invalid source at
that exact path and checks your unmodified command reacts. If it doesn't, the file is
reported `[TEST UNSOUND]` and its proven count is **withheld** rather than reported
as a clean zero.

**What varies is whether the authored test comes back sound — not whether corral can
prove gaps once it does.** Measured on `src/flask/cli.py` in pallets/flask across
four runs: one authored test passed on the unmodified source and then killed **14 of
14** survivors, proving every one by execution; another failed on the unmodified
source and proved nothing. The determining factor was the test's own soundness, and a
test that fails on correct code is now reissued to the writer with the failure fed
back rather than abandoned. (One file, one project: a measurement, not a guarantee
about your repo.)

Which is why zero proven gaps means *nothing was proven this run*, and never *your
tests are fine*.

### Certify a change by its declared check

The other entry point takes any commit and any command:

```bash
corral certify <ref> -- <cmd>      # e.g. corral certify HEAD -- go test ./...
```

It checks `<ref>` out into a jail, runs `<cmd>`, reads the exit code, and writes a
signed record you verify offline with `corral certify verify` — no server required.
`--brain <url>` optionally posts it to a running brain.

## Three constraints the machine enforces

Not a slogan — the code refuses to do otherwise.

1. **The judge is a different judge.** Cross-model checking is **distinct by
   construction**: the model that critiques a suite is *forced* to differ from the one
   that wrote the exposing test — the run refuses to start where they collapse onto
   one model. Most swarm frameworks run one LLM in N roles: parallelism with
   *correlated* blind spots, because the "reviewer" shares the "builder's" failure
   modes when it's the same model. Distinctness raises the floor; it does not buy
   independence outright, and corral is the only tool we know of that **measures**
   the difference rather than asserting it — see `--shadow-writer-model` below. Bring Claude, Gemini, GPT, anything
   OpenAI-compatible, or a local model — no lock-in.
2. **The verdict is measured, not reported.** The gate **runs the actual check** —
   `go test`, the build, the control owner's tests, your suite against the mutants —
   itself, in the jail, and reads the result. A worker's "it passed" is never the
   verdict; it's a claim, and the claim is checked by execution. The correctness call
   is a deterministic bit, not a judgment.
3. **It's built to be contained.** A model that writes and runs code is a security
   problem, so corral starts from *"an agent can be hijacked"* and answers it
   structurally: every command runs behind **fences** (a jail, a credential boundary,
   trust-tiered knowledge), and because all traffic funnels through the brain, every
   action is **recorded and attributable**. Prevention *and* forensics — see
   **[SECURITY.md](SECURITY.md)**.

The name is the metaphor: the **corral** is the enclosure the models work in, the
**fences** are the security boundaries, and the brain corrals a herd of (possibly
different) models — it coordinates and contains, it doesn't do the work itself.

> **Where it's at:** pre-1.0, solo-maintained, tested honestly — every claim in this
> README was run before it was written. Issues and verified-harness PRs welcome.

## The gate — for a repo, and for a control owner

Beyond the one-shot CLI, the headless **brain** daemon runs continuous gates that
branch protection can require:

- **The repo (merge) gate.** A poller watches each covered repo's open PRs; on a new
  head commit it checks the PR out, runs the repo's declared check **in the jail**
  (never a self-report), signs the result, and posts a `corral/gate` status.
- **The control gate.** The same poll-and-jail pattern, but it runs the **control
  owner's** independently-vetted tests against the PR head — not the repo's own check —
  and posts a distinct `corral/control-gate` status. The person accountable for code
  they didn't write sets the bar. It's separation of duties, mechanized: *a judge may
  not certify herself.*
- **Multi-forge primitives** (`internal/repo`) back both: clone, checkout,
  commit/push, and PR/review calls against **GitHub, GitLab, and Gitea**, including
  self-hosted instances (`CORRALAI_FORGES` maps a host to its type, API base, and
  token) — each forge's token stays isolated to its own host.

The same adversarial audit `--local` runs is available on the brain for a wired repo,
via the admin-only `start_adversarial_run` MCP tool (see [the flags reference
below](#the-audit-flags)).

**No brain required — the GitHub Action.** `pdbethke/corralai@main` runs `corral
certify --repo` straight in your own CI job, on the checkout that's already
there: no jail, no brain, no separate infra. It mutates the runner's checkout
in place and grades each mutant with your own test command — the runner
itself is the isolation boundary, so this is for CI, not a working tree you
care about. Scoped to the PR's changed files by default (auditing every file
on every PR is expensive — roughly 84 suite runs per file); a whole-repo run
is opt-in. By default a weak-but-gradable kill rate still exits 0 — the
opt-in `min-kill-rate` input (`--min-kill-rate` on the CLI) fails the run
when any *individual* audited file scores below the threshold you set. See
**[docs/corral/github-action.md](docs/corral/github-action.md)**.

**Coverage pre-flight (`--preflight`, CLI only, Go and Python).** `certify
--repo --preflight` runs the project's test suite **one extra time**, with
coverage instrumentation, and reports which source files it never touches at
all — a whole-repo inventory for the cost of one suite run, instead of the
~84-suite-runs-per-file the adversarial audit itself costs. It's
**coverage-grade evidence, not proof**: instrumentation has blind spots
(subprocesses, dynamic imports, native extensions), so the report separates
what it actually knows into three buckets — files the suite **executed**,
files it **measured and never executed** (the real finding, printed by
name), and files it **never measured at all** (printed only as a count,
never named — naming one would be an accusation about a file the run never
looked at). On **both** Go and Python, "executed" can mean "imported" rather
than "tested": Go runs `init()`/var-initializer code at import time, and in
Python every module-scope `def` and `class` is a counted statement, so
importing a module clears it outright — Python's exposure here is the wider
of the two (see [docs/corral/github-action.md](docs/corral/github-action.md)
for both measurements). Implemented for **Go and Python only** — Ruby, JS, and TS have
no coverage-pre-flight plugin yet, so a scan in one of those languages reports
that it could not run and names nothing, rather than guessing. A scan whose
candidates span more than one language usually declines the same way — one
instrumented run can't cover two — **unless** an explicit `-- <test-command>`
unambiguously names exactly one of them (e.g. a Python+TypeScript repo with
`-- pytest -q`: TypeScript has no coverage plugin at all, so Python is the
only candidate, not merely the likeliest one — that repo is instrumented, its
TypeScript files simply fall into "never measured"). Two languages that could
both plausibly own the given command (e.g. Go, whose coverage command accepts
any test invocation by design) still decline as ambiguous. Same fail-closed
rule when the coverage tool itself is missing from the runner. Not yet wired
into the GitHub Action as an input — today it's a `corral certify --repo`
flag only.

**Per-file timeout (`--timeout`, CLI only).** `certify --repo` shares
`--local`'s own `--timeout` flag (default 10 minutes): the wall-clock budget
each file's run gets before the pool is forced to a `needs-review` verdict
instead of converging. A file whose run hits this deadline after the dev
suite's own kill-rate was already measured is reported as **audited**, not
dropped — marked `[TIMED OUT — pool did not converge]` in the weakest-files
list (and `timed_out` in `--record`'s ledger row) so it never reads as a
clean convergence. Raise it for a large file that needs more room — on the
CLI, or through the GitHub Action's `timeout` input.
**A scan whose only audited files are all `[TIMED OUT]` still exits non-zero**
even without `--min-kill-rate`: the dev-adequacy measurement is real, but
corral's own adversarial verification (test-writer, critic) never ran to
completion for anything the scan touched, so there is nothing for a merge
gate to certify — the report says `DID NOT FINISH` in that case. A scan
where only SOME files timed out and the rest converged keeps today's
exit-code logic. **`certify --local`'s own exit code for a banked,
measured timeout changes from `1` to `3`** (both non-zero, so a CI gate
still fails either way) — `3` is the same code an ordinary converged
`needs-review` verdict already uses, since a banked timeout now prints a
real (marked) verdict instead of a bare internal-failure error.

**The scan ledger (`--record`, CLI only).** `certify --repo --record` keeps
what every scan already computes and normally just prints: a row per file
the scan audited (with its kill rate) or rejected (with a machine-stable
reason), plus one header row per invocation, in an embedded DuckDB file
(`--record-db <path>`, default `$CORRALAI_SCANS_DB` or
`~/.claude/corralai_scans.duckdb`). It's opt-in and off by default, and a
recording failure — a full disk, a locked DuckDB file — never changes the
scan's own verdict or exit code: it prints a loud line on stderr and the
scan's result stands, because this command's exit code is a CI merge gate
and a ledger write must never be able to red-build a PR over bookkeeping.
**One writer at a time**: DuckDB locks its file for the process holding it
open, so in a parallel CI matrix only the first concurrent `--record` run
actually lands — the rest print the same loud fail-open line and lose
their ledger entry, though their own gate verdict is unaffected. That's
the right trade for a merge gate (a scan's pass/fail must never depend on
winning a file lock), but point `--record-db` at a per-job path if you
need every matrix leg's ledger kept. `--record` here is a **bool** —
unlike `certify --local --record <file>.json` on the sibling subcommand,
which takes a replayable-tape *path* — so don't hand it one; `--record-db`
is where the ledger path goes.

**Reading it back (`corral scans`).** The ledger is only worth writing if
something can query it, so:

```bash
corral scans list                    # recent scans: repo, substrate, audited, kill rate
corral scans show <id>               # per-file dispositions for one scan
corral scans show <id> --evidence    # + the pool's own authored test
corral scans show <id> --timing      # + where each file's wall clock went
```

A local DuckDB file, no brain required, read-only by design. `show` renders
what a bare number cannot: **why a proven-gap count of 0 is 0** — the pool
never authored a compiling test, the test it authored never genuinely graded,
or a perfectly sound test ran and proved nothing ("tried and missed"). Those
are very different facts and the report has always known the difference.
`--evidence` prints the authored test **even when it proved nothing** — that
is the case worth reading, and it is stored precisely so diagnosing it never
requires paying for a second audit. A never-graded scan renders `—`, never
`0.00`: corral does not report a score for something it never measured.

`--timing` prints one line per audited file naming every phase — selection,
generation, pool, dev pass (with how long grading one mutant took, median and
worst), authored, critic, total. It is the same line `certify --repo` prints
when the audit finishes, from the same helper, so a stored scan and the run
that produced it read identically. **A phase that did not run prints `—`, never
`0s`**: the jail substrate builds no trees, `--critic-model off` runs no critic,
and reporting either as zero seconds would tell a cost model those phases are
free.

The **selection** pass is one instrumented run shared by the whole scan, so it
is announced once above the file lines (`selection 1m32s (once per scan)`) and
recorded once, on the scan row. It is still named on each file's line — a
readout has to account for every phase that file's audit waited on — but it is
deliberately **not** part of a file's `total`, so `sum(total_ms)` across a
scan's files is a sound number rather than one that grows with the file count.

**The warehouse (`--push`, CLI and Action).** `certify --repo --push <path or
md:<db>>` appends this scan's per-file verdicts to a DuckDB **you** own —
corral has no hosted tier and keeps nothing, so any DuckDB works, and this is
a destination rather than a lock-in. Append-only. Every row carries the local
scan ledger's row id (`0` when `--record` wasn't given) and, traceable only
with `--attest`, the sha256 of the signed statement it came from — so a row
can be checked against something a third party can verify, and without
`--attest` that column is honestly empty rather than fabricated. It answers
what one pull request cannot: a single kill rate is a sample — the same
unchanged diff has scored `0.85` and `0.90` — while forty of them, pushed
across forty PRs, are a distribution you can read a trend from. `md:<db>`
targets MotherDuck and reads its token from `motherduck_token`
(`--motherduck-token`, or the Action's `motherduck-token` input) in the
environment.

`--push-source` additionally sends the pool's authored test and the full
verdict JSON — never mutant code, which corral keeps at rest under no
setting. Off by default: without it, a pushed row carries numbers, hashes,
reasons, and model names, and no source leaves the box.

`corral seal` (see `corral --help`) reads the warehouse's `corral_seal`
view back — the union of every push's still-valid verdicts, not any one
scan's snapshot. Running this Action per PR, at scale, is documented in
**[docs/corral/actions-as-swarm.md](docs/corral/actions-as-swarm.md)**;
what a pushed row's timing and cost columns actually mean, quoted from two
real recorded scans, is in
**[docs/design/cost-model.md](docs/design/cost-model.md)**.

**Look before you spend (`--dry-run`, `--json`).** Enumeration needs no model
key, no jail and no money, and it already knows a great deal about your
repository. It reports a per-language profile — how many source files corral can
audit, how many have **no paired test at all**, and how many pairings are
ambiguous — plus a machine-stable reason for every excluded file:

```bash
corral certify --repo . --dry-run            # human report, seconds
corral certify --repo . --dry-run --json     # the same inventory as data
```

```
languages detected:
  python  28 source file(s): 6 auditable, 21 with no paired test, 1 ambiguous (+9 test file(s))
```

The JSON form carries every auditable file with its inferred test pairing and,
for languages corral can parse symbols in, a per-file complexity measure
(`symbols`, `max`, `total` — cyclomatic-style, the same decision-point
approximation gocyclo and radon use). Complexity is **absent** rather than zero
where corral has no extractor, because a `0` would read as "this code is
trivial" when the truth is "never measured".

There is deliberately **no headline percentage**. 6 auditable files out of 130
walked is 4%; out of 6 candidates it is 100%. Neither is the truth, so the
report gives you every term of the funnel and lets you draw the conclusion.

**When convention can't pair your repo (`--tests`).** Pairing matches a source
file to a conventionally-named test. That works when a project names tests after
source files and cannot work when it doesn't — `expressjs/express` tests
`lib/response.js` from `test/res.send.js`, `test/res.json.js`, and no filename
rule derives `response → res`. A rule loose enough to try would pair the *wrong*
files, planting mutants in one file and grading them against another's tests.

So you can say it yourself — a JSON map of source path to test path, consulted
before convention:

```bash
corral certify --repo . --tests corral-tests.json
```

Unmapped files still fall through to convention, so a partial map is normal: you
correct only what convention gets wrong. A mapping to a file that does not exist
is **refused and reported**, never silently ignored — a typo must be visible.

**Which tests grade a file — the ones that execute it (default).** Scoring runs
a test command once per mutant, so an audit costs roughly *mutants × that
command's runtime*. corral runs your suite **once** per scan with per-test
coverage instrumentation, learns which tests actually execute each file, and
grades that file's mutants with only those tests. Every verdict says so —
`graded by 14 of 1,431 tests (coverage-context)` — because it is a different
measurement from the whole suite: *do the tests for this file test it?* rather
than *did anything in the repo happen to catch it?* The selection comes from
execution evidence, never from filenames: scoping by the conventionally-paired
test file was tried in July and inverted a verdict on `requests/adapters.py`
(1.00 → 0.00) because that file's real tests live in `test_requests.py`.
Coverage finds them; a filename rule cannot.

Today this is implemented for Python (pytest with `pytest-cov`). A language
or harness without a selector, a project without `pytest-cov`, or a run whose
evidence cannot be read grades against the **whole suite, and the record says
why** (`graded by the whole suite (no selector for ruby)`). `--whole-suite`
asks for that deliberately. A file no test executes is reported
`[UNCOVERED — no test executes this file]` with its kill rate withheld — the
pool still writes and proves tests against it, and it fails `--min-kill-rate`.

**The foreign-repo sweep (CI, every PR).** `scripts/foreign-sweep.sh` runs
`certify --repo --dry-run` — enumeration, language detection, test pairing,
ambiguity demotion, ranking, and selection, but **no audit and no suite
execution** — against seven SHA-pinned real-world repos nobody on this
project wrote, and diffs the walked/candidate/ambiguous file counts against
the checked-in golden file `testdata/foreign-sweep-expected.tsv`. It exists
because pointing corral at foreign repos surfaces defects the in-repo suite
never can (a suite only ever exercises repos shaped like this one); see the
script's own header comment for the incident that motivated it. It runs in
`.github/workflows/foreign-sweep.yml` on every pull request and every push
to `main` — not just after merge — and needs only network access and the Go
toolchain (no model key, no jail, no language toolchain beyond Go).

If a change legitimately moves one of these counts (a pairing improvement
that finds candidates it used to miss, a new demotion rule, etc.), regenerate
the golden file and commit the diff:

```
rm testdata/foreign-sweep-expected.tsv
FOREIGN_SWEEP_BOOTSTRAP=1 bash scripts/foreign-sweep.sh   # writes a fresh golden file
git diff testdata/foreign-sweep-expected.tsv
```

A missing golden file is refused (exit 1), not silently bootstrapped: if
it's ever lost to a bad rebase or a careless PR, the gate must fail loudly
rather than quietly regenerate itself against whatever the tree happens to
produce and stay green forever. `FOREIGN_SWEEP_BOOTSTRAP=1` is the explicit
opt-in that says "yes, I mean to write a new golden file right now."

**Moving these numbers is a deliberate act, not something to do reflexively
to make the gate pass.** The diff is the whole point — review it like a
production change, because a golden file that silently absorbs a regression
(a pairing rule that quietly finds *fewer* real candidates, say) is worse
than no gate at all. `expressjs/express` is pinned to pair **zero** test
candidates on purpose: that's a known JS/TS test-pairing limitation, not a
bug, and if a future change makes that number nonzero, look hard at *why*
before accepting the new golden file.

That `express` pin is a **one-directional** canary: it catches JS/TS test
pairing going from zero candidates to nonzero, but would not notice the
JS/TS plugin being removed or `express` being dropped from language
detection entirely — a "still zero" diff looks identical to both "still
broken as expected" and "the whole plugin silently disappeared." `gin-gonic/gin`
is the positive canary that covers that direction (a live, working plugin
whose count must never go to zero). The asymmetry is inherent to pinning a
zero and doesn't prove more than it does.

## A knowledge corpus that makes every audit sharper

Audit knowledge compounds instead of dying with each context.

- **The corpus (`CORRAL.md`).** A repo carries its working knowledge as markdown in
  the repo itself — `CORRAL.md` at the root, `docs/corral/*.md` as the corpus. One
  corpus, four readers: developers read it as onboarding; any developer's coding agent
  queries it conversationally (point `.mcp.json` at the brain and ask); the herd
  searches it before working; and it grows the way code does — through pull requests,
  where **code review is the trust gate for knowledge exactly as it is for code**.
  Ingested as *advisory* memory (searchable, never auto-injected), so a repo you don't
  control can't smuggle authority in by shipping a file.
- **Shared memory** (DuckDB, full-text + optional HNSW vector) — a multi-tier
  searchable corpus; the source of truth is plain markdown. A finding one run learns
  becomes available to every later run, and every *model*. Lessons are
  **trust-tiered**: searchable, never auto-promoted into an authoritative instruction.
- **The learning loop — the herd proposes, a human approves.** Recurring finding
  signatures and clusters of similar lessons are swept into **skill proposals**: an
  LLM drafts corrective guidance plus a reusable skill, the operator approves or
  rejects it (a live **Proposals tab** or `corral-admin proposals`). Approval promotes
  it into vetted memory and a versioned skill; every later run starts already warned.
  The loop watches its own efficacy — if a signature keeps recurring after promotion,
  a revision proposal reopens.
- **Shared skills, human-gated.** Approved skills sync across the fleet via
  `corral sync`, so what one machine's herd learns, every machine's herd can *do* —
  but publishing to the fleet is superuser-only (a worker proposes, it can't publish).
  Corralai ships a [`using-corralai`](skills/using-corralai/SKILL.md) skill that
  teaches any coding agent to drive the gate.
- **Reference RAG** — upload your own grounding material (text · URLs · **PDFs**);
  chunked and vector-embedded (any OpenAI-compatible embedding endpoint) for agents to
  query. Runs on **embedded DuckDB — no Postgres, no separate vector database**.

## Watch it back — every run recorded and replayable

Nothing about a run is thrown away: every task's claim and completion, every finding
and its resolution, every command actually run, and the event log survive
indefinitely. `corral certify --record <file>.json` writes a replayable tape of an
audit — the pool's reasoning beats, the task lifecycle, the findings — in the same
`{events:[…]}` shape the corralai.dev cockpit replays. **With reasoning capture on,
the replay streams each model's own words, verbatim,** interleaved with the commands
they triggered (*"the retry test is flaky because the backoff refills too slowly"* →
`go test ✗`) — so you watch the herd *think*, not just move. One scrub bar moves the
whole cockpit — canvas, progress, files — to the same instant, at up to 16×; the
captured reasoning is real, never synthesized, which is what turns a replay into a
*debugger*.

**See it live at [corralai.dev](https://corralai.dev).** The hero is a real recorded
run replaying in your browser; the **recordings gallery** holds more, each labeled
with the hardware it ran on and honest per-run analytics. And
**[corralai.dev/warehouse](https://corralai.dev/warehouse/)** runs DuckDB itself — in
your browser, via WebAssembly — over the real audit ledger + execution telemetry, so
you can query the signed records with live SQL. Full docs at
[corralai.dev/docs](https://corralai.dev/docs).

## Coordinate — one swarm or many

- **Coordination substrate** (SQLite, transactional) — atomic exclusive path/branch
  claims with TTL, presence, a lease/presence reaper, a completed-work log, one-call
  `bootstrap`.
- **Fleet analytics** (optional, MotherDuck) — runs and telemetry from many brains
  roll up into one place, retention/compaction built in.
- **Ask the fleet** — a natural-language oracle over that data ("what did agent X do
  across every run? who ingested that document?"), turning the audit trail into
  something you can query.
- **Cross-swarm coordination** — brains hold signed (Ed25519) identities and
  publish/read *advisory* claims through the fleet, so independent swarms avoid
  colliding — observe, never coerce.
- **Shared reach (the MCP gateway)** — register any service (yours, wrapped as MCP)
  with the brain and the herd can *use* it without ever holding the key: the brain
  proxies the call, holds the upstream secret (never returns it), SSRF-guards every
  dial (resolve-and-pin), and appends the call to the audit ledger under the verified
  caller. Governance is scoped to bound mischief — `register_endpoint` makes an
  **owner-scoped** endpoint only that user can reach; only an admin's `promote_endpoint`
  makes it team-wide (optionally swapping in a team credential); `list_capabilities` /
  `call_capability` are the herd's use path. The same pattern as everything else:
  *share the capability, hold the credential.*

## Run anywhere

- **Model-agnostic** — Ollama or any OpenAI-compatible backend (Gemini, OpenRouter,
  Anthropic, local, …). Not wired to one LLM.
- **Harness-agnostic** — the herd "contract" is nothing but MCP calls against the
  brain (`bootstrap → claim_task → work → complete_task`, where the tasks are the
  adversarial-audit roles — mutant-generator, test-writer, test-critic); `corral-agent`
  is its reference implementation. **`corral-harness`** loops any headless coding-agent
  CLI as an audit-role worker — Claude Code, Gemini CLI, Codex, GitHub Copilot CLI —
  each bringing its own tool loop, sandbox, and **its own auth**: they run on their own
  Pro/Max/Plus subscriptions, no API billing.
  ```bash
  CORRAL_BRAIN=http://localhost:9019 AGENT_NAME=Cody AGENT_ROLE=reviewer \
  HARNESS_CMD='claude -p {prompt} --mcp-config {mcp_config} --allowedTools "mcp__corral,Read,Write,Edit,Bash" --permission-mode acceptEdits' \
  corral-harness
  ```
- **Auth from day 0** — identity was designed in, not bolted on:
  - **OIDC relying party, any provider** — point `CORRALAI_OIDC_ISSUER` at a discovery
    URL (Keycloak, Auth0, Okta, Dex, Zitadel, …); the brain validates bearer JWTs
    against its JWKS. Agents get tokens via `client_credentials`; humans via normal
    login. No bespoke auth.
  - **Principals & membership** — a member allowlist with superusers for the
    privileged surfaces. The verified principal from the token is AUTHORITATIVE:
    stamped over whatever name a client claims, so no agent can act as anyone else.
  - **Signed delegation tokens** — an agent can spawn an out-of-process subagent with a
    scoped, TTL-bound token: the subagent acts under its own identity, accountability
    rolls up to the spawning principal, the token dies on schedule.
  - **The human gate** — every admin write (approving a proposal, sharing memory,
    promoting a reference) refuses a delegation token even when it rolls up to a
    superuser: workers propose, the operator disposes. The same rule holds by
    convention in dev mode, so an agent can't vet its own knowledge.
  - **Read-only observer tokens** — for dashboards and demo audiences: watch the live
    swarm, every mutating call refused.
  - Dev mode (no issuer) runs open with the same code paths, so "works on my machine"
    and "works with auth" don't drift apart.

## Security model

The headline feature, not a footnote. Full write-up in **[SECURITY.md](SECURITY.md)**;
the short version is three pillars:

- **Prevention (the fences).** Every command runs in a `bwrap` jail (no network by
  default, workspace-confined, secret-free env). The git/forge token lives only in the
  brain — scrubbed from the environment, never written to `.git/config`, never given
  to an agent, never used against a forge other than its own. The "ask the fleet"
  query runs in a locked-down DuckDB connection that can't read files or reach secrets.
  Ingested knowledge is trust-tiered so a poisoned document can't become an
  instruction.

  This is what makes **full-auto safe**: an interactive harness gates risky commands on
  a human click — unworkable for a dozen autonomous agents overnight. Corralai bounds
  *what a command can touch* instead of asking *whether it may run*: the jail replaces
  the permission prompt. (Docker is only the demo's packaging — on bare-metal Linux the
  jail is one unprivileged `bubblewrap` package.)
- **Detection (forensics).** Because every agent acts *through* the brain — the single
  trusted egress — the brain records every consequential action, attributed to a
  verified principal. Agents can't forge or erase their own trail; the subject of the
  record doesn't control the ledger.
- **Isolated artifact storage.** Task outputs (screenshots, files) decouple into an
  isolated `corralai_task_artifacts.sqlite3` database. Uploads pass multiple gates:
  the uploader must hold an active lease on the target task, magic-byte inspection
  enforces a strict MIME allowlist (blocking executable/HTML scripts), size is capped
  at 5 MB, and paths are sanitized against traversal.
- **Portable, secure key storage.** Provider API keys and the worker token never sit
  in plaintext or leak into a process listing. `corral secret set NAME` reads the value
  from **stdin, never a CLI argument**, and the keystore resolves each secret through
  **env var → OS keyring → an age-encrypted file** — your OS keychain on a desktop, an
  age-encrypted store on a headless server (the identity fails closed, protected by a
  systemd credential or a `0600` key). Every log redacts secret values to a
  fingerprint. It's the GCP-ADC pattern, shipped in one binary.

Every security core was adversarially red-teamed, and the tests ship with the repo.
The codebase runs clean through **`gosec`** (0 findings at medium+ — every one fixed or
adjudicated inline) and **`govulncheck`** (0 known dependency vulnerabilities), both
enforced in CI by [`scripts/check-security.sh`](scripts/check-security.sh).

**Don't trust the claims — run them:** `go test ./...` and `bash scripts/check-security.sh`.

## The fleet — a daemon and its client apps

Corralai is a **headless server with thin client apps**, like a backup system:
`corral` holds the state and authority; everything else connects over MCP/HTTP.

| Binary | Role | CGO | Ships as |
|--------|------|-----|----------|
| **`corral`** | the **brain** — MCP coordination, the gates, task queue, memory, reference RAG, repo-work + multi-forge, the fleet oracle, embedded UI; owns the databases | yes | `deploy/demo/Dockerfile.brain` |
| **`corral-agent`** | the reference **audit-role worker** — model-agnostic, claims an adversarial-audit role (mutant-generator / test-writer / test-critic) off the queue | no | `deploy/demo/Dockerfile.agent` (distroless) |
| **`corral-observe`** | the **observer** — read-only credentialed window onto a brain's live UI | no | `deploy/observe/Dockerfile` (distroless) |
| **`corral-admin`** | the **operator** — privileged live console plus command verbs over MCP | no | binary / `go install` |
| **`corral-desktop`** | the **desktop client** — native-window (`--app` mode) launcher onto a local console | no | binary / `go install` |
| **`corral-harness`** | the **harness-agent launcher** — loops any headless coding-agent CLI as an audit-role worker on ITS auth | no | binary / `go install` |
| **`corral-top`** | the **terminal dashboard** — a read-only TUI over a live brain (tasks, agents, findings), for a glanceable window without a browser | no | binary / `go install` |

The observer and admin consoles share one reverse-proxy core (`internal/console`),
parameterized read-only vs read-write.

## Platforms

The design premise keeps your OS mostly out of the picture: **the brain lives on a
Linux server; everything else joins it over MCP/HTTP.** A Mac or Windows developer
participates fully without installing anything beyond a config stanza.

| | Linux | macOS | Windows |
|---|---|---|---|
| **Thin client** (your coding agent + `.mcp.json`) | ✅ | ✅ | ✅ |
| **`corral-admin`** (operator CLI) | ✅ | ✅ compiles | ✅ compiles |
| **`corral-observe`** (read-only window) | ✅ | ✅ | ✅ |
| **`corral certify --local`** — real exec (bwrap jail) | ✅ | via Docker (`--jail container`) | via Docker/WSL2 |
| **`corral` (the brain)** | ✅ first-class | ⚠️ untested | via Docker/WSL2 |

**The jail is a Linux capability — and that's the point.** `bwrap` (bubblewrap) is
Linux namespaces; on a bare-metal Linux host it runs **unprivileged** (one package,
no root, no daemon). macOS and Windows have no equivalent, so exec runs inside a Linux
environment — Docker Desktop or WSL2, or the `--jail container` fallback. The brain's
two CGO deps (DuckDB memory, tree-sitter code index) make it the one binary that cares
about its platform; deploy it once on a Linux host (systemd + your tunnel/proxy).

## Why Go — and why your stack doesn't have to be

**The substrate is Go** because a coordination brain has infrastructure-shaped
requirements, and Go is the boring, correct answer: **one static binary per
component** (no runtime, no virtualenv, no `node_modules` on the server); **mostly
concurrent I/O** (dozens of agents heart-beating over MCP/HTTP+SSE is exactly what
goroutines are for); **embedded databases without an ops bill** (SQLite + DuckDB
compile straight in — no Postgres, no separate vector DB); and it **cross-compiles
honestly** (the Platforms table was produced with `GOOS=darwin|windows go build`).

**What corral audits is a different axis — any language the models know.** The gate
takes any command — `go test`, `pytest`, `npm test`, `cargo test` — and refuses to
certify until a passing run is on record. A Python-and-Svelte team never writes a line
of Go to run, join, or benefit from the gate; Go is just what the corral fence is made
of.

## The audit flags

`corral certify --local` is one command, but it fans out:

- **Sharded generation.** The file's top-level functions are bin-packed
  (complexity-balanced, deterministic) into up to `--max-shards` (default 8) generator
  seats, each attacking a different group of functions, so **every function gets
  probed** instead of whatever one generator happened to pick. `--n-mutants` (default
  5) is a **per-shard** budget; the default 8 shards means up to ~40 mutants scored.
- **The shadow challenger.** `--shadow-model <model>` fans a challenger
  mutant-generator seat across every region for a region-controlled,
  execution-proven head-to-head between generator models — same file, same goal,
  same commit. **Off unless named**, like every other seat. **It never affects the verdict**: shadow mutants
  are scored and recorded to the scorecard (`corral scorecard`), but only the primary
  generator's mutants feed the kill-rate. It roughly doubles generator API calls and
  jail wall-clock.
- **The challenger writer, and the decorrelation measurement.**
  `--shadow-writer-model` (off unless named) runs a SECOND test-writer against the
  same survivors as the primary, so the two seats' misses can be compared. The
  headline is **Jaccard over survivors** — of everything either writer missed, what
  fraction did both miss — because agreement on *kills* is cheap: any competent
  suite kills the easy mutants, so a correlation over kill vectors is driven by the
  mutants being easy rather than by the models being alike. Cohen's kappa rides
  along as a chance-corrected companion, reported separately and never blended.
  Recording is **pair-or-nothing**: if either seat produced no usable test, nothing
  is stored, because a zero for a seat that never ran is a fabricated comparison.
  **It never gates the verdict** — it is measurement, and it is how corral checks
  its own central claim instead of asserting it. First result: [Jaccard 0.750 over
  13 survivors](https://corralai.dev/field-notes/both-models-missed-the-same-nine/)
  between two frontier models from different labs.
- **Parallel mutant scoring.** Scoring runs the target's whole suite once per
  mutant, so an audit costs `O(mutants × your suite's runtime)` — the dominant cost
  on any project whose suite isn't trivially fast. `certify --repo` now splits one
  bounded jail budget between two axes, files-at-once and mutants-at-once, so
  whatever file-parallelism can't spend goes to the mutant loop (the common case
  being a diff-scoped PR with one changed file, where every other worker would
  otherwise idle). No flag: it's derived from `--swarm` and reported in the scan
  header. **Only on `--substrate jail`** — the workspace substrate mutates one
  checkout in place with no locking, so it stays strictly sequential, and that is a
  correctness boundary, not a tuning choice. Honest caveat: this pays in proportion
  to how much of your audit is suite time, which on a very fast suite is not much.
- **A failing baseline tells you why.** If your suite doesn't pass on its own
  unmodified code, corral refuses to grade — a kill rate measured against a broken
  baseline is a fabricated number. It now prints the runner's own output alongside
  that refusal, so `baseline does not pass unmutated` comes with the traceback,
  missing import or failing test that caused it. Costs no extra run.
- **Turning the critic off.** `--critic-model off` drops the test-critic entirely. The critic is **advisory** — its
  findings ride the verdict as unverified review and never gate certification — so a
  run without it reports the same execution-proven kill-rate and proven-missed count,
  just with no second opinion attached. That absence is reported as *empty*, not as a
  clean review: "nobody looked" must not read as "looked and found nothing". Needed
  whenever a single-vendor run has only one model you're willing to use, since the
  critic must otherwise differ from the writer.
- **Critic precision.** `corral scorecard`'s C-PREC column scores the test-critic role
  itself: how often a critic's findings, once a human adjudicates them
  (`corral criticscore list|show <id>|confirm <id>|refute <id>`), turn out to be real
  vs. wrong — the same "who watches the watchmen" question the gate asks of generators,
  now asked of the critic. It's a human-gated metric on the brain path only: `--local`
  shows the auto-adjudicated verdict on the run's tape, but persists nothing to the
  scorecard (there's no server-side store to write to without a brain).
- **The tests×mutants matrix (`--matrix`, opt-in).** After the primary pass, re-score
  EVERY dev test alone against the run's mutants — a per-test adequacy readout instead
  of one suite-wide kill-rate, plus a safe-to-delete candidate list (a test that caught
  none of the planted mutants). Read it with `corral matrix list [--json]`. Costly (T
  tests × M mutants extra jail runs) and opt-in for that reason; go + python only
  today; a delete-candidate is relative to the mutants THIS run happened to plant, not
  proof a test is dead weight — review before deleting, don't auto-delete on it.
  Python single-file mode: the `--test` filename must follow pytest's discovery
  convention or enumeration finds nothing and the matrix is silently skipped
  (repo-dir mode unaffected).
- **Robustness.** A non-terminating mutant is killed fast and counted (a broken loop
  can't stall the run); `--test-timeout` overrides the auto-derived per-run cap. The
  run always converges to a signed verdict — even when the herd can't author a killing
  test for a survivor, it routes to `needs-review` rather than spinning.
- **`--swarm N`** bounds how many audit tasks run concurrently (0 = auto-size to the
  host's cores, capped). **`--repo-dir <path>`** audits `--code` in the context of a
  whole cloned repo (the tree is seeded into the jail and the project's own test
  command, after `--`, grades it). **`--record <file>.json`** writes the replayable
  tape.
- **Dependency dirs are bound, not copied (`--repo-dir` mode).** `node_modules`,
  `vendor`, `.venv`, `venv`, and `.bundle` are auto-detected and mounted **read-only**
  into the jail instead of being copied into the workspace seed — they're usually the
  bulk of a checkout's size and irrelevant to the mutant/text seed, so binding keeps
  them off the 64 MiB workspace cap. Dependencies must already be **present**
  (vendored/installed, the same way CI expects them) — corral binds what's there, it
  never installs anything. `--bind-dir <path>` (repeatable, repo-relative) binds
  additional dirs the same way; `--no-bind-deps` restores the pre-bind behavior
  (copy every dep dir into the seed, subject to the size cap). A run prints `deps:
  bound N dir(s) read-only (...)` whenever anything was bound.

**The audit always runs sandboxed** (`bwrap` on Linux by default; `--jail container`
for a docker/podman fallback; `sandbox-exec` on macOS) — there is no unsandboxed
option. On Ubuntu 24.04+, apparmor disables unprivileged user namespaces and bwrap
won't start; the CLI's error message spells out the one-line fix, or use `--jail
container` with a toolchain image (`export CORRALAI_EXEC_IMAGE=python:3`). One gotcha:
the language toolchain has to be **jail-visible** — installed system-wide under `/usr`,
not a `--user`/snap/pyenv install invisible to the sandboxed mount namespace.

The hosted brain runs the same sharded + shadow machinery via `start_adversarial_run`
(`max_shards` default 8, ceilinged at 20 for a hosted run; `shadow_model` defaults on
daemon-wide via `CORRALAI_ADVPOOL_SHADOW_MODEL`, `off` to disable; the run deadline is
widened automatically when a shadow model is set, so shadow work can never force a
timeout `needs-review`).

## Running the brain

```bash
go test ./...
go run ./cmd/corral     # MCP /mcp/ · health /healthz · UI / · on 127.0.0.1:9019
```

Common knobs: `CORRALAI_OIDC_ISSUER`/`_AUDIENCE` (cross-machine auth) ·
`CORRALAI_GIT_TOKEN` + `CORRALAI_FORGES` (repo-work / multi-forge) ·
`CORRALAI_EMBED_URL` (reference RAG + vector search) · `CORRALAI_MOTHERDUCK` (fleet
analytics + oracle) · `MODEL_BACKEND`/`OPENAI_BASE_URL` (bring your own model). See
**[docs/DESIGN.md](docs/DESIGN.md)**.

### The jail, in detail

The `corral` / `corral-agent` process is never sandboxed — only the subprocess a check
spawns is isolated:

- **Default-deny.** Execution only runs once a backend has been resolved and
  preflighted. If the host can't isolate, execution stays disabled and returns a loud,
  actionable error — it never silently degrades to running unprotected.
- **`bwrap` backend (default, Linux).** Each command runs in an unprivileged namespace
  jail: network off, read-only root except the workspace, no privileged caps, a
  secret-free env (the token never reaches it). Needs `bubblewrap` present.
- **`container` backend.** `--jail container` (or `AGENT_EXEC_BACKEND=container`) runs
  the jailed command inside a docker/podman container with `--cap-drop=ALL`,
  `--read-only`, `--network=none`, and pid/memory limits — for hosts without bwrap.
- **Network off by default.** Opt a build step in only where it legitimately fetches
  deps.

bwrap shares the host kernel — it stops casual damage, egress, and filesystem escape,
**not** a kernel-exploit escape. For adversarial code use a stronger backend
(container/microVM); the pluggable `Isolator` makes that a drop-in.

## Credits

Inspired by — not derived from — prior open work (`mcp_agent_mail`,
`agent-orchestration`, `Agent-MCP`). Design concepts only; no third-party code
incorporated.

## License

Corralai is **source-available** under the [Elastic License 2.0](LICENSE)
(`Elastic-2.0`). You're encouraged to read the whole codebase, modify it, and
self-host it. The one restriction that matters: you may **not** provide Corralai to
third parties as a hosted or managed service.

Want to run it as a service anyway? A **commercial license** is available — contact
licensing@corralai.dev.

Contributions are welcome under a one-time [CLA](CLA.md); see
[CONTRIBUTING.md](CONTRIBUTING.md).

---
**[corralai.dev](https://corralai.dev)** — a live-replay one-pager (`site/`, Astro,
Cloudflare Pages) · github.com/pdbethke/corralai. Full docs — concepts, a UI tour, and
a generated CLI reference — at [corralai.dev/docs](https://corralai.dev/docs).
