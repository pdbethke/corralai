# CorralAI Licensing Design

**Date:** 2026-06-30
**Status:** Approved (brainstorming)
**Author:** Peter Bethke

## Goal

Make CorralAI **source-available**: let developers read the entire codebase
and run it themselves, so they can verify it's legitimate and adopt it —
**without** allowing anyone to turn around and run a competing hosted
("CorralAI Cloud") business off it. Anyone who *does* want to offer it as a
service can buy a commercial license.

This is a deliberate move **off** the current `MIT` license. The repo is not
yet public, so this is a clean-slate relicensing — nothing has escaped under
MIT, and the new license is fully binding from the first public push.

## Decision summary

| Dimension | Decision |
|---|---|
| What devs may do | Read, modify, self-host, redistribute — for free |
| What devs may **not** do | Provide CorralAI to third parties as a hosted/managed service; circumvent license-key functionality; strip notices |
| Public license | **Elastic License v2** (`Elastic-2.0`) |
| Commercial path | Dual-license: sell a separate commercial license to anyone who wants the restricted use |
| Time conversion | **None** (no BSL-style change date / time-bomb) |
| Contributions | Accepted, gated by a **CLA** with a relicensing grant, enforced by a bot |

## Why Elastic License v2

The requirement set — "let people read and self-host, but stop a competing
managed service, and let me sell an exception" — is the canonical
source-available + open-core problem. Three licenses were weighed:

1. **Elastic License v2 (chosen).** One short, readable file. Grants use,
   modification, self-hosting, and redistribution with exactly three
   carve-outs; the load-bearing one is *"you may not provide the software to
   third parties as a hosted or managed service."* No copyleft, no time-bomb.
   Widely recognized (devs don't have to lawyer it), and Elastic themselves run
   the identical ELv2 + paid-commercial dual model — a proven template.

2. **BSL 1.1 (rejected).** More configurable via an "Additional Use Grant,"
   but *structurally requires* a change date on which it converts to open
   source. That time-bomb was explicitly not wanted. More knobs than the goal
   needs.

3. **SSPL (rejected).** Strongest anti-SaaS posture via aggressive
   service-copyleft, but it spooks exactly the developers we're trying to win
   over and is heavier than the goal requires.

**Accepted tradeoff:** ELv2 is *not* "open source" by the strict OSI
definition, so a purist may note that. The actual goal — devs can read the
whole thing and run it themselves to confirm it's legit — is fully delivered
regardless, the same trade Sentry, HashiCorp, and Elastic made without losing
credibility.

## Commercial (dual) license

The ELv2 hosted-service restriction is what makes a commercial license
*sellable*: a party that wants to run a CorralAI service buys an exception.

- **Now:** a "commercial licensing available — contact <maintainer>" pointer in
  `NOTICE` and `README.md`. No contract drafted yet.
- **Later (when someone bites):** draft the actual commercial agreement from a
  template or with a lawyer. Deferred deliberately — no value in drafting it
  speculatively.

## Contributions: CLA, not DCO

Dual-licensing requires controlling the copyright on **all** code that ships in
both the public and commercial builds. Therefore:

- **DCO is insufficient.** A `Signed-off-by` line only certifies a contributor
  had the right to submit under the existing license; it grants no relicensing
  rights.
- **A CLA is required.** Contributors retain their copyright but grant the
  maintainer a broad, perpetual license **including the right to
  sublicense/relicense**, so their contributions can flow into both the public
  ELv2 build and the commercial license.
- **Enforcement:** CLA Assistant (free GitHub App). Contributors check a box on
  their first PR — low friction, auditable. Set up *before* the first outside
  PR can land; merging an outside PR without it breaks the dual-license chain.

## Repo artifacts

All of the following land in a **single commit that precedes making the repo
public** — nothing escapes under MIT:

| Artifact | Change |
|---|---|
| `LICENSE` | Replace MIT text with the full Elastic License v2 text |
| Source files | Add SPDX header `// SPDX-License-Identifier: Elastic-2.0` |
| `NOTICE` | Copyright line + "commercial licensing available, contact …" |
| `CONTRIBUTING.md` | Explain the CLA and the CLA-Assistant bot |
| `README.md` | Replace the "Plain MIT, no riders" line and the License section with the ELv2 + commercial-option story; explicitly tell devs they may read and self-host, just not resell as a service |

## Sequencing

1. Land all repo artifacts above in one pre-public commit.
2. Stand up CLA Assistant before accepting any outside PR.
3. Make the repo public.
4. Draft the commercial agreement only when a real licensee appears.

## Out of scope

- The text of the commercial agreement itself (deferred until a licensee
  appears).
- Trademark policy for the "CorralAI" name (separate from copyright licensing;
  can be added later if name-based forks become a concern).
