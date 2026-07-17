<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Eval harness — `corral eval` + a versioned, known-adequacy corpus (design)

**Status:** design (2026-07-17). Precedes an implementation plan. Builds on the shipped bug-catching scorecard (`internal/bugcatch`, the pool `BugCatchSink`, `/api/bugcatch`, `corral scorecard`) and `corral certify --adversarial` (`cmd/corral/certify_adversarial.go`).

## The thesis (why this exists)

The bug-catching scorecard is *correct but empty* — a cell needs `runs ≥ 3` to stop being provisional, and a *useful* recall/precision number needs far more. Volume is the product; volume is also frontier-inference cost. The founder will not underwrite thousands of runs, and — more importantly — a number generated on one person's machine is not evidence anyone should trust.

So the harness is designed from the start for **distributed, donated agentic time whose data is verifiable, not trusted** — corral's own thesis (record-anchored, re-executable, no self-report) turned on the eval data itself. **v1 ships the single-machine harness + a corpus that lets us prove the metric is sound**, so that when the LinkedIn article generates interest and invites donated runs, the thing being fed is already validated. Federation and donation-verification are designed-for here but shipped as later slices.

Two consequences shape v1:
1. **The corpus is the soundness foundation.** It is versioned and content-addressed so runs from different contributors and times are *comparable*, and it includes targets with **known adequacy** — some dev tests deliberately thorough, some deliberately gappy — so the scorecard can be checked against ground truth: the pool should find the gaps we planted and not invent ones we didn't.
2. **v1 proves soundness on one machine before inviting donations.** No federation, no donor trust model in the running code yet — but the observation the harness produces is already the signed, record-anchored row those later layers consume.

## What already exists (verified 2026-07-17)

- `corral certify --adversarial --code <path> --test <path> --goal <text> -- <cmd>` triggers ONE pool run against a (code, test) pair and polls to a signed verdict (`runCertifyAdversarial`, `cmd/corral/certify_adversarial.go`); `mcpAdvClient.StartRun`/`RunStatus` dial the brain over MCP with a bearer.
- Every converged pool run already feeds the bugcatch scorecard (the `BugCatchSink` on the driver → `internal/bugcatch` store), keyed by `(model, role)`, anchored to the signed record.
- The verdict carries `dev_kill_rate`, `survivors`, `proven_missed`, `models_by_role` — the harness reads these per run to validate adequacy.

**Key fact that keeps v1 lean:** the harness is "`certify --adversarial` in a loop over a corpus." It reuses the existing pool-trigger client and the existing scorecard feed — it adds the corpus, the loop, resumability, and the soundness report. It does NOT touch the scorecard store schema.

## v1 architecture

### 1. The corpus (`eval/corpus/`) — versioned, content-addressed, known-adequacy
A committed, in-repo corpus so results are comparable and the corpus itself is auditable.

- `eval/corpus/manifest.json` — a versioned manifest:
  ```json
  {
    "corpus_version": "2026-07-17.1",
    "targets": [
      {
        "id": "fence-neutralization",
        "code_path": "internal/fence/fence.go",
        "test_path": "internal/fence/fence_test.go",
        "goal": "Untrusted content cannot forge or close the fence …",
        "test_cmd": "go test ./internal/fence/...",
        "expected_adequacy": "thorough",
        "notes": "real production code; its suite is strong — expect high kill-rate, few survivors."
      },
      {
        "id": "passwd-gappy",
        "code_path": "eval/corpus/passwd/passwd.go",
        "test_path": "eval/corpus/passwd/passwd_gappy_test.go",
        "goal": "A password is valid iff length ≥ 12 AND it contains an upper, a lower, a digit, and a symbol.",
        "test_cmd": "go test ./eval/corpus/passwd/... -run Gappy",
        "expected_adequacy": "gappy",
        "known_gap": "the suite only checks length; a mutant that drops the character-class checks SURVIVES — a good test-writer should catch it.",
        "expected_survivors": ">=1"
      }
    ]
  }
  ```
- **Corpus targets** are self-contained Go under the corralai module (so the jail can `go test` them; the pool mutates a jail copy, never the real files). Each `known-adequacy` target ships the same `code.go` with **two test files** — a `_thorough_test.go` (should kill every mutant) and a `_gappy_test.go` (misses a specific behavior) — referenced as separate manifest targets. Start with ~4–6 targets: `fence` (real, thorough), and 2–3 hand-crafted units (e.g. `passwd`, a small `interval`/range check, a `parse` helper) each with a thorough + a gappy variant.
- **Content-addressing:** the harness computes a digest of each target's `(code, test, goal, test_cmd)` and records it (`target_digest`) with the run, so a donated observation later can be tied to the exact target version it ran. In v1 this rides in the run's `Repo`/`Commit`-adjacent metadata (see §Provenance); it does not alter the bugcatch schema.

### 2. The harness (`corral eval`) — `cmd/corral/eval.go` + `internal/eval`
```
corral eval [--corpus eval/corpus/manifest.json] [--iterations N]
            [--brain URL] [--only <target-id,…>] [--timeout D]
```
- For each `(target × iteration)` not already completed: build the `advStartSpec` from the target (reuse `certify_adversarial`'s spec construction), `StartRun` on the brain, poll to convergence, collect the verdict. Each converged run feeds the scorecard through the existing sink; the harness additionally records the per-run verdict for the soundness report.
- **Resumable + idempotent.** A small local progress file (`eval/.eval-progress.json`, git-ignored) records completed `(corpus_version, target_id, iteration)` so an interrupted or repeated invocation *tops up* rather than restarting. Re-running `corral eval --iterations 20` after 12 completed runs 8 more. (The scorecard store is append-only, so extra runs never corrupt it; the progress file just avoids redundant spend.)
- **Cost-aware.** Prints a plan up front (`M targets × N iterations = K pool runs to trigger`) and a running counter; `--only`/`--iterations` bound the spend. The harness assumes a brain + a running herd (like `certify`); it is a driver, not a worker.
- **Requires** a reachable brain (`CORRAL_BRAIN`) + bearer (same token path `certify` uses) + a running herd to execute pool tasks — documented, and it fails with one clear line if the brain/herd isn't there (no error spam).

### 3. The soundness report — the whole point of v1
After a run (or via `corral eval report`), print, per target, aggregated over its iterations:
- mean `dev_kill_rate`, mean `survivors`, mean `proven_missed`, iterations.
- **The ground-truth check:** does each target behave as its `expected_adequacy` says? A `thorough` target should show high kill-rate / ~0 survivors; a `gappy` target should show `survivors ≥ expected_survivors`. Flag any target that violates its expectation LOUDLY — a gappy target with 0 survivors means the pool failed to find a gap we KNOW is there (the metric is under-sensitive), and a thorough target with many survivors means the pool is inventing gaps (over-sensitive). This report is the "the metric is sound" evidence — and, honestly surfaced, the article's proof.
- The per-model recall/precision comes from `corral scorecard` (the scorecard the runs fed); the eval report cross-links to it.

## Provenance seam (designed for donation — NOT built in v1)
Everything a later federation/verification layer needs is produced now:
- Each run is already a **signed, record-anchored** bugcatch observation (`record_id`/`record_head`).
- The harness stamps the corpus identity onto the run so the observation is attributable to an exact target version: v1 carries `corpus_version` + `target_id` + `target_digest` in the run's `Repo`/`Commit` metadata fields (e.g. `Repo="eval:<corpus_version>"`, `Commit="<target_id>@<target_digest>"`) — no scorecard-schema change; a later federation slice may promote these to first-class columns.
- **The donated-time trust model (design, not code):** a contributor runs `corral eval` on their brain + keys; observations federate to a shared MotherDuck scorecard (the `CORRALAI_BUGCATCH_DB` → `md:` DSN flip). Trust is by **re-execution** — the signed record carries the exact inputs, so a verifier re-runs a sample and rejects any that don't reproduce the verdict — plus the brain's Ed25519 identity (`internal/attest`) attests *who* donated. The founder does not underwrite the volume; contributors do, and the numbers are checkable, not trusted.

## Soundness invariants
- **The corpus is versioned + content-addressed.** A result is only comparable to another from the same `corpus_version` + `target_digest`. The report never pools across corpus versions silently.
- **Known-adequacy targets are the calibration.** If the report shows a gappy target with no survivors or a thorough target riddled with them, the metric is NOT sound and must not be published — the harness says so loudly rather than printing a confident number.
- **The eval adds NO new judgement path.** It only triggers existing pool runs; every catch is still `proven_missed` (execution-proven), fed through the sink we already reviewed.
- **Cost is bounded + visible.** The harness never runs unbounded; it states the plan and honors `--iterations`/`--only`.

## Non-goals (this design)
- **MotherDuck federation** (the DSN flip live) — a later slice; v1 feeds the local store.
- **Donated-time verification** (the re-execution acceptance pipeline + donor attestation) — designed here, built later.
- **External OSS repos in the corpus** — v1 is our packages + a hand-crafted known-adequacy set; external repos (cloning, licenses, per-repo commands) are a later corpus expansion.
- **A UI for the eval report** — CLI (+ the scorecard endpoint) only.
- **Routing off the results / changing the leaderboard.**
- **Determinism of a single run** — the pool is intentionally stochastic (LLM mutants); soundness is statistical, over iterations, which is exactly why volume matters.

## Testing posture
- **Corpus loader** (`internal/eval`): parses the manifest, computes stable `target_digest`s, resolves each target's code/test files; a malformed manifest / missing file → clean error.
- **The loop**: over a FAKE pool client (mirroring `certify_adversarial_test.go`'s `fakeAdvClient`), `corral eval --iterations 2 --only X` triggers exactly 2 runs for X and records progress; a second invocation with the progress file present triggers 0 (resumability); `--only`/`--iterations` bound the count.
- **The soundness report**: given canned per-target verdicts, a gappy target with 0 survivors and a thorough target with many survivors are both flagged; a well-behaved corpus reports "calibrated."
- **Progress file**: interrupted mid-run (partial progress) resumes correctly; corrupt/absent progress file is handled (start fresh).
- End-to-end (manual, documented): run `corral eval --iterations 3` against the prod brain + herd; confirm the scorecard fills and the soundness report calibrates on the known-adequacy targets.

## Open questions for review
- **Corpus size for v1:** enough targets to be credible without a huge authoring cost — proposed ~4–6 (fence + 2–3 hand-crafted units × thorough/gappy). More is better for the article but each is hand-authored. Is ~6 the right v1 bar?
- **Where the per-run verdict record for the soundness report lives:** re-query `get_adversarial_run` per run (simplest, already available), vs. a small local eval-results file. Proposed: collect from the poll result the harness already has in hand — no new store.
