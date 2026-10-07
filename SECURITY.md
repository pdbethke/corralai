# Security Model

corral audits code it did not write, so it **runs untrusted code by design**: your test
suite against deliberately broken copies of your code, tests a model wrote, and, in
`corral review`, reproduction scripts a model wrote. A model can also be steered by text
inside the code it reads (a prompt injection in a comment or a test). The posture starts
from that:

> **Nothing a model says is trusted, and nothing a model wrote runs unconfined without
> you choosing that.** A verdict is decided by execution, and every result is signed.

## What runs where

| What runs | Where | Network | Your model keys in its environment |
|---|---|---|---|
| `certify` (default): your suite against each mutant, and the written tests | a disposable copy of your tree in a `bwrap` sandbox (container or `sandbox-exec` elsewhere); project dependency dirs bound read-only | none | no |
| `certify --repo --substrate workspace` | your checkout, in place (you, or the CI runner, are the isolation boundary) | yes | no (scrubbed) |
| `corral review`: the reviewer's and verifier's reproduction scripts | a detached git worktree at the reviewed commit, **not a sandbox** | yes | no (scrubbed) |
| an agentic review seat (Claude Code, Codex, …) | that agent, started in a disposable copy of the repository | the agent's own | the agent's own |

Two of those rows are not sandboxed, and that is stated here because it is easy to miss:

- **`--substrate workspace` mutates your real checkout.** Use it on a CI runner, a scratch
  clone, or a tree with no uncommitted work. Restores run on a failing command, a timeout, a
  panic and an interrupt (SIGINT/SIGTERM); a process killed outright (kill -9, out of
  memory) can leave a mutant behind, and nothing detects it.
- **`corral review` runs model-written scripts with your user's permissions**, on your
  filesystem and network (in a throwaway worktree, with model keys removed from their
  environment). Run it where that is acceptable: a CI runner, a container, a machine you do
  not mind a script touching. The scripts are recorded in the ledger, so what ran is never
  hidden.

## Why a hijacked model cannot change a verdict

- **The kill rate is your suite's exit code**, run against each planted fault. No model
  reports it, so no injected instruction can raise it.
- **A proven gap is execution too:** a written test counts only if it fails with the fault in
  and passes on your real code, both run.
- **The critic is advisory.** Its opinion is recorded as unverified and never moves a status.
  Its answer is held to a fixed schema where the provider supports one, and parsed
  strictly either way, so it can say only *sound*, *vacuous* or *dead check*.
- **Review claims are demoted unless their script reproduces**, and a second seat tries to
  refute what stands; both seats are graded by what held.

## The record

- Every `certify --repo` and `review` run writes a **signed (Ed25519), hash-linked** entry to a
  ledger directory, by default `.corral/ledger`, and in CI to the orphan branch
  `corral/ledger`. An edited, removed or re-ordered entry breaks the chain;
  `corral verify --ledger <dir> --pub <key>` names it. The key that signs corral's own ledger
  is published as `LEDGER_PUBKEY` and anchored in Sigstore Rekor.
- `certify --repo --attest --transparency` logs the signed statement to **Rekor**, a public,
  append-only log, so a record can be checked by someone who trusts neither you nor us.
  `corral certify verify` refuses an unanchored record unless you pass `--allow-unanchored`.
- A record's own embedded key is never a trust anchor: verification takes the key from you.

## Keys and cost

- Model keys come from the environment or corral's keystore (`corral secret`: OS keyring, then
  an age-encrypted file; `set` reads stdin, never argv). They are removed from the
  environment of anything corral runs.
- **A run spends money.** `--max-tokens` caps a run's tokens across every seat. In the GitHub
  Action, a pull request from a fork runs with your key unless you require approval for fork
  workflows; the Action docs say how.

## The optional daemon (`corral-wrangler`, frozen)

No audit needs or reads the daemon. For those who run it, its model is the one below: it
coordinates a herd of agents and assumes any agent can be hijacked, so it relies on
containment and on every action being attributable.

### Prevention (containment)

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

### Detection (forensics)

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
- **`corral review` and `--substrate workspace` are not sandboxed** (see "What runs where");
  `certify`'s default substrate is.
- **It has not been battle-tested at scale.** The properties are proven by design review and the
  adversarial tests above; they have not (yet) survived a hostile production adversary.

---

## Reporting a vulnerability (coordinated disclosure)

Please report suspected vulnerabilities **privately** — do not open a public issue. Use the
repository's **GitHub Security Advisories** ("Report a vulnerability") so a fix can be prepared
before disclosure. Include reproduction steps and, ideally, a failing test in the style above.

We follow **coordinated disclosure** (aligned with ISO/IEC 29147 *disclosure* and 30111 *handling*):

- **Acknowledgement:** within **3 business days**.
- **Triage + initial assessment:** within **10 business days**.
- **Fix & disclosure:** coordinated with the reporter; a public advisory + credit (unless you
  prefer anonymity) once a fix is available, targeting **90 days** or sooner for severe issues.
- **Safe harbor:** we will not pursue or support legal action against researchers who act in good
  faith, avoid privacy violations and service disruption, and give us reasonable time to remediate
  before public disclosure.

Corralai is source-available under the **Elastic License 2.0**; see `LICENSE`.

## Supply-chain security posture (our own methods)

We hold corralai's *own* build and distribution to the standards it helps others meet, and publish
the evidence:

- **Provenance:** releases target SLSA build provenance; the console UI bundle and release artifacts
  are signed and (as the transparency work lands) anchored to a public log — the same in-toto/SLSA +
  Sigstore/Rekor machinery `corral certify` provides.
- **Scoring:** an **OpenSSF Scorecard** runs on every push to `main` (see the badge in the README).
- **SBOM:** an SPDX Software Bill of Materials is generated in CI.
- **Vulnerabilities:** `govulncheck` gates every merge (`scripts/check-security.sh`); `gosec`
  static analysis runs alongside it.
- **Dev process:** mapped to NIST **SSDF (SP 800-218)**; the local console is verified against
  **OWASP ASVS**.
