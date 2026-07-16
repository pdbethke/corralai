<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Pool reasoning trace + shared tests — "show the work" (design)

**Status:** design (2026-07-16). Precedes an implementation plan. Builds on the shipped adversarial pool + `corral certify --adversarial`.

## The thesis (why this exists)

**Transparency is the buy-in, and the buy-in is the product.** Corral's difference from Fugu is that Fugu makes its judgement inside a black box; corral **shows the work** — every fault it planted, which one your tests missed, the critic's actual argument, and the test that proves the gap is real. An opinionated developer can check the working and either accept the verdict or push back on it. A verdict you can't inspect is a verdict a dev won't trust; a proof is.

Two consequences shape the whole design:
1. **A pool run must be a reasoning TRACE, not a status board.** Every step carries its *evidence* verbatim (the real code/text), never a summary.
2. **The authored tests are a DELIVERABLE, not decoration.** The herd doesn't just grade the dev's tests — it hands back the test that catches the gap, ready to adopt. That "sharing tests back to the dev" is a first-class product pillar (the swarm structure's payoff), so the tests get durable, retrievable, record-anchored storage — they are output, not exhaust.

## The gap today (verified 2026-07-16)

A pool run's recording is empty/thin, because:
- The pool's evidence — mutants (`queue.Task.Result` of the mutant-generator), surviving mutants (`runState.devSurvivors`), the critic's argument (`queue.Finding.Evidence`), the authored killing test (`queue.Task.Result` of the test-writer), the signed verdict — **exists but is never surfaced anywhere a replay can see.** `BuildReplayStream` (`internal/brain/replay.go`) reads the queue but **drops `Task.Result` and `Finding.Evidence`** from the event detail.
- The pool's tracking mission is created `status='running'` and **never transitioned to terminal**, so it's excluded from `/api/history` → the export meta comes out `task_count=0/finding_count=0/duration=0` (`scripts/export-golden-run.sh` finds no matching mission).
- The pool driver emits **no** telemetry/replay events of its own (`internal/advpool/driver.go` has zero `rec(`/`tel.Record` calls); `report_thought` is a no-op because the tracking mission has `RecordStory=false`).

**Key architectural fact that keeps this lean:** most evidence is ALREADY persisted in the queue (task results + findings). We SURFACE it; we do not duplicate it. New storage is small and justified (the shared tests are product output).

## Architecture — three slices

### Slice 1 — Data + durable evidence (the recording becomes complete + honest)

**1a. Surface what's already stored (no new writes).** Thread into `BuildReplayStream`'s event detail:
- `task_created`/`task_done` detail gains the task's `Result` (the mutants; the authored test) — the source the trace and the story-modal need.
- `finding_reported` detail gains `Evidence` (+ `SuggestedAction`, `Target`) — the critic's actual argument, not just `{type, severity}`.

**1b. Transition the tracking mission status on convergence.** The pool runtime (`internal/brain/advpool.go` `AdvPoolRuntime.tick`, which already sees the terminal `Verdict`) sets the tracking mission's status to a terminal value (`certified` / `needs-review`, mapped from `Verdict.Status`) via `mission.Store`. This single change un-breaks the meta: the mission enters `MissionHistoryList` → `summarize()` computes `task_count`/`done`/`finding_count`/`duration` correctly, and the recording card gets a real verdict summary.

**1c. Emit the two evidence events the queue can't provide (small, once per run).** The pure `advpool.Driver` gains an OPTIONAL `EventSink` interface — mirroring its existing optional `Signer`/`Leaderboard` (nil ⇒ no-op; driver stays pure + testable). The brain wires it to telemetry keyed on the run's `missionID`:
- `pool_subject` (once, at run start) — the **inputs**: `RunSpec.Code` (code under review) + `RunSpec.DevTestCode` (the dev's tests being graded) + the goal. A few KB. This is the "here's the change and the tests guarding it" the trace opens on.
- `pool_verdict` (once, at convergence) — `{status, dev_kill_rate, mutants_total, survivors, proven_missed, models_by_role, record_id, record_head}` + the surviving-mutant IDs (references into the mutant-gen result, not re-stored source). Numbers + a few strings.

Also emit lightweight `pool_dev_adequacy` (kill-rate + survivor IDs) at scoring time so the trace has an ordered beat there; the survivor SOURCE is recovered from the mutant-gen `Task.Result` at render/export time (no duplication).

**1d. Durable, record-anchored run artifacts (the sharing substrate).** Store the **authored test(s)** and the **surviving-mutant evidence** as durable artifacts keyed to the signed record (`Verdict.RecordID`). Reuse the existing artifacts store pattern (`internal/artifacts`/`taskartifacts`, mirroring `buildstore.Open`) or a small `pool_artifacts` table; the artifact's digest is anchored by the already-signed record, so a shared test is tamper-evident (you can prove it's the one certified). Retrievable by record id. This is what makes the tests survive queue pruning and become shareable output.

**Bloat posture:** 1a/1b reuse existing storage (zero new writes). 1c adds ~a few KB of events per run. 1d durably stores the authored tests + survivors — justified because they are the deliverable, retention-managed like other product data, and the heavy long-term copy lives in the exported static recording (a scrubbed JSON committed to the site), not the live brain DB.

### Slice 2 — Share the tests back to the dev (advisory + mandatory)

Retrieve the record-anchored authored tests by record id and hand them to the dev. Two modes over the SAME artifact:
- **Advisory (this slice, CLI first):** `corral certify --adversarial` gains an output that writes/prints the authored test ("add this test — it catches the gap the pool proved your suite misses"), and/or a `corral certify tests <record>` retrieval verb. The dev may adopt it.
- **Mandatory (control-plane mode; design here, wiring is a later control-gate slice):** the **control owner (CISO) can make adoption mandatory and gated** — the pool's authored test becomes a required check, and the merge does not pass until that exact test is present in the suite. This is separation-of-duties (the CISO owns the gate, [[corralai-control-plane-positioning]]) applied to the herd's output: the herd proposes the test, the control owner mandates it, the gate enforces it. It is precisely why the artifact is **record-anchored and tamper-evident** — a *mandated* test must be provably the one corral certified (a dev can't quietly swap it). Reuses the existing control-gate substrate (`internal/controlgate`/`controlspec` vetted-test store + required-check pattern); the pool's authored test is staged as a candidate the owner promotes to a mandatory gate test. Full wiring is out of scope for this design's build; the record-anchored, verifiable-identity storage in Slice 1d is the enabling substrate.
Same artifacts as Slice 1d; this is the retrieval + presentation surface for both modes.

### Slice 3 — The render ("show the work")

Extend the replay-player's existing vocabulary (`internal/ui/web/replay-player.js`) rather than build a new view:
- The pool events + the surfaced `Task.Result`/`Finding.Evidence` become an ordered **console trace**: subject → mutants planted → dev suite's grade + the surviving mutant [code] → the critic's argument [text] → the killing test [code] → signed verdict. Each artifact is **inspectable** (rendered as readable code/text, never a summary).
- **Highlight where the violation occurred.** A mutant is a same-signature drop-in of the code under review, so the render **diffs the original (`pool_subject`'s `RunSpec.Code`) against the surviving mutant and highlights the changed lines** — the precise planted fault — with the framing "here is the exact fault, and your suite passes anyway." For the dev's test, the critic's `Finding.Target` names the specific test; highlight that test function and show the critic's argument beside it. (v1: highlight the flagged test + argument. A line-precise anchor inside the test — the exact missing assertion — requires the critic to emit a line reference; that's a follow-up, gated on the critic prompt returning a location.)
- A **signed-verdict panel** closes the trace: status, kill-rate, `models_by_role`, record id + "verify offline with `corral certify verify`."
- The task-story modal (click a task) shows that task's `Result` (the mutants / the authored test) — the source, not just the title.
- The hero/gallery card reads the verdict summary from the (now-populated) meta.

## Soundness / honesty invariants (unchanged, and reinforced)

- **Show the real evidence, never a summary.** The trace renders the verbatim mutants/tests/critique. If it can't show the source, it says so — it never paraphrases a verdict.
- **The shared test is tamper-anchored.** Its digest rides in the already-signed record, so a dev can verify the test corral handed them is the one it certified — transparency you can check, not trust.
- **Decorrelation / certify-by-execution / human-gate / off-by-default** all unchanged. This surfaces evidence the deterministic gate already produced; it adds no new judgement path.

## Non-goals (this design)
- PR-comment sharing (forge round-trip) — a later slice; Slice 2 is CLI retrieval.
- A bespoke pool-only replay UI — reuse the console + story-modal + a verdict panel.
- Changing the signed record's digest contents — the evidence is stored as record-*anchored* artifacts, not folded into the digest (which stays over the scored Verdict fields).
- Recording the full mutant/test source into the live telemetry event log — the source is reused from the queue / stored as artifacts; only small subject/verdict events go to telemetry.

## Testing posture
- Slice 1: unit — `BuildReplayStream` now carries `Task.Result`/`Finding.Evidence`; the mission-status transition on convergence (a fake mission store asserts terminal status + that `summarize` counts it); the `EventSink` emits `pool_subject`/`pool_verdict` with the right payload (fake sink); artifacts stored + retrievable by record id. Keep the pool/queue/certify tests green.
- Slice 2: CLI retrieval renders the authored test from a record; missing record → clean error.
- Slice 3: player unit/interaction over a canned pool replay stream → the trace beats + verdict panel render; a run with survivors shows the surviving mutant source; the story modal shows a task's Result.
- End-to-end: re-run the cross-vendor fence pool run, export it, confirm the recording has real counts + the evidence renders as a trace, and `corral certify --adversarial` can hand back the authored test.
