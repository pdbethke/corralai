# Records Dashboard Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a certified build a first-class UI object — a team-rollup records dashboard + a per-record verify/attribution/commit detail view — reusing the cockpit SPA, the read-only console gate, and the replay engine.

**Architecture:** (1) Extract the CLI's verification into a shared `internal/certverify.VerifyRecord` (DRY: CLI + UI run the identical 4 checks). (2) Capture commit info + `git verify-commit` signature at certify time. (3) Add `buildstore.List` + summary. (4) Serve `GET /api/builds` (list) + `GET /api/builds/{id}` (detail, server-re-verified) from `internal/ui`. (5) A new "Records" cockpit view (the default landing) with a rollup table + a verify/attribution/commit detail panel. The UI's verify is honestly the brain's self-view; it surfaces the `corral certify verify` CLI for independent, trustless verification.

**Tech Stack:** Go 1.26; `internal/certify` (VerifyDSSE/VerifyLedger/UnmarshalSteps), `internal/transparency` (Witness.VerifyInclusion); `internal/buildstore` (DuckDB); `internal/ui` (SPA + JSON routes); vanilla JS in `internal/ui/web/index.html`.

## Global Constraints

- SPDX header `// SPDX-License-Identifier: Elastic-2.0` on every new Go file.
- TDD: failing test first, watch it fail, minimal code, watch it pass, commit.
- `go vet ./...` clean; full `go test ./...` green; `go build ./...`; `bash scripts/check-security.sh` green before each commit.
- The signing key never leaves the brain / never logged; only public keys + envelopes + proofs appear.
- **Trust honesty:** the UI's verify is the brain re-running checks on its own records (central-trust, operator convenience). Independent verification is the CLI against the *published* key + Rekor — the detail view MUST surface that command and MUST NOT imply "dashboard green = independently verified."
- **Unsigned commits are recorded, not failed** — never faked green.
- DRY: exactly one implementation of the 4-check verification (`certverify.VerifyRecord`), shared by CLI + UI.
- Corral metaphor in user copy; no bee/hive/swarm (note: existing `Deps` comments say "bee" — do not add new bee terms; don't mass-rename existing ones in this plan).
- Branch: `feat/records-dashboard` (spec committed there).

## File Structure

- `internal/certverify/certverify.go` (+ test) — `VerifyRecord`, `Check`, `Record`.
- `cmd/corral/verify.go` — refactor `runCertifyVerify` to call `certverify.VerifyRecord`.
- `cmd/corral/certify.go` (+ test) — capture commit info + `git verify-commit`.
- `internal/brain/buildcert.go` + `internal/buildstore/store.go` (+ tests) — thread + store commit fields; `List`.
- `internal/ui/ui.go` (+ test) — `Deps.BuildStore`/`CertifyPub`/`Witness`; `builds` + `buildDetail` handlers + routes.
- `cmd/corral/main.go` — wire the new `Deps` fields.
- `internal/ui/web/index.html` — the "Records" view (table + detail panel), default landing.

---

### Task 1: `internal/certverify` — the shared 4-check verifier (DRY extract)

**Files:** Create `internal/certverify/certverify.go`, `internal/certverify/certverify_test.go`; modify `cmd/corral/verify.go` (+ its test stays green).

**Interfaces — Produces:**
```go
type Check struct { Name string; OK bool; Detail string } // Name ∈ {signature, ledger, subject, rekor}
type Record struct {
    Statement map[string]any
    Signature string            // DSSE envelope JSON
    Steps     []map[string]any
    Head      string
    Rekor     string            // marshaled transparency.Entry (JSON), when Anchored
    Anchored  bool
}
// VerifyRecord runs the four checks against an EXTERNAL trust anchor (pub + w).
// pub: the published Ed25519 key (caller obtains it out-of-band, never from rec).
// w:   a transparency.Witness for the Rekor inclusion check (TUF-rooted).
// allowUnanchored: accept a signed-but-unwitnessed record (weaker).
func VerifyRecord(rec Record, pub ed25519.PublicKey, w transparency.Witness, allowUnanchored bool) (checks []Check, allOK bool)
```
The four checks (extracted verbatim from `cmd/corral/verify.go:194-230`): (1) `signature` — `certify.VerifyDSSE([]byte(rec.Signature), pub)` → also yields the statement; (2) `ledger` — `certify.UnmarshalSteps(json(rec.Steps))` + `certify.VerifyLedger(steps, rec.Head)`; (3) `subject` — the DSSE statement's `subject[0].digest.sha256 == rec.Head`; (4) `rekor` — if `rec.Anchored`, `w.VerifyInclusion(entry, []byte(rec.Signature))`; if not anchored, `OK=false` with a "not publicly witnessed" detail unless `allowUnanchored`. `allOK` = all applicable checks OK.

- [ ] **Step 1: Failing test** `certverify_test.go` `TestVerifyRecord`: build a real record in-process (via `certify.BuildLedger`/`BuildAttestation`/`SignDSSE` + a `transparency.NewFakeWitness()` anchor), assert all four checks OK + `allOK`; mutate the DSSE payload → `signature` check fails + `!allOK`; a tampered inclusion proof → `rekor` check fails; an unanchored record → `rekor` fails unless `allowUnanchored`.
- [ ] **Step 2: Run it, verify it fails** (`go test ./internal/certverify/`).
- [ ] **Step 3: Implement `VerifyRecord`** — lift the check sequence from `verify.go`.
- [ ] **Step 4: Refactor `cmd/corral/verify.go`** — `runCertifyVerify` builds a `certverify.Record` from its `certRecord` + the resolved external `pub` + the constructed witness, calls `certverify.VerifyRecord`, and formats the returned checks into its existing stdout/exit-code behavior. **`go test ./cmd/corral/ -run Verify` must stay green** (identical behavior).
- [ ] **Step 5: Run** `go test ./internal/certverify/ ./cmd/corral/` + `go vet` → green. **Commit.**
```bash
git add internal/certverify/ cmd/corral/verify.go
git commit -m "refactor(certverify): shared VerifyRecord — one 4-check verifier for CLI + UI (DRY)"
```

---

### Task 2: capture commit info + `git verify-commit` at certify time

**Files:** Modify `internal/buildstore/store.go` (columns + `Save`), `internal/brain/buildcert.go` (report_build params), `cmd/corral/certify.go` (+ test — git capture).

**Interfaces:**
- buildstore: add idempotent columns (mirror the `steps`/`rekor` ALTERs at store.go:57-65): `commit_message VARCHAR`, `commit_author VARCHAR`, `commit_date VARCHAR`, `commit_signature JSON`. `Save` grows: `Save(repo, commit, branch, actor, head, sig, statementJSON, stepsJSON, rekorJSON, anchored, commitMessage, commitAuthor, commitDate, commitSignatureJSON string/…)` — append the four; its only caller is `buildcert.go`.
- `report_build` gains input params `commit_message`, `commit_author`, `commit_date`, `commit_signature` (a `{signed,signer,mechanism,verified}` object) and stores them.
- `corral certify` captures them via the existing `run.GitOutput(...)` seam (certify.go:242-254 pattern): message = `git show -s --format=%s <sha>`, author = `%an <%ae>`, date = `%cI`; signature = parse `git verify-commit --raw <sha>` (exit 0 + a `GOODSIG`/`VALIDSIG` line → `{signed:true, verified:"good", signer, mechanism}`; exit non-zero with output → `bad`/`unknown-key`; no signature → `{signed:false, verified:"unsigned"}`). A failure to run git (not a repo) → empty/unsigned, never fatal.

- [ ] **Step 1: Failing tests.** buildstore: `Save`/`Get` round-trip carrying the four commit fields. certify (fake `GitOutput`): a signed commit → `commit_signature.verified=="good"` + signer captured; an unsigned commit (`git verify-commit` non-zero, no GOODSIG) → `verified=="unsigned"`, not failed; the record posted carries message/author/date.
- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement** the columns + `Save` growth + `report_build` params + the certify git-capture (parse `verify-commit --raw`).
- [ ] **Step 4: Run** `go test ./internal/buildstore/ ./internal/brain/ ./cmd/corral/` + `go vet` + `bash scripts/check-security.sh` → green.
- [ ] **Step 5: Commit.**
```bash
git add internal/buildstore/ internal/brain/ cmd/corral/certify.go
git commit -m "feat(certify): capture commit info + git verify-commit signature (the git link in the chain)"
```

---

### Task 3: `buildstore.List` + filter + summary

**Files:** Modify `internal/buildstore/store.go` (+ test).

**Interfaces — Produces:**
```go
type ListFilter struct { Repo, Actor, Status string /*pass|fail|all(""=all)*/; Anchored *bool; Since, Until float64; Limit, Offset int }
type Summary struct {
    ID int64; Repo, Commit, Branch, Actor string
    Pass bool               // derived: the record's execution exit_code == 0
    Anchored bool; CommitSigned bool
    ProducedBy []string; CreatedTS float64
}
func (s *Store) List(f ListFilter) ([]Summary, error)
```
`List` builds a parameterized `SELECT … FROM build_records WHERE …` (repo/actor exact-match when set; status filters on the stored pass flag or the statement's exit code — store a `pass BOOLEAN` column alongside for a cheap list, populated in Task 2's `Save`; `anchored` when non-nil; `created_ts BETWEEN`), `ORDER BY created_ts DESC`, `LIMIT/OFFSET` (default Limit 100). `CommitSigned` reads `commit_signature.signed`. `ProducedBy` from the statement's producers (or a stored column). Keep it a cheap projection — do NOT decode the full statement/steps per row.

> NOTE (Task 2 dependency): add a `pass BOOLEAN` column in Task 2's ALTER set and populate it in `Save` (from the execution exit code) so `List` filters without decoding JSON. If you reach Task 3 and it's absent, add it here.

- [ ] **Step 1: Failing test** `TestList`: insert several records (varied repo/actor/pass/anchored/time), assert each filter narrows correctly, ordering is newest-first, pagination (Limit/Offset) works, and `Summary` fields populate (incl. `CommitSigned`, `ProducedBy`).
- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement `List`** (parameterized query; `?` placeholders like the rest of the store).
- [ ] **Step 4: Run** `go test ./internal/buildstore/` + `go vet` → green.
- [ ] **Step 5: Commit.**
```bash
git add internal/buildstore/
git commit -m "feat(buildstore): List(filter) + Summary — the records-dashboard query"
```

---

### Task 4: `GET /api/builds` + `/api/builds/{id}` (list + server-verified detail)

**Files:** Modify `internal/ui/ui.go` (Deps + handlers + routes), `cmd/corral/main.go` (wire Deps).

**Interfaces:**
- `ui.Deps` gains: `BuildStore *buildstore.Store`, `CertifyPub ed25519.PublicKey`, `Witness transparency.Witness`.
- `GET /api/builds?repo=&actor=&status=&anchored=&limit=&offset=` → `s.builds`: parse query → `buildstore.List` → JSON `[]Summary`. Guard `s.deps.BuildStore == nil` → `[]`.
- `GET /api/builds/{id}` → `s.buildDetail`: `buildstore.Get(id)` → build a `certverify.Record` → `certverify.VerifyRecord(rec, s.deps.CertifyPub, s.deps.Witness, false)` → JSON `{record fields, commit info, produced_by, commit_signature, checks: []Check, allOK, verify_command: "corral certify verify <file> --brain <this-brain>"}`. Use Go 1.22 method-pattern routing (`mux.HandleFunc("GET /api/builds/{id}", …)`, `r.PathValue("id")`), or `?id=` if that matches the existing route style — mirror the neighbours.
- Both handlers are **GET-only** (write JSON like `s.history`/`s.state` at ui.go:367/870); they pass the read-only observe gate.
- `main.go`: set the three new `Deps` fields from the brain's already-loaded `buildStore`, `certifyKey.Public()`, and `certifyWitness` (main.go:602-631).

- [ ] **Step 1: Failing test** `internal/ui/ui_test.go`-style: construct a `Server` with a temp `buildstore` (seed a couple of records via `Save`), a keypair, and a `transparency.NewFakeWitness`; `GET /api/builds` returns the summaries (+ a filter narrows); `GET /api/builds/{id}` returns the detail with `checks` all OK + `allOK` for a good record and a `verify_command`; a non-GET is refused; `BuildStore==nil` → empty list, no panic.
- [ ] **Step 2: Run, verify fail.**
- [ ] **Step 3: Implement** the Deps fields, `s.builds`, `s.buildDetail`, route registration (near ui.go:150-164), and the `main.go` wiring.
- [ ] **Step 4: Run** `go test ./internal/ui/ ./cmd/corral/` + `go vet` + `go build ./...` + `bash scripts/check-security.sh` → green.
- [ ] **Step 5: Commit.**
```bash
git add internal/ui/ui.go cmd/corral/main.go
git commit -m "feat(ui): /api/builds list + /api/builds/{id} server-verified detail (records API)"
```

---

### Task 5: the "Records" cockpit view (table + detail) + default landing + docs

**Files:** Modify `internal/ui/web/index.html` (the SPA — a new view + JS), `site/src/content/docs/docs/ui-tour/` (a new records page + sidebar entry via `site/astro.config.mjs`).

**Interfaces:** Consumes `GET /api/builds` + `GET /api/builds/{id}`.

- A new tab **"Records"** declared alongside the existing tabs (index.html ~606-615) and wired into `setView` (~1459-1492). It fetches `/api/builds`, renders a **table**: repo · commit (short SHA + message) · actor · produced-by · **pass/fail pill** · **commit-signed ✓/–** · **anchored (Rekor) ✓/–** · relative time. Client-side filter/group controls for repo + actor + status (re-query `/api/builds` with params). Newest-first.
- Row click → **detail panel**: the **chain line** (*commit `abc…` signed by alice ✓ · check passed ✓ · publicly witnessed Rekor #N ✓* — greying any absent link honestly), the four `checks` rendered green/red with their `detail`, commit info (message/author/date), attributions (produced-by + commit author), and a **"verify it yourself"** block showing the `verify_command` verbatim + a link to `/api/certify/pubkey`. It MUST NOT claim the record is independently verified — it states the checks are the brain's, and points to the CLI for the trustless path.
- **Make "Records" the default landing view** (the `setView` initial/default), demoting the swarm canvas to a selectable tab. Reuse the SPA's existing fetch/render/styling helpers — no new framework, no new file.
- Docs: a `site/src/content/docs/docs/ui-tour/records.mdx` page describing the view + the honest trust note; add it to the UI-tour sidebar in `astro.config.mjs`.

- [ ] **Step 1:** Add the "Records" tab + view container + the fetch/render JS for the table (against `/api/builds`); wire `setView`.
- [ ] **Step 2:** Add the detail panel (fetch `/api/builds/{id}`; render chain + checks + commit + attributions + verify-it-yourself). Make Records the default landing.
- [ ] **Step 3:** Manually smoke-check against a running brain with records (or a seeded temp brain): the table lists records, filters work, a row opens the detail with the four checks + the chain + the verify command. (The SPA is vanilla JS — no Go unit test; if an existing Playwright/e2e harness covers the cockpit, add a minimal assertion there.)
- [ ] **Step 4:** Write `records.mdx` + sidebar entry; `cd site && npm run build` succeeds; run `bash scripts/sync-site-assets.sh --check` if the shared replay asset was touched (it should NOT be).
- [ ] **Step 5: Commit.**
```bash
git add internal/ui/web/index.html site/
git commit -m "feat(ui): Records dashboard view — team rollup + verify/attribution/commit detail; default landing"
```

---

## Self-Review

**Spec coverage:** `buildstore.List` (Task 3) + commit-info/signature capture (Task 2) + shared verify helper (Task 1) + routes (Task 4) + the dashboard/detail view + default-landing + docs (Task 5). Trust-honesty (UI = self-view + surfaces CLI) enforced in Task 4's `verify_command` + Task 5's detail panel. ✓

**Placeholder scan:** none — the one NOTE (the `pass` column) is a concrete cross-task dependency with the fix stated.

**Type consistency:** `certverify.{Check,Record,VerifyRecord}` (Tasks 1, 4); `buildstore.{ListFilter,Summary,List}` + the commit columns + `Save` growth (Tasks 2, 3, 4); `ui.Deps.{BuildStore,CertifyPub,Witness}` (Task 4). Consistent.

**Out of scope (per spec):** per-record replay linkage, public share links, the live org board, git-interleaved timeline, gitsign/Rekor unification + PR-merge sigs, MotherDuck federation.
