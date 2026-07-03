# Security Model

Corralai runs autonomous AI agents that write and change code. Its security posture starts
from one assumption:

> **An agent can be hijacked.** A model can be steered by a prompt injection — in a ticket, a
> web page, an ingested PDF, a code comment, another agent's output. So corralai does not rely
> on the agent behaving. It relies on a hijacked agent being unable to do damage, and on every
> action being attributable after the fact.

That is the whole design: **contain, and observe.** Two pillars.

---

## Pillar 1 — Prevention (containment)

The blast radius of a compromised agent is bounded by construction, not by trust.

- **Agents are jailed.** A bee's shell commands run in a `bwrap` sandbox: no network by
  default, filesystem confined to its workspace, host `$HOME` and secrets stripped from the
  environment. The bee process is confined at the point it executes anything.
- **The credential boundary.** The git/forge token lives **only** in the brain. It is scrubbed
  from the process environment right after load, injected into a clone/push URL for exactly one
  network call, and **never** written to `.git/config` (which a bee could read) or any log. Bees
  hold no credentials — they ask the brain to perform git operations.
- **Per-forge credential isolation.** With multiple forges configured (GitHub / GitLab / Gitea),
  a token is only ever used against **its own** host — one forge's token is never injected into
  another forge's URL.
- **The fleet oracle is sandboxed.** The free-form "ask the fleet" feature has an LLM generate a
  read-only SQL query, but it runs inside a locked DuckDB connection (`disabled_filesystems`,
  extension-autoload off, `lock_configuration`) so a generated query **cannot** read local files,
  attach other databases, hit URLs, or reach environment secrets — it can only read the curated
  fleet metadata. Verification is fail-closed.
- **The fleet is metadata-only.** What a brain syncs to the shared fleet (and thus what the oracle
  can reach) is a curated, per-table **column allowlist** of mission/task/finding *metadata* —
  counts, roles, models, severities, outcomes, timestamps. Agent-authored content, reference/RAG
  bodies, memory text, and free-form JSON `detail` values are **not** synced: JSON columns that
  cross are value-sanitized to a fixed key whitelist at the sync boundary (a regression test seeds
  a canary and asserts it never appears in the synced table). The **one deliberate exception** is
  the mission **directive** — synced as mission *identity* so a mission is recognizable across the
  fleet. Treat the directive as identity, not as a secret; if that matters, redact it before it
  leaves the brain.
- **Knowledge is trust-tiered.** Agent-written "lessons" and ingested reference material are
  **unvetted** — searchable as clearly-fenced data, but **never** auto-injected as an
  authoritative instruction into a future mission. Only human-vetted knowledge is authoritative.
  This kills the "poisoned document propagates through the swarm" worm.
- **Cross-swarm coordination is signed and fail-closed.** Brains have Ed25519 identities;
  coordination intents are signed and verified against a trust registry, replay-protected, and
  advisory-only — a brain publishes its own signed intents and reads verified peers'; it can
  never call, command, or coerce another brain.
- **Least privilege elsewhere:** an SSRF guard on the MCP gateway, bounded agent-spawn governance,
  per-IP and per-principal rate limits, a scoped read-only observer token, and OIDC + a
  per-principal authorization allowlist.

## Pillar 2 — Detection (forensics)

Because **all** agent traffic funnels through the brain (the single trusted egress), the brain
is the forensic authority.

- Every consequential agent action is recorded — the audit trail, telemetry, mission/task/finding
  history, and memory authorship — and is **attributable** to a verified principal.
- **Agents cannot forge or erase their own trail.** They act *through* the brain, which records
  the action as a side effect. The subject of the record does not control the ledger.
- The record is **queryable in natural language** (the fleet oracle) — "what did agent X do
  across every mission? who ingested that document?" — so an incident can be reconstructed.

Prevention bounds what a hijacked agent *can* do; forensics ensures you can always see *what it
did*.

---

## Verify the claims yourself

The security properties above are backed by adversarial tests. Don't take them on faith — run
them. (Sandbox/`bwrap` tests require `go test`, not a direct shell, because they nest user
namespaces.)

```bash
go test ./...            # everything
```

The load-bearing suites, by claim:

**The oracle cannot exfiltrate** (`internal/oracle`)
- `TestSandboxExfilBlocked` — an 11-vector matrix (`read_text`/`read_csv`/`read_blob`/`glob`/
  `read_json`/local `ATTACH`/`COPY`/URL fetch/second-statement/obfuscated/`pragma_*`) — every one
  is blocked, and a seeded canary file's contents never surface.
- `TestLockdownBlocksFilesWithoutValidator` — proves the DuckDB *config* lockdown (not just the
  string validator) blocks file reads, and that the lockdown is irreversible.
- `TestGetenvCannotReadScrubbedSecret` — `getenv()` in a generated query cannot read a scrubbed
  secret.

**Knowledge-poisoning worm is contained** (`internal/memory`, `internal/fence`, `internal/mission`)
- `TestRecallLessonsSharedOnly` — agent-written (private) lessons are never auto-recalled into
  instructions; only vetted (shared) lessons are.
- `TestUntrustedNeutralizesEmbeddedSentinel` — untrusted content can't forge or escape the data fence.
- `TestReplanFencesEvidence` / `TestReplanVerifyFencesEvidence` — reported evidence is fenced, not
  executed.

**Cross-swarm identity is unforgeable** (`internal/attest`, `internal/fleet`)
- `TestVerifyRejectsTamper` / `TestVerifyWrongKeyFails` — tampered or impersonated intents fail
  verification.
- `TestRegisterTOFUPinAndConflict` — a brain cannot overwrite another brain's pinned identity.
- `TestActiveClaims_ForgedSigDropped` / `TestActiveClaims_ImpersonationDropped` /
  `TestActiveClaims_ReplayResurrectionRejected` — forged, impersonated, and replayed claims are
  all dropped before they can influence a decision.

**The credential boundary holds** (`internal/repo`, `cmd/corral`, `internal/sandbox`)
- `TestPushCredRegistryStrict` — a token is never injected into a foreign forge's URL.
- `TestTokenNeverPersistedInConfig` — the token never lands in `.git/config`.
- `TestMinimalEnvHasNoGitToken` / `TestRunEnvIsSecretFree` — a jailed subprocess's environment is
  secret-free.

**The jail confines** (`internal/sandbox`)
- `TestBwrapConfinesWritesAndReads` — writes outside the workspace and reads of sensitive paths
  fail.
- `TestBwrapWrapNetOffByDefault` — no network unless explicitly enabled.

---

## What this does *not* claim

Honesty is part of the model.

- **It does not make an LLM immune to instructions in content it reads.** Fencing untrusted data
  is hardening, not a guarantee; the *control* is that unvetted content can't reach an
  authoritative position and that a fooled agent is contained. If an agent chooses to follow
  injected text in fenced reference data, containment (jail, no credentials, human PR review) is
  what limits the damage.
- **It does not defend against a malicious insider holding a valid key.** Cross-swarm attestation
  stops identity *forgery*, not an authorized brain publishing *false* claims; that is mitigated
  by advisory-only semantics, TTLs, and revocation, not prevented.
- **It assumes a single trust domain.** Cross-swarm coordination assumes all participating swarms
  belong to one owner; it is not multi-tenant isolation.
- **It has not been battle-tested at scale.** The properties are proven by design review and the
  adversarial tests above; they have not (yet) survived a hostile production adversary.

---

## Reporting a vulnerability

Please report suspected vulnerabilities **privately** — do not open a public issue. Use the
repository's **GitHub Security Advisories** ("Report a vulnerability") so a fix can be prepared
before disclosure. Include reproduction steps and, ideally, a failing test in the style above.

Corralai is source-available under the **Elastic License 2.0**; see `LICENSE`.
