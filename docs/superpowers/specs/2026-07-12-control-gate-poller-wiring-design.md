<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Control-gate poller wiring — design (v1)

**Status:** approved 2026-07-12. Feeds an implementation plan.

## Goal

Wire the already-built, unit-tested control-gate stack (`internal/controlgate`
+ `internal/controlspec` + `adequacy.NewJail`) into a live per-PR poller that
posts a distinct, signed **`corral/control-gate`** required check. Per open PR,
the gate checks out the head, runs the control owner's **already-vetted** tests
against the head's file content, and posts a fail-closed pass/fail verdict —
deterministically, with no LLM in the path.

## Background — the seam

The merge-gate poller (`internal/gate`) is fully wired and running in
production: per open PR it checks out the head, runs one command in the jail,
certifies, and posts `corral/gate` (see `internal/gate/poller.go`,
`internal/gate/runner.go`, `internal/brain/gate.go:StartGate`).

The control-gate stack is built and unit-tested but has **zero call sites**
outside its own packages — it is never constructed in `cmd/corral` or
`internal/brain`. Signatures we consume (unchanged by this work):

```go
// internal/controlgate/run.go:41
func RunControlGate(ctx context.Context, jail adequacy.Jail, base map[string]string,
    checks []ControlCheck, testCmd []string) (ControlResult, error)
// internal/controlgate/post.go:40
func PostControlGate(ctx context.Context, cert Certifier, poster StatusPoster,
    req PostRequest, res ControlResult) error
// internal/controlspec/store.go
func (s *Store) ListVetted(owner string) ([]GateTest, error)
// internal/adequacy/jail.go:38
func NewJail(backend sandbox.Isolator, timeout time.Duration) Jail
// internal/repo/repo.go:299
func (e *Engine) CheckoutPR(ctx, repoURL string, pr int, sha, destDir string) error
// internal/repo/read.go:37
func (e *Engine) ReadFile(dir, path string) (string, error)
```

`ControlCheck` = `{Test controlspec.GateTest; HeadCode string; CodePath string;
TestPath string}`. `RunControlGate` builds, per check, a jail workspace =
`base ∪ {CodePath: HeadCode, TestPath: Test.Test}`, runs `testCmd`, and
aggregates fail-closed (all must pass; empty checks → vacuous pass — the caller
must treat an uncovered goal as a fail). `PostControlGate` certifies **before**
posting (no unsigned green) and maps `res.Pass` → `success`/`failure`.

## Scope

**In (v1):**
1. Persist the two per-test recipe fields (`CodePath`, `TestPath`) on
   `controlspec.GateTest` so the gate reproduces the exact vetted workspace.
2. A control runner that turns one PR into a `corral/control-gate` verdict.
3. `StartControlGate` + config parsing + `Options` fields + `cmd/corral` wiring.
4. A `corral control seed` CLI verb to place one real vetted test (+ recipe) in
   the store, so the gate demonstrably gates a live PR.

**Out (deferred):**
- The owner **authoring/vetting surface** (goal definition → `StageCandidate`
  draft → owner approval UI). v1 assumes vetted tests already exist (seeded).
- Only-changed-file optimization — v1 runs **all** vetted tests against head.
- Multi-repo `Target` encoding — v1 binds one owner per repo via policy.
- Non-GitHub forges (the merge-gate is GitHub-only today; same limit here).
- Per-test `base`/`testCmd` — v1 treats the scaffold as repo-level (see §"Two
  gaps").

## Two gaps this closes

**Gap 1 — goal→file mapping.** `GateTest.Target` is opaque today. v1 fixes the
convention: **`Target` = a repo-relative POSIX path** (e.g.
`internal/auth/login.go`). The runner reads the target's head content via
`ReadFile(dest, Target)`. **Fail-closed:** if the target does not exist at head
(deleted/renamed/unreadable), that control is a **failure**, never a silent
skip. Note `Target` (the real repo path to read) is distinct from `CodePath`
(the flat filename the vetted test expects inside the minimal jail workspace).

**Gap 2 — workspace reproducibility.** A vetted test was authored in a minimal
scaffold. We reproduce it by splitting the recipe:
- **Repo-level (policy config):** the `base` scaffold map (a language default,
  e.g. Go → `{"go.mod": "module control\ngo 1.26\n"}`) and `testCmd` (e.g.
  `["go","test","./"]`). One scaffold per repo/language in v1.
- **Per-test (persisted on `GateTest`):** `CodePath` + `TestPath` — where the
  head content and the vetted test land in that scaffold.

This split means **`RunControlGate`'s signature is unchanged** (one `base` +
one `testCmd` for the batch; per-check `CodePath`/`TestPath` already supported).
If a vetted test cannot compile/run in the repo `base`, it **fails** — surfaced
loudly, never hidden.

## Components

### 1. `controlspec` — persist the per-test recipe
`internal/controlspec/types.go`: add `CodePath string` and `TestPath string`
to `GateTest`.
`internal/controlspec/store.go`: `gate_tests` `CREATE TABLE` gains
`code_path VARCHAR NOT NULL DEFAULT ''`, `test_path VARCHAR NOT NULL DEFAULT ''`.
`SaveCandidate` persists them; `GetVetted`/`ListVetted`/`ListPending` select and
scan them. (No production data exists yet — the store is not opened anywhere in
`cmd/corral` today — so no migration of live rows is required; new columns on
`CREATE TABLE IF NOT EXISTS` suffice, with `ADD COLUMN IF NOT EXISTS` guards for
any pre-existing dev DB.)

### 2. Control policy + parser
`internal/brain/controlgate.go` (new): a `ControlPolicy` config struct and a
parser for `CORRALAI_CONTROL_GATE`.

```go
type ControlPolicy struct {
    Repo  string // required, e.g. "github.com/o/r"
    Base  string // "" = all bases
    Owner string // required, the control-owner principal (ListVetted key)
    Lang  string // "go" (default) → built-in base scaffold + testCmd
}
// ParseControlPolicies("repo=..,owner=..,lang=go,base=main; repo=..") ([]ControlPolicy, []string)
```

Built-in language scaffolds (v1: `go`; extensible):
```go
func langScaffold(lang string) (base map[string]string, testCmd []string, ok bool)
// "go" → {"go.mod": "module control\ngo 1.26\n"}, {"go","test","./"}
```
Unknown/empty lang → not ok → policy rejected into `bad` and logged (fail loud).

### 3. Control runner
`internal/brain/controlgate.go`: `runControlGate` — one PR → one verdict. It
reuses the injected `repo.Engine` (checkout/read/status) and `certifierAdapter`
(signing), and an `adequacy.Jail`.

```go
type controlDeps struct {
    Repo    *repo.Engine            // Checkouter + PRLister + StatusPoster
    Cert    controlgate.Certifier   // certifierAdapter
    Spec    *controlspec.Store      // vetted tests
    Jail    adequacy.Jail
    Workdir string
    Record  func(repo, sha string) string
    Timeout time.Duration
}
// per-PR: post pending → CheckoutPR head into a temp dir →
//   vetted := Spec.ListVetted(pol.Owner)
//   for each vetted gt: head, err := Repo.ReadFile(dest, gt.Target)
//       err → record a synthetic failing ControlTestResult (fail-closed), skip run of that one
//       else → append ControlCheck{Test: gt, HeadCode: head, CodePath: gt.CodePath, TestPath: gt.TestPath}
//   res, err := controlgate.RunControlGate(ctx, Jail, base, checks, testCmd)   // base,testCmd from langScaffold(pol.Lang)
//   merge in the synthetic missing-target failures so res.Pass reflects them
//   PostControlGate(ctx, Cert, Repo, PostRequest{RepoURL, HeadSHA, Context:"corral/control-gate", RecordURL: Record(...)}, res)
```

Coverage semantic: **all** of `pol.Owner`'s vetted tests run against head every
PR. A vetted list that is empty for a configured control gate is itself a
**failure** posture in v1 (an owner configured a gate but has no vetted
controls — surface it, do not post a vacuous green); the runner treats
zero-vetted as `failure` with description "no vetted controls".

### 4. `StartControlGate` + wiring
`internal/brain/controlgate.go`: `StartControlGate(ctx, opts Options)
(*gate.Store, error)`, mirroring `StartGate`. Off-switch: empty
`ControlPolicies` → `(nil,nil)`; nil `GateBackend` or nil `Repo` → log +
`(nil,nil)`. It opens a **separate** dedup run-store (its own DSN, so
`(repo, head_sha)` dedup does not collide with the merge gate), opens the
`controlspec` store, builds an `adequacy.NewJail(opts.GateBackend, timeout)`,
constructs a `gate.Poller` (reusing its loop + dedup) whose `Run` closure calls
`runControlGate` with the `ControlPolicy` matched by `pol.Repo`, and
`go poller.Loop(ctx)`.

`internal/brain/identity.go` `Options` gains:
`ControlPolicies []ControlPolicy`, `ControlSpecDB string` (vetted-tests DSN),
`ControlGateDB string` (dedup run-store DSN), `ControlPollInterval time.Duration`.
Reuses existing `GateBackend`, `Repo`, `CertifyKey`, `BuildStore`,
`GateRecordURL`.

`cmd/corral/main.go`: parse `CORRALAI_CONTROL_GATE`,
`CORRALAI_CONTROL_GATE_SPEC_DB` (default `~/.claude/corralai_control_spec.duckdb`),
`CORRALAI_CONTROL_GATE_DB` (default `~/.claude/corralai_control_gate.duckdb`),
`CORRALAI_CONTROL_GATE_POLL_SECONDS` (default 120); set the new `Options`
fields; call `brain.StartControlGate(ctx, brainOpts)` next to `StartGate`.
Reuses the shared `execBackend` isolator already resolved for the merge gate.

### 5. `corral control seed` CLI verb
`cmd/corral/*` (a new subcommand file): writes one real vetted `GateTest`
(+ recipe) into the `controlspec` store, so a live PR can be gated.

```
corral control seed \
  --spec-db <path> --owner <principal> --goal <id> \
  --target <repo-relative-path> --code-path <flat.go> --test-path <flat_control_test.go> \
  --test-file <path-to-vetted-test-source> [--kill-rate <float>]
```
It `SaveCandidate` (which forces unvetted) then `Promote(owner, goal, target)`
— going through the same candidate→vetted human-gate path the owner would, so
the seeded row is genuinely vetted, not a back door.

## Data flow (per PR)

```
poller.Tick → ListOpenPRs(repo, base) → GetBySHA dedup (control run-store)
  → runControlGate:
      SetCommitStatus(pending, corral/control-gate)
      CheckoutPR(head) into temp dir
      vetted := ListVetted(owner)     ; empty → failure "no vetted controls"
      for gt in vetted:
          head := ReadFile(dest, gt.Target)   ; err → synthetic FAIL (fail-closed)
          else check := {gt, head, gt.CodePath, gt.TestPath}
      res := RunControlGate(jail, base, checks, testCmd)  ; + synthetic fails
      Certify(...)   ; err → return WITHOUT posting (no unsigned green)
      SetCommitStatus(success|failure, corral/control-gate, record URL, describe)
      run-store.Save(Run{repo, head_sha, passed})
```

## Error handling / fail-closed invariants

- Missing/unreadable target file at head → that control **fails**.
- Zero vetted controls for a configured gate → **failure** ("no vetted controls").
- Any jail error → propagates → **failure**/error status (never silent pass).
- Certify error → **no status posted** (no unsigned green); the next poll retries.
- Unknown `lang` / malformed policy → rejected into `bad`, logged, feature stays
  off for that entry (loud, not silent).
- `success` is posted only when every vetted control passed on real jail exit 0
  **and** the verdict was signed first.

## Testing strategy (TDD)

- `controlspec`: `CodePath`/`TestPath` round-trip through
  `SaveCandidate`→`ListVetted`/`GetVetted` (extend existing store tests).
- `ParseControlPolicies`: valid multi-entry parse; missing `repo`/`owner`;
  unknown `lang` → `bad`.
- `langScaffold`: `go` returns the expected base+testCmd; unknown → `!ok`.
- `runControlGate` (fakes for `PRLister`/`Checkouter`/`Jail`/`Certifier`/
  `StatusPoster`): a passing vetted test → `success` + signed; a failing one →
  `failure`; a missing target → `failure`; zero vetted → `failure`; a certify
  error → no status posted.
- `StartControlGate`: empty policies → `(nil,nil)`; nil backend/repo → `(nil,nil)`.
- `corral control seed`: writes a vetted row readable by `ListVetted`.

## Honesty seam (for the eventual field note)

v1 gates by **execution** of vetted, owner-approved tests against the head
commit — certified by running, not by self-report. It does **not** yet include
the authoring/vetting UI; the vetted tests are seeded by an operator standing in
for the control owner. The field note ships only once a live PR is gated by this
path — same don't-advertise-unbuilt discipline as the ROADMAP.
