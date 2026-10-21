# Corral — the bugs your tests miss, and the tests that catch them

[![CI](https://github.com/pdbethke/corralai/actions/workflows/deploy.yml/badge.svg?branch=main)](https://github.com/pdbethke/corralai/actions/workflows/deploy.yml)
[![License: Elastic 2.0](https://img.shields.io/badge/license-Elastic--2.0-e8a838)](LICENSE)
[![docs](https://img.shields.io/badge/docs-corralai.dev-2f6f4e)](https://corralai.dev/docs/getting-started/)
[![OpenSSF Scorecard](https://api.securityscorecards.dev/projects/github.com/pdbethke/corralai/badge)](https://securityscorecards.dev/viewer/?uri=github.com/pdbethke/corralai)

**Your tests pass. Corral finds the bugs they miss — and hands you the tests that catch them.** It plants faults in your code and runs your own test suite against each one. For survivors, it attempts new tests and keeps only execution-demonstrated proofs: tests that fail on the fault and pass on your unmodified code. Not every survivor gets proven; the tests still need your review. Run on its own signing code, corral planted 78 faults; 35 slipped past our own tests, and it wrote tests proving 30 of them were catchable gaps. We merged tests from that run the next day ([#210](https://github.com/pdbethke/corralai/pull/210)); [corralai.dev](https://corralai.dev/) shows one beside the fault it catches.

**Everyone is building build harnesses. Corral is the one that refuses to build.** It never authors the code it judges: the faults it plants are probes, discarded once scored, and the tests it writes are evidence — each proven against its own fault and handed back to you.

**Corral is an auditing engine.** It judges code it did not write — by execution and by adversary — onto a signed, tamper-evident record. Accountability, paper trail, analysis, certification: everything but building. Two extensions carry the engine's rule into practice:

- **`certify`** — by execution. Corral breaks your code on purpose and checks whether your tests notice: it plants faults that violate a stated guarantee, runs **your own suite** against each one in a sandbox, and reports how many it killed — measured, never taken on a model's word.
- **`review`** — by adversary. A model that has never seen your repository is told to assume the code is wrong and prove it; every claim comes with a script corral runs, a claim that does not reproduce is demoted on the record, a second seat tries to refute what stands, and both seats are graded by what held.

Both write hash-linked entries into a ledger in your own repository, signed when a certify key is configured and unsigned otherwise. Every seat — the one planting faults, the one writing tests, the reviewer, the verifier — can be ranked by the record it leaves. Corral never writes the code. That is what it judges.

**GitHub becomes the record.** A CI runner forgets everything when the job ends. The ledger workflow preserves each recorded verdict as a hash-linked entry on an orphan branch, `corral/ledger`, signed when a certify key is configured. Any DuckDB can read that branch over HTTPS as tables: the weakest files across scans, which model catches faults in which language, cost per audited file, and whose review claims hold. MotherDuck is the same engine over every repository that pushes. `corral ledger verify --pub <trusted-key>` checks retained signatures and links; detecting deletion from the tail requires an external record of the expected head. The repository owner controls the files, so this is tamper evidence, not immutable or guaranteed-complete history. The next run reads earlier faults as the generator's prior, reuses eligible verdicts, and knows which scopes were reviewed. [How it works](https://corralai.dev/docs/how-it-works/) has the query; corral's own record is at [`corral/ledger`](https://github.com/pdbethke/corralai/tree/corral/ledger).

***Nemo iudex in causa sua*** — no one may be judge in their own cause. The one who
wrote the code doesn't get to certify it: the verdict is **measured by execution**, by
a **decorrelated** party, behind a **human gate**. That maxim isn't a slogan here — it's
the constraint everything below is built on. ([why it's the whole design](https://corralai.dev/field-notes/nemo-iudex/))

## Quickstart

### See it work (2 minutes)

One command. `corral demo` writes a small Go package with a five-clause
password rule and a test that checks only two of them, then audits it with the
real `certify --local`:

```bash
go install github.com/pdbethke/corralai/cmd/corral@v1.0.0-rc.27   # @latest still resolves to 0.8.x: a release candidate is a pre-release to Go

# with API keys (the model names are an example, not a default; corral has none):
export GEMINI_API_KEY=...  ANTHROPIC_API_KEY=sk-ant-...
corral demo --writer-model gemini-3.8-flash --mutant-model gemini-3.8-flash --critic-model claude-haiku-4-5

# or with no API key at all, on the coding-agent CLIs you are already logged in to:
corral demo --writer-model codex --mutant-model codex --critic-model claude-code
```

**Any seat can be a coding-agent CLI instead of an API model.** `claude-code`
and `codex` come defined (`antigravity` too, unmeasured), and you can define
your own as `CORRALAI_AGENT_<NAME>`. The agent brings its own login, so on a
Claude or ChatGPT subscription a seat has no per-token API bill. The second
command above was run with no API key in the environment: 6 of 6 survivors
were proven by an authored test. This works on every verb that takes a seat
(`demo`, `certify --local`, `certify --repo`, `doctor`, `review`), not only on
`review`. Seats start cold, so the agent never sees your own `CLAUDE.md`, memory or
skills. An exported vendor API key may bill instead of the subscription. See
[docs/corral/agent-seats.md](docs/corral/agent-seats.md).

On a warm Go module cache, the install took **~25s** and the demo **~75s** to
reach a verdict, so about two minutes end to end, longer on a first-ever
`go install`. What you need:

- **Go**, and a **C compiler** (gcc or clang; Xcode Command Line Tools on
  macOS). corral links two cgo dependencies, DuckDB and tree-sitter.
- **A sandbox.** On Linux that means `bubblewrap` (`apt install bubblewrap`). On Ubuntu 24.04
  and later, AppArmor stops bwrap from starting until you load Ubuntu's
  `bwrap-userns-restrict` profile. corral's error prints the two commands,
  and they are also [below](#the-jail-in-detail). On macOS the default is
  `sandbox-exec`, which is built in.
- **Seats**: one API key, or a logged-in `codex` / `claude` CLI.

You need no virtualenv, no database and no fixtures. The demo leaves the
project on disk so you can read the test and see what it never asserts.

### Audit a Rust crate (a Cargo workspace such as Warp)

Rust is audited a crate at a time. Run this from a **scratch clone**, because
`--substrate workspace` plants each fault in the checkout itself:

```bash
corral certify --repo . --path crates/<crate> --dry-run      # free: no key, no sandbox, no model call

corral certify --repo . --path crates/<crate> --substrate workspace --top 1 \
  --derive-model gemini-3.8-flash --mutant-model gemini-3.8-flash \
  --writer-model gemini-3.8-flash --critic-model codex \
  --max-tokens 3000000 \
  -- cargo test -p <crate>          # or: cargo nextest run -p <crate>
```

What each part is for, and the limits that apply:

- **`--path crates/<crate>`** limits the scan to one directory but keeps the
  whole repository. The build still sees the workspace's root `Cargo.toml`, and
  tests are still found anywhere in the repository. Scope the test command the
  same way, with `-p <crate>`. `cargo test --all` and `--tests` are also
  accepted after `--`.
- **`--substrate workspace`** runs your own toolchain, so a stock rustup under
  `~/.cargo` works. The default sandboxed run uses a system-wide toolchain only
  (`RUSTUP_HOME=/usr/local/rustup`), and when that install is missing it is
  refused before anything is spent. To use the sandbox, install Rust like this:
  `RUSTUP_HOME=/usr/local/rustup CARGO_HOME=/usr/local/cargo rustup-init
  --no-modify-path --default-toolchain <version>`, with `/usr/local/cargo/bin`
  on `PATH`.
- **Only sources whose tests live in a separate file can be audited.** That
  means a file declaring `#[cfg(test)] #[path = "foo_tests.rs"] mod tests;` or
  a bare `mod tests;`. corral appends its test to that file as a
  `mod corral_tests { use super::super::*; … }` block. A file whose tests are
  inline (`mod tests { … }`) is listed as `test-landing-not-built`. It is never
  audited, because the test would have to be written into the source under
  audit. `certify --local` refuses a `.rs` file unless you pass `--repo-dir`.
- **What Rust does not have yet:** a coverage pre-flight, a verified fail-fast
  flag, salvage of a partly failing authored test, and `--matrix`. The critic
  is still given every test name, and each of its "this test can never fail"
  claims is checked by running that test alone (`-- --exact`). See
  [docs/corral/language-support.md](docs/corral/language-support.md).
- **What it found on Warp.** The first run was on `crates/fuzzy_match` at
  `714c51f1`, with the critic on the Codex CLI on a subscription. Of 34 planted
  faults, the crate's 33 tests caught 11 (0.32). corral wrote and ran tests
  proving 22 of the 23 survivors were real gaps. The record is signed ledger
  entry [`94d9296c5801`](https://github.com/pdbethke/corralai/blob/corral/ledger/scans/20261014T133712Z-714c51f1e29f-8f1bc4949be5.json.gz),
  and [the field note](https://corralai.dev/field-notes/the-first-rust-audit/)
  walks through it. A second run on the same commit, from a clean Ubuntu
  container, graded 37 and proved 29 (CHANGELOG, rc.20). Each run plants a
  fresh set of faults.

### What a run does

In the age of AI, the thing that wrote the code tends to be the thing that grades
it — the model writes the code and says it's good, or writes the tests for its own
code and reports that they pass. That's the author reading his own verdict into the
record, and authors are kind to themselves. Corral is built the other way: the party
that did the work never certifies the work, nothing is taken on a model's say-so, and
the one number that means anything is the one no model was allowed to author — it's
what happened when the check actually ran, in a sandbox.

**The model names in these commands are examples, not defaults — corral has none.** Every seat
is yours to name, from whichever provider you have a key for or whichever agent CLI
you are logged in to; the models above are simply what *we* run. The one rule is that the **critic must differ from the
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

It asks the question you can never answer honestly about your own code — *do my tests
actually test anything, or do they just pass?* — and answers it by execution:

- A **mutant-generator** seeds real, goal-violating bugs into your code.
- Your **own test suite** is run against every one of them, **in a jail** — the
  fraction it catches (the *kill-rate*) is the adequacy score, measured, never a
  self-report.
- A **test-writer** attempts a compiling test for each survivor and keeps the ones
  that prove, correcting itself when its test doesn't compile rather than blindly repeating. A
  surviving mutant is *disclosed, unadjudicated* — a real gap, or an equivalent
  mutant no test can catch, your call. Only a survivor a compiling test actually
  kills is a **proven** gap. Zero proven gaps means *nothing was proven this run* —
  never *your tests are fine*. ([why that distinction is the whole
  point](#when-corral-proves-a-gap-and-when-it-proves-nothing))
- A **test-critic** — always a *different* model, enforced — reads your test
  file next to the code and returns one verdict per test: sound, vacuous (it can
  never fail), or a dead check inside an otherwise real test. The answer is held
  to a schema by the provider; for Go, Python, PHP and Rust the critic is handed
  every test name and must judge each one, in batches of 20. A review cut short is reported
  as INCOMPLETE, never as clean. Its opinion is carried as **unverified advice;
  it never gates the verdict.**

You get a signed verdict — `certified` or `needs-review` — printed and written to a
local, tamper-evident ledger. `--out` also writes it as a self-contained file you
re-check anytime, offline:

```bash
corral certify verify verdict.json --pubkey "$(corral certify pubkey)" --allow-unanchored
```

`--allow-unanchored` is deliberate: a record minted off *your own* key, with no
server, was never submitted to a public witness, so it's "signed by you, not
third-party attested" — a weaker claim, and verify makes you say so out loud.
A stronger record is one a third party witnessed: `certify --repo --attest
--transparency` logs the signed statement to **Sigstore Rekor**, a public,
append-only log, and `corral verify --attest` checks it (see [the warehouse
and public transparency](#in-ci--the-github-action) below). A record file like
the one above is anchored only when the optional, frozen brain daemon signed
it, in which case the inclusion proof is carried inside the record and
`corral certify verify` checks it against the Rekor key from Sigstore's TUF
root, with no round trip to the log. A record that was never anchored says
`anchored=false`, never a fabricated proof, and verify refuses it unless you
pass `--allow-unanchored`.

### Audit a real repo

**Start free.** Enumeration needs no key, no sandbox and no money, and tells you
in seconds which files corral can audit and which have no paired test:

```bash
corral certify --repo . --dry-run
```

Install your project's dev dependencies first — the suite must pass for you
before corral can plant bugs against it. For Python, per-test selection needs
`pytest-cov` specifically (not `coverage` alone — pytest exits 4 with no
output if the plugin is missing, and corral reports that rather than grading
blind); without it corral grades by the whole suite and says so. Then:

```bash
corral certify --repo . --top 3 --max-tokens 3000000 \
  --writer-model gemini-3.8-flash --mutant-model gemini-3.8-flash \
  --derive-model gemini-3.8-flash --critic-model claude-haiku-4-5 \
  -- <your test command>       # e.g. -- python -m pytest, or -- npm test
```

The model names are an example — what we run, not a default. `--top 3` audits the
three highest-ranked files and `--max-tokens` caps the whole run's spend. For
scale, one maintainer-run audit of flask's `src/flask/app.py` with those seats
took about six minutes and 0.5M input tokens, and proved 9 of 9 surviving bugs
catchable (a self-report: no artefact of that run is in this repository). Each
proven test is printed at the end of the report with "add it to your suite", and
`corral scans show <id> --evidence` prints it again later; corral does not write
it into your repository or open a pull request.

That's four seats, not three: a repo scan derives a goal per file, so
`--derive-model` is required whenever you're not supplying `--goals`
yourself — without it corral refuses with "no goal source" before it does
anything else. The test-writer and test-critic must also be *different*
models — corral's decorrelation guard refuses a shared model between the two
("nemo iudex in causa sua," no judge in their own case).

**What that guard does and doesn't cover, stated plainly:** `CheckDecorrelation`
(`internal/advpool/driver.go`) enforces exactly one thing —
`test-critic != test-writer` by model name — and refuses the run otherwise.
Cross-vendor seats (naming a critic from a different lab than the writer) are
**advised**, for the reasons above, but are not required: two models from the
same provider satisfy the guard. And the guard says nothing at all about the
**mutant-generator** — it may share a model with the test-writer, and corral will
not stop you. That's a real gap between the guard and the "no one may be judge in
their own cause" framing; it matters less than it sounds because no model's
opinion enters the signed verdict — the kill-rate is decided by your suite's exit
code in the jail, not by what the generator or critic say. Widening the guard to
cover generator/writer and vendor is on the list; doing so is a breaking change to
every previously-recorded verdict, so it hasn't shipped yet.

By default the scan runs in the sandbox, on a copy of your tree; dependency
directories inside the project (`.venv`, `venv`, `node_modules`, `vendor`,
`.bundle`) are bound into it read-only, so a virtualenv that lives in the project
works. What the sandbox cannot see is toolchain state under your home directory
(`pip install --user`, pyenv, a virtualenv outside the project).
`--substrate workspace` instead mutates your own checkout in place — the caller
(your shell, a CI runner) *is* the isolation boundary. **Commit or stash first,
or use a scratch clone:** a process killed outright (kill -9, out of memory) can
leave a mutant behind, and nothing detects it. Pairing a source file with its test now
searches `tests/**` (and each language's conventional test roots) for a file
that actually exists rather than guessing a filename, so most
conventionally-laid-out repos pair without any extra step. The exception is a
project that names tests after *behavior* rather than after the source file
(`expressjs/express` tests `lib/response.js` from `test/res.send.js`,
`test/res.json.js` — no filename rule derives that); for those, hand it a
**`--tests` map** (JSON, source path → test path — see below) instead of
relying on pairing to find it. Before spending a real run, `corral doctor`
checks the environment for free — see below.

Go, Python, Ruby, JavaScript, TypeScript, PHP and Rust — the language is
inferred from `--code`'s extension, each a plugin in `internal/lang`. The
authored test is written in **your project's own harness** (it is shown your
existing test file and told to match it). corral recognises the runner from
your test command — `go test`, gotestsum, pytest, unittest, jest, vitest,
mocha, `node --test`, RSpec, minitest, PHPUnit, `cargo test`, `cargo nextest`
— or from the package script behind `npm test`, `yarn test`, `pnpm test` or
`composer test`; `--runner <id>` names it when corral cannot. What corral does with each
language's tests, runner by runner, and why each gap exists, is on
[docs/corral/language-support.md](docs/corral/language-support.md) —
generated from the table the code dispatches on, not written by hand.

**Where each language's support actually stands.** A tool that argues for
execution over self-report should not claim a language on the strength of a
passing unit test, so here is what has actually been run, against what:

| language | evidence | verdict |
|---|---|---|
| **Go** | this repo's own entry point, audited by the GitHub Action on a real commit | 40 faults planted, 10 killed, **0.25**, 1 gap proven (that's corral's own `cmd/corral/main.go`; we published it rather than picking a flattering file) |
| **Python** | 6 whole-repo scans of [Flask](https://github.com/pallets/flask) | 10 files audited, **48 gaps proven by execution** |
| **TypeScript** | a private SDK, and [vercel/ms](https://github.com/vercel/ms) | 0.79 / **0.94**, 3 and 2 gaps proven |
| **JavaScript** | [vercel/ms](https://github.com/vercel/ms) under jest | **CERTIFIED**, 33 of 35 killed, 2 gaps proven |
| **Ruby** | [minitest](https://github.com/minitest/minitest) itself | 36 of 40 killed, **0.90** |
| **PHP** | [webmozart/assert](https://github.com/webmozart/assert) under PHPUnit | **CERTIFIED**, 40 of 40 planted faults killed; a later run on `src/Assert.php` left 1 survivor, and the writer proved it |
| **Rust** | [Warp](https://github.com/warpdotdev/warp)'s `crates/fuzzy_match` under `cargo nextest` | 11 of 34 killed, **0.32**, 22 gaps proven |

**Which of these can you open for yourself?** Three. The Go row is GitHub
Actions run `30846983897`, named in
[docs/corral/github-action.md](docs/corral/github-action.md); the vercel/ms
result is a published recording under `site/src/data/recordings/`; the Rust
row is a signed entry on the [`corral/ledger`](https://github.com/pdbethke/corralai/tree/corral/ledger)
branch, `94d9296c5801`. The Python,
Ruby, PHP and private-SDK figures are maintainer-run and have no artefact in
this repository — they are self-reports, and a project whose whole argument is
execution over self-report should say which of its own numbers are which.
Reproducing them is the point: every one names the repo and the runner.

Go and Python are exercised hardest — Go on demand in CI, Python across
repeated whole-repo scans. The self-audit workflow is opt-in per pull request
(it needs the `audit` label) and is `continue-on-error`, so it is a tool the
maintainer reaches for, not a gate that runs on every commit: as of
2026-10-16, its last 100 runs were all skipped. **TypeScript, JavaScript, Ruby,
PHP and Rust each rest on a single third-party repository**, which is enough
to show the plugin works and is not evidence about the ecosystem. Treat them
accordingly. PHP's 40/40 run gave the proving seat nothing to prove; the
later run that left one survivor is where the PHP writer proved a gap (the
suite tests `-123`, never `-1`, and the authored test calls
`Assert::notNegativeInteger(-1)`; CHANGELOG, rc.22).

One number in that table deserves its own warning: kill rate moves **run to
run on the same file with the same suite**, because the faults are generated
afresh each time. Measured swing on one file: 0.55 to 0.80. A single run is
evidence of specific gaps, **not a grade** — never quote one as a score.

**A PHP-specific trap, closed before it costs you anything:** Debian/Ubuntu's
`/usr/bin/php` is commonly a symlink through `/etc/alternatives` — resolves
fine on the host, but the sandbox's own mount table binds `/usr` itself, not
`/etc/alternatives`, so that chain can dangle once inside the jail even
though `php` looks perfectly normal from outside it. `certify --local`'s
preflight now resolves the real interpreter and probes it *through* the
sandbox before spending a single model call, and refuses with the fix named
if it can't follow the chain: pass an explicit interpreter in your test
command, e.g. `-- php8.5 vendor/bin/phpunit tests/`.

### Single files, and what the demo runs: `corral certify --local`

`--local` is the primitive everything above builds on — a complete adversarial
audit of one file, in-process, off your own key, no server. `corral demo` runs
it under the hood; call it directly to audit one file of your own, with your
own goal:

```bash
corral certify --local \
  --code path/to/your/file.py \
  --goal "what this code must guarantee" \
  --writer-model claude-sonnet-5 \
  --mutant-model claude-sonnet-5 \
  --critic-model claude-haiku-4-5 \
  --out verdict.json \
  -- python -m pytest
```

It runs inside a jail. A virtualenv inside the project (`./.venv` or `./venv`)
is bound into it read-only when you pass `--repo-dir`; one under your home
directory, or a `pip install --user`, is invisible there.

> **Whole-repo scanning is not equally strong across those languages.** `certify
> --local` audits any single file you name — you give it the path, so nothing
> has to be discovered (a Rust file also needs `--repo-dir` and a separate test
> file; see [the Rust section](#audit-a-rust-crate-a-cargo-workspace-such-as-warp)). `certify --repo` must first *find* the files, by
> pairing each source file with its test using naming conventions, and that pairing
> is much better at some ecosystems than others. On a real Python run that also
> changes: the one instrumented coverage pass promotes any file the tests
> demonstrably execute, whether or not a filename pairs. The numbers below are
> `--dry-run` figures — naming convention alone, before any evidence exists.
> Measured on real repos:
> `rubocop/rubocop` **737** candidates, `gin-gonic/gin` **29**, `pallets/flask`
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
> sweep](#in-ci--the-github-action) below.

One key can satisfy the distinctness rule on its own — name two different models
from the same provider (Sonnet writing and mutating, Haiku critiquing, say) — though
two models from one lab are the WEAKEST form of it and share the most lineage. Naming
`--critic-model gemini-3.6-flash` plus `GEMINI_API_KEY` (or `GOOGLE_API_KEY`) routes
the critic to Gemini via the OpenAI-compatible Google endpoint, a real cross-vendor
critic, while the writer and mutant-generator stay on whatever you named — a missing
key fails the run closed rather than silently falling back.

**Every seat routes by the model you name.** A Gemini generator, a Claude writer
and a GPT critic each go to their own vendor's endpoint, each needing its own key,
and a missing key refuses the run before anything is spent. The one exception is a
pinned *gateway*: `MODEL_BACKEND=openrouter` (or `ollama`) means "every seat on
this endpoint", and a `claude-*` name is then an OpenRouter call, never re-routed
to Anthropic behind your back. For a deliberately single-vendor run, use
**`--critic-model off`**: the critic is advisory
and never gates the verdict, so dropping it changes nothing about the
execution-proven result — it only removes the second opinion. (Useful when a vendor
offers just one model you're willing to run, since the critic must otherwise differ
from the writer.) Full walkthrough of a real verdict: **[the "first audit"
guide](https://corralai.dev/docs/first-audit/)**.

### What does an audit cost?

Scoring runs the audited file's real test command once per mutant, so the shape is

```
O(mutants × the target's suite runtime)
```

and the second term is the one that bites: it's a property of *your* repo, not of
corral, and it varies by ~50× between projects. Measured on two real recorded scans
(`docs/design/cost-model.md`, generated from the ledger, never hand-edited):
`pallets/flask`'s suite runs in 1.46s and its scan finished in 2m16s (40 mutants);
`psf/requests`'s suite runs in 77s — **53× slower** — and the otherwise-comparable
scan took 14m21s (39 mutants) because the dominant cost is suite time, not file size
or diff size. A repo whose suite takes 60–90s per invocation lands one to two orders
of magnitude past the flask number, for that reason alone.

Read those two numbers for what they are: both are **replays of a recorded mutant set
with the critic off**, so they price the scoring loop — the part this section is
about — and not a cold end-to-end run. A first run on the same file also pays goal
derivation, mutant generation, and the per-survivor test-writer, and that last phase
is the one most likely to surprise you on a slow suite: each authored test is
compile-verified against the unmutated code and reissued when it fails, so a file
with a dozen survivors can spend longer being *written for* than it spent being
scored. Corral's own `time:` line reports the phases it measures; phases that did not
run read `—`. Treat the totals above as the floor for a repeat run, not the ceiling
for a first one.

**Declare your models once — a registry.** Naming four seats on every command
line is the visible cost of having no defaults, and it is where stale model
names come from. Put them in `.corral/models.json` and name seats by alias:

```jsonc
{ "strict": true,
  "fast":   { "provider": "google",    "model": "gemini-3.6-flash" },
  "writer": { "provider": "anthropic", "model": "claude-sonnet-5" },
  "critic": { "provider": "google",    "model": "gemini-3.7-flash" },
  "local":  { "provider": "ollama",    "model": "qwen3.5:9b-q8_0",
              "endpoint": "http://127.0.0.1:11434" } }
```

```bash
corral certify --repo . --substrate workspace \
  --derive-model fast --mutant-model local --writer-model writer --critic-model critic \
  -- pytest
```

Seat flags still take a concrete model name, so nothing above changes if you
prefer them, and a seat you do not name still refuses the run — the registry
declares what is *available*, never what is chosen. `"strict": true` makes a
mistyped alias cost two seconds instead of failing at the seat hours into a
paid run: the run refuses and lists the aliases you declared. Local models are
first-class, so the generator can run on your own hardware while a hosted
model writes. `CORRALAI_MODELS_FILE` points at a file elsewhere;
`CORRALAI_MODELS` takes the JSON inline. The verdict records the concrete
model each seat resolved to, never the alias — an alias rename cannot move a
cache key or blur a record. Two rules keep the registry the *operator's*:
an alias may not be spelled like a concrete model (`"claude-sonnet-5": {…}`
is refused — an operator who types that model gets that model), and on a CI
runner the checkout's own `.corral/models.json` is ignored, because there the
checkout is the change under audit and may not choose its own auditors; a
workflow declares the registry through `CORRALAI_MODELS_FILE` or
`CORRALAI_MODELS`. The reasoning, and what comes next, is in
[docs/design/model-registry.md](docs/design/model-registry.md).

**Which model should staff which seat?** `corral models rank` answers it from
evidence you recorded rather than from benchmarks — proven gaps per survivor
attempted for the writer, missed-fault yield for the generator, adjudicated
precision for the critic — and it refuses to recommend on thin evidence. Its
evidence is what you gave it: by default the local ledger `certify --local`
writes, or with `--db` a warehouse that `certify --repo --push` filled (a
`--repo` scan that was never pushed left nothing for it to rank). It prints a
ranking; it never staffs a seat.

The levers that bound it: `corral doctor` (below) catches environment failures for
free before a run spends anything; `--top` bounds a whole-repo scan to the
highest-ranked N candidates instead of auditing every file; the mutant budget is
sized to each file's complexity and fitted to your `--timeout` from a measured
suite cost (below), and on `certify --local` `--n-mutants` sets it by hand
(`certify --repo` has no such flag); and `--mutants`/`--record-mutants` let you pin a recorded
mutant set and replay it — comparing runs, or re-grading after a fix, without paying
for a fresh generator call. There is no cache that makes a slow suite fast; these
bound *how many times* the suite runs, not how long each run takes.

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
prove gaps once it does.** Measured by the maintainer on `src/flask/cli.py` in
pallets/flask across four runs (no artefact in this repository): one authored test passed on the unmodified source and then killed **14 of
14** survivors, proving every one by execution; another failed on the unmodified
source and proved nothing. The determining factor was the test's own soundness, and a
test that fails on correct code is now reissued to the writer with the failure fed
back rather than abandoned. (One file, one project: a measurement, not a guarantee
about your repo.)

Which is why zero proven gaps means *nothing was proven this run*, and never *your
tests are fine*.

**A recorded proof is specific; discovery is variable.** A fresh run can plant
different faults and produce different tests, so both the kill rate and the
proven-gap count can change on unchanged code. Replay a recorded mutant set when
comparing suites; writer success and flaky execution can still vary. A proven
test shows a distinction from one planted fault, not that the original program
is correct or that the assertion matches its intended specification.

**Review and integrate the tests, do not merge them blindly.** Tests that pass
alone can interfere through fixtures, global state or execution order. On
unreleased main after rc.24, per-survivor runs with multiple proven tests also
run the merged file once against the unmodified code. `authored_together` is
`pass`, `fail`, or absent when not checked; a failure warns without changing the
individual proven count or certification status. A pass is not a flakiness
guarantee. Run the tests in your full suite before adding them.

Mutation-guided test generation has prior art, including
[Meta's ACH (arXiv January 2025; FSE 2025 Companion, Industry track)](https://arxiv.org/abs/2501.12862) and
[LLMutantKiller (ISSTA 2026)](https://doi.org/10.1145/3832098).
Corral's offering is the integration of execution checks, model seats, CI and a
portable record, not invention of the survivor-to-test loop. These citations are
not evidence that Corral outperforms those systems.

### Certify a change by its declared check

The other entry point takes any commit and any command:

```bash
corral certify <ref> -- <cmd>      # e.g. corral certify HEAD -- go test ./...
```

It checks `<ref>` out into a jail, runs `<cmd>`, reads the exit code, and writes a
signed record you verify offline with `corral certify verify` — no server required.
`--brain <url>` posts it to the optional, frozen brain daemon instead, if you
run one.

## Three constraints the machine enforces

Not a slogan — the code refuses to do otherwise.

1. **The judge is a different judge.** Cross-model checking is **distinct by
   construction**: the model that critiques a suite is *forced* to differ from the one
   that wrote the exposing test — the run refuses to start where they collapse onto
   one model. Most swarm frameworks run one LLM in N roles: parallelism with
   *correlated* blind spots, because the "reviewer" shares the "builder's" failure
   modes when it's the same model. Distinctness raises the floor; it does not buy
   independence outright, and corral is the only tool we know of that **measures**
   the difference rather than asserting it — see `--shadow-writer-model` below.
   Bring Claude, Gemini, GPT, anything OpenAI-compatible, or a local model — no
   lock-in.
2. **The verdict is measured, not reported.** The gate **runs the actual check** —
   `go test`, the build, the control owner's tests, your suite against the mutants —
   itself, in the jail, and reads the result. A worker's "it passed" is never the
   verdict; it's a claim, and the claim is checked by execution. The correctness call
   is a deterministic bit, not a judgment.
3. **The boundary is named, and so is the record.** A model that writes and runs
   code is a security problem, so corral says where the fence is instead of
   implying one: `certify --local` runs every mutant in a jail (`bwrap` on Linux; no network,
   secret-free env, workspace-confined — the only mode, `--jail none` is refused);
   `certify --repo --substrate workspace` runs your suite **unjailed, in your own
   checkout**, and the caller — a CI runner, a throwaway tree — *is* the boundary,
   which the scan header says out loud. Provider keys never sit in plaintext or a
   process listing. And what a run keeps is explicit: its ledger entry under the
   audited repo's own `.corral/ledger/` (`--no-ledger` to skip it), `--push` for a
   warehouse you own, `--attest` for a signed statement a third party can check.
   Nothing leaves the box that you did not send, and nothing recorded is
   fabricated — see **[SECURITY.md](SECURITY.md)**.

The name is the metaphor, but the mechanism is **separation of duties**: the seat
that writes a test, the seat that plants the fault, and the seat that reviews the
suite are separate *duties*, and the machine refuses to let one model hold two of
them where that would let an author grade himself. The **corral** is the enclosure
those seats work in and the **fences** are the security boundaries.

> **Where it's at:** solo-maintained. The rule for this README is that every
> claim was run before it was written; a claim found to break that rule is
> corrected in the open. Issues and verified-harness PRs welcome.

## In CI — the GitHub Action

**No brain, no server — the GitHub Action.** `pdbethke/corralai@v1.0.0-rc.27` (pin the
tag, or a reviewed SHA — `@main` floats) runs `corral
certify --repo` straight in your own CI job, on the checkout that's already
there: no jail, no brain, no separate infra. It mutates the runner's checkout
in place and grades each mutant with your own test command — the runner
itself is the isolation boundary, so this is for CI, not a working tree you
care about. Scoped to the PR's changed files by default (auditing every file
on every PR is expensive — between 5 and 40 suite runs per file, one per
mutant, the count set by the file's complexity; it doubles only if you name a
--shadow-model); a whole-repo run is opt-in. `path` scopes it to one directory
(one crate of a Cargo workspace, one package of a monorepo), `top` caps how
many files one run audits, and `max-tokens` caps its spend. By default a weak
but gradable result still exits 0. The merge gate to use is
`max-proven-missed` (`--max-proven-missed` on the CLI): it fails the run when
any audited file has more proven missing tests than you allow, and each of
those is a specific bug your suite was shown not to catch. `min-kill-rate`
also exists, but a kill rate is a proportion of freshly generated faults and
moves between runs on unchanged code, so a threshold near a healthy value
fails good work at random. See
**[docs/corral/github-action.md](docs/corral/github-action.md)**.

**Coverage pre-flight (`--preflight`, CLI only — every language but Rust).** `certify
--repo --preflight` runs the project's test suite **one extra time**, with
coverage instrumentation, and reports which source files it never touches at
all — a whole-repo inventory for the cost of one suite run, instead of the
5-to-40-suite-runs-per-file the adversarial audit itself costs (one per mutant;
see the budget below). It's
**coverage-grade evidence, not proof**: instrumentation has blind spots
(subprocesses, dynamic imports, native extensions), so the report separates
what it actually knows into three buckets — files the suite **executed**,
files it **measured and never executed** (the real finding, printed by
name), and files it **never measured at all** (printed only as a count,
never named — naming one would be an accusation about a file the run never
looked at). **"Executed" does not mean the same thing in every language, and the difference
is worth knowing before you act on a report.** On Go and Python it can mean
*imported* rather than *tested*: Go runs `init()`/var-initializer code at import
time, and in Python every module-scope `def` and `class` is a counted statement,
so importing a module clears it outright — Python's exposure here is the wider
of the two (see [docs/corral/github-action.md](docs/corral/github-action.md) for
both measurements). Ruby, JavaScript, TypeScript and PHP do **not** have that
exposure, because their reporters count *methods and named functions*, not
lines: Ruby uses the stdlib `Coverage` module's method coverage, the Node path counts named
functions in V8's own range data, and PHP reflects over method bodies. Measured on a fixture, a
module that is required and never called reports `lines_hit 2/3` — indis-
tinguishable from a file under test — and `methods_called 0/1`, which is the
truth; those three languages report it as **measured and never executed**, which
is the finding you want.

None of these reporters asks the audited project to install anything. Ruby's `Coverage`
is standard library, reached through `RUBYOPT` (the only window in which it can
start before application files load), and Node's is `NODE_V8_COVERAGE`, which is
built in — so no SimpleCov in the Gemfile, no c8 or nyc in `package.json`, and
no edit to the tree under audit. Because both are environment variables and
environment is inherited, one mechanism covers every way those suites are
actually launched: `rspec`, `rake`, a bare `ruby`, `node --test`, jest's
workers, vitest, mocha, `npm test`.

**PHP works too, with one disclosed condition.** It is the only one of the six
pre-flight languages that cannot be instrumented with what a machine already has: PHP reports no
coverage without a runtime extension (pcov or Xdebug). It still asks nothing of
the audited *project* — the extension is injected through `PHP_INI_SCAN_DIR`,
which is why `vendor/bin/phpunit` and `composer test` are instrumented as
readily as a bare `php`, neither of which would accept a `-d` flag. Without a
driver the run fails and the pre-flight reports that it could not run, naming
nothing — never "nothing is covered". PHP needs the method-body treatment most
visibly of the four: pcov reports an executed line for a file's implicit include
marker, one past its last line, so any file that was merely required looks
covered under a naive rule. Reflection supplies the start and end line of every
user-defined method, so the question asked is whether a *body* ran.

A scan whose candidates span more than one language usually declines the same
way — one instrumented run can't cover two — **unless** an explicit `--
<test-command>` unambiguously names exactly one of them. Each reporter accepts
only its own runners for exactly this purpose (`npm test` is Node's, `pytest -q`
is Python's, `go test ./...` is Go's). The Ruby, JavaScript, TypeScript and PHP
reporters instrument through the environment, so they also see through a `sh
-c` wrapper to find the runner inside. Python's and Go's add arguments to the
runner's own command line, which in a wrapper would reach `sh` instead, so they
claim only an unwrapped command: Python a `pytest …` or `<interpreter> -m
<module> …`, Go a `go test …` or `gotestsum … -- …`. Go claims nothing else —
`make test`, or `gotestsum` without `--`, never passes the coverage flags to `go
test`. Languages that would run the identical instrumented command are one run,
not a choice: JavaScript and TypeScript share theirs. Any other command claimed
by more than one language still declines as ambiguous. Same fail-closed
rule when the coverage tool itself is missing from the runner. Not yet wired
into the GitHub Action as an input — today it's a `corral certify --repo`
flag only.

**Per-file timeout (`--timeout`, CLI only).** `certify --repo` shares
`--local`'s own `--timeout` flag (default 30 minutes): the wall-clock budget
each file's run gets before the pool is forced to a `needs-review` verdict
instead of converging. A file whose run hits this deadline after the dev
suite's own kill-rate was already measured is reported as **audited**, not
dropped — marked `[TIMED OUT — pool did not converge]` in the weakest-files
list (and `timed_out` on its ledger row) so it never reads as a
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

**The record (the ledger directory, CLI and Action).** Every `certify
--repo` run keeps what it computes — a row per file the scan audited (with
its kill rate) or rejected (with a machine-stable reason), every mutant's
fate, every model call, the run's events, the verdict JSON and the authored
test — as **one hash-linked, gzipped JSON entry** (signed when a certify key is
configured) under the audited
repo's `.corral/ledger/` (`--ledger <dir>` moves it, `--no-ledger` skips
it, `$CORRAL_LEDGER` overrides the default). Plain text, ~21 KB a run; a
git branch on a runner, a folder on a laptop; DuckDB reads it in place.
There is no database to record into; `--cache-db` names the one local
file corral keeps, a cache of derived goals and test selections, which
nothing a verdict rests on lives in. A write failure — a full disk,
an unwritable directory — never changes the scan's own verdict or exit
code: it prints a loud line on stderr and the result stands, because this
command's exit code is a CI merge gate and bookkeeping must never be able
to red-build a PR. Two runs writing one directory at once is the one case
the chain refuses by construction (a chain has one head); on a shared
branch the Action's recipe fetches, `corral ledger append`s and pushes.

**Reading it back (`corral scans`).** The record is only worth writing if
something can query it, so:

```bash
corral scans list                    # recent scans: repo, substrate, audited, kill rate
corral scans show <id>               # per-file dispositions for one scan
corral scans show <id> --evidence    # + the pool's own authored test
corral scans show <id> --timing      # + where each file's wall clock went
```

Plain files, no brain and no database required, read-only by design (an
id is the entry's position in the chain, oldest first; `--ledger <dir>`
reads another directory). `show` renders
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
a destination rather than a lock-in. Append-only. Every row carries the run's
`scan_uid` and, traceable only with `--attest`, the sha256 of the signed
statement it came from — so a row
can be checked against something a third party can verify, and without
`--attest` that column is honestly empty rather than fabricated. It answers
what one pull request cannot: a single kill rate is a sample — the same
unchanged diff has scored `0.85` and `0.90` — while forty of them, pushed
across forty PRs, are a distribution you can read a trend from. `md:<db>`
targets MotherDuck and reads its token from the `motherduck_token`
environment variable (the Action exposes it as the `motherduck-token` input).
There is no `--motherduck-token` flag: a token on a command line lands in shell
history and in every process listing on the box.

`--push-source` additionally sends the pool's authored test and the full
verdict JSON — never mutant code: a pushed row never carries a hunk, under
any setting. (Mutant hunks land on disk only where you ask for them —
`--record-mutants`, or a `--local --record` tape.) Off by default: without
it, a pushed row carries numbers, hashes, reasons, and model names, and no
source leaves the box.

**Public transparency (`--attest --transparency`).** When a local signing key
is configured (`CORRALAI_CERTIFY_KEY_FILE`), `--attest` also signs the same
statement into a DSSE envelope beside the plain file (`<path>.dsse.json`) —
the plain file `actions/attest` consumes in CI is untouched either way.
`--transparency` uploads that envelope to Sigstore's public Rekor log and
prints the receipt (`attestation logged: rekor index <n> (uuid <u>)`); it
refuses (exit 2) rather than upload an unsigned statement, and fails **open**
on the upload itself — a Rekor outage prints one line and leaves the scan's
own exit code untouched. **The entry is public and permanent: once logged it
cannot be removed or edited, by anyone, including corral.** `corral verify
--attest <path> [--db <dsn>] [--rekor-index <n>]` is the checker that ships
with the claim: it verifies the DSSE signature and reports who signed,
recomputes the pushed warehouse rows' hash against a `--db` and compares it to
the statement, and confirms a Rekor entry — given or read back from `--db` —
matches the envelope on disk. Three independent checks, one line each, never
a silent pass.

`--transparency` is a CLI flag; the Action exposes `attest` but not (yet)
transparency. A real one is already in the public log — [Rekor index
`2667058567`](https://rekor.sigstore.dev/api/v1/log/entries?logIndex=2667058567),
from a flask audit — so you can check the claim before you install anything.

Every pushed mutant row carries its **shape** — the kind of fault the hunk
plants, read from the SEARCH → REPLACE diff itself and never from the model's
own label (`condition-negated`, `boundary-shifted`, `constant-changed`,
`return-changed`, `call-removed`, `exception-dropped`, `branch-removed`,
`argument-changed`, `other`) — and the **generator model** that planted it.
"Which shapes does this model plant, and which does this suite let through"
is then one query:

```sql
SELECT generator_model, shape, count(*) planted,
       sum(outcome = 'survived') survived, sum(proven) proven
FROM corral_mutants GROUP BY ALL ORDER BY survived DESC;
```

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
evidence-paired 0 · name-paired 28 · uncovered 0 · import-only 0 — uncovered/evidence-paired unknown without a run — pairing shown
languages detected:
  python  28 source file(s): 6 auditable, 21 with no paired test, 1 ambiguous (+9 test file(s))
```

The candidacy line (`evidence-paired · name-paired · uncovered · import-only`) is the
v0.8.1 addition: `--dry-run` runs no suite, so `uncovered`/`evidence-paired` read 0
and say so rather than implying a measurement — on a real run, evidence can widen
candidacy past filename pairing (see below). Widened candidates compete under the
same `--top` bound as everyone else: the report re-ranks and says `auditing N of M`
once more, and N never exceeds what you asked for. Files under a language's test
tree (`tests/conftest.py`, `spec/support/*.rb`, an in-tree test server) are
`test-support` — part of the surface that grades, never a subject, however many
tests load them.

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
asks for that deliberately. Coverage evidence also decides **candidacy**. A
library file the evidence positively measured at zero covering tests is
excluded rather than audited, under one of three honest names — `uncovered —
no test executes this file`, `imported at load time — no test exercises it
directly`, or `no executable code` — because a test-scoped kill rate has
nothing to grade it against. Absence of evidence is never treated as evidence
of absence: a file the run never measured keeps `no paired test`. A file that
reaches the audit and is *then* found uncovered has its kill rate withheld and
fails `--min-kill-rate` — a withheld number must never satisfy a threshold.

**The foreign-repo sweep (CI, every PR).** `scripts/foreign-sweep.sh` runs
`certify --repo --dry-run` — enumeration, pairing, ranking, selection, no
audit and no suite execution — against eight SHA-pinned real-world repos
nobody on this project wrote, and diffs the walked/candidate/ambiguous
counts against a checked-in golden file. It exists because pointing corral
at foreign repos surfaces defects the in-repo suite never can; `express` is
pinned to pair **zero** candidates on purpose, as a one-directional canary
for the JS/TS pairing limitation above. How to move a golden number, and why
that is a deliberate act, is in
**[docs/corral/foreign-sweep.md](docs/corral/foreign-sweep.md)**.

## In your coding agent — the `corral` skill

[`skills/corral/`](skills/corral/SKILL.md) is an
[Agent Skills](https://agentskills.io) folder that teaches a coding agent to
use corral the way corral uses a model: read the record, check every claim by
running something, and leave the verdicts to a person.

**Claude Code — install it as a plugin.** Two commands, which install the skill
and wire the findings MCP server together, so there is no `.mcp.json` to edit:

```bash
claude plugin marketplace add pdbethke/corralai
claude plugin install corral@corralai
```

`corral` itself must be on your `PATH`: the plugin runs the CLI you already
installed, it does not ship one.

**Any other Agent Skills client.** Copy the folder to where your agent loads
skills (`~/.claude/skills/corral/`, or `.claude/skills/corral/` in the repo).
To let the agent read findings over MCP as well, add this to `.mcp.json`:

```json
{ "mcpServers": { "corral": { "command": "corral", "args": ["mcp"] } } }
```

The skill teaches four things agents got wrong without it:
- **Finding the record at all.** Without the skill, every agent looked in
  git history and asked the user where the findings were.
- **Checking a critic finding against its own claim.** "Cannot fail" is
  disproved by any bug that turns the test red. Without the skill, agents
  confirmed the claim from a bug the test missed.
- **Undoing planted bugs without `git checkout`.** Checkout also wipes the
  user's uncommitted work.
- **Handing adjudication back to a person.** The critic's precision score
  would otherwise measure the agent.

**What has been run:** Claude Code 2.1.280 in headless mode (Sonnet 5), against
a small Python fixture, three repetitions per scenario, before and after the
skill. The two install commands above were run on Claude Code 2.1.284 on Linux
from a clean config directory — the marketplace added from this repository, the
plugin installed and reported enabled, the inventory showing one skill and one
MCP server, and `corral mcp` answering `tools/list` over stdio with
`list_audit_findings` and `get_audit_finding`. They have not been run on macOS
or Windows, nor in Cowork, and the skill has not yet been run in other Agent
Skills clients (Codex, Cursor, Gemini CLI, Hermes Agent, OpenClaw), so no
support for any of those is claimed.

## Review — a cold model's opinion, linked to reproductions it ran

`corral review --scope internal/advpool --reviewer-model <m>` hands a scope
of the repository at HEAD to a model that has never seen it, told to assume
the code is wrong. What comes back is an opinion carrying findings, each
with a tier the reviewer declared: **REPRODUCED**, with a `sh` script that
exits 0 iff the defect is demonstrated; **CODE-READ**, a `file:line`
argument with no execution; **HYPOTHESIS**. corral runs every REPRODUCED
script in the same offline sandbox `certify` uses, seeded from the reviewed
commit — your checkout is never the subject — and with no sandbox on the
machine, review refuses before any seat is called. `--substrate workspace`
runs each script in its own plain worktree at the commit instead; it is opt-in, and the record
says which ran. A script that does not hold demotes its finding to
CODE-READ on the record, saying why. The reviewer must also list what it
checked and found sound, so the review's coverage is legible. corral does
not sign the opinion; it signs the entry that carries the reproductions
(script, output, exit) as one more ledger entry beside the audits. A
person's verdict on a finding is its own entry: `corral review adjudicate
<dir> <hash>#R1 --confirm|--refute --reason "…"`, the newest per finding
standing, automatic passes never writing one. Not a gate — exit 0 either
way. Its first two runs were on corral itself (see the CHANGELOG): one
claim refuted, one real bypass confirmed in a verb written that morning.
With `--verifier-model`, a third model — never the reviewer's — is handed
the review as recorded and tries to refute every finding by the same rules;
a refutation whose script holds demotes the finding, and the record names
who refuted it and how. `--attest <path>` signs the reproductions — never
the opinion — as an in-toto statement the ledger entry names, and `corral
verify --attest <path> --db .corral/ledger` recomputes their hash from
the entry. `corral models rank --db .corral/ledger` grades both seats —
the reviewer by the claims that held of those checked, the verifier by the
verdicts that agreed with the outcome, a person's adjudication counting
as the outcome when there is one — under the same evidence floor as every
other seat. Both entry kinds also name the **audited party** — the commit's
author, committer and `Co-authored-by` trailers, by name, never an address
— so the record says who wrote the change beside who judged it, and
`models rank --seat committer` grades each party, a person or an agent
alike, by the changes that held under audit. The first use of that row is
the party's own: what the audit gave back about their change. Names are in
the ledger and in a warehouse push (they are already in the git history
beside it); corral shares nothing on its own. `--push <dsn>` on `review` and `adjudicate` appends the same
rows — `corral_reviews`, `corral_findings`, `corral_adjudications`, scripts
withheld unless `--push-source` — to a warehouse or MotherDuck, where the
seats rank across every repository that pushes. A seat can be a coding
agent — any agent you assign: `--reviewer-model <name>`, pinned to a model
as `<name>:<model>`, where a name is a command line you define as
`CORRALAI_AGENT_<NAME>="…"` (`claude-code`, `codex` and `antigravity` come defined; see [docs/corral/agent-seats.md](docs/corral/agent-seats.md)) —
started in a throwaway worktree with the brief on stdin; it reads the
tree itself and hands back scripts, and only corral's run of them is the
record. `corral review plan` is
the round planner: which scopes were reviewed and when, which changed
since (a fix batch nobody re-attacked), which never were — and a proposal
a person confirms. `corral review churn` reads the same record and says, per
round, whether findings were in code an earlier review had already looked at
(drain) or in lines written since (churn). `corral brief --scope <path>` (or `--changed <base>`)
is the record handed back to whoever writes next: for each file, the
newest scan's verdict, each fault your tests missed with the test that
closes a proven gap, each review claim that stands and what a person
ruled — claims that did not hold are counted, not listed. It reads the
ledger and writes nothing; `--json` is the same report for an agent to
read. In CI, the Action runs the review on the pull request's changed
directories when `reviewer-model` is named, into the same ledger branch as
the audit, with `--substrate workspace` because a hosted runner cannot start
bwrap. By default a REPRODUCED finding that stands fails the check (exit 3,
after the entry is written); `review-fail-on: never` records without failing.
Agent seats are wired for the Action (`claude-code-oauth-token` and
`codex-api-key` pass a login through) but have not yet been run on a
runner. The design, and the week that produced it:
**[docs/design/adversarial-review.md](docs/design/adversarial-review.md)**.

## The audit flags

`corral certify --local` is one command, but it fans out:

- **Sharded generation.** The file's top-level functions are bin-packed
  (complexity-balanced, deterministic) into up to `--max-shards` (default 8) generator
  seats, each attacking a different group of functions, so **every function gets
  probed** instead of whatever one generator happened to pick.
- **The mutant budget — sized to the file, fitted to the clock.** How many faults
  a file is planted with is derived from its **complexity**: about one per
  decision point, floor 5, ceiling 40, split across the seats by each seat's
  share. Before this existed the exam was flat — every seat asked for 5, so a
  file of eight one-line wrappers got 39 faults and a file of real logic got the
  same — and on `psf/requests` that put most of a four-hour run into files with
  the least to get wrong (`api.py`: complexity 8, 39 mutants, 36 survivors,
  timed out). Under `--substrate workspace` the budget is also **fitted to your
  `--timeout`** from a *measured* cost: the concurrency probe already ran this
  file's own selected tests in every tree, twice, so half of it is what one round
  of mutants costs, and a budget that would not grade inside half the deadline is
  lowered — never below the floor — under its own rule name. On `certify
  --local`, `--n-mutants N` sets a per-seat budget by hand instead (`certify
  --repo` has no `--n-mutants`); it is never fitted. **Every verdict, ledger row and signed statement carries the
  budget and its rule** (`complexity`, `complexity-fitted`, `explicit`, `default`),
  because a kill rate over 8 mutants and one over 40 are different measurements.
- **The ledger — the record, as text, written by default.** A `--repo` scan
  writes its entry into `.corral/ledger/` under the repository: one gzipped
  JSON file — the scan, its files, every mutant with its place, shape and
  outcome, the model calls, the events — that names the previous entry's
  hash and, when a certify key is configured, carries an Ed25519 signature
  over its own. `corral verify --ledger .corral/ledger` walks the chain and
  checks retained hashes, links and signatures under a supplied public key.
  Interior deletion breaks a link; tail truncation needs an external expected
  head to detect. An unsigned chain can be rewritten and rehashed by its owner.
  DuckDB is the view,
  never the store: `seal --db <dir>`, `models rank --db <dir>` and `verify
  --db <dir>` read a directory as they read a warehouse, `--push md:` sends
  the same entries to MotherDuck, and plain DuckDB reads the files with
  `read_json_auto('scans/*.json.gz')`. On a runner the Action writes the same
  entries into a checkout of the `corral/ledger` branch; make your local
  directory a worktree of that branch and a laptop run and an Action run are
  one writer. `--ledger <dir>` moves it, `--no-ledger` skips it. `certify --repo`
  and `review` also keep a copy in the machine ledger under the state directory
  (`CORRALAI_MACHINE_LEDGER`; skipped by `--no-ledger`, and never written inside
  the audited checkout); `certify --local` writes there by default. A
  `--repo` tree with no commit writes no entry. Twenty-one
  kilobytes a run, measured. ([the field note](https://corralai.dev/field-notes/the-ledger-is-just-text/))
- **The prior (`--prior`) — the next run plants what the last one didn't.**
  Hand a run what earlier runs recorded — a `--record-mutants` document (the
  hunks), a ledger directory (the outcomes), or a directory of either — and
  the generator is told, for each file whose bytes are **exactly** what the
  prior was recorded against, every edit already tried there: its place, its
  shape, its hunk, and what happened (*killed by test_x* — the suite watches
  this; *survived, gap already proven*; *survived, unproven*), then asked to
  plant different faults. A file the prior knows only under other bytes gets
  none, and the report says so. **A primed exam is a different exam**: the
  verdict, both ledgers and the signed statement carry `priorsApplied` and the
  prior's digest, and the digest is in the cache key, so a repeat audit never
  reads as the tests changing when only the exam did. Unset, the prior is the
  repository's own ledger — a second run on a laptop is primed by the first,
  and says so. With the budget sized to
  the file and the reach recorded per run, successive primed runs cover the
  decision points the last one didn't — cumulative reach, computable from the
  warehouse. On a runner, the `corral/ledger` branch is where the prior lives
  (see [docs/corral/github-action.md](docs/corral/github-action.md)).
- **A cap on money (`--max-tokens`).** Corral bounded mutants, shards, wall clock
  and concurrency and never tokens; a per-survivor writer on a survivor-heavy
  file paid a call per survivor and nothing clamped it. `--max-tokens N` is one
  cap across every seat of a run (every file of a scan). A call is admitted only
  while the spend so far, plus every call in flight sized at the largest call
  the run has measured, leaves room for one more, so parallel calls cannot
  slip past it; the overshoot is at most about one call. Once reached, no
  further model call is made: a file whose generator
  never ran is ungradable *for that reason*, a writer or critic seat past the
  cap is skipped and the file flagged as it is for a provider failure — the
  dev kill rate already measured stands — and the cost line reports the run's
  real totals (`REACHED — N call(s) made, M tokens spent`). A seat that
  reports no tokens (a `text`-wire agent) cannot be capped, so a capped run
  naming one is refused before it starts. Local (Ollama) seats now meter their tokens
  too; they used to read as zero.
- **Confidence — two terms, never one number.** Beside every kill rate the report
  prints what the bare rate hides: its **95% interval**, computed over the
  distinct graded edits only, so a repeated edit is counted once (Wilson, because
  the exams are small and the rates sit near 0 and 1 — `0.62,
  95% interval 0.30–0.86 (n=8 distinct)`), and the exam's **reach** — how many of the
  file's symbols and decision points a fault actually landed on (`reached 8 of 8
  symbols, 8 of 8 decision points`). Both are computed from what the ledger
  records per mutant (its line span) and per file (its decision points), so a
  reader with the warehouse can recompute them, and both are signed into the
  statement. They are deliberately not blended into one index: a blend hides
  which term is weak, and the point is to see that a 39-mutant exam which
  reached 2 of 8 decision points covered *less* than an 8-mutant exam that
  reached all of them. The point estimate still decides the rate, but the
  interval now gates certification: a rate that clears the threshold on an
  interval wider than 0.35 is routed to `needs-review` and marked
  `[INDICATIVE — …]`. Reach is reported and signed, not gated. The usual Wilson
  interpretation assumes independent observations sampled representatively from
  a defined fault population; generated edits do not establish those assumptions.
  This interval is not a probability of software correctness, even when narrow.
  See [the measurement limits](https://corralai.dev/docs/concepts/mutation-testing/#what-the-confidence-interval-does-and-does-not-mean).
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
  header. On `--substrate workspace` the same budget buys PRIVATE TREES instead:
  one file is audited at a time, and its mutants are scored concurrently in
  separate copies of the checkout (budget/4, minimum 1), because two mutants in
  one tree would overwrite each other. The isolation is the tree, not the
  sequence. Honest caveat: this pays in proportion to how much of your audit is
  suite time, which on a very fast suite is not much.
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
  now asked of the critic. It's human-gated: `certify --local` records each
  critic finding to a local store (`corralai_criticscore.duckdb` in corral's state directory, `CORRALAI_STATE_DIR`), and
  `corral criticscore` reads it there — no brain required — so the C-PREC column
  reflects only findings a person has confirmed or refuted. `certify --repo`
  records nothing to it. Independently of that store, every run checks the
  critic's "this test can never fail" claims by execution: the named test runs
  alone against the run's mutants, and a claim it disproves (the test killed
  one) is recorded on the verdict as refuted and left out of the critic's count.
  Every language has a runner that can do this (go test, pytest, jest,
  vitest, mocha, `node --test`, RSpec, PHPUnit, `cargo test`, `cargo
  nextest`). The claim is left unchecked, and the run output says why, under
  minitest, under a shell-wrapped command, and for a name that would run
  more than one listed test.
- **The tests×mutants matrix (`--matrix`, opt-in).** After the primary pass, re-score
  EVERY dev test alone against the run's mutants — a per-test adequacy readout instead
  of one suite-wide kill-rate, plus a safe-to-delete candidate list (a test that caught
  none of the planted mutants). The summary is printed with the run's verdict
  (`matrix: N test(s) scored, M delete-candidate(s)`). Costly (T
  tests × M mutants extra jail runs) and opt-in for that reason; go + python only
  today; a delete-candidate is relative to the mutants THIS run happened to plant, not
  proof a test is dead weight — review before deleting, don't auto-delete on it.
  Python single-file mode: the `--test` filename must follow pytest's discovery
  convention or enumeration finds nothing and the matrix is silently skipped
  (repo-dir mode unaffected).
- **Robustness.** A non-terminating mutant is a kill only if the unmutated
  baseline still passes under the same cap — otherwise it is unmeasured, never a
  fabricated kill (a broken loop can't stall the run); `--test-timeout` overrides
  the auto-derived per-mutant cap. A writer seat that can't author a killing test
  for a survivor routes the file to `needs-review` rather than spinning. One thing
  does *not* converge to a verdict: a provider that never answers the
  mutant-generator ends the run with that error and no verdict, because a verdict
  over zero mutants would be a number nobody measured.
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
  never installs anything into your checkout. The one kind of step it runs for you is
  vendoring a project whose dependencies live outside the tree (`go mod vendor` for a Go
  module with no `vendor/`): in a throwaway copy, inside the sandbox, with network
  access for that step only. `--bind-dir <path>` (repeatable, repo-relative) binds
  additional dirs the same way; `--no-bind-deps` restores the pre-bind behavior
  (copy every dep dir into the seed, subject to the size cap). A run prints `deps:
  bound N dir(s) read-only (...)` whenever anything was bound.

**`certify --local` always runs sandboxed** (`bwrap` on Linux and `sandbox-exec`
on macOS by default; `--jail container` for a docker/podman fallback) — it has
no unsandboxed option. **`certify --repo --substrate workspace` is the
unsandboxed option**, by design: it runs your suite in your checkout and the
caller is the boundary.

**On Ubuntu 24.04 and later, bwrap will not start until AppArmor allows it.**
AppArmor restricts the unprivileged user namespaces bwrap needs. Ubuntu ships
a profile for bwrap, `bwrap-userns-restrict`; load it (on 24.04 it may still be
under `/usr/share/apparmor/extra-profiles`). These are the commands corral's
own error prints:

```bash
[ -f /etc/apparmor.d/bwrap-userns-restrict ] || sudo cp /usr/share/apparmor/extra-profiles/bwrap-userns-restrict /etc/apparmor.d/
sudo apparmor_parser -r /etc/apparmor.d/bwrap-userns-restrict
```

Do not write your own `/etc/apparmor.d/bwrap` marked `unconfined`. It takes
bwrap away from the shipped profile and leaves it without the capabilities it
needs. corral printed that advice before 1.0, so if you followed it, remove the
file:

```bash
sudo apparmor_parser -R /etc/apparmor.d/bwrap && sudo rm /etc/apparmor.d/bwrap
```

Or skip bwrap with `--jail container` and a toolchain image
(`export CORRALAI_EXEC_IMAGE=python:3.12-bookworm`). One gotcha:
the language toolchain has to be **jail-visible** — installed system-wide under `/usr`,
not a `--user`/snap/pyenv install invisible to the sandboxed mount namespace.

### The jail, in detail

The `corral` process is never sandboxed — only the subprocess a check spawns is
isolated (under `--substrate workspace`, nothing is: see above):

- **Default-deny.** Execution only runs once a backend has been resolved and
  preflighted. If the host can't isolate, execution stays disabled and returns a loud,
  actionable error — it never silently degrades to running unprotected.
- **`bwrap` backend (default, Linux).** Each command runs in an unprivileged namespace
  jail: network off, read-only root except the workspace, no privileged caps, a
  secret-free env (the token never reaches it). Needs `bubblewrap` present.
- **`sandbox-exec` backend (default, macOS).** A Seatbelt profile confines the
  command to its workspace and denies reads of your home directory. It is a
  weaker boundary than the Linux jail: it is the same user, so a jailed command
  can read the start-up environment of your other processes through the
  kernel (measured on macOS 27, 2026-10-13). Do not run it with API keys
  exported in other shells you have open, or use `--jail container`.
- **`container` backend.** `--jail container` (or `AGENT_EXEC_BACKEND=container`) runs
  the jailed command inside a docker/podman container with `--cap-drop=ALL`,
  `--read-only`, `--network=none`, and pid/memory limits — for hosts without bwrap.
  Refuses to start without `CORRALAI_EXEC_IMAGE` naming the toolchain image, e.g.
  `CORRALAI_EXEC_IMAGE=python:3.12-bookworm`.
- **Network off by default.** Opt a build step in only where it legitimately fetches
  deps.

bwrap shares the host kernel — it stops casual damage, egress, and filesystem escape,
**not** a kernel-exploit escape. For adversarial code use a stronger backend
(container/microVM); the pluggable `Isolator` makes that a drop-in.

## Security model

The headline feature, not a footnote. (The OpenSSF Scorecard badge above read
5.7 on 2026-10-16. It scores project practice, not code quality: its low
checks include code review, which a solo maintainer cannot score on, dependency
pinning, fuzzing and static-analysis tooling; see SECURITY.md.) Full write-up in **[SECURITY.md](SECURITY.md)**;
the short version for the audit:

- **The jail (`certify --local`).** Every mutant is graded in a `bwrap` jail (`sandbox-exec` on macOS, weaker; see above): no
  network, workspace-confined, a secret-free environment (the jail is built from
  a scrubbed environ — your provider key never reaches the suite). This is what
  makes running a model's test unattended safe: corral bounds *what a command
  can touch* instead of asking *whether it may run*. On bare-metal Linux the
  jail is one unprivileged `bubblewrap` package; Docker is only a fallback.
- **The workspace (`--repo --substrate workspace`) is not a jail**, and says so
  in the scan header. Your suite runs in your checkout with the same scrubbed
  environment, and the caller is the boundary — a CI runner, or a tree you can
  throw away. It is the path for a project whose virtualenv the jail cannot see.
- **The record is yours.** Every run writes its signed entry into the audited
  repo's own `.corral/ledger/` (`--no-ledger` to skip); `--push` appends to a
  warehouse you own; `--attest` signs a statement and `--transparency` logs it
  publicly. The last three are off unless named; a write failure never changes
  a verdict or an exit code.
- **Portable, secure key storage.** Provider API keys never sit
  in plaintext or leak into a process listing. `corral secret set NAME` reads the value
  from **stdin, never a CLI argument**, and the keystore resolves each secret through
  **env var → OS keyring → an age-encrypted file** — your OS keychain on a desktop, an
  age-encrypted store on a headless server (the identity fails closed, protected by a
  systemd credential or a `0600` key). Every log redacts secret values to a
  fingerprint. It's the GCP-ADC pattern, shipped in one binary.

The daemon's own surfaces — the console bundle's trust anchor, the single
trusted egress, artifact storage — are in
[docs/corral/brain.md](docs/corral/brain.md#the-brains-own-security-surfaces).

Every security core was adversarially red-teamed, and the tests ship with the repo.
The codebase runs clean through **`gosec`** (0 findings at medium+ — every one fixed or
adjudicated inline) and **`govulncheck`** (0 vulnerabilities reachable from corral's own
code paths — it also reports a handful in imported packages and required modules that
nothing calls, names them, and says so), both
enforced in CI by [`scripts/check-security.sh`](scripts/check-security.sh).

**Don't trust the claims — run them:** `go test ./...` and `bash scripts/check-security.sh`.

## Platforms

| | Linux | macOS | Windows |
|---|---|---|---|
| **`corral certify --local`** — real exec (sandboxed) | ✅ bwrap | `sandbox-exec` (default) or `--jail container` (Docker) — **not exercised in CI** | `--jail container` (Docker) or WSL2 — **not exercised in CI** |
| **`corral certify --repo --substrate workspace`** (no jail) | ✅ | should work — **not exercised in CI** | should work — **not exercised in CI** |
| **The GitHub Action** | ✅ `ubuntu-latest` | — | — |

**"Not exercised in CI" is meant literally.** Every test job runs on
`ubuntu-latest` (only the job that redeploys the optional daemon runs on a self-hosted runner), so the macOS and Windows rows describe a path that should work
and that no run has proven. The container backend itself IS exercised — and was
found completely broken the first time anyone executed it, because Docker mounts
`--tmpfs` `noexec` and Go compiles its test binary into `/tmp` — but its
integration tests used to skip without `CORRALAI_EXEC_IMAGE`, which nothing set — that
is fixed: CI now builds the jail image with **both** docker and podman and runs the
backend's integration tests twice, once pinned to each, on every code change.
Hosted macOS and Windows runners are free for public repositories, so this is a
task rather than a limitation.

**The jail is a Linux capability — and that's the point.** `bwrap` (bubblewrap) is
Linux namespaces; on a bare-metal Linux host it runs **unprivileged** (one package,
no root, no daemon). macOS has `sandbox-exec`, a weaker boundary (see [the
jail](#the-jail-in-detail)); Windows has no equivalent, so exec runs inside a
Linux environment — WSL2, or the `--jail container` fallback. `corral`
itself carries two CGO deps (DuckDB for the ledgers, tree-sitter for the symbol
index), which is why `go install` builds for ~25s; the optional daemon's client
binaries are pure Go (see [the brain](#the-brain-optional-and-not-read-by-any-audit)).

## Why Go — and why your stack doesn't have to be

**The substrate is Go** because the audit has infrastructure-shaped requirements
and Go is the boring, correct answer: **one static binary**, no runtime and no
virtualenv on the machine that runs it; embedded databases without an ops bill
(DuckDB compiles straight in); honest cross-compilation.

**What corral audits is a different axis — any language the models know.** The
audit takes your own test command — `go test`, `pytest`, `npm test`, `rspec`,
`phpunit` — and reads its exit code. A Python-and-Svelte team never writes a line
of Go to run, or benefit from, the audit; Go is just what the corral fence is made
of.

## The brain (optional, and not read by any audit)

Corral began as a coordination daemon — a **brain** with an MCP surface, a task
queue, shared memory, a learning loop, a live console and a fleet of client
binaries. It is now its own binary, **`corral-wrangler`** — the wrangler is the
hand who manages the string of horses, and this is the coordinator for the
agents that share a codebase (`corral` with no subcommand points there).
Everything above runs without it: `certify` audits in-process, on a laptop or
a CI runner, and writes its entry to the ledger in your own repository.
**Nothing in the brain is required for a verdict, and nothing in it is read by
one** — an audit's prompts are built from the goal, the code and its symbols, and
a lesson the brain has learned is not consulted by `certify`. If you want remote
workers, a live console, or the human-gated proposal loop, it is documented,
with those limits stated, in **[docs/corral/brain.md](docs/corral/brain.md)**.

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

## Sponsor this work

Corral is built in the open by one maintainer, with agentic assistance — and
audited by itself: the [recordings gallery](https://corralai.dev/recordings/)
leads with corral grading its own signing code, gaps and all. If you want the
old guards — CI/CD, software tests, accountability — to hold as AI writes more
of both the code and the tests that gate it, you can fund that work:
**[github.com/sponsors/pdbethke](https://github.com/sponsors/pdbethke)**.

---
**[corralai.dev](https://corralai.dev)** — a live-replay one-pager (`site/`, Astro,
Cloudflare Pages) · github.com/pdbethke/corralai. Full docs — concepts, a UI tour, and
a generated CLI reference — at [corralai.dev/docs](https://corralai.dev/docs).
