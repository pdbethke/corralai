<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Own-supply-chain hardening — hold corralai to the standards it enforces, publicly

**Date:** 2026-07-10
**Status:** approved (brainstorm) → spec for review
**One line:** An accountability tool must have an *exemplary, publicly-verifiable* supply chain of
its own. Adopt the recognized standards (SLSA · Sigstore · TUF · NIST SSDF · OpenSSF · OWASP ASVS ·
ISO 29147) for corralai's own releases and **publish the evidence** — the ultimate dogfood and the
strongest credibility signal for the pivot.

## Why

"We help you prove your builds" is hollow if our own build isn't provable. The pivot's whole claim
is execution-verified, publicly-witnessed provenance — so corralai's *own* releases should be
SLSA-built, Sigstore-signed, Rekor-witnessed, TUF-updated, OpenSSF-scored, SSDF-attested, and
ASVS-verified, **with the artifacts public.** Bonus: our validating CISO sits on the **OWASP
committee**, so the ASVS conformance must be concrete (real requirement IDs), not aspirational.

## Scope: two tiers

### Tier 1 — quick wins (each a public, checkable accountability artifact; ~a day each)
- **`SECURITY.md` + coordinated disclosure** *(ISO/IEC 29147 disclosure + 30111 handling)* — a
  security contact, a disclosure policy, response SLAs, a safe-harbor statement. Request a **CVE**
  Numbering Authority scope or at least a reporting path. *Deliverable: `SECURITY.md` in the repo.*
- **OpenSSF Scorecard** — add the `ossf/scorecard` GitHub Action; it scores branch protection,
  signed releases, pinned deps, CI hardening, token perms, etc., and emits a **public badge**.
  *Deliverable: the workflow + the badge in the README; fix the low-scoring checks it surfaces.*
- **OpenSSF Best Practices Badge** *(bestpractices.dev)* — complete the checklist, earn + display the
  badge. *Deliverable: the badge + the passing criteria.*
- **SBOM in CI** *(SPDX / CycloneDX)* — generate an SBOM per release (e.g. `syft`) and attach it to
  the release + publish it. *Deliverable: SBOM artifact on each release.*
- **Pin + verify dependencies** — pin Go modules (already via go.sum) and CI actions to digests;
  keep `govulncheck` green (already in `scripts/check-security.sh`). *Deliverable: digest-pinned CI.*

### Tier 2 — the release-signing & provenance arc (the hardening)
- **Sigstore-signed releases (keyless)** — sign the release binaries + the console bundle with
  `cosign` keyless (Fulcio/OIDC → ephemeral cert) and anchor to **Rekor** — no long-lived key to
  steal; every signing publicly logged. *This is the same machinery `corral certify` sells;* the
  release pipeline should literally `corral certify` its own build (execution-verified provenance).
- **SLSA L3 provenance** — hermetic, isolated builds (e.g. GitHub Actions + `slsa-github-generator`)
  producing signed SLSA provenance; **reproducible builds** so anyone can rebuild and confirm the
  artifact hash. Publish the provenance. *Answers the SolarWinds threat: a compromised pipeline is
  detectable because the build is reproducible + the signing is logged.*
- **TUF for client/bundle distribution** — a TUF trust root over the client + the console bundle:
  **key rotation/revocation + rollback protection** (the two gaps in the daemon/client spec's v1).
  This is what lets the pinned-key model in the daemon/client spec grow up.
- **NIST SSDF (SP 800-218) attestation** — map the dev process to SSDF and produce the attestation
  **execution-verified** (dogfood), not a self-signed PDF.

### The console, verified to OWASP ASVS (for the committee reviewer)
The local console (daemon/client spec §4d) is verified against **ASVS** with concrete requirements:
- **Session management (ASVS V3)** — the per-session secret binding the browser to the console;
  no ambient-authority proxy.
- **Access control (ASVS V4)** — read-only `corral-observe` enforced server-side at the client;
  token scoping (defense in depth).
- **API & web-service / CSRF (ASVS V13 / V50)** — `Origin`/`Referer` allowlist + the anti-CSRF
  per-session token on every proxied `/api` request; the injected bearer never rideable from a
  drive-by origin.
- **Cryptography (ASVS V6)** — Ed25519/DSSE, keys never logged, TLS-verified transport.
- **Files/resources (ASVS V12)** — bundle path traversal rejected, size cap, `0700` cache, serve-time
  hash re-check.
*Deliverable: an `docs/security/asvs-console.md` mapping each control to its ASVS requirement ID,
ready for an OWASP-committee review (and a coordinated ASVS review with Bil).*

## What "publish the evidence" means (the accountability payoff)
A `SECURITY` / trust page (repo + `corralai.dev`) linking, all live: the OpenSSF Scorecard badge, the
Best-Practices badge, the latest **SLSA provenance + Rekor entry** for the release, the **SBOM**, the
**SSDF attestation**, and the **ASVS console mapping**. "Here is the standard we meet, and here is the
public, machine-checkable proof" — for our own product, held to our own thesis.

## Out of scope (this spec)
- The daemon/client refactor itself (its own spec) — this hardens *how corralai is built and
  distributed*, not the runtime architecture.
- Third-party pen-test / formal audit engagement (a later, funded step; ASVS + Scorecard are the
  self-serve floor).
- FedRAMP/CMMC formal authorization (a GTM/compliance track, not this hardening).

## Sequencing
Tier 1 first (each is independently shippable and immediately public-facing — `SECURITY.md`, then
Scorecard, then SBOM, then the Best-Practices badge). Tier 2 is the release-pipeline arc, best done
alongside (and validating) the daemon/client bundle-signing work. The ASVS console mapping tracks the
daemon/client §4d implementation.

## Decisions (defaulted; revisit in review)
- **Publish everything** — the accountability signal *is* the public artifact; a private security
  posture defeats the point.
- **Dogfood the signing** — the release pipeline uses `corral certify` + Sigstore/Rekor on its own
  build, so corralai is its own first customer.
- **ASVS mapping is concrete** (requirement IDs), ready for a committee-grade review.
- **Tier 1 quick wins ship first** — each is a same-day, public, checkable artifact.
