<!-- SPDX-License-Identifier: Elastic-2.0 -->

# Audit batch-2 Lows — triage dispositions (2026-07-13)

Task 5.1 of the batch-2 audit-fix run. Each candidate from the 2026-07-12 audit's
Low list was investigated against current `main` (branch `feat/audit-fix-batch-2`),
confirmed in code, and — where real — fixed with a TDD test. Every candidate's
disposition is recorded below; nothing was silently dropped.

Result: **all 5 candidates CONFIRMED real and FIXED** (5 separate commits, each
with a failing-first test). No candidate turned out benign or already-fixed.

| # | Candidate | Disposition | Commit |
|---|-----------|-------------|--------|
| 1 | `mint_observer` gates `isAdmin` not `isHumanAdmin` | FIXED | `f556fe7` |
| 2 | `send_instruction` cross-principal ungated write | FIXED | `781ad3d` |
| 3 | `observer` token can drive narrator + NL→SQL | FIXED | `2458904` |
| 4 | forge query-param unescaped (`repo/provider.go`) | FIXED | `38a2bc9` |
| 5 | telemetry read-only guard is prefix-only (`read_csv` reachable) | FIXED | `e0f1949` |

---

## 1. `mint_observer` gates `isAdmin` not `isHumanAdmin` — FIXED (`f556fe7`)

**Confirmed:** `internal/brain/admin.go:159` gated `mint_observer` on `opts.isAdmin(req)`
while every other principal-table write (`create_superuser`, `add_member`,
`set_superuser`, `remove_principal`) gates on `isHumanAdmin`. `isAdmin` passes for a
delegation/worker token rolled up to a superuser (`identity.go:332`), so a delegated
subagent could mint standing read-only observer tokens for any principal — a
privileged write the herd must not self-authorize.

**Fix:** swapped the gate to `opts.isHumanAdmin(req)`; updated the `registerAdmin`
doc comment to list `mint_observer` in the human-gated set.

**Test (`internal/brain/admin_test.go` `TestMintObserverRequiresHumanAdmin`):** a real
minted delegation token rolled up to a superuser is refused `mint_observer`, and the
wired `MintObserver` callback never runs. RED before fix (tool succeeded), GREEN after.

## 2. `send_instruction` cross-principal ungated write — FIXED (`781ad3d`)

**Confirmed:** `internal/brain/inbox.go` registered `send_instruction` with **no**
ownership gate — it queued an instruction for any `target` agent name. `check_instructions`/
`ack_instruction` both guard with `ownsInbox`, but the send side did not, so one
authenticated principal could queue commands into another principal's agent inbox and
drive its work loop.

**Fix:** added `canInstruct(req, target, opts)` mirroring the existing registration/claim
namespacing invariant (`identity`/`inNamespace`): an authenticated non-admin may only
instruct agents in its own namespace (itself or its `p/…` subagents); a superuser may
instruct anyone; unauthenticated dev is unchanged (non-loosening). This is the
codebase's established ownership pattern, not a new interlock.

**Test (`internal/brain/inbox_test.go` `TestSendInstructionNamespaceGuard`):** alice is
refused `send_instruction` to `bob@x.com/worker-1` and the instruction is not queued;
alice's own-namespace send and a superuser's cross-namespace send both succeed. RED before
(alice's cross-principal send accepted), GREEN after.

## 3. `observer` token can drive narrator + NL→SQL — FIXED (`2458904`)

**Confirmed:** the observer/read-only scope is enforced at the MCP boundary
(`cmd/corral/main.go:1314` wraps `/mcp` in `denyReadOnly`, so the `ask_fleet` MCP tool is
refused observers) and at most UI action handlers (`internal/ui/ui.go` checks `auth.ReadOnly`).
But the UI handlers `internal/ui/ask.go` (`/api/ask`, narrator) and
`internal/ui/askfleet.go` (`/api/ask_fleet`, NL→SQL) had **no** `auth.ReadOnly` gate, and the
UI mux is wrapped only in `verifier.Wrap(authz(...))` (`main.go:1343`) — not `denyReadOnly`.
A read-only observer holding the raw token could POST directly to the brain and invoke the
model (cost + a model call the MCP path already forbids). This is a real scope-exceedance:
the same capability (`ask_fleet`) is denied to observers over MCP.

**Fix:** added an `auth.ReadOnly(r)` → 403 gate at the top of both POST handlers, before the
availability/nil check, so the observer never reaches the model. The GET availability probe
on `/api/ask_fleet` stays open (read-only info the observer's UI needs to show/hide the panel).

**Test (`internal/ui/observer_scope_test.go` `TestObserverCannotDriveNarratorOrOracle`):** an
observer POST to `/api/ask` and `/api/ask_fleet` returns 403 even with no narrator/oracle wired
(proving the gate precedes the model); the GET probe returns 200. RED before (503 — reached the
availability check), GREEN after.

## 4. forge query-param unescaped — FIXED (`38a2bc9`)

**Confirmed:** `internal/repo/provider.go:223` (`rcFindOpenPR`) built the open-PR lookup URL by
raw string concatenation: `.../pulls?head=` + owner + `:` + head + `&state=open`. A branch/ref
(`head`) containing query metacharacters (`&`, space, `#`) breaks out of the query string —
dropping part of the ref, injecting a spurious param, or failing the request.

**Fix:** build the query with `url.Values{"head": {owner+":"+head}, "state": {"open"}}.Encode()`
so every param is escaped and round-trips intact.

**Test (`internal/repo/provider_query_escape_test.go` `TestFindOpenPRQueryEscaping`):** a `head`
of `feature/a & b` reaches the forge decoded exactly as `o:feature/a & b`, with `state=open`
intact. RED before (400 Bad Request — the raw `&`/space mangled the query), GREEN after.

## 5. telemetry read-only guard is prefix-only — FIXED (`e0f1949`)

**Confirmed:** `internal/telemetry/store.go:343` (`Store.Query`, the ad-hoc SQL path behind the
`mission_analytics` tool) guarded with a `HasPrefix("select"/"with")` + no-`;` check only. A
query beginning with `SELECT` that reaches the filesystem/network still passed — verified in the
RED test: `SELECT * FROM read_csv('/etc/passwd')` executed with no error. Sibling ad-hoc-SQL
stores (`internal/recordings/store.go`, `internal/oracle/sandbox.go`) already ban these constructs.

**Fix:** replaced the prefix check with `validateReadOnly`: strip comments, collapse whitespace,
require a single SELECT/WITH statement, and reject the banned constructs (`read_`, `getenv`,
`glob(`, `attach`, `copy`, `parquet_scan`, DML verbs, …) — the same defense-in-depth pattern the
two sibling stores use.

**Test (`internal/telemetry/store_test.go` `TestQueryRejectsFileAndExtensionFunctions`):**
`read_csv`, `getenv`, `glob`, `read_parquet`, a commented-out `read_text`, and a non-SELECT
`ATTACH` are all rejected at the guard; a plain aggregate `SELECT count(*)` still passes. RED
before (`read_csv` accepted), GREEN after.

---

## Follow-up (not blocking, logged for an owner)

**DRY — three near-duplicate ad-hoc-SQL validators.** `internal/oracle/sandbox.go`
(`validateSelect`), `internal/recordings/store.go` (`validateSelect`), and now
`internal/telemetry/store.go` (`validateReadOnly`) carry near-identical
normalize-then-ban logic with slightly divergent banned lists (oracle bans bare
`set `; recordings/telemetry use padded ` set ` and add DML verbs). A shared
`internal/sqlguard` helper would consolidate them, but reconciling the intentional
per-store divergences is a cross-package refactor beyond this Low-triage task's scope
and risk budget. Flagged here rather than done under a "Low" fix. (Ref: the standing
DRY directive.)
