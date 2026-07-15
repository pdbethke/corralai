<!-- SPDX-License-Identifier: Elastic-2.0 -->
# `corral certify --adversarial` — the CLI trigger for the adversarial pool (design)

**Status:** design (2026-07-15). Precedes an implementation plan. This is **slice 3's CLI trigger** from the adversarial-pool spec (`docs/superpowers/specs/2026-07-14-adversarial-pool-design.md`), which scoped it as an explicit non-goal of slice 1: *"`corral certify --adversarial` / gate-poller trigger — slice 3. Slice 1 triggers via the admin MCP tool."* Slice 1 shipped and deployed (the full pool: driver, three decorrelated roles, dynamic gate-earned routing, signed verdict, leaderboard sink, `start_adversarial_run`). This slice makes the pool **triggerable and observable from one command** — the demo surface for the launch narrative.

## Why now — the north star

Every other pool artifact exists but is only reachable through an admin MCP tool that returns a bare `run_id` and drops the verdict on the floor. That is not a story anyone can watch land. The LinkedIn article needs a single command that a human runs, waits on, and gets a legible signed verdict back from — *"this change's tests catch K% of injected bugs; here are the ones they miss; signed record N."* This slice builds exactly that command and the one brain-side query tool it requires.

## The gap (EXISTS-vs-DELTA, verified 2026-07-15)

**Exists:** `StartAdversarialPool`/`AdvPoolRuntime`/`advpool.Driver` (the async engine), the admin-gated `start_adversarial_run(AdvPoolRunSpec) → AdvPoolRunOut{RunID}` MCP tool, the signed verdict via the certify chain, the leaderboard sink. In the CLI: `runCertify` with the `--brain` MCP path, `mcpPoster` (`brainclient.Dial` + `CallTool` + `FirstText`), `brainToken()` (`CORRALAI_BRAIN_TOKEN`), `splitCertifyArgs` (the bare-`--` split), the `verify`/`pubkey` sub-subcommands, and the git-context helpers (`GitOutput`, repo/commit/branch resolution).

**Delta — two things:**

1. **The pool is asynchronous and no verdict is exposed.** `start_adversarial_run` returns a `run_id` immediately; the run converges later in `AdvPoolRuntime.tick`, which *logs* the verdict and clears the active slot **without retaining it at the runtime layer**. The pure `advpool.Driver` *does* retain `runState.verdict` in `d.runs[missionID]` (the run is never deleted from the map), but there is **no getter on the Driver and no MCP query tool**. A trigger with no way to read the result is a dead demo.
2. **No CLI surface** exists to trigger a run, gather the target (code + dev test + goal + test command), or render a verdict.

## Design

Three pieces, smallest-first.

### Piece 0 — Carry the signed record id on the Verdict

Verified against `internal/advpool/driver.go`: `Verdict` is `{Repo, Commit, DevKillRate, MutantsTotal, Survivors, ProvenMissed, VacuousFindings []queue.Finding, ModelsByRole map[string]string, Status}` — it does **not** carry the signed record id, and `tickAggregate` currently **discards** `SignVerdict`'s `(recordID, head)` return (`if _, _, serr := d.Signer.SignVerdict(ctx, v); serr != nil`). The whole point of the render is "here is your signed proof, record N, verify it offline" — so add two fields and populate them:

```go
type Verdict struct {
    // ...existing fields...
    RecordID   int64  // the signed build-record id (0 if signing was skipped/failed)
    RecordHead string // the record's ledger head
}
```

In `tickAggregate`, capture the return and set `v.RecordID`/`v.RecordHead` from it **before** storing `run.verdict = &v` (order matters — the stored verdict is what `RunStatus` returns). A signing failure still yields a Verdict (RecordID stays 0); the CLI then renders "signed: (signing failed)" rather than a fake id. This is additive — the signed record's own contents are unchanged; we only surface its id on the queryable verdict.

### Piece 1 — Driver getter: `advpool.Driver.RunStatus`

```go
// RunState is the observable status of one run.
type RunState struct {
    Converged bool     // true once the run has a terminal Verdict
    Verdict   *Verdict // non-nil iff Converged
}

// RunStatus reports whether missionID's run has converged, and its Verdict if
// so. found is false when the driver has no such run (unknown/expired id).
func (d *Driver) RunStatus(missionID int64) (st RunState, found bool)
```

Reads the already-retained `d.runs[missionID].verdict`. **Correction (verified 2026-07-15): the `Driver` currently has NO mutex** — `runs`/`noProgress`/`lastFingerprint` are bare maps, and the existing `StartRun`↔`Tick` path is race-free only by accident of the runtime's `rt.mu` ordering (StartRun fully publishes a run before any Tick sees a nonzero `activeID`). `RunStatus`, called from the `get_adversarial_run` MCP handler goroutine, would race `tickAggregate`'s `run.verdict = &v` write. So Piece 1 **adds a focused `sync.Mutex` to `Driver`** guarding exactly the `runs` map lookups and the `run.verdict` read/write — NOT the slow jail work (the lock is never held across `Scorer.Score`). `noProgress`/`lastFingerprint` stay lock-free (touched only by the single tick goroutine). `found=false` for an id the driver never saw; `Converged=false` for a run still ticking. A `-race` test drives `RunStatus` concurrently with `Tick` to lock this in.

### Piece 2 — Runtime + MCP tool: `get_adversarial_run`

`AdvPoolRuntime.RunStatus(runID) (advpool.RunState, bool)` delegates to `rt.driver.RunStatus` (no runtime-level lock needed — the driver owns the state; `rt.mu` guards only `activeID`/`tickErrors`). Note the runtime clears `activeID` on convergence but **the driver keeps the run**, so a converged verdict is queryable *after* the active slot frees — which is exactly when the CLI polls for it.

New admin-gated tool mirroring `registerAdvPoolTools`' `start_adversarial_run`:

```go
type AdvPoolQuery struct {
    RunID int64 `json:"run_id" jsonschema:"the run id returned by start_adversarial_run"`
}
type AdvPoolStatusOut struct {
    RunID     int64           `json:"run_id"`
    Found     bool            `json:"found"`
    Converged bool            `json:"converged"`
    Verdict   *advpool.Verdict `json:"verdict,omitempty"` // present iff converged
}
// get_adversarial_run — ADMIN: query a run's status/verdict by id.
```

Registered in `registerAdvPoolTools` alongside `start_adversarial_run`, same `isHumanAdmin` gate, same `opts.AdvPool` runtime. `advpool.Verdict` marshals as-is — with Piece 0's additions it carries everything the render needs: `Status`, `DevKillRate`, `MutantsTotal`, `Survivors`, `ProvenMissed`, `VacuousFindings`, `ModelsByRole`, and `RecordID`/`RecordHead`. Durable proof remains the signed record + `corral certify verify`; this tool is for **live observation**, in-memory by design. (The Verdict's JSON tags today are Go defaults — `DevKillRate` etc.; the plan pins the wire contract by giving the CLI's `advVerdict` matching `json:` tags, or adding explicit tags to `advpool.Verdict`, so the CLI decodes what the tool encodes.)

### Piece 3 — CLI: `corral certify --adversarial`

A new branch in `runCertify`, dispatched **before** the standalone/`--brain` split when `--adversarial` is set. Flags:

| flag | required | default |
| --- | --- | --- |
| `--adversarial` | — | selects this mode |
| `--code <path>` | yes | — |
| `--test <path>` | no | the `_test.go` sibling of `--code` (e.g. `internal/fence/fence.go` → `internal/fence/fence_test.go`) |
| `--goal "<text>"` | yes | — |
| `--n-mutants <n>` | no | 5 (brain clamps to ≤20) |
| `--brain <url>` | yes (or `$CORRAL_BRAIN`) | `$CORRAL_BRAIN` |
| `--poll <dur>` | no | 5s |
| `--timeout <dur>` | no | 10m |
| `-- <cmd...>` | yes | the dev test command (`TestCmd`), via `splitCertifyArgs` |

Flow:
1. Parse flags; validate `--code`, `--goal`, and a non-empty `-- <cmd>` are present (else usage, exit 2). Resolve `--test` default from `--code`.
2. Read `--code` and `--test` from the **working tree** (not git — the change under review is what's checked out). A missing/unreadable file is a usage error (exit 2) naming the path.
3. Resolve `repo`/`commit` via the existing helpers (`remote.origin.url`, `rev-parse HEAD`); degrade to empty on failure (the pool tolerates it), same as the shipped path.
4. Build the `start_adversarial_run` args (`repo, commit, goal, code_path, code, dev_test_path, dev_test_code, test_cmd, n_mutants`), `CallTool`, parse `AdvPoolRunOut.RunID`.
5. **Poll** `get_adversarial_run` every `--poll` until `Converged` or `--timeout`. Emit a one-line progress heartbeat each poll (`… still running (elapsed 12s)`) so the wait reads as alive, not hung. A timeout prints the `run_id` and how to re-query, exit 1 (infra/timeout, not a verdict).
6. **Render** the verdict block and exit by status.

New `buildPoster`-style seam so the CLI is unit-testable without a brain: an `advPoolClient` interface with `StartRun(ctx, brainURL, spec) (runID int64, err error)` and `RunStatus(ctx, brainURL, runID) (converged bool, v *advVerdict, err error)`, real impl over `brainclient`, fake injected in tests. Mirrors how `buildPoster`/`mcpPoster` already isolate the MCP call.

### Verdict rendering (the screenshot)

A single legible block, stable enough to screenshot:

```
adversarial verdict — internal/fence/fence.go @ 88b6ff7
  status:        CERTIFIED               (dev suite killed 6/6 mutants)
  dev_kill_rate: 1.00
  survivors:     0
  proven_missed: 0
  vacuous tests: none flagged
  models:        mutant-generator=qwen2.5-coder:7b  test-writer=qwen2.5-coder:7b  test-critic=llama3.2:3b
  signed:        record 41  (verify offline: corral certify verify <record>)
```

- **Headline line** = `status` + a plain-English gloss of the kill-rate.
- Needs-review renders the same block with `status: NEEDS-REVIEW` and, when present, the survivor descriptions and the test-critic's vacuous-test findings verbatim (the "your tests suck" pan).
- Exit codes: **0** certified · **3** needs-review · **2** usage · **1** infra/timeout. Distinct `3` lets CI gate on "not certified" without confusing it with a CLI error — mirrors the shipped certify's "the verdict is the exit code" philosophy, but here the verdict is the adequacy status, not a wrapped check's code.

## Soundness — unchanged, and one honesty note

This slice adds **no trust surface**: the verdict is still computed brain-side in the jail, still human-gated, still signed; `get_adversarial_run` is a read-only query behind the same admin gate; the CLI only *displays* what the brain signed. The rendered `models_by_role` and `signed record N` come straight from the Verdict — the CLI invents nothing.

**Honesty note for the render:** the block must not print "CERTIFIED" for a `needs-review` verdict, and must show `proven_missed`/survivors truthfully even when they're unflattering — the tool's whole credibility is that it reports the pan when the pan is deserved (the "Your Tests Suck!" and "Nobody Fails a Test They Never Took" field notes both turn on this). No rounding a 0.79 up to "certified."

## Operator note (not a code change)

`start_adversarial_run`/`get_adversarial_run` register only when `Options.AdvPool` is non-nil, which needs `StartAdversarialPool` to succeed (GateBackend + Missions + Queue + CertifyKey + BuildStore) **and** the pool enabled (off by default). The prod brain (mission engine disabled since retire-the-builder) does not currently enable the pool, so the launch demo runs against a **locally-run brain with the pool enabled** — the same posture as the "Your Tests Suck!" runs. Enabling it on a public brain is a separate operator decision, out of scope here.

## Non-goals (this slice — explicit)
- **Diff-driven target inference** (pick the changed file automatically) — rejected for slice 3; explicit `--code`/`--goal` is unambiguous and demo-legible. A later slice can add `--diff`.
- **Multi-target runs** (a whole diff → many targets) — one target per command, matching the pool's one-active-run scope.
- **Concurrent runs / gate-poller auto-trigger** — still later slices; the CLI triggers one run and waits.
- **Persisting live run status across a brain restart** — the signed record is the durable artifact; live polling is in-memory. A restart mid-run means re-running; acceptable for a minutes-long run.
- **Enabling the pool on the prod brain** — operator decision, separate change.
- No new metaphor/rename; no change to the merge gate, the shipped `corral certify` standalone/`--brain` paths, or the pool's driver logic beyond the additive `RunStatus` getter.

## Testing posture
- **Driver unit:** `RunStatus` returns `found=false` for an unknown id; `Converged=false` mid-run; `Converged=true`+Verdict after `tickAggregate` stored one, with `RecordID`/`RecordHead` populated from the (fake) Signer's return (Piece 0). (Extend `internal/advpool/driver_test.go`.)
- **Brain unit:** `get_adversarial_run` refuses a non-admin (`errAdminOnly`); returns the runtime's status for a known id; `found=false` for an unknown id.
- **CLI unit (fake `advPoolClient`):** flag validation (missing `--code`/`--goal`/`-- cmd` → exit 2); `--test` sibling derivation; a certified verdict → exit 0 + the CERTIFIED block; a needs-review verdict → exit 3 + survivors/vacuous rendered; a poll timeout → exit 1 + the re-query hint; a start error → exit 1. No real brain, no jail.
- Keep the shipped pool / `corral certify` / queue tests green; this slice is additive.
- **CLI-docs drift gate:** `corral certify -h` changes, so `bash scripts/gen-cli-docs.sh` must run and its output be committed (the site deploy enforces `--check`).
