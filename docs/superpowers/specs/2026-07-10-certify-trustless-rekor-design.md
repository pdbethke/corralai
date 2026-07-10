<!-- SPDX-License-Identifier: Elastic-2.0 -->
# `corral certify` — the trustless tier (Sigstore Rekor anchoring)

**Date:** 2026-07-10
**Status:** approved (brainstorm) → spec for review
**One line:** Anchor every certify record to **Sigstore Rekor** (a public, append-only
transparency log) so tampering is detectable *without trusting corral's own brain* —
turning the central-trust wedge into a trustless, independently-witnessed record.

## Why

`corral certify` (shipped) mints a signed, tamper-evident record and verifies it against
the brain's **published** key — but that is **central-trust**: a verifier still trusts that
the brain didn't rewrite its own history before publishing. The first question a CISO asks
is *"so your server vouches for itself?"* This tier answers it: each record is anchored to
**Sigstore Rekor**, the public transparency log the SLSA/npm/PyPI/GitHub ecosystem already
uses. Verification then trusts **Rekor (a public good) + the published key — not corral.**
To forge history an attacker would have to rewrite a publicly-witnessed, cryptographically
chained log entry. That is the trustless property, and it is what makes the
supply-chain-in-the-agentic-era argument something a security leader re-shares instead of
politely ignores.

## What exists to build on (the shipped wedge)

- `internal/certify` — `BuildLedger`/`VerifyLedger`, `BuildAttestation`, and detached-Ed25519
  `SignStatement`/`VerifyStatement` (over canonical statement bytes). **This tier replaces the
  detached signature with a DSSE envelope** (the format Rekor accepts) — an evolution, not a
  rewrite; nothing is in prod yet so there is no migration.
- `internal/buildstore` — DuckDB `build_records` (statement, signature, steps, head).
- `internal/brain` `report_build` — signs + stores + returns the record; `GET /api/certify/pubkey`.
- `cmd/corral` `certify` / `certify verify` — produce + independently verify a record.

## Design

### 1. DSSE envelope (replaces the detached signature)
`internal/certify` gains DSSE signing over the in-toto statement, using the canonical DSSE
**PAE** (pre-authentication encoding) via `github.com/secure-systems-lab/go-securesystemslib/dsse`:
- `func SignDSSE(stmt map[string]any, priv ed25519.PrivateKey, keyID string) (envelope []byte, err error)`
  — returns a DSSE envelope `{payload(b64 statement), payloadType:"application/vnd.in-toto+json", signatures:[{sig,keyid}]}`.
- `func VerifyDSSE(envelope []byte, pub ed25519.PublicKey) (statement map[string]any, ok bool, err error)`
  — verifies the PAE signature and returns the decoded statement.
The stored `signature` column becomes the DSSE envelope; `statement` remains the canonical
in-toto statement (the DSSE payload). `SignStatement`/`VerifyStatement` are removed (unused,
un-shipped).

### 2. The witness abstraction (`internal/transparency`)
A thin, mockable interface so the brain never depends on Rekor internals and CI stays hermetic:
- `type Entry struct { LogIndex int64; LogID string; IntegratedTime int64; InclusionProof []byte; SET []byte; Body []byte }`
- `type Witness interface {`
  - `Anchor(ctx, dsseEnvelope []byte) (Entry, error)` — submit a `dsse`-type entry, return the log entry.
  - `VerifyInclusion(entry Entry, dsseEnvelope []byte, rekorPub ed25519.PublicKey) (bool, string)` — verify the
    **inclusion proof + signed entry timestamp OFFLINE** against Rekor's public key, and confirm the entry body
    matches the envelope. No network needed to verify a stored proof.
  - `}`
- `RekorWitness` — the real impl, likely over **`sigstore-go`**'s verifier (it bundles
  inclusion-proof + SET verification and resolves Rekor's public key via the Sigstore **TUF
  trust root** — the correct, non-circular way to obtain the log's key). `CORRALAI_REKOR_URL`
  (default `https://rekor.sigstore.dev`). **Trust-source note:** the Rekor verifying key must
  come from a trusted root (TUF), NOT from the record or the same Rekor instance's key endpoint
  circularly; v1 uses the TUF root (or a pinned public-Rekor key as a fallback), full custom
  trust-root config is v2.
- `fakeWitness` (test) — deterministic in-memory log for unit tests; a build-tag/env-gated
  integration test hits the real public Rekor.

### 3. `report_build` anchors after signing
After `SignDSSE`, the handler calls `Witness.Anchor(envelope)` and stores the returned `Entry`
alongside the record. **Graceful on outage:** if `Anchor` errors (Rekor down/unreachable), the
record is still signed + stored but flagged `anchored=false` (we never fail a build because a
*log* was down — corral's degrade-don't-deadlock principle). The tool result carries the Rekor
log index + `anchored` bool. `Witness` is an `Options` field; nil → anchoring off (report_build
still works, records are `anchored=false`).

### 4. `build_records` stores the Rekor evidence
Add a `rekor JSON` column holding the `Entry` (log index, log ID, integrated time, inclusion
proof, SET) + an `anchored BOOLEAN`. `Get`/the exported record include it.

### 5. `corral certify verify` checks the witness
`verify` adds, after the existing DSSE-signature + ledger + subject-digest checks:
- if the record is `anchored`: `Witness.VerifyInclusion(entry, envelope, rekorPub)` — verify the
  inclusion proof + SET **offline** against Rekor's public key, and confirm the anchored body is
  our envelope. Rekor's public key comes from `--rekor-pubkey`/a pinned default, fetched once.
- if `anchored=false`: `verify` prints `signed, NOT publicly witnessed` and (default) exits
  non-zero unless `--allow-unanchored` is passed — an unwitnessed record is honestly weaker.
ALL applicable checks must pass → `verified (publicly witnessed at <time>, Rekor #<index>)`.

### 6. The demo (the article's anchor)
A reproducible `scripts/demo-certify-trustless.sh` (or a documented sequence): an agentic build
→ `corral certify -- <check>` (anchors to public Rekor) → `corral certify verify` (passes,
shows the Rekor index + timestamp) → **tamper** the record → `verify` fails naming the broken
link (signature / ledger / Rekor inclusion). Captured as a recording/asciinema for the piece.

## Trust model (honest, for a CISO reading)
Trustless-*against-corral*: verification requires trusting **Rekor** (a public, monitored,
append-only log with its own transparency guarantees) **+ the published signing key** — not
corral's brain. What Rekor gives: the entry existed at time T and the log is append-only and
publicly auditable. What it does **not** give: it doesn't prove the *content* is true, only that
*this signed statement* was witnessed at that time and hasn't been altered since. We claim
tamper-**evident** (detectable), never tamper-**proof**. **v1 verifies only against the public
Sigstore TUF trust root** — `RekorWitness` always resolves the log's verifying key(s) from that
root, regardless of `CORRALAI_REKOR_URL`. Pointing `CORRALAI_REKOR_URL` at a private/non-public
Rekor instance does **not** get you air-gap today: `Anchor` submits to whatever URL you point it
at, but `VerifyInclusion` fails **closed** — a private log's logID/key isn't in the public TUF
root, so the check reports "no TUF-rooted public key for this transparency log" rather than
silently trusting it. True air-gap (a private Rekor + a custom, non-Sigstore trust root the
witness verifies against) is captured in the out-of-scope/v2 list below, not delivered here.

## Out of scope — v2 backlog (captured, not built)
- **Keyless (Fulcio/OIDC) signing** — no long-lived key; the brain's OIDC identity (Zitadel) →
  Fulcio short-lived cert. The "no keys to steal" gold standard; bigger lift.
- **Private-Rekor air-gap productization** — a custom, non-Sigstore trust root the witness can
  verify against (v1 only fetches the public Sigstore TUF root), plus bundling/operating a
  private Rekor for IL4-5.
- **Alternate/secondary witnesses** — the MotherDuck warehouse as a co-witness; other logs.
- **Batched/deferred anchoring** — anchor N records in one entry; retry queue for `unanchored`.
- **Re-verifying a prebuilt external artifact** corral didn't build.

## Testing (TDD)
- `internal/certify`: DSSE sign→verify round-trip; a tampered payload fails; wrong key fails; the
  statement recovered from the envelope equals the input.
- `internal/transparency`: `fakeWitness` Anchor→VerifyInclusion round-trip; a tampered envelope
  fails inclusion; a tampered proof fails; wrong Rekor key fails. One env-gated integration test
  against real `rekor.sigstore.dev`.
- `report_build`: anchors via `fakeWitness`, stores the Entry, `anchored=true`; on `Anchor` error
  the record is stored `anchored=false` and the build isn't failed.
- `verify`: full path passes with a good witnessed record; a tampered inclusion proof fails at the
  Rekor step; an `unanchored` record fails without `--allow-unanchored`, passes with it.

## Dependencies
Sigstore Rekor Go client + `go-securesystemslib/dsse`. New deps → a `scripts/check-security.sh`
(gosec + govulncheck) pass, same discipline as the keystore; pin versions; keep the `transparency`
wrapper thin so the blast radius of the Rekor client stays contained.

## Decisions (defaulted; revisit in review)
- **Keyed Ed25519 + DSSE + Rekor** (v1); keyless is v2.
- **Graceful on Rekor outage** (`anchored=false`), never fail a build on a log outage.
- **Offline inclusion verification** (verify a stored proof without calling Rekor) — air-gap-
  friendly verification + hermetic tests.
- **`verify` rejects unanchored by default** (`--allow-unanchored` to override) — honest that an
  unwitnessed record is weaker.
- **Public `rekor.sigstore.dev` default**; `CORRALAI_REKOR_URL` lets you point `Anchor` at a
  different instance, but v1 verification is hard-wired to the public Sigstore TUF trust root, so
  a non-public Rekor there fails closed at `verify` today — true air-gap is v2 (see out-of-scope).
