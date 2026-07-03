# Governed Cross-Swarm Coordination (v1) — Design

**Status:** design · **Date:** 2026-07-01 · **Sub-project:** E (the marquee)

## Problem

Cross-swarm capability today is one-directional and read-only: each brain pushes curated
metadata to a shared MotherDuck (#19), and any brain reads the aggregate via the `ask_fleet`
oracle (#20). There is **no brain-to-brain interaction**, and — the crux — **`brainID` is a
self-asserted string** (an env var / hostname, no signing, no attestation; any operator can set
`CORRALAI_BRAIN_ID=someone-else` and write rows indistinguishable from another brain). So nothing
"governed" can be built on top: any coordination that gates a decision on *which brain* said
something is trivially spoofable.

**Goal (v1):** enable swarms to **coordinate to avoid duplicating each other's work**, on a
foundation of **cryptographically attested brain identity**, staying entirely within the existing
MotherDuck medium (no brain-to-brain RPC, no credential sharing). A brain **publishes its own
signed intents** and **reads + locally decides on** peers' verified intents. No brain ever calls,
controls, or commands another.

## First principles

1. **Identity before governance.** Nothing is governable while `brainID` is a spoofable string.
   E1 makes brain identity cryptographic; E2 builds on it. Identity ships first.
2. **Observe, don't coerce.** A brain can never make another brain act. It publishes signed
   intents; peers read *verified* intents and decide *for themselves*. This is a bulletin board,
   not remote control — no new authority over another swarm, no shared credentials, no live locks
   (consistent with `fleet/sync.go`'s "only the record, never the live locks").
3. **Single trust domain (inherited from #20).** All swarms belong to one owner. E1 secures
   *authenticity* (a brain cannot forge another brain's identity), NOT multi-tenant isolation.
4. **Honest about the boundary.** E1 stops identity *forgery*; it does not stop an insider holding
   a *valid* key from publishing *false* claims (griefing) — that residual is out of scope for v1
   and mitigated structurally (advisory-only + TTL + rate-limit + revocation). Because coordination
   is advisory, a bad claim at worst makes a peer *skip* work; it can never make a peer do wrong.

## E1 — Attested brain identity (the foundation)

### Keypair
- Each brain has an **Ed25519 keypair**. The private key is loaded like the delegation secret:
  `CORRALAI_BRAIN_KEY` (base64 seed) → systemd credential → **generated-and-persisted** to a
  brain-local file (`$CORRALAI_STATE/brain_key` or alongside the other stores) on first run. The
  private key **never leaves the brain, is never synced, is scrubbed from env after load** (the
  #21 process-env-scrub pattern). Public key = derived.

### Registry (`fleet_brains` in MotherDuck)
- Table: `(brain_id VARCHAR, pubkey VARCHAR, registered_ts DOUBLE)`.
- On startup a brain **self-registers** its `(brain_id, pubkey)` if absent.
- **Trust bootstrap = TOFU + conflict alerting** (SSH `known_hosts` model): the **first** pubkey
  seen for a `brain_id` is trusted and pinned; a **later, different** pubkey for an existing
  `brain_id` is **refused** (not overwritten) and surfaced as a **tamper alert** (a would-be
  impersonator, or a brain that lost its key). Honest limit: a first-registration race (whoever
  registers a fresh `brain_id` first pins it) — acceptable under the single-owner assumption +
  operator visibility.
- **Optional hardened mode — operator allowlist:** `CORRALAI_BRAIN_PEERS` (a file/env of trusted
  `brain_id:pubkey` pairs the human provisions out-of-band). When set, ONLY allowlisted pairs are
  trusted and TOFU self-registration is disabled. Recommended for a hardened deploy; TOFU is the
  zero-config default.

### Sign / verify (`internal/attest`)
- `Sign(priv, brainID, kind, subject string, ts float64, nonce string) (sig string)` over a
  canonical byte encoding of those fields.
- `Verify(registry, brainID, kind, subject string, ts float64, nonce, sig string) error` — looks
  up the pinned/allowlisted pubkey for `brainID`, verifies the Ed25519 signature, and enforces
  **freshness** (`|now − ts| ≤ skew`, e.g. 5 min) + **nonce non-reuse** (a bounded seen-nonce
  cache / a `(brain_id, nonce)` uniqueness on the intents table) to defeat replay.
- Verification failure ⇒ the intent is IGNORED (never trusted, never acted on). Fail-closed.

## E2 — Signed coordination plane + work-claim dedup

### Intents table (`fleet_intents` in MotherDuck)
- `(brain_id VARCHAR, intent_id VARCHAR, kind VARCHAR, subject VARCHAR, ts DOUBLE,
   expires_ts DOUBLE, nonce VARCHAR, sig VARCHAR)`.
- `kind` for v1: `claim` and `release` (extensible later to `offer`/`request`).
- `subject` = the coordinated resource — a normalized repo URL (v1's use) or a capability tag.
- Every row is **signed** (E1) over `(brain_id, kind, subject, ts, nonce)`; consumers **verify**
  before trusting. Unverified or expired rows are ignored.

### Publish / release
- A brain publishes a `claim` for a subject before working it, and a `release` (or lets it expire)
  when done. It signs with its own key, so it **cannot publish as another brain** (the signature
  won't verify) and cannot forge another's `brain_id`.
- **Auto-expiry (TTL):** claims carry `expires_ts` (e.g. now + a bounded lease, renewed by
  heartbeat while active) so a dead brain's claims don't wedge the fleet. D's retention/compaction
  already handles cleanup of this table (add it to the retention set — cursor-safe).

### The capability — cross-swarm work-claim observation (dedup)
- Before starting a mission on `subject` (repo), the brain queries `fleet_intents` for a
  **verified, active (non-expired), other-brain** `claim` on that subject. If found, it can
  **surface it / skip / defer** to avoid duplicating another swarm's in-flight work.
- Exposed as: a read tool `fleet_claims{subject}` (sibling to `ask_fleet`, returns verified active
  peer claims), plus an optional pre-mission check in `create_mission` (advisory — logs/annotates,
  does not hard-block, since coordination is observe-not-coerce; behavior configurable).
- **Advisory, always:** it informs a *local* decision. It never blocks another brain, never
  commands, never shares work or credentials.

### Governance controls
- Only **verified-signature, non-expired** intents from a **registry-trusted** `brain_id` are ever
  considered. Everything else is ignored.
- **Per-brain publish rate limit** (a brain can't flood `fleet_intents`).
- **Revocation:** remove a `brain_id` from the registry/allowlist ⇒ its intents stop verifying ⇒
  it's out. (A `revoked` flag on `fleet_brains`, or allowlist removal.)

## Architecture / components

- `internal/attest` (new) — Ed25519 keypair load/generate/persist; `Sign`/`Verify`; the registry
  trust logic (TOFU-pin + conflict-detect, or allowlist). No I/O to MotherDuck itself (takes a
  registry-lookup func) so it's pure + testable.
- `internal/fleet` (extend) — the `fleet_brains` + `fleet_intents` DDL + sync/registry helpers,
  mirroring the existing in-mem-DuckDB-ATTACH-to-`md:` pattern; publish/release/query intents;
  register/lookup brains; retention entry for `fleet_intents` (cursor-safe like D).
- `internal/brain` (extend) — the `fleet_claims` read tool (+ optional pre-mission check); wiring
  the brain keypair + registry at startup; per-brain publish rate limit.
- `cmd/corral` — load/generate the brain key (env→cred→file), scrub the key env after load, wire
  attestation + registration into startup + the fleet ticker.

## Error handling / edge cases

- **No MotherDuck / no key** → coordination disabled gracefully (no registry, no intents); the
  brain runs standalone exactly as today. Never a hard failure.
- **Registry conflict (impersonation attempt / lost key)** → refuse the new pubkey, keep the pinned
  one, emit a loud tamper alert (log + audit event); the conflicting brain simply can't participate
  until the operator resolves it.
- **Clock skew** → freshness window (±5 min) tolerates normal skew; wildly-off `ts` → rejected.
- **Replay** → nonce non-reuse (uniqueness on `(brain_id, nonce)` + a bounded recent-nonce cache).
- **A brain's own key rotation** → operator re-provisions (allowlist) or clears the pinned entry
  (TOFU) deliberately; not automatic (auto-rotation would defeat conflict-detection).
- **Stale/expired claims** → ignored on read; compacted by retention.
- **Verification is fail-closed** everywhere: any doubt ⇒ ignore the intent ⇒ fall back to
  "no cross-swarm info" ⇒ the brain behaves as a standalone swarm. Coordination never breaks normal
  operation.

## Testing (adversarial — adopters WILL red-team this)

- **Forgery:** an intent with a wrong/absent signature, or signed by a different key than the
  `brain_id`'s registered pubkey, is **rejected** (never appears in `fleet_claims`, never affects a
  decision).
- **Impersonation:** brain A publishing an intent tagged `brain_id = B` fails verification (A can't
  sign as B). A TOFU **conflict** (a second, different pubkey for an existing `brain_id`) is refused
  + alerted, not overwritten.
- **Replay:** a captured valid intent replayed with a stale `ts` (outside the window) or a reused
  `nonce` is rejected.
- **Expiry:** an expired `claim` is ignored by the dedup check.
- **Own-claims-only:** a brain can `release` only its own claims (a release signed by A for B's
  claim doesn't verify).
- **Dedup capability:** `fleet_claims{subject}` returns only verified, active, other-brain claims;
  the pre-mission check surfaces a real peer claim and ignores a forged one.
- **Graceful:** no key / no MotherDuck / allowlist-empty → coordination disabled, brain runs normally.
- **Crypto unit tests** for `internal/attest`: sign→verify round-trip; tamper any field → verify
  fails; freshness + nonce enforcement.

## Out of scope (deferred — each its own design)

- **Direct brain-to-brain RPC / delegation** (live pull, a brain invoking another's MCP) — the "full
  matrix"; a large separate security surface (cross-brain auth, credential/egress boundary, SSRF).
- **Cross-swarm KNOWLEDGE federation** (sharing vetted memory/reference across swarms) — its own
  plane, ties to #21's trust tiers.
- **`offer`/`request`/negotiation intents** — v1 is `claim`/`release` for dedup only.
- **Multi-tenant isolation** — v1 assumes a single owner (per #20).
- **Real CA/PKI, key auto-rotation, insider-griefing defense** — TOFU/allowlist + advisory-only +
  revocation is the v1 posture; deeper trust infrastructure is later.
