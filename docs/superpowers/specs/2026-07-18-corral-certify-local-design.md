# `corral certify --local` — One-Command Adversarial Audit

**Date:** 2026-07-18
**Status:** Approved for planning
**Author:** Peter Bethke (+ Claude)

## Problem

Corral's product is the adversarial audit gate — but there is no frictionless way to
try it. Running one signed verdict today requires: a headless brain daemon (with
`CORRALAI_ADVERSARIAL_POOL=1`), two `corral-agent` worker processes registered over MCP,
an OIDC/JWT brain token, and a jail-visible toolchain. The docs, worse, still describe the
*retired* build-mission flow. A curious developer cannot get to a first "wow."

This adds a single command that runs a complete adversarial-pool audit **in-process** —
no daemon, no MCP, no auth, no separate workers — reading the user's own API key from the
environment and printing a signed, offline-verifiable verdict:

```bash
go install github.com/pdbethke/corralai/cmd/corral@latest
export ANTHROPIC_API_KEY=sk-ant-...            # your own key; that is all
corral certify --local \
  --code path/to/your/file.go \
  --goal "what this code must guarantee" \
  -- go test ./...
```

The buy-in mechanism: from install to a signed verdict in ~2 minutes with one key.

## What stays load-bearing (soundness is NOT relaxed)

`--local` changes *packaging*, not the trust model. All four soundness invariants hold:

1. **The jail.** The kill rate is still measured by *executing* the dev's tests against
   the mutants in a `sandbox.Isolator` jail (`adequacy.Score`) — never a self-report.
2. **Decorrelation.** `advpool.CheckDecorrelation` still refuses a run where test-critic
   and test-writer share a model. `--local` picks two distinct models by default.
3. **The user's own API keys** — read from env, never stored.
4. **The signed verdict.** Still produced by the real certify chain
   (`certify.BuildLedger`/`SignDSSE` + `buildstore`), verifiable with
   `corral certify verify <record>`.

What `--local` collapses is pure *architectural friction*: the separate daemon, the two
worker processes, MCP registration, the OIDC token, and the feature flag.

## Design (Option A — drive the pure Driver in-process)

`internal/advpool.Driver` is already pure: it drives a 3-role DAG on a `queue.Store` and
never calls an LLM. `internal/brain/advpool_integration_test.go` already proves a hermetic,
serverless run: local `queue.Store` + `buildstore` + a generated signing key + the real
`advpoolSigner` → a signed verdict verified with `certify.VerifyDSSE`. `--local` turns that
test into a product path by (a) using the real jail-backed Scorer/Validator, and (b) adding
an in-process LLM worker.

### Components

1. **`internal/advpool/localgate` (new) — the jail-backed adapters, relocated.**
   Move `advpoolScorer`, `advpoolValidator`, `advpoolSigner` out of `internal/brain`
   (where they reference `brain.Options`) into a package the CLI can import without the
   server. They are thin wrappers over `adequacy.Score` / `adequacy` compile-check /
   `certify.*`+`buildstore` — no `brain.Options` is actually required (the signer needs
   only a signing key + a `buildstore`). The brain keeps using them from the new home.

2. **`internal/agentworker` (new) — the embeddable worker loop, extracted from
   `cmd/corral-agent`.** The three roles are single-shot: `mutant-generator` and
   `test-writer` take the structured fast path (one `llm.Client.Chat` with the task's
   pre-rendered `Instruction`, raw output returned for `ParseMutants`/`ParseTest`);
   `test-critic` is a short findings loop. Extract this into an importable
   `RunRole(ctx, llmClient, task) (result string, findings []Finding, err error)` so both
   `corral-agent` (unchanged behavior) and `--local` share it (DRY). No MCP, no queue
   broker needed for a single local run.

3. **`cmd/corral/certify_local.go` (new) — the orchestrator.** On `--local`:
   - Resolve the jail: `sandbox.Resolve(Config{Backend: env or auto})`; on a bwrap userns
     failure, print the exact fix (Ubuntu apparmor profile) and offer `--jail container`.
   - Open a temp `queue.Store`, `buildstore.Open` (temp or `~/.claude`),
     `buildstore.LoadOrCreateSigningKey` (the user's own persistent key).
   - Build `localgate` Scorer/Validator/Signer over the jail; `advpool.NewDriver(...)`.
   - Resolve models (decorrelation, below); build two `llm.Client`s.
   - `StartRun`-equivalent: build the DAG (`advpool.BuildDAG`) for the run spec, enqueue.
   - Drive: `for { d.Tick(ctx, mid); for each ready task in the queue: agentworker.RunRole
     → complete/add-findings; } until RunStatus(mid).Converged`.
   - Render with the existing `renderAdvVerdict`; exit by status (0 certified, 3
     needs-review, 2 usage, 1 infra).
   - **No `brainToken()`, no `--brain`, no HTTP.**

### Decorrelation default (approved)

Two Claude models off a single `ANTHROPIC_API_KEY`: **test-writer = claude-sonnet-5**,
**test-critic = claude-haiku-4-5**, **mutant-generator = claude-sonnet-5**. This satisfies
`CheckDecorrelation` (critic ≠ writer) with one key. If `OPENAI_API_KEY` (or a Gemini key)
is also present, prefer a **cross-vendor** critic automatically (the stronger, honest
default). Overridable via `--writer-model` / `--critic-model` / `--mutant-model`.

### Jail on the user's machine

`sandbox.Resolve` already supports `bwrap` (Linux default), `container` (docker/podman via
`CORRALAI_EXEC_IMAGE`), `sandbox-exec` (macOS), and `none`+unsafe. `--local` adds:
auto-detect + a one-line actionable error. **Ubuntu 24.04 caveat:** unprivileged userns is
disabled by apparmor → bwrap preflight fails; the error must print the surgical
`/etc/apparmor.d/bwrap` fix and suggest `--jail container`. Never silently fall back to
unsandboxed (fail closed).

## Non-goals

- No hosted/SaaS trial. No change to the brain server, MCP tools, or the distributed
  worker path (they keep working). No new external Go dependencies. `--local` is additive.

## Testing

- **In-process e2e (hermetic-ish):** a test that drives `--local`'s orchestration with a
  *fake* `llm.Client` (canned mutants + a killing test) through the *real* jail-backed
  Scorer/Validator/Signer, asserting a signed, `VerifyDSSE`-valid verdict — mirrors and
  extends `advpool_integration_test.go`. Skips cleanly when no jail backend is available.
- **agentworker unit:** `RunRole` for each role with a fake LLM (structured parse for
  mutant-generator/test-writer; findings for test-critic).
- **localgate:** the relocated adapters keep the existing brain/advpool tests green
  (behavior-identical move).
- **CLI:** `--local` with a fake worker/driver: decorrelation default picks two distinct
  models; unknown language / missing key / jail-unavailable all fail closed with clear
  messages; verdict render + exit codes.
- **Manual:** a real `corral certify --local` on a small target with a live key
  (the launch demo).

## Rollout / docs (load-bearing — the docs are currently broken)

1. Make `corral certify --local` the **headline of the README Quickstart** and the site
   `getting-started.mdx`; delete the retired `mission create` / build-mission references
   and the stale "no CLI trigger" disclaimer.
2. A short "your first audit" doc: install, one key, one command, the verdict, `verify`.
3. The LinkedIn article + the site proof page use this exact command as the CTA.
4. Follow-on: `--jail container` polish; a `corral certify --local` recording.
