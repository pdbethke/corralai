<!-- SPDX-License-Identifier: Elastic-2.0 -->

# AGENTS.md — operating instructions for AI agents working in this repository

This file is for an AI coding agent working **on corral itself**. It is not
about using corral; that is [`CORRAL.md`](CORRAL.md) and
[`docs/corral/`](docs/corral/), which are the corpus corral ships to *your*
project.

Everything here was learned by getting it wrong first. If a rule seems
arbitrary, the paragraph under it is why.

## What this project is

A Go CLI that audits whether a test suite actually tests anything: one model
plants deliberate, goal-violating faults in a file, the project's **real** test
command runs against every fault inside a sandbox, and the kill rate is what
execution measured — never what a model reported. A second, decorrelated model
reads the suite and flags tests that assert nothing; its opinion is advisory and
**can never gate a verdict**.

The single invariant behind all of it: **a claim is worth nothing until
something executed it.** Most defects in this codebase are violations of that
invariant hiding as ordinary bugs — see "The bug shape that keeps recurring".

## Build, test, and the gates that will fail your PR

Go 1.26.6 (see `go.mod`). No Makefile; use the Go toolchain directly.

```bash
go build ./...
go test ./...           # full suite; must be green
go vet ./...
gofmt -l ./cmd ./internal   # MUST print nothing
bash scripts/check-security.sh
```

`scripts/check-security.sh` is the same script CI runs. It asserts, in order:
**gofmt** on every tracked `.go` file, **gosec** with zero MEDIUM-or-higher
findings, and **govulncheck** (non-fatal if not installed locally).

`gofmt` has failed a PR here for a one-line import ordering slip. Run it before
you push; it costs nothing and the CI round trip costs minutes.

Two drift gates bite agents specifically, because both files look hand-written
and are not:

- `bash scripts/gen-cli-docs.sh --check` — the CLI reference is **generated
  from the binaries' own `-h` output**. Never hand-edit it; regenerate.
- `bash scripts/sync-site-assets.sh --check` — the site's vendored replay
  player must not drift from source.
- `bash scripts/check-licensing.sh` — every tracked `.go` file carries the
  Elastic-2.0 SPDX header, and LICENSE/NOTICE/README still say what they must.
  `scripts/add-spdx.sh` fixes the header half. **Two of the three gates in this
  list run on EVERY change, docs-only included** — this one and the `^TestDocs`
  tests — because all three of them grade Markdown as well as Go.

### CI workflows

| workflow | what it does |
|---|---|
| `deploy.yml` (`validate`) | the doc gates and the licensing gate ALWAYS; then, only when something other than Markdown changed: vet, provision language toolchains, `go test -v` with a SKIP census, the drift checks, gosec, security gate |
| `foreign-sweep.yml` (`sweep`) | `certify --repo --dry-run` over SHA-pinned third-party repos, diffed against `testdata/foreign-sweep-expected.tsv` |
| `self-audit.yml` | corral auditing corral. Non-blocking, label-gated, all-Gemini, `top: "1"` |
| `cla.yml`, `sbom.yml`, `scorecard.yml` | CLA, SBOM, supply-chain scorecard |

The test step runs `go test ./... -v` **on purpose**: plain `go test` never
prints SKIP markers, and a sandbox test that silently skips is indistinguishable
from one that passed. A skipped jail test proves nothing about the jail, which is
what the whole product rests on. If you change that step, keep the census.

The sweep's golden file **fails hard when missing** — it must never bootstrap
itself green. Regenerating it is a deliberate act with a human reading the diff.

## Merging

**Always merge through a pull request.** Branch protection has
`enforce_admins: false`, so a direct push to `main` silently **bypasses every
gate above**. The gate existing is not the same as the gate running.

Commit messages here are full sentences explaining *why*, not just what. Look at
recent history before writing one — that convention is load-bearing for a
codebase whose comments carry most of its institutional memory.

## Where corral writes, and the one mode that touches your checkout

**By default corral never writes to your repository.** Both `certify --local`
and `certify --repo` run on the `jail` substrate: the tree is copied into a
disposable bwrap sandbox, dependency dirs are bound read-only, and every mutant
is applied to the COPY. `certify --local` has no `--substrate` flag at all, so
it cannot select anything else.

**One substrate mutates the real checkout, and it is opt-in:**
`certify --repo --substrate workspace`. That is what the GitHub Action uses
(`action.yml`), and it is correct there for the reason the flag's own help
gives — an ephemeral CI runner IS the isolation boundary. `--repo-dir` is a
*precondition* of that substrate, not a trigger for it.

Under the workspace substrate the apply/restore ledger is defended: writes go
through `os.OpenRoot` so a symlink cannot escape the root, originals are
journaled, files that did not exist are removed rather than left as strays, and
restore runs via `defer` — covering a failing command, a timeout, and a panic,
each asserted by its own test.

**The residual risk, stated exactly:** there is no SIGINT/SIGTERM handler in
`cmd/corral`, so Ctrl-C during a `--substrate workspace` run can leave a mutant
on disk, and nothing detects a stale mutant on the next start. That is a real
gap on the opt-in path. It does not apply to jailed runs, which is every
default invocation.

So: run `--substrate workspace` only where the caller is the isolation boundary
(CI, a scratch copy, a tree with no uncommitted work). And never audit a
repository belonging to someone who has not agreed to it.

## Running an audit: the traps, in the order you will hit them

Every one of these cost real runs, and none of them announce themselves.

**`--repo-dir` is required for any multi-file project.** The bare
`--code FILE -- <cmd>` form is the *single-file* shape: it seeds only that file
and its test into the sandbox, so a real project's imports and config are absent
and the baseline fails with a build error. Quickstart examples show the short
form; it does not work on a real repository.

**The test command's first token is resolved on the HOST, but the command runs
in the JAIL.** So `./node_modules/.bin/vitest` is refused host-side, while a
bare `vitest` is not on the jail's PATH. What works is a host-resolvable
interpreter with a workspace-relative script: `node ./node_modules/vitest/vitest.mjs`.

**The test command must be broad enough to collect the AUTHORED test.** corral
writes its killing test as a *new file* beside the developer's. Pin the command
to one file (`vitest run path/to/one.test.ts`) and the run grades perfectly and
reports **`proven_missed: 0` forever**, because the authored test is never
collected. Scope to a directory or a prefix. This one has silently under-reported
the tool's best result more than any other.

**Toolchains must be visible to the jail.** Install language test frameworks
system-wide (`/usr`), not `--user`: the sandbox binds `/usr` and will not see a
user-local install.

**The jail is offline.** Anything needing the network fails inside it, by design.

## Trying it quickly

`corral demo --writer-model <m> --mutant-model <m> --critic-model <m>` writes a
self-contained Go package and audits it with the real `certify --local`. Use it
to check a change end to end without needing a cooperative target repo — five of
six audits on real third-party code died on the ENVIRONMENT, not on the tool.

`corral doctor` checks sandbox, toolchain-inside-the-sandbox, credentials and
test pairing for free, before any spend. It cannot check whether the suite passes
on unmutated code inside the jail, which is the most common way an audit dies —
run the target's own test command by hand first.

A run echoes the pool's beats to stderr as they happen (`--quiet` disables), and
`certify --local` ends with the tokens it consumed.

## Model routing and spend

**corral has NO default models.** `--writer-model` and `--mutant-model` are
required on `certify --local` and `--repo`; `--critic-model` has no fallback
(`off` disables the critic); `--derive-model` is required on `--repo` unless
`--goals` is supplied. A run with an unnamed grading seat is refused before any
jail, store or spend, and the refusal reports which provider credentials it can
actually see. The brain is the same: with no `CORRALAI_ADVPOOL_MODELS` and no
leaderboard evidence, the pool is DISABLED rather than cold-starting on
something nobody chose.

This is deliberate. corral claims to be model-agnostic, and a binary that names
one vendor's models when the operator named none is making an exception to that
claim. We run Claude; we do not make anyone else.

**The shadow challenger is OFF unless named**, and it doubles the mutant count
when on. It used to default to a Claude model and be on, which quietly kept an
Anthropic seat alive through an otherwise all-Gemini run.

**A key alone does not move providers.** Each role resolves its own backend from
its own model name; a Gemini model name with only an Anthropic key configured is
a 404 from Anthropic, not a Gemini call. Set the role models *and* the matching
key. An explicit `MODEL_BACKEND` pointing at a gateway (OpenRouter, Ollama) is
never re-routed — those front many vendors behind one endpoint.

`CheckDecorrelation` enforces only **critic ≠ writer** — a property, not a
vendor, so any two distinct models satisfy it. It is legal to run every seat on
one vendor — which satisfies the letter
of "decorrelated" and not its point. Prefer genuinely different vendors.

## Adding or changing a language plugin

Plugins live in `internal/lang/` (`go.go`, `python.go`, `ruby.go`,
`javascript.go`, `typescript.go`). A plugin declares detection, test-path
convention, test command, compile check, and the role system prompts.

Two rules, both learned expensively:

**A compile check must be scoped to the audited file and its test — never
project-wide.** A whole-project type-check fails on any pre-existing error
anywhere in the repository, so a perfectly good authored test is rejected for
reasons the audit never touched, and every survivor is reported unproven while
the run still looks healthy. Go scopes `go vet` to the audited package;
TypeScript names the two files explicitly.

**A plugin's system prompt pins ONE harness, and real projects pick their own.**
That is correct in single-file mode, where corral owns the harness, and wrong
against a real repository. The writer is shown the project's existing test file
and told, with explicit precedence, to match it. If you edit a
`TestWriterSystem`, do not re-introduce a hard "builtin modules only"-style rule
without a way to rank it below the project's own conventions.

## The bug shape that keeps recurring

**A real measurement, correctly computed, then dropped on the floor.** It has
appeared as: a failing baseline's output computed and never printed; that same
string dropped again at a struct-to-struct copy that simply omitted the field; a
kill rate fabricated as `0.00` when nothing had been graded; and a per-role model
scorecard the router could act on for one role in three.

When touching anything that computes a number or a diagnostic, ask what reads it
and follow the value all the way to the surface. **A field-by-field converter
fails open on whatever field nobody remembered to add** — prefer a test that
asserts the value survives the boundary.

Related: never let a could-not-measure outcome render as a measured zero. A
`COULD-NOT-GRADE` that explains itself is worth more than a number that is wrong.

## Claims reviewers keep getting wrong

Four independent AI reviews of this repository (2026-09-01) produced eight
confident false claims between them. Every one had the same shape: **the
evidence was read correctly and the state of the world was inferred wrongly** —
which is precisely the failure this project exists to refuse. The list below is
here so the ninth reviewer does not spend an afternoon on the same ground.

Before reporting any of these, run the check named beside it.

- **"`Verdict` is constructed in two places, so a new field can be dropped."**
  **THIS ONE IS TRUE, and it was wrongly listed here as false.** The two sites
  are `tickAggregate` (`internal/advpool/driver.go`) and `timeoutVerdict` in the
  same file, whose own comment says it "is the second Verdict construction site
  in this package, and it has now been the place a field was forgotten more than
  once." Both route through `verdictFromSpec`, which is why a `grep 'Verdict{'`
  finds one literal and looks reassuring — the literal is shared, the field
  ASSIGNMENTS are not. The drift this entry was written to record —
  `timeoutVerdict` carrying `ProvenMissed` without `ProvenMutantIDs` or
  `AuthoredTest` — is FIXED, along with the missing `PoolScored` discriminator
  that made the count unreadable. The two paths remain two paths, so the
  hazard stands: a new scored field must be added to both.

  The entry is left here, corrected rather than deleted, because of HOW it got
  here: the reviewer was overruled on a `grep -rn "func tickAggregate"`, which
  cannot match a method with a receiver. A search that cannot find the thing is
  not evidence the thing is absent. **Grep for the bare identifier before
  concluding a symbol does not exist**, and prefer `go doc` or a compile error
  to a pattern you wrote yourself.
- **"GitLab and Gitea are stubs."** Both providers are complete implementations.
  Exactly two methods return `errors.ErrUnsupported` — `ListOpenPRs` and
  `SetCommitStatus`. The true, narrower claim: **the gate is GitHub-only.**
- **"The workspace substrate has no signal handling."** `cmd/corral/signalctx.go`
  cancels (never exits) so deferred restores run. What is genuinely missing is
  crash *detection* — see `WorkspaceRunner.Verify`'s doc comment for why that
  needs a durable journal rather than a check.
- **"Add a fast/deep tiered mode."** It exists: the driver cancels the
  test-writer outright when a run has no survivors. Pass 2 is already
  conditional on Pass 1. `grep -n 'moot test-writer' internal/advpool/driver.go`
- **"`0 proven gaps` is reported as a pass."** `TestWriterFailed` and
  `PoolTestUnsound` are distinct verdict fields and both force `needs-review`
  unconditionally. Whether the CLI *renders* them legibly is a fair question;
  the scoring is not.
- **"CheckDecorrelation is blind to vendors — two models from one provider
  pass."** The FUNCTION compares names, but corral is not blind: same-vendor is
  detected and reported twice — at seat resolution (`certify_local.go`, "an
  independent MODEL but not an independent VENDOR") and on the verdict itself
  (`certify_adversarial.go`, "every graded seat is google"). It WARNS rather
  than refuses, deliberately, because refusing strands a single-vendor
  operator. Reported as a missing capability twice; it is a product decision.
  `grep -rn "independent VENDOR\|every graded seat is" --include=*.go .`
- **"Pair tests to sources by parsing imports."** Pairing already uses execution
  coverage (`evidence-paired` on the scan line), which proves a test exercises a
  file. An import proves only that it mentions one. Import parsing is a
  downgrade.
- **"Run-to-run kill rates swing wildly."** The measured spread on an unchanged
  diff is **0.85 → 0.90**, and it is documented in `CHANGELOG.md` and the
  `--push` help. Variance is real; do not invent a magnitude for it.
- **"`go test ./...` is red, so the repo is not green."** Check whether the
  offending file is tracked first — `git check-ignore -v <path>`. A gitignored
  local draft under `docs/launch/` or `docs/superpowers/` is not part of this
  repository. (The doc gates now filter ignored paths themselves; this note is
  for every *other* test that walks the tree.)

The general rule this list is an instance of: **this repository documents its
tradeoffs in the comment above the code that makes them.** When something looks
like an oversight, read the doc comment on the function before reporting it —
several of the misses above are explained ten lines from where the reviewer was
looking.

## Documentation rules

Docs are part of the product's argument, so they are held to the product's
standard.

- **Never state a number nobody ran.** An estimate is labelled as an estimate.
  A cost figure in these docs was once off by roughly ten times, in the direction
  that would have talked a reader out of trying the tool.
- **Never claim support that has not been executed** against a real repository.
  A tool arguing for execution over self-report cannot claim a capability on the
  strength of a passing unit test.
- Corrections stay visible where they illustrate the point, rather than being
  quietly edited away.

## Where things live

- `cmd/corral/` — the CLI; `certify_local*.go` is the headline audit path
- `internal/advpool/` — the adversarial pool: roles, prompts, the driver
- `internal/adequacy/` — scoring, the bwrap jail, the workspace runner
- `internal/sandbox/` — the isolation boundary and bind construction
- `internal/lang/` — language plugins
- `site/` — the Astro/Starlight site (`npm run build` in `site/`)
- `docs/corral/` — the public knowledge corpus that ships to users
- `scripts/` — the gates; read the header comment before changing one
- `private/` — **gitignored, never committed.** Machine-specific operator notes for
  whoever's box you're on (host setup, local GPU/model plans). This repo is public;
  nothing host-specific goes in a tracked path. If `private/local-gpu-plan.md` exists,
  read it before planning local-model work — the hardware it describes may be mid-change.
