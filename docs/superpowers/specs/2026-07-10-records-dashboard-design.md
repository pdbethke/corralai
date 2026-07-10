<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Records dashboard — the observe/verify UI (first brick)

**Date:** 2026-07-10
**Status:** approved (brainstorm) → spec for review
**One line:** Make a certified build a **first-class UI object** — a team-rollup dashboard of
signed build records + a per-record verify/attribution/commit view — turning the CLI-only
accountability system into something you can *see*, *share*, and *drill into*.

## Why

The pivot is to accountability, but the entire certify/verify system is **CLI + MCP + one
HTTP endpoint** (`/api/certify/pubkey`) — there is **zero web UI** for it. The existing
cockpit is overwhelmingly *swarm/orchestration* (a live board of a herd coordinating), which
goes quiet when the real case is **many independent devs+agents on one repo, each submitting a
signed build record to the brain**. This brick makes the record a first-class object: the
dashboard *is* the team rollup (records already carry repo · commit · actor · produced-by ·
pass · signed · anchored), and the detail view shows the full **git → corral → Rekor** chain.
It reuses the crown-jewel replay engine and the read-only console gate rather than rebuilding.

## What exists (from the UI map)

- The cockpit is a single-page app (`internal/ui/web/{index.html, replay-player.js}`), served
  with ~30 data routes in `internal/ui/ui.go` (`Server` struct, `Deps`, `Handler(d Deps)`;
  routes like `/api/state`, `/api/history`, `/api/replay`).
- `internal/console` is a token-injecting proxy with a **read-only observe gate** (refuses
  non-GET) — a ready-made shareable, act-proof viewing lane.
- `replay-player.js` is a read-only, backend-free, embeddable audit engine (scrubber,
  causal-chain task-story, file-tree lens, per-agent console).
- `internal/buildstore` has `Save`/`Get` only — **no `List`** (net-new).
- **No web UI touches records, signatures, ledgers, Rekor, or verification.** All net-new.

## Design

### 1. Data layer — `buildstore.List` + commit columns
- `func (s *Store) List(f ListFilter) ([]Summary, error)` — net-new. `ListFilter{ Repo, Actor
  string; Status string /*pass|fail|all*/; Anchored *bool; Since, Until float64; Limit, Offset
  int }`. `Summary{ ID int64; Repo, Commit, Branch, Actor string; Pass bool /*exit==0*/;
  Anchored bool; ProducedBy []string; CommitSigned bool; CreatedTS float64 }` — cheap list
  (reads columns; does NOT decode the full statement/steps).
- Add columns (idempotent `ADD COLUMN IF NOT EXISTS`, mirroring `steps`/`rekor`): `commit_message
  VARCHAR`, `commit_author VARCHAR`, `commit_date VARCHAR`, `commit_signature JSON`
  (`{signed, signer, mechanism, verified}`). Captured at certify time (§2).

### 2. Commit info + signature capture (certify side)
- `corral certify` captures, at certify time, via `git`: the commit message/author/date, and
  `git verify-commit --raw <sha>` → `commit_signature = {signed bool, signer string, mechanism
  string /*gpg|ssh|gitsign*/, verified string /*good|bad|unknown-key|unsigned*/}`. Threaded
  through the `report_build` params → stored on the record.
- **Honest:** an unsigned commit records `verified: "unsigned"` — it is *recorded*, never a
  failure and never faked green. `git verify-commit` uses the CI env's keyring/allowed-signers;
  independent verification of a GPG/SSH signature needs the signer's key from a trusted source
  (same external-anchor rule as the Rekor key) — the detail view says so.

### 3. Shared verify-checks helper (DRY)
- Extract the CLI's inline verification (currently in `cmd/corral/verify.go`) into
  `internal/certverify.VerifyRecord(rec Record, pub ed25519.PublicKey, w transparency.Witness)
  (checks []Check, allOK bool)` — runs the four checks (DSSE signature via `certify.VerifyDSSE`;
  ledger via `certify.VerifyLedger`; `subject[0].digest.sha256 == head`; Rekor inclusion via
  `w.VerifyInclusion`) + a fifth *informational* commit-signature line. **Both** `corral certify
  verify` (CLI) **and** the UI detail handler call it — one implementation, no duplicated
  check logic (standing DRY directive).

### 4. HTTP routes (`internal/ui/ui.go`, `Deps.BuildStore` + certify pubkey + witness)
- `GET /api/builds?repo=&actor=&status=&anchored=&limit=&offset=` → `List` summaries as JSON.
- `GET /api/builds/{id}` → detail: the stored record + the **server-re-run check results**
  (`certverify.VerifyRecord` with the brain's own certify pubkey + a witness) + commit info +
  attributions (`produced_by` models + commit author) + the exact `corral certify verify`
  command a third party would run.
- **GET-only**, so both routes work through the read-only observe gate. Add `BuildStore`, the
  certify public key, and a `transparency.Witness` to `ui.Deps` (wired in `cmd/corral/main.go`
  where the brain's Options are assembled).

### 5. The UI view (`internal/ui/web/index.html` + a records view)
- A new cockpit view **"Records"**: a table — repo · commit (short + message) · actor ·
  produced-by · **pass/fail** · **commit signed ✓** · **anchored (Rekor) ✓** · when — filterable
  and groupable by repo + actor + status, newest-first. **This is the multi-dev team rollup.**
- Row → **detail panel** rendering the **git → corral → Rekor chain** as one line
  (*commit `abc123` signed by alice ✓ · check passed ✓ · publicly witnessed Rekor #N ✓*), the
  four checks green/red, commit info, attributions, and a **"verify it yourself"** block (the
  `corral certify verify` command + a link to `/api/certify/pubkey`).
- **Records is the DEFAULT landing view** (the accountability front door); the swarm canvas is
  demoted to a tab. Reuse the SPA's fetch/render/`setView` patterns and styling — no new app.

### Trust honesty (load-bearing)
The dashboard's checks are **the brain re-running verification on its own records** — an
operator convenience, still central-trust. The **independent, trustless** path is the CLI
(`corral certify verify` against the *published* key + Rekor). The detail view surfaces that
command explicitly, so the UI never implies "my dashboard says verified = you should believe
me." Same discipline as everywhere else: the log and the published key are the trust anchors,
not the server showing you a green check.

## Out of scope — later layers (captured, not built)
- Per-record ↔ mission-replay **linkage** + replaying a *single* certified build in the scrubber
  (replay is whole-mission-only today).
- **Public, tokenless per-record share links** (for a client/auditor/gov demo).
- The live org **board** visualization of the rollup (the "swarm relocated to the team scale").
- **Git-interleaved timeline** (commits + their certifications on one repo timeline).
- **gitsign/Rekor unification** (commit + build as two entries in one log), PR/merge-approval
  signatures, full-history verification.
- **MotherDuck federation** across repos/orgs.

## Testing (TDD)
- `buildstore.List`: each filter (repo/actor/status/anchored/time), pagination, summary fields;
  the new commit columns round-trip.
- `certverify.VerifyRecord`: the four checks + the commit-signature line; a tampered record
  fails the correct check; the CLI and a direct call produce identical results (proves DRY).
- Commit-signature capture: signed / unsigned / bad-signature / unknown-key cases via a fake
  git runner; unsigned recorded honestly.
- Routes: `/api/builds` list + each filter; `/api/builds/{id}` detail incl. server-verify
  results; both GET-only (pass the observe gate; a non-GET is refused).
- UI: follow the existing `internal/ui` test patterns for the handlers; the SPA view gets a
  smoke/manual check (JS, not unit-tested in Go).

## Decisions (defaulted; revisit in review)
- **Records is the default landing view** — the pivot makes accountability the front, not a tab.
- **UI verify = brain self-view + surfaces the CLI** for independent verification (honest, not
  a trust overclaim).
- **Commit signature captured at certify** via `git verify-commit`; unsigned recorded, not failed.
- **Reuse** the SPA + console read-only gate + replay engine; no new front-end app.
- **DRY:** one `certverify.VerifyRecord` shared by the CLI and the UI.
