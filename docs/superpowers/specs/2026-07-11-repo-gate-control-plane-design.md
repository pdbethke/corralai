<!-- SPDX-License-Identifier: Elastic-2.0 -->
# The Repo Gate — corral as a required, signed merge check (control-plane v1)

**Status:** design — for review before an implementation plan.
**Decision context:** [[corralai-control-plane-positioning]] — corral is the org-owned
control plane, not a passive warehouse. The bank runs the brain; every contribution
flows through a PR; the brain runs the mandatory gate and the forge won't merge without
corral's signed green.

## Goal
One sentence: **make a PR unmergeable until the bank's brain has run its declared check
on the exact head commit, in the jail, and posted a tamper-evident signed `success`.**

This is `corral certify`, *inverted*: instead of a developer voluntarily running
`corral certify` in their own CI, the **brain** runs the gate on every open PR and
publishes the verdict as a forge status that branch protection enforces.

## Architecture (2–3 sentences)
Reuse two things already in the tree: the **forge-provider abstraction**
(`internal/repo.Provider`, GitHub/Gitea/GitLab) and the **certify-by-execution engine**
(checkout a SHA in the bwrap jail, run a command, bind the real exit code, DSSE-sign a
record). Add a new **`internal/gate`** package that polls covered repos' open PRs, runs
the declared check on each new head SHA through the certify engine, and publishes
`corral/gate = pending|success|failure` to that SHA via two new `Provider` methods.
GitHub branch protection (org-side, one-time) requires the `corral/gate` context, so a
red or missing verdict blocks merge. **v1 targets GitHub only and Model A (required
status check), per the design decision.**

## Tech / reuse
- `internal/repo.Provider` (extended) — the forge API surface; per-forge token isolation
  and `PushCredURL` credential boundary preserved.
- `internal/certify` + the jail exec path the verify gate already uses — the gate's
  execution and signing core. **No new execution or signing path is introduced.**
- Go 1.26.5; TDD; the existing `provider_github_test.go` fake-server harness for the new
  provider methods.

---

## Non-goals (explicitly out of this spec — each is its own later spec)
- **Posture / coverage verifier** (read branch-protection config, assert required-check +
  no-force-push + no-direct-push + no-bypass, alarm on drift). *This is "half the product"
  and the very next spec (v1.1) — but it is not v1.* v1 assumes the org has configured
  branch protection correctly; v1.1 proves it.
- **Ledger reconciliation** (a protected-branch commit with no matching signed record =
  flagged violation).
- **Webhooks.** v1 polls (the codebase already ingests PR events by polling `ListReviews`;
  there is no webhook receiver). Webhooks are a v2 *latency* optimization.
- **Gitea / GitLab** enforcement. The `Provider` shape is forge-agnostic; other forges
  follow the same two methods in later work.
- **Multi-gate role sets** (SAST / SCA / secret-scan / policy-as-code as distinct
  certified gates). v1 runs one declared check command per repo; role-gates grow later.
- **Model B** (corral as sole merger / write monopoly). v1 is Model A only.
- **GitHub App check-runs.** v1 posts a **commit status** with the brain's existing token.
  This satisfies a required check but can be posted by any holder of a status-write token,
  so v1's un-forgeability rests on *token hygiene*. The **App-based check-run** (which a
  developer PAT structurally cannot forge) is a named v1.1 hardening, not v1.

---

## Components & interfaces

### 1. `internal/repo` — two new `Provider` methods (GitHub impl in v1)
```go
// PRRef identifies an open change request and its current head.
type PRRef struct {
    Number  int
    HeadSHA string
    HeadRef string // source branch
    Base    string // target branch
}

// ListOpenPRs returns open PRs targeting `base` (all bases if base == "").
ListOpenPRs(ctx context.Context, owner, repo, base string) ([]PRRef, error)

// SetCommitStatus posts a commit status to `sha`. state ∈ {"pending","success","failure","error"}.
// context is the required-check name (e.g. "corral/gate"); targetURL links to the signed record.
SetCommitStatus(ctx context.Context, owner, repo, sha, context, state, targetURL, description string) error
```
Gitea/GitLab implementations return `errors.ErrUnsupported` in v1 (compile-complete, so
the interface is honest across forges) with a `// TODO(gate v1.x)` — never a silent no-op.

### 2. `internal/gate` — new package (single responsibility: the gate loop)
```go
// Policy is the per-repo gate contract the org declares.
type Policy struct {
    Repo         string   // "owner/name"
    Base         []string // gated target branches, e.g. ["main"]
    Context      string   // required-check name; default "corral/gate"
    CheckCmd     []string // the command run under certify-by-execution
    AllowNet     bool     // jail network for the check (module downloads etc.)
}

// Run is the durable record of one gate execution, keyed by (Repo, HeadSHA) for dedupe.
type Run struct {
    Repo, HeadSHA string
    PR            int
    Passed        bool
    RecordID      string    // the DSSE-signed certify record bound to HeadSHA
    RanAt         time.Time
}
```
- **Poller:** for each `Policy`, `ListOpenPRs` → for each PR whose `(Repo, HeadSHA)` has no
  `Run`, enqueue a gate execution. Idempotent by head SHA (a new push = new SHA = re-gate;
  an unchanged SHA is never re-run).
- **Runner:** post `pending` → checkout `HeadSHA` in the jail (net per `AllowNet`) → run
  `CheckCmd` via the certify engine → build + DSSE-sign the record bound to
  `(Repo, PR, HeadSHA, CheckCmd, exitCode)` → store the `Run` → post `success` iff the
  check truly ran and exited 0, else `failure`. **Fail-closed: any internal error
  (checkout, jail, timeout) posts `failure`/`error`, never `success`.**
- **Store:** a sibling `gate_runs` table in the brain's DuckDB (alongside
  `internal/buildstore`'s records), keyed by `(Repo, HeadSHA)`; it is both the dedupe
  index and the read model for the CISO view and the status target URL.

### 3. Brain wiring & config
- Gate policies loaded at brain startup from config (a `gate:` block / drop-in listing
  `{repo, base, checkCmd, allowNet}`). If no policies are configured, the poller does not
  start (feature is opt-in; zero behavior change for existing brains). *CLI management of
  policies (`corral gate ...`) is a v1.1 nicety; v1 is config-driven per the "daemon
  configured by CLI/config only" directive.*
- A read endpoint `GET /api/gate/run?repo=&sha=` returns the `Run` + a link/handle to the
  signed record, so the `SetCommitStatus` targetURL resolves to verifiable proof and the
  CISO client has something to render. Auth-gated like every other `/api` route.

### 4. The org's one-time forge setup (precondition, NOT corral code)
Documented, not enforced by v1 (v1.1 verifies it): on each covered branch, branch
protection **requires the `corral/gate` status**, **blocks force-push**, **requires PR
(no direct push)**, and **includes administrators (no bypass)**. v1's honesty line in the
docs: *"corral produces the verdict; your branch protection enforces it. Until the
posture verifier (next) lands, corral cannot yet prove the protection is in place."*

## Data flow (v1)
```
poll open PRs (covered repos)
  └─ new head SHA?
       ├─ SetCommitStatus(sha, "corral/gate", "pending")
       ├─ jail: checkout sha → run CheckCmd → exit code   (certify-by-execution)
       ├─ build + DSSE-sign record bound to sha → store Run
       └─ SetCommitStatus(sha, "corral/gate", passed ? "success" : "failure",
                          targetURL=/api/gate/run?repo&sha)
  → GitHub branch protection blocks merge until "corral/gate" == success
```

## Security invariants (load-bearing — the reviewer's lens)
1. **No self-report.** `success` is posted only from a real passing exit code of an actual
   execution in the jail — never a claim. (This is the novel property; do not weaken it.)
2. **Fail-closed.** Any internal error → `failure`/`error`, never `success`.
3. **Bound to the exact SHA.** The signed record binds the head SHA that will merge; a new
   push produces a new SHA and re-gates (GitHub invalidates the prior status on push;
   "require up-to-date" closes stale-base merges — an org-setup precondition).
4. **Key stays in the brain.** Signing key never leaves; verify path uses the published
   key, never the record's own (reuse `corral certify verify` semantics).
5. **Credential boundary preserved.** The forge token stays brain-side, per-forge isolated;
   `SetCommitStatus` uses the same token plumbing as `OpenPR`. No token to any worker.
6. **Loud failures.** Forge API errors and gate-run errors are logged with context and
   retried with backoff; the poll loop is idempotent by SHA so a transient failure self-heals.

## Testing strategy (TDD)
- `internal/repo`: `SetCommitStatus` / `ListOpenPRs` against the existing GitHub
  fake-server harness — assert the exact API path, `state`, and `context`; assert the
  Gitea/GitLab stubs return `ErrUnsupported` (honest, not silent).
- `internal/gate` poller: a new head SHA enqueues exactly one run; the same SHA never
  re-runs; a second push (new SHA) re-gates.
- `internal/gate` runner: a passing `CheckCmd` → `success` + a signed record whose bound
  SHA and exit code are correct; a failing command → `failure`; a forced internal error
  (bad checkout) → `failure`/`error` and **never** `success` (the fail-closed test).
- End-to-end (fake GitHub + a temp repo + a trivial check): open-PR poll → gate → status
  posted → record verifies with the published key.

## The incremental arc (context, not this plan)
- **v1 (this spec):** GitHub, Model A, poll, one declared check → a PR blocked until
  corral's signed green. Dogfood on corralai's own repo.
- **v1.1:** the **posture/coverage verifier** + drift alarms (stops the *dishonest* path);
  the **GitHub App check-run** (un-forgeable green).
- **v1.2+:** ledger reconciliation; webhooks (latency); Gitea/GitLab; multi-gate role sets
  (SAST/SCA/secret/policy); optional Model B for max-control shops.
