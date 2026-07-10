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

### Signing (honest MVP)

- **v1: the brain signs** the ledger head with a brain-held key (Ed25519). Central trust,
  fits the server model. The CLI never holds a signing key.
- **Hardening (out of scope):** DSSE envelope + Sigstore/cosign, and public **Rekor
  anchoring** so not even the operator can rewrite history.

## What it reuses vs. builds

**Reuses:** verify-by-execution semantics, the attestation/ledger format (prototype),
the keystore auth, the telemetry store + replay. **Builds (small):** the `certify`
subcommand, `internal/attest`, the brain ingest endpoint + `build_records` store, and the
Ed25519 head-signing.

## Out of scope (the vision layers on top of this wedge)

- **Offline mode** — emit the record as a CI artifact when the brain's unreachable (fast-follow).
- **The team dashboard / multi-project org rollout** — many devs, many apps, one server.
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
