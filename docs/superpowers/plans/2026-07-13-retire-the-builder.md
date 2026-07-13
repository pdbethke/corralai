<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Retire the builder — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. This is the *first slice* of the re-focus in `docs/superpowers/specs/2026-07-13-corral-refocus-audit-not-builder-design.md` — **remove build-from-directive.** It is a demolition, not the re-point; the adversarial-verification flow + `corral certify <change>` CLI are later slices.

**Goal:** Remove "build software from a freeform directive" from corral, leaving the **gate + certify + staffing-as-a-library + the mission shell/findings-gate** standing and green. After this slice: the engine no longer builds/commits/pushes/opens PRs; `create_mission` no longer accepts a build directive; the control gate is the primary surface; `StaffingManager` survives detached and re-pointed to verifier roles.

**Architecture:** The gate (`internal/gate`, `internal/controlgate`, `internal/brain/gate.go`+`controlgate.go`+`controltools.go`) and the certify chain (`certify`/`certverify`/`transparency`/`attest`/`buildstore`) are **already package-independent of `internal/mission`** (verified: no cross-imports; the gate never calls `Tick`/`certifyBuild` is called only by `report_build` + the gate adapters). So this is mostly **deletion inside `internal/mission` + the build entry points**, plus **extracting `StaffingManager` from the engine** so it survives. The gate is untouched.

**Tech Stack:** Go 1.26.5, module `github.com/pdbethke/corralai`.

## Global Constraints
- SPDX on any new file; per commit `export PATH="$PATH:$HOME/go/bin"` then `go vet ./...` + `go build ./...` + `go test ./...` + `bash scripts/check-security.sh` — all green.
- **Leaf-first deletion order** so every commit builds. Delete a surface *and its tests* together.
- **KEEP surfaces must stay green** every commit: the gate (`internal/gate`, `internal/controlgate`, `internal/brain` gate/control tools), certify chain, `StaffingManager`/`rolemodel`/leaderboard, `internal/sandbox`, `internal/recordings`, `internal/telemetry`, `internal/fleet`, the mission `Store`/`Mission`/`PhaseSpec` shell, the findings-gate.
- **NAMING TRAP — do NOT delete:** `BuildStore`, `report_build`, `certifyBuild`, `buildcert.go`, `/api/builds` are the **signed certification ledger** (a "build record" = a certified check run), NOT `create_mission`'s build arc. They survive.
- corral metaphor deferred (mission/herd/builder words stay for now; re-scope later — out of scope here).
- Verified-clean invariants untouched (jail nil-backend, credential boundary, claim atomicity, human-gate, certify chain).

## The one design fork (decide before Phase 6)
What survives as the remnant of `engine.go` once the build loop is gone? **Recommendation: keep the stripped shell** — staffing dispatch, the findings-gate (`blockingFindingOpen`→`needs-review`→`ResolveNeedsReview`), dep-gating (`PromoteReady`/`MissionDone`/`sweepBlockedDeps`), the give-up backstop, the verify-gate seam — as the **seed of the slice-2 adversarial-verification engine** (the map calls the findings-gate "the future home of the re-pointed engine"). Slice 1 disables the *build* Tick body but does **not** build the new verification flow. (Alternative: delete `engine.go`/`Tick` entirely and park the findings-gate for slice 2 — smaller now, more rework later. The plan below assumes KEEP-the-shell.)

---

# PHASE 1 — Stop shipping builds (delete the build *output* path)

The engine stops committing, secret-scanning-for-push, pushing, opening PRs, and polling PR reviews. All strictly builder-only (the gate has its own status-post via `repo.Engine`, unaffected).

## Task 1.1: remove commit/egress/push/PR + PR-review-poll from the engine
**Files:** Modify `internal/mission/engine.go`; Modify `cmd/corral/main.go`; delete/trim `internal/mission/*_test.go` covering these.

- [ ] **Delete these engine.go members and every `Tick` call to them:** `commitDonePhases` (~736-775), `runEgressGate` (~790-845) + the `egressBlocked` map (~206), `finishRepoMission` (~855-893), `recordPRFailure` (~900-911), `maxPRAttempts`/`prAttempts`/`prGaveUp` (~252,197-206), `ReviewPoll` (~924-1000)+`newestActionable`+`reviewPhases`+`sanitizePhase`+`maxReviewRounds`+`etags`+`botLogins` (~916-1060,210-219). In `Tick` (413-527) remove the promote→commit→converge→PR body (`commitDonePhases` 458-460, the `finishRepoMission` push/PR 502-525) — **but keep** the staffing dispatch (424-435), `PromoteReady` (448), `sweepBlockedDeps` (455), `MissionDone` (461), and the give-up backstop.
- [ ] **Unwire in `cmd/corral/main.go`:** remove `engine.Egress = egressAdapter{}` (~905) and the `egressAdapter` type (~1420-1429); remove the engine's build use of `engine.Repo`/`OpenPR`. **KEEP** `repo.Engine`/`repoEng` (the gate uses it for checkout + `SetCommitStatus`), the gateway SSRF guard (`gateway.NewGuard`, ~579 — different thing), and `internal/egress` the *package* (the gate does not use it, but leave the package unless nothing references it — if truly orphaned after this, delete it in a follow-up).
- [ ] **Tests:** delete mission tests asserting commit/push/PR/egress/review-poll behavior (e.g. `TestEngineSweepsBlockedDependencies` keep the dep-sweep part; the `finishRepoMission`/PR tests go). Keep dep-gating + staffing + findings tests.
- [ ] **Gate green.** **Commit:** `refactor(mission): stop shipping builds — remove commit/egress/push/PR + review-poll from the engine (retire builder 1/N)`.

---

# PHASE 2 — Delete the build plan (the SDLC arc + sizer)

## Task 2.1: delete `complexity.go` and the default build plan
**Files:** Delete `internal/mission/complexity.go`; Modify `internal/mission/store.go`; Modify `internal/brain/missions.go`, `internal/ui/ui.go`.

- [ ] **Delete `internal/mission/complexity.go` entirely** (`classifyComplexity`, `ScaledPlan`, `leanPlan`, `standardPlan`, `Tier*`, `fullSignals`/`leanSignals`) — every plan it emits is a build arc.
- [ ] **Delete `DefaultPlan` (store.go ~178-207) and `verifyCommands` (~162-172)** — the researcher→…→retro SDLC pipeline + the `go build`/`go test` per-phase verify picker.
- [ ] **Callers:** `internal/brain/missions.go` `create_mission` calls `ScaledPlan` at ~83 and defaults a nil plan to it (~82-84); `internal/ui/ui.go` `createMission` calls `ScaledPlan` at ~1279. These callers are gutted in Phase 3 — for now make them not compile-depend on the deleted symbols (temporarily require an explicit plan / stub, to keep the build green between commits), or fold Phase 3 into this commit if cleaner.
- [ ] **Tests:** delete `complexity_test.go`, `DefaultPlan`/`verifyCommands` tests.
- [ ] **Gate green.** **Commit:** `refactor(mission): delete the build-plan sizer + default SDLC arc (retire builder 2/N)`.

---

# PHASE 3 — Retire the build entry points

## Task 3.1: gut `create_mission` (build) + the UI create handler
**Files:** Modify `internal/brain/missions.go`, `internal/ui/ui.go`.

- [ ] **`create_mission` MCP tool (missions.go ~61-280):** remove the build path — the `ScaledPlan`/`DefaultPlan` nil-plan default (82-84) and the repo-clone/branch/PR provisioning block (179-263). **Decision (recommended):** delete `create_mission` as a *build* verb outright (it's the tell; the re-focus removes it). Keep the underlying `mission.CreateMission`/`Store`/`Mission`/`PhaseSpec` (the slice-2 verification engine reuses them). If a create-verb must remain for slice 2, rename+re-scope it there, not here.
- [ ] **UI `createMission` handler (ui.go ~1229-1292):** remove the `ScaledPlan` build call + the build-mission creation. Keep the handler shell only if slice 2 needs it; otherwise remove the route.
- [ ] **`review_mission`/`RequiresReview`/`SubmitReview` (missions.go ~304-324, store.go ~484-516):** this is *client-accepts-a-build-deliverable*. Remove the build-accept semantics. (The human-accept *pattern* may be reused by slice 2's verdict gate — preserve `ResolveNeedsReview`/`resolve_review`, which is findings-gate, not build-accept.)
- [ ] **Tests + the `createMissionIn` schema:** delete build-mission creation tests; keep any mission-store CRUD tests.
- [ ] **Gate green.** **Commit:** `refactor(brain,ui): retire create_mission as a build verb (retire builder 3/N)`.

---

# PHASE 4 — Delete the reflex build re-planner + build final-state check

## Task 4.1: delete `replan.go` and the build convergence remnants
**Files:** Delete `internal/mission/replan.go`; Modify `internal/mission/engine.go`, `cmd/corral/main.go`.

- [ ] **Delete `internal/mission/replan.go` entirely** (`replan`, `reflexRules`, `reflexFixInstr`, `reflexVerifyInstr`, `isReflexTask`) — every remediation it emits is a `builder`-role "fix with write_file" task; it auto-fixes *built code*. Remove its `Tick` call (~439) and `OnReflexCapExhausted` (engine.go ~183; main.go ~841-875 wiring) + `reflexCap`/`reflex*` fields.
- [ ] **Delete `finalStateOK` (engine.go ~538-561) + `fileFinalStateFinding` (~709-731)** — re-run the *build's* verify commands against the final tree. (`finalStateOK` is the only caller of the shared `e.Verify` runner from the build side — **keep `e.Verify`/`NewSandboxVerify`/`execBackend`**, just remove this caller. `e.Verify` stays for slice 2.)
- [ ] **Tests:** delete `replan_test.go` and the reflex/final-state tests.
- [ ] **Gate green** (incl. `-race ./internal/mission/...`). **Commit:** `refactor(mission): delete the reflex build re-planner + build final-state check (retire builder 4/N)`.

---

# PHASE 5 — Detach + re-point staffing (RETAINED)

## Task 5.1: make `StaffingManager` a standalone library, re-pointed to verifier roles
**Files:** Modify `internal/mission/routing.go`, `internal/mission/engine.go`, `cmd/corral/main.go`; (staffing already lives in `package mission` — no move needed).

- [ ] **Detach from the engine:** `StaffingManager` (routing.go) already imports only `rolemodel` and is already handed to the UI (`main.go` `ui.Deps{Staffing: engine.Staffing}`, and `proposeStaffing` at ui.go ~1294 drives it mission-free). Construct it standalone (`&mission.StaffingManager{Perf, LLM, RoleModels}` — deps at main.go ~1066, ~1480) and stop routing it through `engine`.
- [ ] **Re-home the driver orchestration (~40 lines) that dies with the build Tick:** `staffMission` (engine.go ~360-403), `staffedModelRef` (~338), the once-latch/`maxStaffAttempts`/30s-timeout/`recover()`/`Clamp`→`RoleModels.Set`-apply, and the `staff*` fields (~234-240). For slice 1, **park them** next to the standalone staffing (a small `mission.Staff(directive)` helper reproducing the guard rails) — the slice-2 gate-side driver will call it with a gate-derived directive. Keep the UI `proposeStaffing` path working.
- [ ] **Re-point the two build-coupled role lists (data, not machinery):** the Judge prompt (routing.go ~136-155) and `Clamp` defaults (~252-257) hard-code `builder/tester/pentester/reviewer` + ask for a *build* staffing plan → change to the **adversarial-verification roster** (security-breaker, correctness-reviewer, exploit-attempter, edge-hunter) and a *certification* framing. `Sense`/`Judge`/`Clamp` logic untouched.
- [ ] **Tests:** keep `routing_test.go` green (update the role expectations); keep a `-race` staffing test.
- [ ] **Gate green.** **Commit:** `refactor(mission): detach StaffingManager from the build loop + re-point roles to adversarial verifiers (retire builder 5/N)`.

---

# PHASE 6 — Resolve the engine remnant + make the gate primary

## Task 6.1: reduce `engine.go` to the verification-engine seed; elevate the gate
**Files:** Modify `internal/mission/engine.go`, `cmd/corral/main.go`; docs.

- [ ] **Per the design fork (recommended KEEP-the-shell):** what remains of `Tick` is the *lifecycle* skeleton (staffing dispatch now via the parked helper, dep-gating, give-up backstop, the findings-gate `blockingFindingOpen`→`needs-review`→`ResolveNeedsReview`, telemetry `OnFindingResolved`/`OnMissionCompleted`). With no build phases produced, the engine currently has nothing to *drive* — so either (a) disable the engine's Tick loop (don't start it) and keep the pieces as a library for slice 2, or (b) keep a no-op Tick that only runs the give-up/lifecycle. Recommend (a): **don't start the mission Tick loop**; the gate poller is the only running loop. Keep the code as the slice-2 seed.
- [ ] **Elevate the gate as primary:** confirm `StartControlGate` (controlgate.go ~161) + `StartGate` are wired and running from `main.go`; ensure the daemon's headline surface is the gate, not the (now-inert) mission engine. Update `main.go` startup logging to reflect "gate" not "missions: orchestration engine ticking".
- [ ] **Docs:** update ROADMAP.md / README to state the re-focus (audit, not builder) at a high level, pointing at the design spec. Keep honest (don't advertise the not-yet-built `corral certify <change>` CLI or the adversarial-verification flow — those are later slices).
- [ ] **Gate green** (full suite + `-race`); confirm the gate/certify/control-tool tests all pass and the daemon boots with the mission Tick disabled. **Commit:** `refactor: mission engine reduced to the verification seed; the gate is the primary surface (retire builder 6/6)`.

---

## Self-Review
- **Coverage:** the builder-only surfaces from the map (complexity, DefaultPlan, commit/egress/push/PR, ReviewPoll, replan, finalState, create_mission-build, UI create) are all deleted (Phases 1–4, 3); staffing is preserved+re-pointed (Phase 5); the engine remnant + gate-primary resolved (Phase 6). The gate/certify/certification-ledger/recordings/fleet/staffing-library are untouched or preserved.
- **Order safety:** leaf-first (output path → plan → entry points → replan → staffing → remnant); each commit compiles because callers of a deleted symbol are gutted in the same or a prior commit. The Phase-2/3 caller seam is called out.
- **Naming trap guarded:** BuildStore/report_build/certifyBuild explicitly KEEP.
- **Not in scope (later slices, stated):** the adversarial-verification flow (gate staffs the verifier swarm → files findings → certifies), the `corral certify <change>` / `corral gate <change>` standalone CLI (net-new composition of `controlgate.RunControlGate`+`controlspec`+`adequacy.NewJail`+`certify.SignDSSE`, plus a CLI signing-key path), the metaphor/rename, and the dev plug-in re-pointing (memory/skills/lookbook as accountability-native).
- **Risk:** Phase 6's "disable the mission Tick" leaves a large dormant subsystem; that's deliberate (seed for slice 2). If slice 2 is far off, revisit whether to delete the findings-gate too.
