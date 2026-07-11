<!-- SPDX-License-Identifier: Elastic-2.0 -->
# CISO-Owned Test Gates — design

**Status:** design — for review before decomposition into per-phase implementation plans.
**Decision context:** [[corralai-control-plane-positioning]]. Corral is the org-owned control
plane. This closes the load-bearing gap in it: the repo gate proves a check *ran and passed*,
but not that the check is any *good* — and the person who writes the tests is usually the person
who writes the code. This makes the **CISO** the author of the bar, keeps the **human eye** on
every meaningful decision, and seeds the whole thing from **open standards (OWASP ASVS, …)**.

---

## 1. The problem (why "the gate passed" isn't enough)

`corral certify` / the repo gate certify **by execution**: the check ran, exit 0, signed, bound to
the commit. But:

- **The gate is only as good as the check it runs.** A hollow suite (`func TestIt(t){}`) exits 0
  and gets a *signed* green — worse than plain CI, because the signature lends it authority.
- **Whoever writes the tests decides what "passing" means.** The code author writing their own
  tests is self-certification one level below the gate — the exact thing "a judge may not certify
  herself" was meant to stop. We closed the front door (code review) and left the test-authorship
  window open.

So a CISO can't trust a green, because underneath it they're trusting the developer's test
discipline — the one thing they can't see.

## 2. The thesis

Separate the **standard** from the **code**, and the **test author** from the **code author**:

1. The **CISO owns durable test *goals*** — intent ("no PII reaches a log", "every endpoint
   enforces authz", "money math uses decimals") — held **brain-side and dev-untouchable**, seeded
   from **open-standard bundles** (OWASP ASVS first).
2. Corral binds each goal to the **current** code per-commit, reading the shape from a
   **deterministic parser** (tree-sitter), so the control never goes stale and never depends on the
   dev's tests.
3. An **independent test-writer role** (a capable model) drafts the executable tests; the drafts are
   **proven to bite** (seeded-violation) and (tier-2) **vetted for faithfulness + overreach** by an
   independent reviewer.
4. **A human — the CISO — approves the tests as code** before they can gate, reusing corral's
   existing memory-vetting cycle (unvetted → promoted). This is the meaningful gate; the AI is
   decision support, never a replacement.
5. The cheap, deterministic **tester** runs the *approved* tests per-commit via the **shipped repo
   gate**, which signs the verdict and posts a **distinct required check** the org's branch
   protection enforces.

Dev tests keep existing — dev-owned, for velocity — but are **never the gate**. The CISO gate is a
separate required check running only CISO-vetted tests, so lousy dev tests can't weaken it.

## 3. Global constraints (invariants — bind every phase)

1. **A human eye at every *meaningful* gate.** Meaningful = the CISO promoting a test into gating
   authority, adopting a new standard version, and a human approving the merge. AI roles
   (writer/reviewer/seeded-violation) are decision *support* — they make the human's yes/no fast and
   informed; they never replace it. The per-commit *run* of already-approved tests is mechanical —
   no human there.
2. **Fail-closed / no self-report** (inherited from the repo gate): a `pass` only from a real jail
   exit 0; any internal error → fail, never pass.
3. **Execution-certified vs. attributed — the honest seam.** Executable requirements → generated
   tests + orchestrated existing scanners. Architectural/manual requirements → *attributed and
   tamper-evident, but NOT execution-certified*. Never label an attested item "tested".
4. **CISO goals are dev-untouchable** — brain-side, versioned, separate from the repo the dev
   controls. If the standard lived in the dev's repo, the dev could weaken it.
5. **Deterministic shape from a parser, not a model.** Signatures come from tree-sitter (a fact),
   never an LLM's reading (a guess). The LLM's job is intent→test-logic, bound to parsed facts.
6. **Reuse, don't reinvent.** Memory-vetting cycle for approval; `internal/repoindex` tree-sitter for
   shape; the shipped repo gate for enforcement; existing scanners (semgrep OWASP rules, CIS-CAT,
   dependency-check) for the static subset.
7. SPDX Elastic-2.0; TDD; corral metaphor.

## 4. Components (each: purpose · interface · dependency)

### 4a. Signature-surface extractor  *(extends `internal/repoindex`)*
- **Does:** walk the gated commit's tree-sitter parse and emit the callable surface (functions,
  methods/receivers, params + types, exported symbols) per file/language — the map the writer binds
  tests against.
- **Delta from today:** `repoindex.chunkSymbols` yields symbol *boundaries* for retrieval; this
  extends the same tree-sitter layer to *signature detail*. Reuse the 13 wired grammars.
- **Depends on:** tree-sitter (present). Deterministic; no LLM.

### 4b. Control-spec store  *(new; brain-side, dev-untouchable)*
- **Does:** hold each CISO's **goals** — durable, declarative intent, versioned, scoped to the repos
  the CISO covers. Never in the dev's repo.
- **Interface:** author/list/version goals; a goal = {id, source-standard+version (optional),
  intent-text, level/severity, executable|attested}. CLI/API in v1 (the "daemon configured by
  CLI/config" directive); the CISO GUI window (component 4h) is deferred past v1.
- **Depends on:** the brain's store + auth (CISO principal).

### 4c. Standard bundles + versioning  *(new; the "starter library")*
- **Does:** ship open-standard catalogs as importable goal-sets (OWASP **ASVS** L1/L2/L3 first;
  then MASVS/SCVS, CIS, SLSA/SSDF). A bundle ships the **goals** (portable, code-shape-independent),
  NOT pre-baked tests.
- **Versioning:** each bundle is pinned to a standard version. A new upstream version is ingested,
  **diffed** against the adopted version (new/changed/removed), and surfaced to the CISO to
  **adopt** (a meaningful gate → human eye). Only the delta re-authors. A raised bar gets a
  human-set **effective date / grace window** — never a surprise mass-block.
- **Honesty:** bundle currency is real maintenance; mitigated by machine-readable formats (ASVS
  JSON/CSV, CIS-CAT) for semi-automated delta ingestion + community/OWASP-blessed maintenance.
- **Depends on:** 4b.

### 4d. The roles  *(orchestrator-staffed off the earned leaderboard unless the CISO pins a seat)*
- **test-writer** (capable model): drafts executable tests from a goal + the 4a signature map.
- **test-reviewer** (independent model, **tier-2**): vets *faithfulness* to intent and — critically
  — *overreach* (a false-red on compliant code trains devs to route around the gate; worse than a
  hole).
- **tester** (cheap, deterministic): runs the *approved* tests. This is the shipped repo-gate runner.
- **Depends on:** the existing role→model routing / leaderboard; role-pinning override for the CISO.

### 4e. Seeded-violation validator  *(new; the "prove it bites" guard)*
- **Does:** plant a known violation of the goal into a throwaway copy of the code and confirm the
  drafted test goes **red**. A test that can't catch a planted violation is rejected before it can
  ever gate. Certify-by-execution, turned on the test itself.
- **Depends on:** the bwrap jail (untrusted-code containment, reuse the repo gate's jail path).

### 4f. Human approval cycle  *(reuse `internal/memory` vetting VERBATIM)*
- **Does:** a drafted+validated test is `unvetted` and **cannot gate** until the **CISO views it as
  code and promotes it** — exactly `shared=false` → `SetShared`; only vetted is authoritative. The
  CISO sees the seeded-violation + reviewer **evidence** alongside the test, so approval is a fast,
  informed yes/no at the intent level.
- **Depends on:** the memory store's shared/promote machinery; the CISO principal.

### 4g. The CISO gate dimension  *(rides the shipped repo gate)*
- **Does:** a **distinct required check context** (e.g. `corral/ciso-gate`) that runs only the
  CISO-vetted tests in the jail and posts the signed verdict. Branch protection requires *this* one —
  so the SoD weight is unmistakably the CISO's, separate from any dev-tests check.
- **Depends on:** the shipped repo gate (poll → jail → sign → post status); `internal/gate`.

### 4h. CISO interface  *(a thin-client window; deferred past v1)*
- **Does:** author goals (adopt a bundle, edit intent), review generated tests **as code** with the
  bite/faithfulness evidence, approve/reject, see the per-commit gate history and the
  standard-version audit trail.
- **Depends on:** the daemon/client architecture (shipped); 4b/4c/4f/4g.

## 5. Data flow

**Authoring tier — rare, expensive, human-gated:**
```
adopt bundle / edit goal  →  4a extract current signature surface
  →  test-writer drafts test  →  (tier-2) reviewer vets faithfulness+overreach
  →  seeded-violation proves it bites  →  CISO reviews AS CODE + evidence → promotes (vetted)
  →  pin (goal-version × code-shape fingerprint)
```
**Running tier — per-commit, cheap, deterministic:**
```
PR head SHA (repo gate)  →  tester runs the pinned VETTED tests in the jail
  →  sign verdict  →  post `corral/ciso-gate = pass|fail`  →  branch protection blocks/allows merge
```
**Drift — one flow, three sources:** code drift (signatures move) OR CISO edits intent OR a standard
version is adopted → **re-author only the delta** → back through the authoring tier (human-approved).

## 6. Two regimes (never conflated)
- **Dev tests:** dev-authored, dev-run, for velocity. Encouraged. **Never the gate.**
- **CISO gate:** independent, CISO-vetted, a **distinct required check**. Its assurance never depends
  on the dev's suite, so "what if the dev writes lousy tests" cannot weaken it.

## 7. The accountability payoff (falls out of the ledger)
Because every verdict is signed + recorded, the ledger shows **which standard version gated which
commit, and when the org moved** — "compliant with ASVS 4.0 through March, adopted 5.0 April 1," with
signed per-commit evidence. Separation-of-duties across the SDLC, mechanized, and provable to an
auditor against a standard they already recognize (ASVS / SSDF / PCI / CIS).

## 8. Error handling & honest edges
- Fail-closed inherited from the repo gate.
- **Overreach** (false-red) is a first-class reviewer target, not an afterthought.
- A generated test that can't pass seeded-violation is **rejected**, not shipped (loud, not silent).
- The CISO never sees a test gate without having approved it (the human-eye invariant); a
  not-yet-approved goal is *reported as uncovered*, never silently skipped.
- Standard-raise transitions carry a human-set grace window.

## 9. Phasing (decomposition — each phase is its own implementation plan)

This design spans several subsystems; it is **not** one plan. Proposed order, thinnest-vertical-slice
first so the thesis is provable end-to-end early:

- **Phase 1 (v1 — the provable slice):** 4a signature extractor + 4b control-spec store (CLI/API) +
  a **small OWASP ASVS L1 starter subset** (4c, no versioning-delta yet) + 4d **test-writer only** +
  4e seeded-violation + 4f **CISO approve-as-code** (reuse memory vetting) + 4g CISO gate dimension on
  the repo gate. One repo, CLI-driven approval. Proves: CISO goal → per-repo generated test →
  human-approved → gates a PR independently of the dev.
- **Phase 2:** the independent **test-reviewer** seat (faithfulness + overreach) + model-pinning.
- **Phase 3:** **standard-version bundles + delta-adoption** (4c full) + the standard-version audit
  trail (§7) + grace windows.
- **Phase 4:** the **CISO interface** window (4h) + multi-repo fan-out of a goal-set.
- **Later:** more bundles (MASVS/SCVS/CIS/SLSA/SSDF; paid MISRA path), scanner orchestration
  (semgrep/CIS-CAT/dependency-check) for the executable-static subset, WCAG/license adjacent bundles.

## 10. Testing approach
TDD throughout. Load-bearing test targets: the signature extractor is deterministic across the wired
languages; the seeded-violation validator itself is tested (a hollow test is *rejected*, a biting
test *accepted*); the human-gate states (unvetted test cannot gate; only promoted gates); the CISO
gate posts its own context and fails closed; overreach is caught (a test that reds compliant code is
rejected). The generation step (writer) is validated by execution (seeded-violation), not by
asserting on LLM output.

## 11. Out of scope
- Replacing dev tests (they coexist).
- Auto-adopting standard changes without a human (violates the invariant).
- Certifying architectural/manual requirements as "tested" (they are attested, not executed).
- A universal pre-baked test per requirement (can't know the code shape — the drift point).
