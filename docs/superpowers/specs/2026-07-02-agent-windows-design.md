# Draggable Multi-Agent Windows — Design

**Status:** design · **Date:** 2026-07-02 · **Sub-project:** swarm UI — interactive agent windows

## Problem

The swarm UI (`internal/ui/web/index.html`) re-renders every panel via `innerHTML = …` on every
`/events` SSE tick (`apply()` → `renderInspector()`/etc.). Three consequences a dev hit immediately
while actually using it:
1. **The "ask the agent" input loses focus every ~second** — `renderInspector()` recreates `#askq`
   on each tick, so focus and caret vanish mid-keystroke. The field is unusable.
2. **The agent detail is a single fixed inspector** — you can only look at one agent, and it's
   pinned in place. A dev wants to open an agent as a movable window.
3. **No side-by-side.** Devs want two agent views at once — which, since roles can run different
   models, means *watching two models work the same swarm in parallel* (the cross-model thesis,
   made visual).

All three share one root: **interactive state (inputs, window position, focus) is destroyed by the
tick re-render.** The fix is one architectural move, not three patches.

## Design

**Move the interactive agent view into a persistent, draggable floating-windows layer that the tick
re-render does not clobber; the tick patches each window's *data* in place.**

### Components
- **`#windows` overlay layer** — a fixed-position container that `apply()` NEVER wholesale-rebuilds.
  It holds zero or more floating agent windows.
- **A floating agent window** (one per agent, keyed by agent name):
  - A draggable **title bar** (agent name · role · **model badge**) + a close ✕.
  - A **live data body** (`.aw-body`) — the agent's recent tool calls / activity, current task,
    status, findings — the part that updates on tick.
  - A persistent **ask footer** — the `ask this agent` input + button, created ONCE when the window
    opens and **never recreated by the tick** (so focus/caret survive).
- **`openAgentWindow(name)`** — opens a window (or raises it if already open). Clicking an agent row
  (currently `selectAgent`) calls this. Multiple can be open → drag them side by side.
- **Drag** — title-bar `mousedown` → `mousemove` repositions the window (absolute left/top),
  clamped to viewport; `mouseup` ends. Raise-to-front on interaction (z-index bump).
- **Tick update (the crux):** `apply()` iterates open windows and, for each, updates ONLY the
  `.aw-body` innerHTML from the latest state (reuse the existing agent-detail HTML builder) and the
  title status — it does **not** touch the ask footer or the window shell/position. So focus, caret,
  drag position, and scroll are preserved.
- **Data source:** reuse the existing per-agent data — the main `state` (active_agents/topology has
  name/role/model/status) for the live body, and the on-demand `/api/agent?name=` fetch (already
  wired in `askAgent`/the inspector) for the deeper detail on open. No backend change required for
  v1 (the model is already on the agent via topology; `/api/agent` already returns the detail).

### Cross-model touch
Each window's title bar shows a **model badge** (from the agent's `model`/`backend`). Two windows
open side by side with different model badges = the cross-model comparison, live and literal.

### Keep the existing panels as-is
The non-interactive panels (feed, findings, topology, tasks, progress) keep their `innerHTML` tick
re-render — they have no focus/position state to lose. Only the agent view + ask field move to the
persistent layer. The single-inspector code path is replaced by the windows layer (or kept as a
fallback for narrow screens — v1 may simply replace it).

## Error handling / edge cases
- Open an already-open agent → raise + focus, don't duplicate.
- Agent goes inactive → its window stays open (shows "inactive"/last-known), closable by the user
  (don't auto-close under the cursor).
- `/api/agent` fetch fails → the window shows a soft "couldn't reach the brain — retrying" in the
  body; the shell/ask footer persist.
- Drag clamped to viewport so a window can't be lost off-screen; position is per-browser only
  (not persisted server-side).
- Windows are display-only client state — a tick with no data for an open agent leaves it intact.

## Testing
Front-end, so verification is primarily manual-in-browser (drag, side-by-side, focus-holds-while-
typing) plus structural checks:
- The served HTML contains the `#windows` layer, `openAgentWindow`, a drag handler, and an ask
  footer created outside the tick path (grep/structure assertions in `ui_test.go` where feasible).
- `apply()` updates `.aw-body` but does not re-create `#askq`-equivalent inputs (assert the ask
  input node identity is stable across a simulated apply, if the test harness allows; otherwise a
  structural assertion that the ask footer is built in `openAgentWindow`, not in the tick renderer).
- Manual acceptance (the real test): type in an agent's ask field through several ticks without
  losing focus; open two agents and drag them side by side; each shows its model badge.

## Out of scope (follow-ups)
- Resizable windows / snap-to-grid / saved layouts.
- Persisting window positions server-side or across reloads.
- A native Go TUI surface (separate later sub-project — this is the web UI).
- Diffing two agents' outputs automatically (the side-by-side is manual/visual for v1).
