<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Session handoff — 2026-07-05 → 06 (the long night)

Branch: **`feat/site-cockpit-tabs`** (7 commits, not yet merged). A marathon that
started as "finish the cockpit tabs" and became "capture a real recording with
the herd thinking." We got there — plus a lot of design clarity and a few
hard-won gotchas. Start here next session.

## TL;DR — where we landed
- ✅ **Shipped (committed):** real cockpit tabs, shared HUD, `--record-story`
  (flag + demo default), the generic two-backend split overlay, site dark mode,
  and the self-orchestrating-brain design thesis.
- ✅ **Proved end-to-end:** a demo mission with `record_story` captures real
  `report_thought` reasoning; the 14b produces *coherent* thoughts; the herd
  genuinely builds+tests a Go package (green `go build ./...` AND `go test ./...`);
  the site console renders the thinking.
- 🎯 **Banked but NOT shipped:** `go-stack-pass` — a good tape (coherent thoughts
  + real green build/test). It clears the "cool that works" bar but isn't swapped
  into the site yet (pending your nod) and isn't the *perfect* team-handoff cut.
- ⏭️ **The next build (the thesis):** the **Shep narrator handoff** — make the
  role handoff the brain already does *visible* on screen. Details below.

## Shipped this session (commits on feat/site-cockpit-tabs)
| commit | what |
|---|---|
| `5fb442b` | share the cockpit HUD (header + tabs + theme) into the DRY demo shell |
| `9601418` | **real cockpit tabs** — recorded panels behind every view (progress/topology/completed from the tape; memory/skills/proposals labeled "sample") |
| `592f2e9` | design note: the self-orchestrating brain |
| `8676da1` | hide the dead theme toggle on the site; add `corral-admin --record-story` |
| `bd9498d` | `--record-story` on the demo seed (so demo replays carry thinking) |
| `7493ee9` | generic `docker-compose.split.yml` two-backend overlay + demo README |
| `24d30f0` | **site light/dark mode** — real toggle, no-flash, cockpit-harmonized palette |

## The recording saga (what we have on disk, untracked)
- **`site/src/data/recordings/go-stack-pass.{json,meta.json}`** — THE keeper
  candidate. 14b on nvidia, coherent debugging thoughts, real green build+test.
  Imperfections (honest): (a) mission stalled at 10/20 `running` after ~47 min,
  so the tape ends on churn, not a clean "done"; (b) only **Bob** emitted
  thoughts (Tess/Iris worked but didn't think out loud); (c) the tester→builder
  handoff *happened* (role-routed) but isn't legible in the console.
- **`go-stack-14b.{json,meta.json}`** — proof-of-pipeline only; it FLAILED (never
  passed `go build ./...`). Safe to delete; kept as evidence.
- The three OLD tapes (`golden-run`, `js-lru-cache`, `python-ratelimit`) are still
  the site's recordings — thought-less, pre-story-engine. Plan is to **nuke them
  and swap in the keeper** once we pick/perfect it. (`golden-run` also lacks a
  `meta.result` GitHub link — the new one should get a result repo.)

## The design corpus (all in docs/superpowers/notes/)
- **`2026-07-05-the-self-orchestrating-brain.md`** — the big one. Wire an LLM into
  the brain's COGNITION (intake/planning/staffing), keep the correctness GATE
  deterministic. "A judge may not certify herself." Plus, appended this session:
  - **The handoff IS the thesis, make it visible** — the roles genuinely hand off
    (finding → `fix` task `role:builder` → `re-verify` task `role:tester`), and
    that routing is **100% deterministic** (reflex-replanner, `replan.go`). The
    brain is **NOT** LLM-powered — don't claim it is.
  - **Searchable past reasoning over MCP** (not raw-tape browsing).
  - **File-tree "files" cockpit tab** — capture already 90% exists (`claim_made`
    beats); `BuildReplayStream` (`replay.go:32`) just excludes them (a flagged v2).
- **`2026-07-05-corral-as-an-operator-system.md`** — the operator-system vision.

## Next session — do these, roughly in order
1. **Shep narrator handoff (THE thesis feature, build first).** The narrator
   (`llm.FromEnv()`, `cmd/corral/main.go:631`) exists but only serves ask-a-bee +
   the oracle — it does NOT emit into the mission tape. Wire it onto coordination
   events (finding→fix, re-verify pass) to emit handoff beats ("Iris flagged a
   build bug → Bob's fixing → Tess re-verifying → green") as telemetry landing in
   `/api/replay`, attributed to Shep, rendered distinctly in the console.
   Additive — does NOT touch the gate. This is what makes the thesis legible.
2. **Fix "only the builder thinks."** Tune tester/reviewer prompts to
   `report_thought` at their decision points so the *team's* reasoning shows.
3. **Re-record** with 1+2 → a real team-handoff tape → export → **swap out the 3**
   (hero → new tape, delete old, create the result repo + set `meta.result`).
4. Then the bigger features: file-tree tab (the flagged v2 + a tab), rich
   agent-click modal in playback (window machinery is product-only — never shared
   to the site; token usage isn't captured), searchable-recordings-over-MCP,
   two-line thought→command console pairing, and eventually the LLM planner.

## Load-bearing gotchas learned this session (do NOT relearn)
1. **Docker Desktop hijacks the context.** `docker context` had flipped to
   `desktop-linux` (a VM with no GPU passthrough → `could not select device
   driver "nvidia"`). Fix: `docker context use default` (native engine has the
   nvidia runtime + CDI). Daemons have SEPARATE image stores.
2. **Don't bypass the demo `.env` with `--env-file /dev/null`.** Doing so left
   agents in a state where they **never ran commands** (0 executions → nothing
   passes the gate → endless churn). The NORMAL `make demo-mission` / plain
   `docker compose` (which auto-loads `deploy/demo/.env`) executes correctly, the
   way the original golden-run did. Just run it normally.
3. **The go.mod trap.** The default stack directive builds files into a subdir
   with no module → `go build ./...` fails forever. Put **`go mod init` in the
   directive** ("FIRST run 'go mod init stack' at the repo root…") and it passes.
4. **EXPORT before teardown.** `down -v` wipes `brain-state`, destroying the tape.
   We lost a good nvidia-14b tape this way — banked the *stats*, not the tape.
   `scripts/export-golden-run.sh --mission N --slug NAME --yes` writes to
   `site/src/data/recordings/`; `--yes` skips the manual manifest but KEEPS the
   secret deny-list scan; it auto-stashes/restores `.env`.
5. **The AMD ROCm GPU works** (`corral-ollama-rocm`, host :11435, qwen2.5-coder:14b,
   96% GPU under load). An earlier "AMD can't work" call was a *sampling error* —
   verify roles inference in bursts, so it looked idle. 14b fits both cards
   (~9 GB nvidia, ~13 GB with context on AMD). Local `docker-compose.split.yml`
   (untracked, since generalized) points roles at a second backend.
6. **`report_host`/telemetry has NO GPU/CPU/token dimension** — model + backend +
   jail + OS only. Resource-aware staffing and token-in-the-modal both need a new
   capture beat.
7. **`pkill -f 'watch3'` / `pkill -f 'astro preview'` self-matches the running
   shell** (its command line contains the pattern) → kills itself, exit 144. Kill
   by pid or a pattern that can't match the pkill command.
8. **"No space left on device" with GB free = build cache bloat.** `docker system
   prune -af` + `docker builder prune -af`. Inodes were fine.

## Current state (as you left it)
- Stack **stopped**, both GPUs idle, `deploy/demo/.env` untouched.
- Branch `feat/site-cockpit-tabs`, 7 commits, not merged, not pushed.
- Uncommitted: the self-orchestrating-brain note edits (this session's appends).
- Untracked: `go-stack-pass.*` (keeper candidate) + `go-stack-14b.*` (flail proof).
- `docker-compose.amd.yml` was replaced by the committed generic `.split.yml`.
