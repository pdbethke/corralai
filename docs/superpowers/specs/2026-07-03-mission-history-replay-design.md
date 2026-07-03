# Mission history + replay: the corral remembers, and you can watch it back

**Status:** approved design, 2026-07-03
**User decisions baked in:** replay v1 reconstructs from data that already exists (tonight's demo run must be replayable), AND richer recording starts now so future missions play back with full ambience. The replay viewer ships before the demo video — the video films a golden run's replay.

## What exists (research-verified)

Nothing about a finished mission is deleted: `missions`/`phases` rows, `tasks` (with `created_ts`/`claimed_ts`/`done_ts`), `findings`, `executions` (every command, mission-scoped, timestamped), and the append-only telemetry event log all survive indefinitely. The live canvas, however, feeds partly on ephemeral state (ExecRing, ActivityRing, HostBook) and computes node positions client-side.

Recording holes (fixed by this spec, going forward): no `mission_completed` telemetry event exists at all; findings carry no resolution timestamp on the row; the activity stream, host topology, claim lifecycle, and memory writes are never durably recorded.

## Part A — Completed tab

A new top-nav tab (skin-aware label; slot exists since the proposals-tab rework) listing non-running missions newest-first: directive, status, duration (derived: first task `created_ts` → last task `done_ts` until the new completion event exists, then event-based), task/finding counts, and what got learned (best-effort linkage: promoted proposals whose signature matches findings this mission reported — heuristic until proposals carry mission provenance, which is not this spec's job). Detail view per mission: phase/task table with timings, findings with outcomes, executions summary, PR link when present. A `▶ replay` button per mission. Data comes from the existing stores via a new read-only brain surface (`mission_history` list + detail); the UI stays thin.

## Part B — Replay viewer (works on already-recorded missions)

Playback mode of the existing corral canvas: a `/api/replay?mission=N` endpoint returns a merged, time-ordered event stream reconstructed from durable stores —

- task lifecycle from `tasks` timestamps (claim → working → done/superseded/cancelled),
- agent nodes inferred from `tasks.claimed_by` + `executions.agent` (first sighting spawns the node, last activity + grace despawns it),
- execution bursts from `queue.executions` (the durable twin of what the live UI draws from ExecRing),
- findings landing from `findings.created_ts`; resolutions, proposal opened/approved, review beats from telemetry events.

The client replays this stream through the existing `apply()`/renderer path at a chosen speed (1×–16×) with a scrub bar; positions recompute via the same force layout (deliberately not recorded — the layout is presentation, not history). Live SSE is paused while replaying; the replay is read-only by construction. Missions recorded before Part C simply play without ambience — the viewer degrades gracefully by feeding whatever kinds exist.

## Part C — Richer recording (telemetry is the one event log)

New telemetry kinds, emitted at the seams where the ephemeral structures are fed today, so playback reads one stream:

| kind | emitted where | payload |
|---|---|---|
| `mission_completed` | mission engine on `done`/`failed` (it must finally learn to speak telemetry) | status, review_rounds |
| `agent_activity` | where ActivityRing is fed | agent, role, tool/verb, one-line summary |
| `claim_made` / `claim_released` | coord claim create/release | agent, path, exclusive |
| `host_seen` | report_host handler (first sighting + material changes only, not every heartbeat) | agent, host, model, backend, jail |
| `memory_written` | memory Add (name, type, shared flag — never the body) | author, slug, typ, shared |

Not a new kind but part of this recording pass: stamp `resolved_ts` on the `findings` row when its status transitions (the `finding_resolved` telemetry event already exists; the row itself is currently timeline-blind).

Volume guard: per-mission cap on `agent_activity` (e.g. 2,000 events; loud log when capped) — the cap protects the log, the log protects the replay. Metadata-only discipline holds: no instruction/directive/body text in detail payloads beyond what existing kinds already carry (the fleet-sync column allowlist must not widen).

## Testing

- History surface: unit tests over seeded stores (list ordering, duration derivation, learned-linkage query).
- Replay stream: golden-file test — seed a small mission's rows, assert the merged stream's order and shape; graceful-degradation test (mission with no telemetry ambience still yields a playable stream).
- Recording: each new kind has an emission test at its seam; cap test for `agent_activity`; a canary that `memory_written` never carries body text.
- Live check: replay tonight's demo mission end-to-end; screenshot the scrub bar mid-replay.

## Deliberately out of scope

- Replay-as-eval (re-running missions under different configs) — future spec amendment, deliberately deferred by the user.
- Recording canvas positions, chatter text, or per-frame state — presentation is recomputed, not recorded.
- Retention/GC policy for the ever-growing stores — separate operational concern; the volume guard is the only cap this spec adds.
- Cross-mission analytics beyond what `mission_analytics` already provides.
