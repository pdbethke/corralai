<!-- SPDX-License-Identifier: Elastic-2.0 -->

# Changelog

Notable changes to corral. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
versions are [semantic](https://semver.org/), and pre-1.0 means the CLI surface can
still move between minor versions.

Entries describe what changed for someone *using* the tool. For the full commit
history of any release, `git log v0.3.4..v0.3.5`.

## [v0.5.8] — 2026-08-23

### Fixed

- **The release title comes from the tag, not from whatever was merged last.**
  actions/checkout does not fetch annotated tag OBJECTS by default, so
  `%(contents:subject)` resolved to the tag's commit and the first
  auto-published release was named after the changelog commit that triggered
  it — a plausible-looking wrong title rather than an error. The workflow now
  fetches tag objects, reads the annotation explicitly, and refuses a tag with
  no message rather than naming a release after an unrelated commit.

## [v0.5.7] — 2026-08-23

### Added

- **The release publishes itself when a tag is pushed.** Three times in one week
  a tag was cut, the docs were updated to pin it, and no release followed — so
  the Releases page advertised an older build than the docs told people to
  install. The tag is the intent; the release now follows from it, with notes
  taken from this file's section for that exact version. A tag with no section
  fails rather than publishing a page that describes nothing.

## [v0.5.6] — 2026-08-23

### Fixed

- **Verify instructions that work on the CLI people have.** The Action's job
  summary told a reviewer to run `gh attestation verify`. That command does not
  exist in gh 2.45, which some distributions still ship — and an older CLI
  answers it with a help dump rather than an error, so the reader concludes they
  mistyped. Found by verifying corral's own first attestation on a machine with
  2.45. The summary now names the version constraint and gives the plain API
  call the command wraps, which needs no particular CLI version.

## [v0.5.5] — 2026-08-23

### Added

- **`--attest` — the verdict becomes a receipt.** A signed record existed only
  from `certify --local`, and only where the run happened; the Action runs
  `certify --repo`, which had no way to emit one. So a pull-request audit
  produced a readable summary and nothing anyone could keep.
  `--attest <file>` writes the scan as an in-toto Statement — every audited
  file's kill rate, survivors and proven gaps WITH the flags that say what a
  zero means, the thresholds it was judged against, the models in each role, and
  the denominator, so a clean result cannot be flattered by omitting how little
  was looked at. The Action publishes it through `actions/attest`, signed
  keylessly by the workflow's own OIDC identity, so the signature chains to the
  repository and workflow rather than to a key that lived on an ephemeral
  runner. Free on public repositories.
  The statement is written BEFORE the gate's exit code is honoured and the
  attest steps run under `always()`: a receipt you only keep when the verdict
  flatters you is not evidence, and the failing runs are the ones a reviewer
  most needs.

### Fixed

- **A changed TEST puts its source in scope** (also in v0.5.3): a pull request
  that deletes assertions touches no source file, so the gate reported
  `NOTHING IN SCOPE` and passed green on the exact change it exists to catch.

## [v0.5.4] — 2026-08-22

### Added

- **`--max-proven-missed`** (and a `max-proven-missed` Action input) — the merge
  gate that does not flap. `--min-kill-rate` is the obvious one and the wrong
  default: a kill rate is a proportion of FRESHLY GENERATED mutants, so it moves
  between runs on unchanged code. A demonstration pull request that deleted
  every assertion pinning a library's central guarantee scored 0.85 and then
  0.90 on the identical diff — the higher number on the more weakened suite —
  and a 0.8 bar passed it both times. A proven-missed gap is a survivor the herd
  then KILLED with a test it wrote and ran: a demonstrated bug, not a
  proportion. `0` means any demonstrated gap fails the build.
  It fails CLOSED. With survivors present and no test that graded them,
  `proven_missed` reads 0 because nothing was proven, not because the suite is
  clean; those files fail and report as `PROVEN-GAP UNMEASURED` rather than
  passing on a question nobody answered.

## [v0.5.3] — 2026-08-22

### Fixed

- **A changed TEST puts its source in scope.** Diff scoping matched a candidate
  only on its source path, so a pull request that deletes assertions — touching
  no source file — was scoped to nothing: `NOTHING IN SCOPE: the diff touched no
  candidate; no audit was needed`, and the check passed green while the suite it
  guarded had just been gutted. Weakening a suite is the pure form of "tests
  that pass and defend nothing", the exact change this gate exists to catch, and
  it was the one change that could not reach it. The pairing was already
  resolved (`reposcan.Candidate.TestPath`, from `--tests` or the language
  plugin's convention), so scoping on either side of the pair needs no new
  configuration.

## [v0.5.2] — 2026-08-22

### Fixed

- **A too-narrow test command is refused before the audit, not after.** corral
  writes its killing test beside your own, and if your command names a single
  test file rather than a directory, nothing collects it. That was detected —
  but only *after* the test-writer had authored a test, so you paid for a whole
  audit to be told `proven_missed: 0` and "widen the command and re-run". The
  same check now runs before any model call: one jail execution, no inference,
  and the refusal names the exact file it would have written. 1.2 seconds
  instead of a wasted run. The assumption underneath was that the dev test's
  directory is "collected by construction" — true for a discovery-based runner,
  false for `node tests/x.test.js` or `pytest tests/x_test.py`, which is an
  ordinary way to invoke a suite.
- **A cross-vendor run no longer routes a seat to Ollama.** `baseVendor()` read
  an unset `MODEL_BACKEND` as "anthropic" while `FromEnv()` read it as ollama.
  They disagreed in exactly one reachable case — a MIXED-vendor run, where no
  single backend can be inferred and the variable stays unset — so a Claude
  critic beside a Gemini generator matched a phantom Anthropic base, skipped
  cross-routing, and was handed to Ollama, which 404s a model it has never
  pulled. The documented cross-vendor shape could not run without pinning
  `MODEL_BACKEND` by hand. Unset is now told apart from a deliberately pinned
  gateway: an unpinned run routes every cloud-named seat by name, while an
  explicit `openrouter`/`ollama` keeps its hands-off guarantee.
- **A disabled critic reports as disabled.** With `--critic-model off` the
  verdict printed "critic review: no vacuous tests flagged" — a clean bill of
  health from a reviewer that never ran, because the line keyed on an empty
  findings list. It now says "not run — no test-critic was assigned". A critic
  that ran and found nothing still says so.
- **Go 1.26.6.** Seven stdlib advisories reachable from corral's own call paths
  (`net/url`, `net/http`, `crypto/tls`, `encoding/asn1`, `encoding/xml`,
  `html/template`), all fixed in that patch release. They went unnoticed because
  `scripts/check-security.sh` runs `govulncheck` only when the binary is present
  and CI never installed it — so the security gate printed "OK: all security
  invariants hold" having checked formatting and nothing else. CI now installs
  it, and the gate means what it says.

### Added

- **The Responses API.** OpenAI serves its Codex models — `gpt-5-codex`,
  `gpt-5.1-codex`, `gpt-5.1-codex-mini`, `gpt-5.3-codex` — on `/v1/responses`
  only; they are not available on `/chat/completions` at all. Naming one used to
  resolve cleanly and then fail at the API boundary, telling an operator that a
  model they can see in the docs does not exist. Routing is now per model rather
  than per vendor.
- **One key is enough.** Three provider inputs and an enforced decorrelation
  rule read like corral needs an account with every vendor. It does not: the
  verdict is measured by execution and the critic is advisory. The GitHub Action
  docs now carry a single-key workflow, along with what that mode costs — corral
  prints a decorrelation warning naming the lineage that both planted the faults
  and graded the tests.

### Note

`v0.1.0` was the day-zero, builder-era release and remained the only published
release until now, so the Releases page advertised a July build while the docs
told you to pin a much later tag. This is the first release published since.

## [v0.5.1] — 2026-08-13

### Added

- **`corral demo`** — the two-minute first run. One command, no setup beyond a
  provider key: it writes a small Go package (a five-clause password rule and a
  test that checks exactly two of them, and passes) and audits it with the real
  `certify --local`. Go on purpose — you installed corral with a Go toolchain, so
  it is the one dependency you are guaranteed to have, and it normally lives under
  `/usr` where the jail can see it.
  It exists because the honest first run was not two minutes: auditing real
  third-party repositories took six attempts to produce one verdict, five lost to
  the environment. That is not a fair first impression of what the tool does.
- **Live progress.** A run used to print a few lines and then go silent for
  minutes while eight seats worked, which reads as a hang. The pool already
  emitted fine-grained beats — `--record` captured them for replay — and they were
  being written to a file instead of the terminal. They now echo as they happen,
  to stderr so nothing parsing stdout is affected. `--quiet` turns it off.

### Fixed

- **An unset `MODEL_BACKEND` is not "Claude".** The last place corral assumed a
  vendor: an unset backend was read as "the default direct-Claude path", so a run
  demanded `ANTHROPIC_API_KEY` no matter which models had been named. All-Gemini
  runs escaped it only because vendor inference happened to fire first; a local or
  unroutable herd fell straight through to it. Unset now means "infer from the
  assigned models" — which the code already did for every vendor except Anthropic,
  the one it handled by assumption instead of by evidence. When the models name no
  cloud vendor, nothing is demanded.
- The demo project is written 0750/0600 rather than 0755/0644.

## [v0.5.0] — 2026-08-13

### Added

- **The verdict cache is real.** `reposcan` has carried a content-addressed cache
  key, a `Cache` interface and consult/populate call sites since v0.3.0 — with no
  implementation, and `nil` passed in production. Every scan recomputed every file
  forever while honestly reporting `CacheHits: 0`. There is now a DuckDB-backed
  implementation, owner-scoped in SQL, and it is enabled.
  **It fails closed without exception:** an unreadable row, verdict JSON that will
  not parse, a missing store or an empty owner all resolve to a MISS. A miss costs
  money; a wrong hit signs a claim about content that was never measured.
- **The ledger records what it measures.** `scan_files` gains the verdict's own
  numbers as columns — model attribution, mutant and region counts, the coverage
  shortfall, status, and the honesty flags — plus `cache_hit`/`reused_from_scan_id`
  so an aggregate can EXCLUDE reused rows. Without that, enabling the cache would
  have made one measurement count once per scan forever.
- **`scan_mutants`** — one row per mutant per file per scan. "Which generator
  produces mutants a suite does not catch" is a question about mutants and could
  not be asked of a table whose finest row was a file. The mutant SOURCE is
  deliberately not stored: `parent_sha256` is enough to group and compare without
  putting tenant code at rest in the warehouse.
- **`suite_baseline_ms`** — the target suite's runtime, which `adequacy.Score` has
  measured on every run since the package existed and then thrown away. It is the
  single input to the audit cost model (`O(mutants × suite runtime)`), so every
  capacity estimate before this was an extrapolation.
- **Token accounting.** Every provider reports usage on every response and corral
  discarded it at the JSON boundary. `certify --local` now ends with
  `model spend: N in / M out token(s) over K model call(s)`. Tokens, not dollars —
  prices change, a token count stays true.
- **Reuse is disclosed by AGE**, not just counted: the report names the oldest
  reused verdict. A scan where most files were reused from weeks ago, presented as
  current, is the self-flattering record this tool exists to prevent.

### Fixed

- **The jail can see the passwd database.** Without `/etc/passwd` the jail's uid
  has no entry, so `getpass.getuser()` raises — and PyTorch calls it at import
  while computing its cache directory. Any Python project transitively importing
  `torch` or `transformers` died before pytest collected a test, reporting
  `COULD-NOT-GRADE` indistinguishably from a broken project. `/etc/shadow` is not
  bound and a test pins that it never will be.
- **The cache key was blind to what it keyed.** `ModelSet` and `AuditConfig` were
  hardcoded literals, so the key could not tell two model sets apart — and the
  same constant reached the ledger, meaning every row ever written recorded
  `model_set='unset'`. Both now carry real values.
- **`EngineVersion` is a deliberate `VerdictGeneration`**, hand-bumped when engine
  behavior can move a verdict. It was the release version, so a documentation-only
  release invalidated every cached verdict for every tenant.
- **Five wrong-hit paths**, each of which would have signed a claim about content
  that was never measured: mutant source reachable from a marshalled `Verdict`
  (closed by type, via `advpool.MutantRef`); the operator's own `-- <cmd>` absent
  from the key; `TestSurfaceDigest` covering one paired test file while the whole
  suite grades; Go `testdata/` goldens outside the keying surface; and a
  file-scoped scan declared on an argv that names tests beyond the selected set
  (closed across exact-path, directory and repo-root token forms).

### Known limitations

Documented in code as open wrong-hit paths, not as design: the file-scoped path
ignores the test-surface list, so weakening `tests/conftest.py` leaves the key
unmoved; a repo-root `conftest.py` with tests under `tests/` is uncovered; and
argv tokens given as absolute paths or symlinked directories do not disqualify a
file-scoped scan.

Two properties worth knowing before judging the cache on hit rate: **any test-file
change invalidates every verdict** on the whole-suite path (correct — the grading
surface really did change for every file), and **`GoalDigest` moves between runs**
because goals are LLM-derived per run, so unless `--goals` is pinned the cache is
largely inert.

## [v0.4.0] — 2026-08-13

### Changed — BREAKING

- **corral has no default models.** Every grading seat is named by the operator.
  `--writer-model` and `--mutant-model` are **required** on `certify --local` and
  `certify --repo`; `--critic-model` has no fallback (`off` still disables it);
  `--derive-model` is required on `--repo` unless `--goals` is supplied (`--goals`
  skips derivation and calls no model at all). A run with an unnamed grading seat
  is refused before any jail, store or spend, and **the refusal reports which
  provider credentials it can actually see** — the usual cause is "I have a key, I
  just don't know what corral wants from me."
- **The challenger (shadow) seat is OFF unless named.** It defaulted to a Claude
  model and was on, which quietly kept an Anthropic seat alive through an otherwise
  all-Gemini run and then failed for want of a key the operator had deliberately
  left behind.
- **The GitHub Action's `model-key-env` no longer defaults to `ANTHROPIC_API_KEY`.**
  A `model-key` with no `model-key-env` is refused rather than guessed; `writer-model`
  and `mutant-model` are required inputs.
- **An unconfigured hosted pool is DISABLED rather than cold-starting.** With no
  `CORRALAI_ADVPOOL_MODELS` and no leaderboard evidence the adversarial pool is never
  registered; a malformed `CORRALAI_ADVPOOL_MODELS` is refused instead of falling back.

Why: corral's claim is that it is model-agnostic — *across any model, local 7B to
frontier*. A binary that names one vendor's models when the operator named none was
making an exception to that claim, and it meant anyone arriving with an OpenAI,
Gemini or OpenRouter key hit a failure on their first command and had to discover
five flags to get past it. We run Claude; we do not make anyone else.

**The one rule that survives is decorrelation** — the test-critic must differ from
the test-writer. That is a *property*, not a vendor: any two distinct models from
any provider satisfy it.

**Upgrading:** add `--writer-model`, `--mutant-model` and `--critic-model` (or
`--critic-model off`) to existing invocations, and the equivalent inputs to Action
workflows. `corral doctor` checks a herd and its credentials for free before you
spend anything.

### Fixed

- **`corral doctor` had its own hardcoded Claude defaults.** With no model named it
  substituted `claude-sonnet-5` and reported a credential failure for it — telling an
  operator holding a Gemini key to go get an Anthropic one, from the command that
  exists to prevent wasted runs. It now reports the real finding, per seat.
- **`corral doctor` was missing from `corral -h`** — routed and working since it
  shipped, but absent from the usage text and therefore from the generated CLI
  reference on the site.
- The site advertised an older Gemini model than the README in the same worked
  example; both now agree.

### Documentation

- The README leads with what the tool does; the test-soundness argument moved from
  the third bullet into its own section rather than being cut.
- Every runnable example names a herd and says plainly that its model names are an
  example rather than a default.

## [v0.3.6] — 2026-08-12

### Added
- **`corral doctor`** — check the environment before paying for a run. It verifies
  that the sandbox starts, that your test command's toolchain is reachable *inside*
  the sandbox, that a credential exists for every model you plan to route to, and
  that the file you named has a test corral can pair with. Every check is free — no
  model is called — and they run in the order an audit would hit them, so the first
  `FAIL` is the first thing to fix. Exits non-zero if any check failed.
- **Findings served to a coding agent over MCP**, and the *reason* a verdict was
  reached is now recorded alongside the verdict, so an agent can act on a gap
  instead of just being told a number. The findings corpus also works with no brain
  running.
- **The verdict names a single-vendor herd** — a run where every role resolved to
  one vendor now says so in the record, rather than leaving decorrelation to be
  inferred.

### Fixed
- **The jail can see toolchains installed outside `/usr`** (#101). Compilers and
  runtimes from snap, asdf, nvm, rustup, pyenv and Homebrew were on the host's
  `PATH` but invisible inside the sandbox, which surfaced only as an undiagnostic
  baseline failure.
- **CI hands back the test that proves the gap** (#101 sibling). A proven gap was
  reported without the authored test that proved it, leaving nothing to act on.

### Changed
- Documentation corrects a claim the audit path never supported, and adds the
  fourth participant to the description of the herd.

## [v0.3.5] — 2026-08-04

### Added
- The findings corpus works without a brain — no server required to read it.

### Changed
- `verify` now says plainly that **v0.3.3 and earlier accept an edited record**.
  If you are relying on tamper-evidence, upgrade.

## [v0.3.4] — 2026-08-04

### Added
- Twelve verifiable records published — and the hole that publishing them exposed
  is fixed.

### Fixed
- Documentation pinned v0.3.3 rather than the stale v0.3.2.

## [v0.3.3] — 2026-08-04

The release that made non-Go repositories auditable at all. The launch scheduled for
this day was called off when the first run against a real Node project showed it
could not be audited; this release is the answer to that.

### Fixed
- **A real Node/TypeScript project can now be audited at all** (#81), plus two
  further gaps found by actually running corral against a third-party TypeScript
  project.
- **The jail could not see any gem installed on Debian/Ubuntu** — Ruby audits failed
  environmentally, not substantively.
- **The cross-vendor router routes every role**, not just the critic.
- **The authored killing test is written in the *project's* harness**, not the
  plugin's (#85), and a failed authored test now says *which way* it failed (#85).
- A test command's argv survives the trip through `TestCmd` (#91).

### Added
- The brain ingests a cloned repo's `AGENTS.md` as advisory memory.
- `AGENTS.md` — the operating guide this repo never had.

### Changed
- The five-languages claim is replaced with the evidence behind it.

## [v0.3.2] — 2026-08-04

### Fixed
- **`corral version` wrote to stderr**, so anything capturing stdout got nothing.
- An all-Gemini scan asked for a Claude key.
- Self-audit: provision the toolchains the audited suite needs; audit the corral
  actually checked out rather than whatever `@main` resolves to; never spend on a
  stranger's pull request.

### Added
- **The Action accepts several provider keys at once** (`gemini-key`,
  `anthropic-key`, `openai-key`), so the critic need not be turned off.
- The Action writes the verdict where someone will read it
  (`$GITHUB_STEP_SUMMARY`) and bounds what a run costs (`top`).

## [v0.3.1] — 2026-08-02

### Fixed
- **`corral version` said "dev" for everyone who installed it.**

## [v0.3.0] — 2026-08-02

The largest release so far: whole-repo auditing, the GitHub Action, the scan ledger,
and a long run of honesty fixes.

### Added
- **`certify --repo`** — fan an audit out over a whole repository, with ranked
  candidates, bounded spend, derived goals, and full accounting of what was *not*
  audited and why. `--dry-run --json` gives you that inventory for free, with no key
  and no money.
- **corral ships as a GitHub Action**, with diff-scoped audits, `--substrate`
  selection, per-role model flags, `--tests` (a source-to-test map for repos whose
  layout convention can't pair), and an opt-in `--min-kill-rate` merge gate.
- **`corral scans`** — the scan ledger was write-only until now.
- **A DuckDB ledger** of what each scan audited and rejected, plus the evidence
  behind `ProvenMissed` rather than only the count.
- **Opt-in parallel mutant scoring**, order-preserving (safe only on the jail
  substrate — see the flag's own documentation).
- **`--critic-model off`** for deliberately single-vendor runs.
- A coverage pre-flight that fails closed, and test-pairing against ordered
  per-language conventions.
- Signature extractors for Ruby, JavaScript and TypeScript.
- A foreign-repo enumeration sweep pinned in CI on every PR.

### Fixed
- **A surviving canary invalidates the report** — a suite that ignores the audited
  file is *ungradable*, not zero.
- **Stopped fabricating `ProvenMissed`** in repo-aware scoring, and stopped claiming
  pairing evidence for files that were never paired.
- A same-length mutant could reuse a stale `.pyc` and read as a survivor.
- The authored test is sited where the project actually collects it, and a positive
  control proves it ran.
- The workspace substrate is serialized (files share one checkout), and idle workers
  no longer eat the mutant budget.
- Action inputs are no longer interpolated directly into shell scripts.
- Extraction of Python class methods — most real Python was invisible to the scan.

## [v0.2.0] — 2026-07-22

### Added
- The cockpit UI: verbose per-agent audit console, a human gate on proposed tests,
  and views grounded in real DuckDB audit data.

### Fixed
- `COULD-NOT-GRADE` is reported when the jail baseline fails, instead of a
  fabricated score.
- The jail test command is shell-quoted (argv-safe), `GOTOOLCHAIN=local` is pinned
  in the offline jail, and Go dependencies are auto-vendored for `--repo-dir`.
- Corrective test-writer retries feed the compiler's own error back instead of
  blindly repeating.

## [v0.1.0] — 2026-07-03

First tagged release.

[v0.5.1]: https://github.com/pdbethke/corralai/releases/tag/v0.5.1
[v0.5.0]: https://github.com/pdbethke/corralai/releases/tag/v0.5.0
[v0.4.0]: https://github.com/pdbethke/corralai/releases/tag/v0.4.0
[v0.3.6]: https://github.com/pdbethke/corralai/releases/tag/v0.3.6
[v0.3.5]: https://github.com/pdbethke/corralai/releases/tag/v0.3.5
[v0.3.4]: https://github.com/pdbethke/corralai/releases/tag/v0.3.4
[v0.3.3]: https://github.com/pdbethke/corralai/releases/tag/v0.3.3
[v0.3.2]: https://github.com/pdbethke/corralai/releases/tag/v0.3.2
[v0.3.1]: https://github.com/pdbethke/corralai/releases/tag/v0.3.1
[v0.3.0]: https://github.com/pdbethke/corralai/releases/tag/v0.3.0
[v0.2.0]: https://github.com/pdbethke/corralai/releases/tag/v0.2.0
[v0.1.0]: https://github.com/pdbethke/corralai/releases/tag/v0.1.0
