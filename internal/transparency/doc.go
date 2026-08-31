// SPDX-License-Identifier: Elastic-2.0

// Package transparency talks to Sigstore Rekor (a public, append-only
// transparency log) on behalf of TWO independent corral features. They
// share nothing but the vendor and the general idea ("put a receipt into a
// public log"), and are deliberately NOT unified into one abstraction — see
// each type's own doc for why.
//
//   - Witness / Entry / NewRekorWitness / NewFakeWitness (witness.go,
//     rekor.go) anchor a `corral certify` BUILD RECORD's DSSE envelope, and
//     verify its inclusion proof fully OFFLINE against the Sigstore
//     TUF-rooted trust root. This is the brain's own accountability chain
//     (internal/brain, Options.Witness, wired in cmd/corral/main.go) — a
//     build attestation is anchored the moment it is signed, and a later
//     `corral certify verify` re-checks the inclusion proof without
//     touching the network again. Entry carries the full inclusion-proof
//     material a verifier needs (LogIndex, LogID, IntegratedTime,
//     InclusionProof, SET, Body).
//
//   - Logger / LogEntry / NewRekor / FakeLogger (logger.go) upload
//     `corral certify --repo --attest`'s signed statement — an AUDIT
//     verdict, a different artifact from a build record — when the
//     operator opts in with `--transparency`. It is a one-way upload, not
//     an anchor-then-verify pair: no offline inclusion-proof checking lives
//     here (a future `corral verify` task adds a read path). LogEntry is
//     the two-column receipt the local ledger and warehouse actually store
//     (rekor_log_index, rekor_uuid) — deliberately a separate, smaller type
//     from Entry above, both because their fields differ and because the
//     name `Entry` was already taken by the Witness path when this one was
//     added.
//
// Both wrap the same underlying Rekor client packages
// (github.com/sigstore/rekor/pkg/...) for the low-level submit/build-entry
// mechanics, but each owns its own client construction, its own flag, and
// its own ledger columns. A change to one must never be assumed to apply to
// the other.
package transparency
