<!-- SPDX-License-Identifier: Elastic-2.0 -->

# Changelog

Notable changes to corral. Format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/);
versions are [semantic](https://semver.org/), and pre-1.0 means the CLI surface can
still move between minor versions.

Entries describe what changed for someone *using* the tool. For the full commit
history of any release, `git log v0.3.4..v0.3.5`.

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
