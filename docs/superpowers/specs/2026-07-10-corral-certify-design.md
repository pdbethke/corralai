<!-- SPDX-License-Identifier: Elastic-2.0 -->
# `corral certify` — design

**Date:** 2026-07-10
**Status:** approved (brainstorm) → spec for review
**One line:** Add one line to a build pipeline — `corral certify -- <check>` — and every
build, no matter who or what wrote the code, comes back with an independently-verified,
tamper-evident, recallable **accountability record**.

## Why (the wedge, and the government pitch)

Corral's value doesn't have to be "the way you build." It can be **"the way you account
for what got built."** This is the first wedge of that: corral as an accountability
*verifier* that plugs into an existing pipeline with near-zero adoption friction — no
workflow to abandon, one line in CI.

The sharpest buyer is government/regulated. Post-EO-14028, software producers must
**self-attest** to secure development (OMB M-22-18/M-23-16, NIST SSDF 800-218) —
a paperwork promise. `corral certify` replaces the promise with **execution-verified
attestation**: a signed, tamper-evident record that the check *actually ran and passed*,
in the SLSA provenance format the federal supply-chain guidance already speaks. Corral's
one-binary, embedded-DuckDB, air-gappable posture fits IL4/IL5 and DoD software factories
building agent-assisted pipelines now. (Same evidence lands for banking SOX change control
and pharma GxP/CSV.)

The crown jewel survives the pivot precisely because corral **runs the check itself** —
a judge may not certify herself. This is the *verifier*, not a passive telemetry sink.

## What exists to build on

- **Verify-by-execution** — the brain already runs a command and certifies on the real
  exit code (`internal/mission` verify gate). `corral certify` runs the command in the
  pipeline the same way, outside a mission.
- **The attestation + tamper-evident ledger** — prototyped tonight
  (`scratch/attestation-spike/`: in-toto Statement v1 + SLSA Provenance v1 predicate,
  subject = the head of a hash-linked signed build ledger). This spec productizes it.
- **The keystore** (`internal/creds`, shipped) — `CORRALAI_BRAIN_KEY` authenticates the
  CLI to the brain.
- **Telemetry store (DuckDB) + replay** — the record lands here, recallable/replayable.

## Design

### The command

```yaml
# any pipeline (GitHub Action, GitLab CI, Jenkins, a runner…)
- run: corral certify --brain "$CORRAL_BRAIN" -- go test ./... && go build ./...
```

Flags: `--brain`/`CORRAL_BRAIN` (required), `--produced-by "claude-opus,gemini"` (optional
— the models/tools that wrote the change, for provenance; also readable from a
`Corral-Produced-By:` git-trailer), `--out <file>` (optional — also write the signed
record as a local artifact), `--repo`/`--commit` (auto-detected from git; overridable).

### The flow (thin CLI, brain owns format + signing)

1. **CLI captures context** — repo URL, commit SHA, branch, the authenticated principal
   (from `CORRALAI_BRAIN_KEY`), timestamp, host/platform, `--produced-by`.
2. **CLI runs the check itself** (`os/exec`), capturing exit code, wall-clock duration, and
   a digest of combined stdout/stderr. *This is the certification.*
3. **CLI POSTs a raw build record** to the brain and receives the signed record back.
4. **The brain** builds the **tamper-evident ledger** (hash-linked steps) + the
   **in-toto/SLSA attestation** (subject digest = ledger head), **signs the head** with a
   brain-held key, and **stores it in DuckDB** — recallable and replayable. Signing lives
   *only* in the brain (matches corral's "hold the credential brain-side" invariant).
5. **CLI exits with the check's exit code** — CI still fails on a failed build; but the
   record is emitted **either way** (a failure is accountability too: "did not pass, here's
   the proof").

### New surfaces

- **`cmd/corral` `certify` subcommand** (`cmd/corral/certify.go`) — context capture, run,
  POST, exit-passthrough. Testable `runCertify(args, runner, poster) error`.
- **`internal/attest`** — productize the spike: `BuildLedger(steps) (ledger, head)` and
  `BuildAttestation(record, head) (statement)` (in-toto/SLSA), with `Verify`. Pure, tested.
- **Brain ingest** — `report_build` MCP tool (and/or `POST /api/build/certify`): accept a
  build record → build ledger + attestation → sign head → store → return the signed
  statement. New DuckDB table `build_records` (or telemetry events shaped for replay).
- **Recall** — `corral certify --get <id>` / a brain endpoint to fetch a stored record; the
  existing replay surfaces it (a certified build is a short "mission" of one gate).

### Signing (independently verifiable v1)

The persisted artifact MUST be verifiable by an independent party, not just at creation
time — otherwise "tamper-evident / independently-verified" is an overclaim (caught in the
whole-branch review). So v1 signs the **full canonical statement**, persists the ledger
steps, and **publishes the verifying key**:

- **The brain signs the full canonical statement** (Ed25519 over deterministic JSON of the
  in-toto/SLSA statement — a DSSE-style detached signature), NOT just the head. This binds
  the *whole* predicate: exit code, command, duration, and `produced_by`. Signing only the
  head left the predicate unsigned and editable.
- **The ledger steps are persisted** alongside `{statement, signature}`, so an independent
  party can run `VerifyLedger(steps, head)` against stored data and confirm the head honestly
  chains the build events — the tamper-detection primitive is runnable post-hoc, not only
  in-process.
- **The certify public key is published** (a `public_key` field on the `report_build`
  response + an unauthenticated `GET /api/certify/pubkey` HTTP route) so a third party can
  obtain the key to verify without brain credentials.
- **`VerifyStatement(canonical, sigHex, pub)` ships** as the verification primitive: verify
  the signature over the stored statement bytes, recompute the head from the stored steps,
  and confirm `statement.subject[0].digest.sha256 == head`. That is independent verification.
- The signing key lives only in the brain; the CLI never holds it. This is **central-trust**:
  a holder of the published key can verify the brain's own records honestly bind what ran.
- **Still deferred to the next spec (trustless tier):** external **witness anchoring** — ship
  the chain head to an append-only timestamped log (the shared MotherDuck warehouse / Sigstore
  **Rekor**) so tampering is detectable even if you *don't* trust the brain. Central-trust v1
  is honestly labeled as such; we do not claim trustless verification until witnessing ships.

## What it reuses vs. builds

**Reuses:** verify-by-execution semantics, the attestation/ledger format (prototype),
the keystore auth, the telemetry store + replay. **Builds (small):** the `certify`
subcommand, `internal/attest`, the brain ingest endpoint + `build_records` store, and the
Ed25519 head-signing.

## The vision this wedge rides on — the MotherDuck accountability warehouse

`corral certify` is the **writer**. The product it unlocks is the **accountability
warehouse for a distributed dev shop**: every dev, every CI runner, every project runs
`corral certify`, and the signed build records federate into one shared, queryable,
shareable warehouse.

Corral's telemetry, memory, reference, and recordings stores are **already DuckDB**
(`go-duckdb/v2`). So the build-record store is DuckDB-native too — and the *same schema*
federates to a shared **MotherDuck** warehouse by swapping the DSN (a local `.duckdb` path
→ an `md:` connection string). No second system; a config flip.

That flip is what makes "distributed dev shop" real: no per-shop server everyone VPNs into
— MotherDuck is the hub. The shop lead queries "what did my whole team ship this sprint,
and did it pass?"; a client gets a **read-only share** proving the whole vendor team's work;
compliance/gov query the lineage in SQL and replay any run. Because each record is a
**signed, tamper-evident ledger head**, the warehouse is cryptographic proof that
federates — not self-reported logs a vendor could doctor.

Honesty guardrail: we build the DuckDB-native store *now* (real); MotherDuck federation +
shares are the **next spec**, not claimed as shipped until the flip runs. Lead with it in
the roadmap and the story; claim it in the product only once it works.

## Out of scope (the vision layers on top of this wedge)

- **Offline mode** — emit the record as a CI artifact when the brain's unreachable (fast-follow).
- **MotherDuck federation + read-only shares** — the DSN flip, the shared warehouse, the
  cross-source rollup, the client/auditor share links. The next spec; the store is built
  DuckDB-native now so this is a config flip, not a rewrite.
- **The team dashboard / multi-project org rollout** — many devs, many apps, one warehouse.
- **Remote *presentation* mode** — the shareable, presenter-friendly playback for gov
  conferences / supervisor oversight.
- **DSSE/Sigstore/Rekor** signing upgrade.
- **Re-verifying an external artifact** the CLI didn't build (this wedge runs the check
  *as* the CI step; re-attesting a prebuilt artifact is later).

## Testing (TDD)

- `internal/attest`: ledger round-trip + tamper detection (alter a step → verify fails);
  attestation subject == ledger head; sign/verify with an Ed25519 keypair.
- `runCertify`: runs the command, passes through exit code (0 and non-0), builds the record
  with git context, POSTs it, writes `--out` when set — with a fake runner + fake poster.
- Brain ingest: accepts a record → returns a signed statement whose head matches; stores it;
  a tampered replay fails verification.

## Decisions (defaulted; revisit in review)

- **Thin CLI, brain signs + owns the format** (credential stays brain-side).
- **Exit-passthrough** — `corral certify` never masks a real build failure; it records it.
- **Ed25519 brain key** for v1; DSSE/Sigstore/Rekor later.
- **`--produced-by`** (flag or git-trailer) captures the models/tools as provenance
  materials — optional, since corral certifies the *result* regardless of what wrote it.
