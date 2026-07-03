# Swarm UI — Status & Parked-Lease Lifecycle (Plan 2)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the permission/parked-lease hazard *visible* in the swarm UI: an `awaiting_approval` agent shows amber with a "⏸" badge, and the exclusive claims it holds render as parked edges with a live countdown to downgrade, then an "open" state once a peer may proceed.

**Architecture:** The UI is a single `go:embed`-ed `internal/ui/web/index.html` (custom canvas force-directed graph) fed by the `coord.Status` JSON over `/api/state` + `/events` (SSE). Plan 1 already put `status`/`status_since` on each agent. This plan (1) adds two scalars to the `Status` payload — `server_now` and `parked_grace_seconds` — so the browser can compute a skew-free countdown, and (2) layers parked visuals onto the existing `apply()`/`draw()` without restructuring them.

**Tech Stack:** Go 1.26 (`internal/coord`, `internal/ui`), vanilla canvas JS in `index.html`. No build step, no JS deps.

## Global Constraints

- Module `github.com/pdbethke/corralai`. Go 1.26. No new third-party deps; no JS framework or build step.
- The swarm UI is **load-bearing**: all changes are **additive/conditional**. Do NOT remove or restructure existing node/edge rendering, the live-activity feed, or any existing indicator. Parked visuals layer on top and are inert when no agent is `awaiting_approval`.
- **The UI is a CLIENT of the brain's public API, not part of the brain.** It may consume ONLY the public HTTP surface (`/api/state`, `/events`, `/api/*`) — never brain internals. `go:embed` delivery from the `corral` binary is a convenience (zero-config demo), NOT coupling: nothing this plan adds may make the UI un-extractable into a standalone client later. Every new datum the browser needs arrives over the JSON API (this is exactly why Task 1 adds `server_now`/`parked_grace_seconds` to the payload rather than computing them browser-side against brain internals).
- The UI stays a single `index.html` (embedded via `go:embed` for now); keep it self-contained so it can be served standalone unchanged.
- `Status` JSON is snake_case (`active_agents`, `live_claims`, `server_now`, `parked_grace_seconds`). Agent JSON carries `status` and `status_since`.
- Time on the server flows through the `now` seam (Plan 1). `parked_grace_seconds` comes from `parkedGraceSeconds()` (env `CORRALAI_PARKED_GRACE_SECONDS`, default 300; demo ~20).
- The browser must NOT trust its own wall clock for absolute time: it computes elapsed parked time from `server_now` (captured per state push) + local `performance.now()` delta.
- Commit prefixes: `feat(coord):`, `feat(ui):`.

## File Structure

- `internal/coord/store.go` — MODIFY: add `Status.ServerNow` + `Status.ParkedGraceSeconds`; populate both in `CoordinationStatus`.
- `internal/coord/status_test.go` — MODIFY: assert the two new Status fields (Task 1).
- `internal/ui/ui_test.go` — CREATE: an `/api/state` handler test asserting the JSON contract the frontend depends on (Task 1).
- `internal/ui/web/index.html` — MODIFY: `apply()` annotates nodes/links with parked state (Task 2); `draw()` + legend + stat render it (Task 3).

---

### Task 1: Status payload exposes `server_now` + `parked_grace_seconds`

**Files:**
- Modify: `internal/coord/store.go` (`Status` struct; `CoordinationStatus` return)
- Test: `internal/coord/status_test.go` (add); `internal/ui/ui_test.go` (create)

**Interfaces:**
- Produces: `Status.ServerNow float64` (json `server_now`), `Status.ParkedGraceSeconds float64` (json `parked_grace_seconds`), both populated by `CoordinationStatus`. Agent `status`/`status_since` already present (Plan 1).

- [ ] **Step 1: Write the failing coord test**

Append to `internal/coord/status_test.go`:

```go
func TestCoordinationStatusExposesClockAndGrace(t *testing.T) {
	clock := 4242.0
	withClock(t, func() float64 { return clock })
	t.Setenv("CORRALAI_PARKED_GRACE_SECONDS", "20")
	s := openTmp(t)
	s.Register("alice", "", "", "", "")
	st, err := s.CoordinationStatus(100000)
	if err != nil {
		t.Fatal(err)
	}
	if st.ServerNow != 4242 {
		t.Fatalf("server_now should be the seam clock, got %v", st.ServerNow)
	}
	if st.ParkedGraceSeconds != 20 {
		t.Fatalf("parked_grace_seconds should reflect env, got %v", st.ParkedGraceSeconds)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/coord/ -run TestCoordinationStatusExposesClockAndGrace`
Expected: FAIL to compile — `st.ServerNow undefined`.

- [ ] **Step 3: Add the fields and populate them**

In `internal/coord/store.go`, extend the `Status` struct:

```go
type Status struct {
	ActiveAgents       []Agent     `json:"active_agents"`
	LiveClaims         []LiveClaim `json:"live_claims"`
	RecentCompleted    []Completed `json:"recent_completed"`
	RecentActivity     []Activity  `json:"recent_activity"`
	ServerNow          float64     `json:"server_now"`
	ParkedGraceSeconds float64     `json:"parked_grace_seconds"`
}
```

In `CoordinationStatus`, set them on the returned literal (it already builds `&Status{ActiveAgents: agents, LiveClaims: live, RecentCompleted: done, RecentActivity: acts}`):

```go
	return &Status{
		ActiveAgents:       agents,
		LiveClaims:         live,
		RecentCompleted:    done,
		RecentActivity:     acts,
		ServerNow:          now(),
		ParkedGraceSeconds: parkedGraceSeconds(),
	}, rows.Err()
```

- [ ] **Step 4: Run the coord test**

Run: `go test ./internal/coord/ -race`
Expected: PASS (all coord tests).

- [ ] **Step 5: Write + run the UI handler contract test**

Create `internal/ui/ui_test.go`:

```go
package ui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/coord"
)

// The frontend reads server_now, parked_grace_seconds, and per-agent status from
// /api/state. This pins that JSON contract.
func TestStateEndpointCarriesParkedFields(t *testing.T) {
	cs, err := coord.Open(filepath.Join(t.TempDir(), "c.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	if _, err := cs.BootstrapSession("alice", "", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := cs.SetStatus("alice", "awaiting_approval"); err != nil {
		t.Fatal(err)
	}
	h := Handler(cs, nil, nil, nil, map[string]bool{}, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/state", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	var payload struct {
		ServerNow          float64 `json:"server_now"`
		ParkedGraceSeconds float64 `json:"parked_grace_seconds"`
		ActiveAgents       []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"active_agents"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if payload.ServerNow <= 0 || payload.ParkedGraceSeconds <= 0 {
		t.Fatalf("server_now/parked_grace_seconds missing: %+v", payload)
	}
	var alice string
	for _, a := range payload.ActiveAgents {
		if a.Name == "alice" {
			alice = a.Status
		}
	}
	if alice != "awaiting_approval" {
		t.Fatalf("agent status not in /api/state, got %q", alice)
	}
}
```

Run: `go test ./internal/ui/ -race`
Expected: PASS. (If `Handler`'s parameter list differs, match it from `internal/ui/ui.go:41` — pass `nil` for the stores/bus/roles the test doesn't use, `map[string]bool{}` for memOwners.)

- [ ] **Step 6: Commit**

```bash
git add internal/coord/store.go internal/coord/status_test.go internal/ui/ui_test.go
git commit -m "feat(coord): expose server_now + parked_grace_seconds in Status payload"
```

---

### Task 2: `apply()` annotates parked nodes & edges

**Files:**
- Modify: `internal/ui/web/index.html` (the `apply(state)` function, ~lines 151-185)

**Interfaces:**
- Consumes: `state.server_now`, `state.parked_grace_seconds`, per-agent `status`/`status_since` (Task 1).
- Produces: module vars `serverNow`, `parkedGrace`, `stateClientT`; each agent node gets `n.status`/`n.statusSince`; each claim link gets `parked`/`downgraded`/`remaining` annotations consumed by `draw()` (Task 3).

- [ ] **Step 1: Add clock-capture + status fields in `apply()`**

In `internal/ui/web/index.html`, near the top of the `<script>` add module vars (next to the existing `let selected = null, lastState = ...` line):

```js
let serverNow = 0, parkedGrace = 300, stateClientT = 0;
function liveServerNow(){ return serverNow + (performance.now()/1000 - stateClientT); } // skew-free elapsed
```

At the very start of `apply(state)` (right after `lastState = state;`), capture the clock + grace:

```js
  serverNow = state.server_now || 0;
  parkedGrace = state.parked_grace_seconds || 300;
  stateClientT = performance.now()/1000;
```

- [ ] **Step 2: Stamp status onto agent nodes**

In the first `state.active_agents` loop, where the node fields are set (`n.last = ...; n.parent = ...`), add the status fields:

```js
    n.last = a.last_active_ts||0; n.parent = a.parent||''; n.role = a.role||''; n.fullname = a.name;
    n.status = a.status||'working'; n.statusSince = a.status_since||0;
```

- [ ] **Step 3: Annotate claim links with parked state**

Build an agent-status lookup before the `live_claims` loop, and annotate each pushed claim link. Replace the existing `live_claims` loop body's `links.push({a:an, b:pn, conflict:pn.conflict});` with parked-aware annotation:

```js
  const agentStatus = {};
  (state.active_agents||[]).forEach(a => { agentStatus[a.name] = {status:a.status||'working', since:a.status_since||0}; });
  for(const n of nodes.values()) if(n.kind==='path') n.claimLast = 0;
  (state.live_claims||[]).forEach(c => {
    const an = ensure('a:'+c.agent,'agent',c.agent); seen.add(an.id);
    const short = c.path.split('/').filter(Boolean).pop() || c.path;
    const pn = ensure('p:'+c.path,'path',short);
    pn.fullpath = c.path;
    pn.conflict = claimsByPath[c.path].size > 1; seen.add(pn.id);
    pn.claimLast = Math.max(pn.claimLast||0, an.last||0);
    const holder = agentStatus[c.agent] || {status:'working', since:0};
    const parked = c.exclusive && holder.status === 'awaiting_approval';
    links.push({a:an, b:pn, conflict:pn.conflict, parked: parked, since: holder.since});
  });
```

(`draw()` computes the live countdown/downgraded state from `link.since` + `parkedGrace` + `liveServerNow()` so it ticks between SSE pushes.)

- [ ] **Step 4: Verify the page still loads and renders unchanged when nobody is parked**

Run the brain locally: `go run ./cmd/corral` (dev mode, auth off) and open `http://localhost:9019/`. With no `awaiting_approval` agents, the swarm must look exactly as before (additive change is inert). Confirm no JS console errors.

- [ ] **Step 5: Commit**

```bash
git add internal/ui/web/index.html
git commit -m "feat(ui): apply() captures server clock and annotates parked claims"
```

---

### Task 3: `draw()` renders parked nodes, countdown edges, legend & count

**Files:**
- Modify: `internal/ui/web/index.html` (`draw()` edge + agent-node sections ~lines 215-235; legend ~105-108; stat line ~186)

**Interfaces:**
- Consumes: `link.parked`/`link.since`, `n.status`/`n.statusSince`, `serverNow`/`parkedGrace`/`liveServerNow()` (Task 2).

- [ ] **Step 1: Render parked / downgraded claim edges with a countdown**

In `draw()`, the edge loop currently does:

```js
    if(l.sub){ ctx.strokeStyle=hexA(C.amber,.35); ctx.lineWidth=1; ctx.setLineDash([3,3]); }
    else { ctx.strokeStyle = l.conflict ? hexA(C.red,.7) : hexA(C.line,.7); ctx.lineWidth = l.conflict ? 2 : 1; ctx.setLineDash([]); }
```

Insert a parked branch BEFORE the `l.sub` check (parked styling wins for a parked exclusive lease), and after drawing the edge line, label it. Replace that two-line block with:

```js
    let parkedLabel = null;
    if(l.parked){
      const remaining = parkedGrace - (liveServerNow() - (l.since||0));
      const open = remaining <= 0;
      ctx.strokeStyle = hexA(open ? C.red : C.amber, .85);
      ctx.lineWidth = 2; ctx.setLineDash(open ? [2,4] : [5,4]);
      parkedLabel = open ? 'lease open to peers' : 'parked ' + Math.ceil(remaining) + 's';
    } else if(l.sub){ ctx.strokeStyle=hexA(C.amber,.35); ctx.lineWidth=1; ctx.setLineDash([3,3]); }
    else { ctx.strokeStyle = l.conflict ? hexA(C.red,.7) : hexA(C.line,.7); ctx.lineWidth = l.conflict ? 2 : 1; ctx.setLineDash([]); }
```

Then, immediately after the existing `ctx.stroke()` that draws the edge line (still inside the edge loop), add the label render:

```js
    if(parkedLabel){
      ctx.setLineDash([]);
      ctx.fillStyle = hexA(C.fg, .85); ctx.font='10px ui-sans-serif';
      ctx.fillText(parkedLabel, (l.a.x+l.b.x)/2 + 4, (l.a.y+l.b.y)/2 - 4);
    }
```

(Find the edge-line `ctx.stroke()` in the loop; the label goes right after it, before the loop iterates.)

- [ ] **Step 2: Render an `awaiting_approval` agent node distinctly**

In the agent-node branch of the node loop, after the node core is drawn and before/with the label, add a parked treatment. Replace the agent label line:

```js
      ctx.fillStyle=C.fg; ctx.font=(sub?'11px':'12px')+' ui-sans-serif'; ctx.fillText(lbl, n.x+core+4, n.y+4);
```

with:

```js
      const parkedNode = n.status === 'awaiting_approval';
      if(parkedNode){
        ctx.beginPath(); ctx.arc(n.x,n.y, core+4, 0, 7);
        ctx.strokeStyle=hexA(C.amber,.95); ctx.lineWidth=2; ctx.setLineDash([2,2]); ctx.stroke(); ctx.setLineDash([]);
      }
      ctx.fillStyle=C.fg; ctx.font=(sub?'11px':'12px')+' ui-sans-serif';
      ctx.fillText((parkedNode?'⏸ ':'')+lbl, n.x+core+4, n.y+4);
```

- [ ] **Step 3: Legend + stat count**

In the legend block (the `<span class="dot">` list ~lines 105-108), add one entry after the conflict line:

```html
    <span class="dot" style="background:var(--amber)"></span>⏸ awaiting approval
```

In `apply()`, where the stat line is set (`stat.textContent = \`${...} agents · ${...} claims\``), append a parked count:

```js
  const parkedN = (state.active_agents||[]).filter(a => a.status === 'awaiting_approval').length;
  stat.textContent = `${(state.active_agents||[]).length} agents · ${(state.live_claims||[]).length} claims` + (parkedN?` · ${parkedN} ⏸`:'');
```

- [ ] **Step 4: Verify unchanged-when-idle, then commit**

Run `go run ./cmd/corral`, open `http://localhost:9019/`, confirm: (a) with no parked agents the view is unchanged and console is clean; (b) the new legend entry shows. Visual proof of the parked path itself is Task 4.

```bash
git add internal/ui/web/index.html
git commit -m "feat(ui): render awaiting_approval nodes + parked-lease countdown edges"
```

---

### Task 4: Visual verification of the parked lifecycle (Playwright)

**Files:** none (verification only — produces screenshots, no commit unless a fix is needed)

**Interfaces:** Consumes Tasks 1-3. Uses the Playwright MCP browser tools available in this session.

- [ ] **Step 1: Drive a parked scenario and screenshot**

Procedure (the implementer runs the brain with a short grace so the lifecycle is watchable, drives it via the MCP tools or a small script, and captures the UI):

1. Start the brain with a short grace: `CORRALAI_PARKED_GRACE_SECONDS=15 go run ./cmd/corral` (dev mode, auth off, on `127.0.0.1:9019`).
2. Using an MCP client (or `curl` against `/mcp/` is not trivial — prefer driving the store via a tiny Go helper OR the existing harness pattern): register two agents, have `owner` `claim_paths(["src/app.go"], exclusive)`, then `heartbeat` with `status=awaiting_approval`.
3. With Playwright MCP: `browser_navigate` to `http://localhost:9019/`, wait ~2s, `browser_take_screenshot` — confirm the `owner` node shows the ⏸ badge + amber ring and the `src/app.go` edge shows a "parked Ns" countdown.
4. Wait past 15s, screenshot again — confirm the edge flips to "lease open to peers" (red dashed).
5. Have a peer `claim_paths(["src/app.go"], exclusive)` (now granted with surfaced conflict), then `owner` `heartbeat status=working`; screenshot — confirm the parked styling clears.

- [ ] **Step 2: Record the result**

If the visuals match, note it in the implementer report with the screenshot paths. If anything renders wrong (wrong color, no countdown, console error), report it with the screenshot and the specific `apply()`/`draw()` line so a fix can be dispatched. Do NOT weaken or skip — a UI that doesn't show the hazard is the failure mode this plan exists to prevent.

> Note: this task has no automated assertion (canvas pixels aren't unit-testable here); the JSON contract is locked by Task 1's tests, and the rendering is verified visually. The demo env (Plan 3) exercises this end-to-end with real agents.

---

## Self-Review

- **Spec coverage:** "swarm UI renders status" → Tasks 2-3 (node ⏸ + amber ring); "parked claim edges with countdown → downgraded" → Task 3 edge branch; "skew-free countdown" → `server_now`/`liveServerNow()` (Tasks 1-2); legend/count → Task 3; visual lifecycle proof → Task 4. The JSON contract is test-locked (Task 1).
- **Placeholder scan:** the only non-code step is Task 4 (visual), which is inherently manual for canvas; it gives an exact procedure + screenshots, not a blank.
- **Type consistency:** `server_now`/`parked_grace_seconds` (snake JSON) ↔ `ServerNow`/`ParkedGraceSeconds` (Go); `link.parked`/`link.since`/`n.status`/`n.statusSince` introduced in Task 2 are consumed verbatim in Task 3; `liveServerNow()` defined in Task 2 used in Task 3.
- **Load-bearing UI:** every frontend change is additive/conditional — parked branches only fire for `awaiting_approval`; idle view is unchanged (verified in Tasks 2-3 Step 4).

## Out of Scope (later)

- The docker demo env / `corral-agent` / clobber contrast (Plan 3).
- Any change to lease policy itself (done in Plan 1) — this plan only visualizes it.
