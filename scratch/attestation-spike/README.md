<!-- SPIKE — throwaway exploration, not shipped code. -->
# Spike: an "AI accountability" package from a real corralai run

Every corralai run already records *what was built, by which model, and whether it
actually passed a real check*. This spike turns that into two coherent, standards-shaped
artifacts a security team can verify:

1. **A tamper-evident build ledger** (`*.ledger.json`) — every build step hash-linked to
   the previous one (like **git / Certificate Transparency / Sigstore Rekor**), with a
   signed head. Alter, insert, remove, or reorder *any* step and the head breaks.
2. **A provenance attestation** (`*.attestation.json`) — an **[in-toto Statement v1]**
   carrying an **[SLSA Provenance v1]** predicate. Its **subject digest is the ledger
   head**, so the "what was built" statement is bound to the tamper-evident "how it was
   built" record. One links to the other.

**Input:** `calc-frontier` — a real recorded run. Three frontier models (Claude Sonnet,
Gemini 2.5 Pro, GPT-5 Codex) built + tested a Go module, 13 minutes, 16 tasks, 19
findings, human-accepted. **Generate:** `python3 ledger.py <run.json>` and
`python3 gen.py <run.json> <run.meta.json> > attestation.json`.

## The tamper-evidence, demonstrated

`ledger.py` builds the chain, then rewrites one past step to make a **failed check look
like it passed** — the exact fraud an auditor fears — and re-verifies:

```
ledger: 186 build steps, hash-linked
signed head: 0320a3e2…   signature: 931995a0…
verify untampered  -> True
[tamper] rewrote seq 17 execution ok:true -> false
verify tampered    -> False  (altered entry at seq 17 (hash mismatch))
```

You cannot silently change what an agent did after the fact. That's the point.

## Why these formats (standards, not invention)

- **in-toto + SLSA Provenance** — the standard for "who/what/how produced this," with the
  materials that went into it. We name every **model as a material**. Slots into the
  software supply-chain security posture a CISO already runs (SLSA levels, DSSE signing).
- **Hash-linked, signed log** — the tamper-evident core of a transparency log. Same tech
  as git commits, Certificate Transparency, and Sigstore Rekor. *(Deliberately **not** a
  blockchain — no consensus, mining, or token; you want the tamper-evidence, not the
  decentralization.)*
- **[SARIF]** for findings and **[SPDX]/[CycloneDX]** for an SBOM slot in as additional
  predicates for full coverage.

## What makes it *accountability*, not just provenance

| Property | Evidence |
| --- | --- |
| **Verifiable** | `certification/execution`: the actual commands + exit codes — "11/11 recorded checks passed" — **not** a self-report. *A judge may not certify herself.* |
| **Answerable** | `accountability/human-gate`: output withheld until a human accepted, with timestamp. |
| **Attributable** | `accountability/attribution`: every task tied to the model + agent that produced it. |
| **Tamper-evident** | the hash-linked ledger + signed head (its digest is the attestation's subject). |

## To make it real (the delta, all bounded)

1. **Emit from the live ledger** (this spike reads an exported recording; production emits
   from the brain's DuckDB + queue at run-end).
2. **DSSE-sign** the head (Sigstore/cosign) instead of the demo HMAC.
3. **Optionally anchor the head in a public transparency log** (Sigstore Rekor) → even the
   operator can't have rewritten history, witnessed by a third party. *Still not a
   blockchain you run.*
4. **`corral-observe` plays the report** — extend the read-only runtime to open the package
   and replay it (distributed review / supervisor oversight / auditor evidence).

## Questions for a CISO (Bil)

- Does an **in-toto/SLSA + Rekor-style transparency log** land in your existing tooling, or
  is there a format you'd rather see?
- Which property carries the most weight for adopting agents at all — the **execution
  certification**, the **tamper-evident ledger**, the **human gate**, or the **attribution**?
- Is public **Rekor anchoring** table-stakes for your world, or is a DSSE-signed head enough?
- Which vertical/use-case is the sharpest wedge — **change-management evidence** (SOX / GxP /
  ATO), or something in Sardine's own risk/fraud world?

[in-toto Statement v1]: https://in-toto.io/Statement/v1/
[SLSA Provenance v1]: https://slsa.dev/provenance/v1
[SARIF]: https://sarifweb.azurewebsites.net/
[SPDX]: https://spdx.dev/
[CycloneDX]: https://cyclonedx.org/
