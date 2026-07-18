# `corral certify --local` Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax.

**Goal:** A single command runs a complete adversarial-pool audit in-process — no brain daemon, no MCP, no OIDC token, no separate workers — off one `ANTHROPIC_API_KEY`, printing a signed, offline-verifiable verdict.

**Architecture:** Drive the already-pure `internal/advpool.Driver` in-process with a local `queue.Store` + `buildstore` + jail-backed Scorer/Validator/Signer + two embedded LLM workers. Grounded in the passing `internal/brain/advpool_integration_test.go`, which already proves a serverless signed run with fake scorers.

**Tech Stack:** Go 1.26; `internal/{advpool,adequacy,sandbox,buildstore,queue,llm,lang,testgen,certify}`; the user's own LLM key.

## Global Constraints

- Soundness is NOT relaxed: the jail still *executes* tests against mutants (`adequacy.Score`); decorrelation is still enforced (`advpool.CheckDecorrelation`); the verdict is still the real signed record (`certify.*`+`buildstore`). `--local` collapses only architectural friction.
- Fail closed: unknown language, missing key, or no working jail → refuse with a clear message; never run unsandboxed.
- Additive only: the brain server, MCP tools, and distributed `corral-agent` path keep working unchanged. No new external Go deps. gofmt + gosec clean; SPDX headers.
- Decorrelation default (approved): one `ANTHROPIC_API_KEY` → test-writer=`claude-sonnet-5`, mutant-generator=`claude-sonnet-5`, test-critic=`claude-haiku-4-5`; if a second vendor key (`OPENAI_API_KEY`) is present, prefer a cross-vendor critic. Overridable via `--writer-model`/`--critic-model`/`--mutant-model`.

---

### Task 1: Relocate the jail-backed adapters out of `internal/brain`

Move `advpoolScorer`, `advpoolValidator`, `advpoolSigner` so the CLI can use them without importing the whole server. Decouple the signer from `brain.Options`.

**Files:**
- Create: `internal/advpool/gate.go` (the relocated adapters, exported)
- Modify: `internal/brain/advpool.go` (construct from the new home)
- Test: `internal/advpool/gate_test.go`

**Interfaces:**
- Produces:
  - `type JailScorer struct { Jail adequacy.Jail }` with `Score(...)` (was `advpoolScorer`)
  - `type JailValidator struct { Jail adequacy.Jail }` with `CompileTest/ParseMutants/ParseTest` (was `advpoolValidator`)
  - `type Signer struct { Key *certify.SigningKey; Store *buildstore.Store; Witness transparency.Witness }` with `SignVerdict(ctx, advpool.Verdict) (int64, string, error)` (was `advpoolSigner{opts}`, now taking only what it uses). Check `advpoolSigner.SignVerdict`'s body for the exact `opts` fields it reads (certify key, build store, witness) and lift ONLY those into `Signer`.
- Consumes (unchanged): `adequacy.Jail`, `advpool.Verdict`, `certify.*`, `buildstore.Store`, `internal/lang`, `internal/testgen`, `internal/repoindex`.

- [ ] **Step 1: Read the three types + their `Options` usage.** In `internal/brain/advpool.go` read `advpoolScorer` (~119), `advpoolValidator` (~168), `advpoolSigner` (~213) and every `opts.X` the signer touches. The scorer/validator use `advPoolBase`/`advPoolTestPath`/`pluginFor` (lang) + the jail only — no Options. The signer uses the certify key + build store + witness.

- [ ] **Step 2: Move them to `internal/advpool/gate.go`** as exported `JailScorer`/`JailValidator`/`Signer`, bringing `advPoolBase`/`advPoolTestPath`/`pluginFor` along (or exporting them from advpool) so the adapters compile in their new home. `Signer` takes `Key/Store/Witness` fields instead of `Options`.

- [ ] **Step 3: Rewire `internal/brain/advpool.go`** (~751-755) to construct `advpool.JailScorer{Jail: jail}`, `advpool.JailValidator{Jail: jail}`, `advpool.Signer{Key: opts.CertifyKey, Store: opts.BuildStore, Witness: opts.Witness}`. Delete the old local types.

- [ ] **Step 4: Run** `go build ./...`; `go test ./internal/advpool/ ./internal/brain/`. Both green (behavior-identical move). `gofmt -l` clean.

- [ ] **Step 5: Add `internal/advpool/gate_test.go`** — a small test that `Signer.SignVerdict` over a temp `buildstore` + generated key produces a record whose id > 0 and that `certify`/`buildstore` can read back. (Reuse the pattern in `advpool_integration_test.go`.)

- [ ] **Step 6: Commit** — `git add internal/advpool/gate.go internal/advpool/gate_test.go internal/brain/advpool.go && git commit -m "refactor(advpool): relocate jail Scorer/Validator/Signer out of brain (decouple Signer from Options)"`

---

### Task 2: Extract the embeddable worker role-runner

Pull the single-shot worker logic out of `cmd/corral-agent` into an importable package both the agent and `--local` share.

**Files:**
- Create: `internal/agentworker/agentworker.go`
- Create: `internal/agentworker/agentworker_test.go`
- Modify: `cmd/corral-agent/main.go` (call the extracted function)

**Interfaces:**
- Produces:
  - `func RunRole(ctx context.Context, model llm.Chatter, role, instruction string) (result string, findings []queue.Finding, err error)` — for `mutant-generator`/`test-writer` (structured roles) it does one `model.Chat`/`Ask` with `instruction` and returns the raw text as `result`; for `test-critic` it runs the short findings loop and returns `findings`. Match the exact behavior in `cmd/corral-agent/main.go` (`isStructuredRole`, the structured fast path ~694-705, `runCriticLoop`/`critFreeformSteps`).
  - `llm.Chatter` is the minimal interface the runner needs (`Chat(ctx, messages)` or `Ask(ctx, system, user)`) — satisfied by `*llm.Client`. If no such interface exists, define a 1-method one in `agentworker` and have `*llm.Client` satisfy it.

- [ ] **Step 1: Read `cmd/corral-agent/main.go`** — the `runQueueLoop`, `isStructuredRole`, the structured-role branch, and `runCriticLoop`. Note exactly how instruction → LLM → result/findings flows for each of the 3 roles.

- [ ] **Step 2: Write `agentworker_test.go`** first: a fake `llm.Chatter` returning canned mutant text for `mutant-generator` (assert `result` is that raw text), canned test source for `test-writer`, and canned findings text for `test-critic` (assert parsed `findings`).

- [ ] **Step 3: Run** to verify it fails (package/func absent).

- [ ] **Step 4: Implement `agentworker.RunRole`** by lifting the per-role logic verbatim from `corral-agent`. Keep the parsing where it belongs (the brain/validator re-parses structured output, so `RunRole` returns the raw text for structured roles — do NOT parse mutants/tests here; just return the model's raw output).

- [ ] **Step 5: Rewire `cmd/corral-agent/main.go`** to call `agentworker.RunRole` in its claim loop (behavior unchanged). Run `go build ./...`; `go test ./cmd/corral-agent/ ./internal/agentworker/`.

- [ ] **Step 6: Commit** — `git add internal/agentworker/ cmd/corral-agent/main.go && git commit -m "refactor: extract single-shot worker role-runner into internal/agentworker"`

---

### Task 3: The `certify_local.go` orchestrator + in-process e2e

Wire everything into the one command.

**Files:**
- Create: `cmd/corral/certify_local.go`
- Modify: `cmd/corral/certify.go` (route `--local` before the `--adversarial`/remote path)
- Test: `cmd/corral/certify_local_test.go`

**Interfaces:**
- Consumes: `advpool.{NewDriver,BuildDAG,RunSpec,CheckDecorrelation,JailScorer,JailValidator,Signer,RoleMutantGenerator,RoleTestWriter,RoleTestCritic}`, `agentworker.RunRole`, `queue.Store`, `buildstore.{Open,LoadOrCreateSigningKey}`, `sandbox.Resolve`, `adequacy.NewJail`, `internal/lang`, `internal/llm`, the existing `renderAdvVerdict` + `splitCertifyArgs`.

- [ ] **Step 1: Read the driver-drive loop.** In `internal/brain/advpool.go` read `AdvPoolRuntime.StartRun` (build DAG, enqueue, run tick loop) and the `Tick`/`RunStatus` usage; and `internal/advpool/advpool_integration_test.go` (or brain's integration test) for the serverless drive pattern. The local loop is: build the run spec + DAG, enqueue on the queue, then `for { d.Tick(ctx,mid); claim each ready task via q.ClaimNext(...); res,find,_ := agentworker.RunRole(...); q.Complete(id,bee,res) / q.AddFinding(...); } until RunStatus(mid).Converged`. Confirm the exact queue claim/complete/finding API (`ClaimNext`, `Complete`, and the finding-add call the critic result needs).

- [ ] **Step 2: Write the e2e test first** — `certify_local_test.go`: a `runCertifyLocal` invoked with a FAKE `llm.Chatter` (canned mutants that a canned test kills, plus a killing test) through the REAL `advpool.JailScorer`/`JailValidator`/`Signer` over a temp `buildstore` + generated key + a resolved jail. Assert: converges, returns a signed record id > 0, verdict verifies with `certify.VerifyDSSE`. **Skip cleanly** (`t.Skipf`) when `sandbox.Resolve` yields no working jail (bwrap blocked). Model the fakes on `advpool_integration_test.go`'s `canonScorer`/`canonValidator` but keep the REAL jail-backed adapters (only the LLM is faked).

- [ ] **Step 3: Implement `cmd/corral/certify_local.go`** — `runCertifyLocal(args, ...) int`:
  - Parse flags: `--code`, `--goal`, `--test`, `--lang`, `--n-mutants`, `--writer-model`/`--critic-model`/`--mutant-model`, `--jail`, and `-- <testcmd>` (reuse `splitCertifyArgs`). Require `--code`, `--goal`, and `$ANTHROPIC_API_KEY` (or `--writer-model` provider key).
  - Resolve language via `lang.Detect(code)`; run `plugin.Preflight()` (fail closed).
  - Resolve jail: `sandbox.Resolve(sandbox.Config{Backend: jailFlagOrAuto})`; on error print the actionable message (Task 4 supplies the helper); `adequacy.NewJail(backend, timeout)`.
  - Open `queue.Store` (temp), `buildstore.Open` (temp or `~/.claude/corralai_local.duckdb`), `buildstore.LoadOrCreateSigningKey`.
  - Build `advpool.JailScorer{Jail}`, `JailValidator{Jail}`, `Signer{Key,Store,Witness:nil}`; `d,_ := advpool.NewDriver(q, scorer, validator, assign, 0.8)`; `d.Signer = signer`.
  - Resolve models → `assign advpool.RoleAssignment{mutant-generator, test-writer, test-critic}`; `CheckDecorrelation(assign)` (fail closed with a clear message if it can't be satisfied).
  - Build two `llm.Chatter`s (writer/mutant model + critic model) off env keys.
  - Build the `advpool.RunSpec` from the code/test/goal/testcmd/lang; enqueue via `BuildDAG`; drive the loop (Step 1) to convergence, routing each task's role to `agentworker.RunRole` with the matching model.
  - Render with `renderAdvVerdict`; return the status exit code (0/3/2/1).
- [ ] **Step 4: Route `--local`** in `cmd/corral/certify.go` `runCertify` before the remote adversarial path.
- [ ] **Step 5: Run** `go build ./...`; `go test ./cmd/corral/ ./internal/advpool/ ./internal/brain/`; the e2e test PASSES where a jail exists (or SKIPs). `gofmt -l cmd/corral` clean.
- [ ] **Step 6: Commit** — `git add cmd/corral/certify_local.go cmd/corral/certify.go cmd/corral/certify_local_test.go && git commit -m "feat(cli): corral certify --local — one-command in-process adversarial audit"`

---

### Task 4: Jail auto-detect, actionable errors, `--jail` flag

**Files:**
- Create/Modify: a small helper `cmd/corral/jail.go` (resolve + friendly error)
- Test: `cmd/corral/jail_test.go`

- [ ] **Step 1:** Write `resolveLocalJail(flag string) (sandbox.Isolator, error)`: try `sandbox.Resolve(Config{Backend: flag-or-""})`; on a bwrap userns failure, return an error whose message names the fix — the surgical Ubuntu `/etc/apparmor.d/bwrap` profile (unprivileged-userns) — and suggests `--jail container` (docker/podman via `CORRALAI_EXEC_IMAGE`). Never return a `none`/unsafe backend implicitly.
- [ ] **Step 2:** Unit-test the error text (contains "apparmor"/"--jail container") for a simulated failure, and that a `none` backend is never returned silently.
- [ ] **Step 3:** Use it in `certify_local.go`.
- [ ] **Step 4:** `go build ./...`; `go test ./cmd/corral/`; gofmt clean. Commit — `git commit -am "feat(cli): actionable jail resolution for --local (apparmor fix + --jail container)"`

---

### Task 5: Docs — make `--local` the front door

**Files:**
- Modify: `README.md` (Quickstart), `site/src/content/docs/docs/getting-started.mdx`
- Create: `site/src/content/docs/docs/first-audit.mdx` (or a section)

- [ ] **Step 1: README Quickstart** — replace the stale build-mission steps and the "no CLI trigger" disclaimer with the one-command flow: `go install .../cmd/corral@latest`, `export ANTHROPIC_API_KEY=...`, `corral certify --local --code <file> --goal "<what it must guarantee>" -- <test cmd>`, then the verdict + `corral certify verify <record>`. Note the jail requirement (bwrap/Linux, `--jail container` fallback, the Ubuntu apparmor one-liner).
- [ ] **Step 2: `getting-started.mdx`** — delete the removed `mission create` / `make demo-mission` references; make `--local` the first thing. Keep it honest (5 languages; jail needed; your own key).
- [ ] **Step 3: A short "your first audit"** page: one key, one command, walk through a real verdict (use the password-validator example), show the handed-back test and the signed record.
- [ ] **Step 4:** `cd site && npm run build` clean; `bash scripts/gen-cli-docs.sh --check` if `--local` added CLI help (regenerate if it drifts). Commit — `git commit -m "docs: make 'corral certify --local' the front door; remove retired build-mission steps"`

---

## Rollout (post-merge)

1. Merge; behavior of the brain/agents unchanged.
2. The site proof page + LinkedIn article use `corral certify --local …` as the CTA.
3. Manual: a real `corral certify --local` on a small target with a live key = the launch demo.
4. Follow-on: `--jail container` polish; a `--local` terminal recording/GIF.
