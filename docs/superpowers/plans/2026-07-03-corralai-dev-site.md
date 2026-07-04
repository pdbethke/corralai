# corralai.dev One-Pager Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship corralai.dev — a static Astro one-pager whose hero is a real, embedded replay of a recorded corral mission, deployed to Cloudflare Pages behind the existing `corralai.dev` domain.

**Architecture:** Extract the replay player + the canvas renderer it drives out of `internal/ui/web/index.html` into a standalone `internal/ui/web/replay-player.js` (loaded by the product page via `<script src>`, no behavior change). A privacy-gated export script bakes one real mission's `/api/replay` stream into `site/src/data/golden-run.json`. A new `site/` Astro project (its own npm toolchain, isolated from the Go module) loads the synced copy of `replay-player.js` plus the baked JSON into a hero component and reuses verified README copy for the remaining sections. A new GitHub Actions workflow builds and deploys `site/` to Cloudflare Pages, gated on the existing Go test suite.

**Tech Stack:** Go 1.26.4 (unchanged, existing repo); Astro (npm, new `site/` toolchain); Playwright (site E2E); Python 3 (export script's privacy gate — no new Go/JS dependency); Cloudflare Pages + `wrangler`.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-07-03-corralai-dev-site-design.md` — read it first.
- Corral voice everywhere in user-facing copy; never bee/hive outside hive-skin content (memory `corralai-metaphor`).
- No external requests from the site: no external fonts, no CDN scripts/styles, no analytics/tracking of any kind — self-contained only. Test-enforced (Task 7).
- Every factual claim on the site traces to README.md/docs/DESIGN.md verified copy — no new capability claims invented for marketing.
- SPDX header `// SPDX-License-Identifier: Elastic-2.0` (or the language's comment-equivalent) on every new source file where comments are possible (`.js`, `.sh`, `.py`, `.astro`) — not on `.json`.
- Every commit message ends with `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`.
- The Go suite stays green after the player extraction: `go test ./... -count=1`.
- `gosec`/`go vet` clean: `bash scripts/check-security.sh` passes (gofmt + gosec MEDIUM+ = 0 findings).
- The golden-run export is privacy-gated, not privacy-assumed: an automated deny-list scan AND a human-review manifest both run before any JSON is written or committed (Task 2) — this is a hard requirement, not best-effort.
- Production DNS/custom-domain cutover is the LAST step performed and verified, after everything else is live and checked (Task 6, final sub-step).

---

### Task 1: Extract the replay player into `internal/ui/web/replay-player.js`

**Files:**
- Create: `internal/ui/web/replay-player.js`
- Modify: `internal/ui/web/index.html`
- Modify: `internal/ui/ui_test.go` (`TestReplayPlayerStructure`)

**Interfaces:**
- Consumes: nothing new — this is a pure code move out of the existing embedded `web/` tree (`//go:embed web` in `internal/ui/ui.go:34-35`, served by `http.FileServer(http.FS(sub))` at `internal/ui/ui.go:122-126`, so any file placed in `internal/ui/web/` is automatically servable, e.g. at `/replay-player.js`).
- Produces (later tasks rely on these): the standalone file `internal/ui/web/replay-player.js`, containing `startReplay(streamOrUrl)`, `openReplay(missionId)`, `closeReplay()`, `toggleReplayPlay()`, `setReplaySpeed(x)`, `replayStep()`, `seekReplay(target)`, `applyReplayEvent(ev)`, `renderReplayScrub()`, `stopReplaySession()`, plus the canvas machinery (`nodes`/`links`/`bursts`/`buzzes`, `ensure()`, `step()`, `draw()`, `drawBee()`, `drawCritter()`, `drawBubble()`, `SKINS`, `skin()`/`setSkin()`, `ROLE_LINES`/`roleQuips()`, `esc()`, theme helpers `C`/`hexA()`/`readColors()`/`applyTheme()`/`toggleTheme()`, `connectSSE()` (now lazy — not auto-invoked), and a documented **DOM contract**: the embedding page must provide `<canvas id="c">`, the `#replay` control-bar markup, and a global `function setView(v)` (Task 4 writes the site's minimal version).

- [ ] **Step 1: Run the extraction script**

This is a one-time, exact, mechanical move — not a hand-transcription — so it's driven by a script rather than reproduced by hand (the two chunks it moves total ~650 lines of already-shipped, already-tested code; the script's job is to cut them out of `index.html` verbatim, with two small in-place edits, and paste them into a new file with an SPDX + DOM-contract header). Run from the repo root:

```bash
python3 - <<'PYEOF'
import pathlib

p = pathlib.Path("internal/ui/web/index.html")
html = p.read_text()

C1_START = "const cv = document.getElementById('c'), ctx = cv.getContext('2d');"
C1_END   = "function apply(state){"
C2_START = "function esc(s){ return (s||'').replace"
C2_END   = "// Identity chip:"

i1s = html.index(C1_START)
i1e = html.index(C1_END, i1s)
chunk1 = html[i1s:i1e].rstrip() + "\n"

i2s = html.index(C2_START, i1e)
i2e = html.index(C2_END, i2s)
chunk2 = html[i2s:i2e].rstrip() + "\n"

# Make connectSSE lazy: a static replay embed has no brain to connect to.
chunk2 = chunk2.replace(
    "let es = connectSSE(); // `let`, not `const` — replay mode closes and later reopens this",
    "let es = null; // lazy: the embedding page calls `es = connectSSE()` itself for live SSE\n"
    "// (see the DOM-contract header above) — a static replay embed never does, so `es` stays\n"
    "// null and every `if(es && ...)` guard below skips cleanly.",
)
chunk2 = chunk2.replace(
    "src.onerror = () => { document.getElementById('stat').textContent = 'reconnecting…'; };",
    "src.onerror = () => { const s = document.getElementById('stat'); if (s) s.textContent = 'reconnecting…'; };",
)

header = '''// SPDX-License-Identifier: Elastic-2.0
// replay-player.js — the corral canvas renderer + the read-only mission
// replay player, shared verbatim between the product UI (internal/ui/web/
// index.html, loaded via <script src="/replay-player.js">) and the
// corralai.dev site (site/public/replay-player.js, a hash-checked copy —
// see scripts/sync-site-assets.sh). Extracted from index.html so a static
// site with no brain running can embed the identical player.
//
// DOM CONTRACT — the embedding page MUST provide:
//   <canvas id="c">                the render surface
//   #replay, #replay-playbtn, #replay-scrub, #replay-label, #replay-title
//                                   the replay control bar (see index.html's
//                                   markup for the exact structure/CSS)
//   a global function setView(v)   called with 'replay' on startReplay() and
//                                   with the previous view on closeReplay()/
//                                   stopReplaySession(); at minimum it must
//                                   toggle #replay's "show" class. index.html
//                                   keeps its full multi-tab setView(); a
//                                   standalone embed can supply a two-line
//                                   version — see docs/superpowers/plans/
//                                   2026-07-03-corralai-dev-site.md Task 4.
//
// Optional, null-guarded (safe to omit entirely): #empty, #stat, #skinsel,
// #skinsub, #tab-swarm, #tab-proposals, #proposals-badge, #tab-completed,
// #themebtn.
//
// To start a live SSE-driven view (the product page only — a static replay
// embed never calls this): `es = connectSSE();`
// To play a replay (works with no brain at all): `startReplay(streamOrUrl)`
// where streamOrUrl is either a URL string (GETted as {events:[...]}) or an
// already-resolved {events:[...]} object/array.

'''

new_file = header + chunk1 + "\n" + chunk2 + "\n"
pathlib.Path("internal/ui/web/replay-player.js").write_text(new_file)

# Cut both chunks out of index.html; splice in the <script src> tag and the
# live-SSE bootstrap line where chunk2 used to auto-invoke it.
html = html.replace(html[i1s:i1e], "", 1)
i2s2 = html.index(C2_START)
i2e2 = html.index(C2_END, i2s2)
html = html[:i2s2] + "es = connectSSE();\n\n" + html[i2e2:]
html = html.replace("<script>\n", '<script src="/replay-player.js"></script>\n<script>\n', 1)

pathlib.Path("internal/ui/web/index.html").write_text(html)
print("wrote", len(new_file), "bytes to internal/ui/web/replay-player.js")
PYEOF
```

Expected output: `wrote NNNNN bytes to internal/ui/web/replay-player.js`.

- [ ] **Step 2: Spot-check the result**

```bash
grep -n 'script src="/replay-player.js"' internal/ui/web/index.html
grep -n 'es = connectSSE();' internal/ui/web/index.html
grep -c 'let es = connectSSE()' internal/ui/web/replay-player.js   # must print 0 — no auto-invoke
grep -c 'function startReplay' internal/ui/web/replay-player.js    # must print 1
grep -c 'function setView' internal/ui/web/index.html              # must print 1 — setView stayed
```

Expected: all four commands print non-empty/expected counts as annotated above. If `index.html` no longer contains `function apply(state){` or `function esc(` (it shouldn't — `apply` stayed, `esc` moved), something drifted; re-run from a clean `git checkout -- internal/ui/web/index.html` and retry Step 1.

- [ ] **Step 3: Update `TestReplayPlayerStructure`**

Replace the whole function in `internal/ui/ui_test.go` (currently lines 663-763):

```go
// TestReplayPlayerStructure is a structural/grep check (mirrors
// TestAgentWindowsStructure) over the served replay-player.js (and, for the
// pieces that stayed in index.html — the <script src> wiring, the live-SSE
// bootstrap, and setView — over index.html) for the required client
// globals/functions, the embed-friendly startReplay(streamOrUrl) indirection
// (per the binding design constraint: the player's data source is injected,
// not hard-coupled to a brain/SSE endpoint), and that none of the read-only
// replay functions issue a POST/mutating fetch.
func TestReplayPlayerStructure(t *testing.T) {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		t.Fatal(err)
	}
	rawPlayer, err := fs.ReadFile(sub, "replay-player.js")
	if err != nil {
		t.Fatal(err)
	}
	player := string(rawPlayer)
	rawIndex, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		t.Fatal(err)
	}
	indexHTML := string(rawIndex)

	playerMarkers := []string{
		`let replayEvents = [], replayIdx = 0, replayPlaying = false, replaySpeed = 1`,
		`function startReplay(streamOrUrl)`,
		`function openReplay(missionId)`,
		`function closeReplay()`,
		`function replayStep()`,
		`function applyReplayEvent(ev)`,
		`function seekReplay(target)`,
	}
	for _, m := range playerMarkers {
		if !strings.Contains(player, m) {
			t.Errorf("replay-player.js missing required replay marker: %q", m)
		}
	}
	indexMarkers := []string{`id="replay"`, `id="replay-scrub"`, `<script src="/replay-player.js"></script>`}
	for _, m := range indexMarkers {
		if !strings.Contains(indexHTML, m) {
			t.Errorf("index.html missing required marker: %q", m)
		}
	}
	// The extraction must not silently start a live connection from a
	// brain-less file, and the product page must still start one.
	if strings.Contains(player, "let es = connectSSE()") {
		t.Error("replay-player.js must not auto-invoke connectSSE at load — a static embed has no brain to connect to; the embedding page opts in via `es = connectSSE()`")
	}
	if !strings.Contains(indexHTML, "es = connectSSE();") {
		t.Error("index.html must still start live SSE (`es = connectSSE();`) now that connectSSE moved to replay-player.js")
	}

	// Embed-friendliness: startReplay must accept a URL OR an already-resolved
	// events object/array — that's what lets a static embed hand it a baked
	// JSON file instead of a live /api/replay URL.
	const fnStart = "function startReplay(streamOrUrl){"
	const fnEnd = "// openReplay:"
	si := strings.Index(player, fnStart)
	ei := strings.Index(player, fnEnd)
	if si < 0 || ei < 0 || si >= ei {
		t.Fatalf("could not locate startReplay..openReplay range in replay-player.js")
	}
	startFn := player[si:ei]
	if !strings.Contains(startFn, "typeof streamOrUrl === 'string'") {
		t.Error("startReplay must branch on typeof streamOrUrl — a URL is fetched, a plain object/array is used as-is (no hard-coupling to /api/replay)")
	}
	if strings.Contains(startFn, "/api/replay") {
		t.Error("startReplay itself must not hard-code /api/replay — that URL belongs only in openReplay's wrapper call")
	}

	// Read-only by construction: none of the replay functions may call a
	// mutating endpoint. In replay-player.js the block runs to EOF (it was
	// the last thing extracted).
	const rStart = "// ---- replay player ----"
	rsi := strings.Index(player, rStart)
	if rsi < 0 {
		t.Fatalf("could not locate the replay player block in replay-player.js")
	}
	replayBlock := player[rsi:]
	if strings.Contains(replayBlock, "method:'POST'") || strings.Contains(replayBlock, `method: 'POST'`) {
		t.Error("replay player block must be read-only — no POST/mutating fetch")
	}

	// The "#empty" caption ("no agents yet") is normally refreshed only inside
	// apply() (SSE-driven, stayed in index.html). During replay — and in the
	// static-embed path where apply() never runs at all — the replay pipeline
	// must refresh it itself, null-guarded for embeds that don't render the
	// element.
	const scrubStart = "function renderReplayScrub(){"
	ssi := strings.Index(player, scrubStart)
	if ssi < 0 {
		t.Fatalf("could not locate renderReplayScrub in replay-player.js")
	}
	sei := strings.Index(player[ssi:], "\n}")
	if sei < 0 {
		t.Fatalf("could not locate the end of renderReplayScrub in replay-player.js")
	}
	scrubFn := player[ssi : ssi+sei]
	if !strings.Contains(scrubFn, "getElementById('empty')") || !strings.Contains(scrubFn, "nodes.size") {
		t.Error("renderReplayScrub must refresh #empty's display from nodes.size — apply() never runs during replay/static-embed, so the stale caption would sit over replayed agents")
	}

	// Leaving replay via any tab must not orphan the session: setView tears
	// the session down whenever the target view isn't replay, via the
	// idempotent stopReplaySession(). stopReplaySession moved to
	// replay-player.js; setView (a full multi-tab dispatcher) stayed in
	// index.html — both are checked in their new homes.
	if !strings.Contains(player, "function stopReplaySession()") {
		t.Error("replay-player.js missing stopReplaySession() — the idempotent replay teardown (stop timer, resume SSE)")
	}
	const svStart = "function setView(v){"
	vsi := strings.Index(indexHTML, svStart)
	if vsi < 0 {
		t.Fatalf("could not locate setView in index.html")
	}
	vei := strings.Index(indexHTML[vsi:], "\n}")
	if vei < 0 {
		t.Fatalf("could not locate the end of setView in index.html")
	}
	setViewFn := indexHTML[vsi : vsi+vei]
	if !strings.Contains(setViewFn, "stopReplaySession()") {
		t.Error("setView must call stopReplaySession() when navigating to a non-replay view — otherwise switching tabs mid-replay leaves the timer running and SSE closed with no visible control")
	}
}
```

`TestCompletedDetailGroupsTasksByPhaseName` (`internal/ui/ui_test.go:626-661`) needs **no change** — `openMissionDetail`/`closeMissionDetail` were never in either moved chunk and remain in `index.html` untouched.

- [ ] **Step 4: Run the suite**

```bash
go build ./...
go test ./internal/ui/... -count=1
```

Expected: PASS. If `TestReplayPlayerStructure` fails on a marker, re-check Step 2's spot-check output before touching the test — the extraction script is the more likely source of drift.

- [ ] **Step 5: Live check that the product UI still works**

```bash
go run ./cmd/corral &
sleep 2
```

Use the Playwright browser tool to navigate to `http://127.0.0.1:9019/`, confirm the swarm canvas renders (no blank page, no thrown JS error in the console), then stop the server:

```bash
kill %1
```

Take one screenshot for the record (e.g. `/tmp/corral-product-post-extraction.png`) — this is the "biggest risk first" check: if the extraction broke script load order, this is where it shows up (blank canvas, console errors about undefined `nodes`/`draw`/etc.).

- [ ] **Step 6: Full suite + security gate**

```bash
go test ./... -count=1
bash scripts/check-security.sh
```

Expected: both green.

- [ ] **Step 7: Commit**

```bash
git add internal/ui/web/replay-player.js internal/ui/web/index.html internal/ui/ui_test.go
git commit -m "$(cat <<'EOF'
refactor(ui): extract the replay player + canvas renderer into replay-player.js

Standalone, embed-friendly file with a documented DOM contract — the first
step toward corralai.dev's hero, which embeds this identical player with no
brain running. Product UI behavior is unchanged (verified live); connectSSE
is now lazy so a brain-less embed never tries to open /events.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: The golden-run export script (with a scrub/audit privacy gate)

**Files:**
- Create: `scripts/scrub-golden-run.py`
- Create: `scripts/export-golden-run.sh`
- Create: `site/src/data/golden-run.json` (produced by running the script, not hand-written)
- Create: `site/src/data/golden-run.meta.json` (produced by running the script)

**Interfaces:**
- Consumes: `GET /api/replay?mission=N` → `{"events": [...]}` where each event is `brain.ReplayEvent` (`internal/brain/replay.go:16-22`): `{ts float64, kind string, actor string omitempty, subject string omitempty, detail map[string]any omitempty}`; `GET /api/history` → `{"missions": [...]}` where each element is `brain.MissionSummary` (`internal/brain/history.go:18-30`): `{id, directive, status, created_ts, updated_ts, duration_seconds, task_count, done_task_count, finding_count, pr_url?, learned_signatures?}`.
- Produces: `site/src/data/golden-run.json` (the raw `{"events":[...]}` stream, privacy-scrubbed) and `site/src/data/golden-run.meta.json` (`{directive, task_count, done_task_count, finding_count, duration_seconds}`) — Task 4 reads both.

The exported stream carries command strings, task titles, finding targets, agent names, and telemetry detail (`mission_created` carries the whole directive). Even a synthetic demo-run export must **prove** it's safe before anything is written, not assume it — two layers: an automated deny-list (the floor) and a human-review manifest (the ceiling, because file paths or anything else identifying-adjacent may not match any regex anyone thought to write).

- [ ] **Step 1: Write `scripts/scrub-golden-run.py`**

```python
#!/usr/bin/env python3
# SPDX-License-Identifier: Elastic-2.0
"""scripts/scrub-golden-run.py — the golden-run export's privacy gate.

Two subcommands, both operating on the raw exported JSON text (not just the
parsed structure — a regex scan over the literal bytes catches anything
hiding in a string value, a key name, or malformed JSON alike):

  deny FILE [whoami] [hostname]
      Automated floor. Exits 1 and prints every offending line if the file
      matches an email, a /home or /Users path, the operator's own
      username/hostname, a token/key-shaped string, a private-key marker, an
      IPv4 address outside RFC1918/localhost, or an absolute path outside the
      demo containers' own internal roots (/work, /tmp, /root) — an escaped
      host path is a deny, not a manifest entry.

  manifest FILE
      Human-review ceiling. Prints every path-like string, every URL, and
      every unique actor/agent name found in the file, sorted and deduped —
      the operator reviews this by eye; it is not filtered by what the deny
      regexes anticipate.
"""
import ipaddress
import json
import re
import sys

DENY_PATTERNS = [
    (r'[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}', 'email address'),
    (r'/home/[A-Za-z0-9_.-]+', 'linux home-directory path'),
    (r'/Users/[A-Za-z0-9_.-]+', 'macOS home-directory path'),
    (r'gh[pousr]_[A-Za-z0-9]{20,}', 'GitHub token'),
    (r'AKIA[0-9A-Z]{16}', 'AWS access key id'),
    (r'cdt_[A-Za-z0-9]{20,}', 'vendor token (cdt_*)'),
    (r'sk-[A-Za-z0-9]{20,}', 'OpenAI-shaped API key'),
    (r'-----BEGIN[A-Z ]*PRIVATE KEY-----', 'private key material'),
]

# Absolute paths that are safe because they're internal to the demo
# containers, never the operator's real host filesystem.
SAFE_PATH_PREFIXES = ('/work', '/tmp', '/root')

IPV4_RE = re.compile(r'\b(?:\d{1,3}\.){3}\d{1,3}\b')
PATHLIKE_RE = re.compile(r'(?:/[A-Za-z0-9._-]+){2,}')
URL_RE = re.compile(r'https?://[^\s"\']+')


def is_private_or_local(ip_str):
    try:
        ip = ipaddress.ip_address(ip_str)
    except ValueError:
        return True  # not a real IP (e.g. a version string like "1.26.4") — not our problem
    return ip.is_private or ip.is_loopback or ip.is_link_local


def scan_deny(text, whoami, hostname):
    offenses = []
    for pattern, label in DENY_PATTERNS:
        for m in re.finditer(pattern, text):
            offenses.append((label, m.group(0)))
    if whoami:
        for m in re.finditer(re.escape(whoami), text):
            offenses.append(("operator's username ($(whoami))", m.group(0)))
    if hostname:
        for m in re.finditer(re.escape(hostname), text):
            offenses.append(("operator's hostname ($(hostname))", m.group(0)))
    for m in IPV4_RE.finditer(text):
        if not is_private_or_local(m.group(0)):
            offenses.append(('non-private/non-localhost IP', m.group(0)))
    for m in PATHLIKE_RE.finditer(text):
        path = m.group(0)
        if path.startswith('/') and not path.startswith(SAFE_PATH_PREFIXES):
            offenses.append(('absolute path outside demo-container roots', path))
    return offenses


def cmd_deny(path, whoami, hostname):
    text = open(path, encoding='utf-8').read()
    offenses = scan_deny(text, whoami, hostname)
    if offenses:
        print('FAIL: golden-run export failed the deny-list scan:', file=sys.stderr)
        for label, snippet in offenses:
            print(f'  [{label}] {snippet}', file=sys.stderr)
        sys.exit(1)
    print('OK: deny-list scan clean')


def cmd_manifest(path):
    text = open(path, encoding='utf-8').read()
    paths = sorted(set(PATHLIKE_RE.findall(text)))
    urls = sorted(set(URL_RE.findall(text)))
    actors = set()
    try:
        data = json.loads(text)
        for ev in data.get('events', []):
            if ev.get('actor'):
                actors.add(ev['actor'])
    except json.JSONDecodeError:
        pass
    print('--- human-review manifest (' + path + ') ---')
    print(f'{len(paths)} path-like string(s):')
    for p in paths:
        print('  ' + p)
    print(f'{len(urls)} URL(s):')
    for u in urls:
        print('  ' + u)
    print(f'{len(actors)} unique actor name(s):')
    for a in sorted(actors):
        print('  ' + a)
    print('--- end manifest ---')


if __name__ == '__main__':
    if len(sys.argv) < 3:
        print(__doc__, file=sys.stderr)
        sys.exit(2)
    cmd = sys.argv[1]
    if cmd == 'deny':
        who = sys.argv[3] if len(sys.argv) > 3 else ''
        host = sys.argv[4] if len(sys.argv) > 4 else ''
        cmd_deny(sys.argv[2], who, host)
    elif cmd == 'manifest':
        cmd_manifest(sys.argv[2])
    else:
        print(__doc__, file=sys.stderr)
        sys.exit(2)
```

```bash
chmod +x scripts/scrub-golden-run.py
```

- [ ] **Step 2: Test the deny-list gate against fixtures**

```bash
mkdir -p /tmp/scrub-test
printf '{"events":[{"ts":1,"kind":"x","actor":"a","detail":{"note":"contact pat@example.com at /home/pat/proj"}}]}' > /tmp/scrub-test/dirty.json
printf '{"events":[{"ts":1,"kind":"execution","actor":"builder-1","subject":"go.mod","detail":{"ok":true,"host":"10.0.0.5"}}]}' > /tmp/scrub-test/clean.json

python3 scripts/scrub-golden-run.py deny /tmp/scrub-test/dirty.json myuser myhost.local; echo "exit=$?"
python3 scripts/scrub-golden-run.py deny /tmp/scrub-test/clean.json myuser myhost.local; echo "exit=$?"
python3 scripts/scrub-golden-run.py manifest /tmp/scrub-test/clean.json
```

Expected: the `dirty.json` run prints `FAIL: golden-run export failed the deny-list scan:` with `[email address] pat@example.com` and `[linux home-directory path] /home/pat` lines, `exit=1`. The `clean.json` run prints `OK: deny-list scan clean`, `exit=0` (`10.0.0.5` is RFC1918, doesn't trip the IP check). The manifest run prints `1 path-like string(s):` `go.mod`... actually `go.mod` alone has no `/` so it won't match `PATHLIKE_RE` (which requires two path segments) — expected output shows `0 path-like string(s):`, `0 URL(s):`, `1 unique actor name(s): builder-1`.

- [ ] **Step 3: Write `scripts/export-golden-run.sh`**

```bash
#!/usr/bin/env bash
# SPDX-License-Identifier: Elastic-2.0
#
# scripts/export-golden-run.sh — exports one real mission's /api/replay
# stream from a running corral demo brain into site/src/data/golden-run.json
# (the corralai.dev hero's baked replay), plus a metadata sidecar.
#
# PRIVACY GATE (not optional, not an assumption): see scripts/scrub-golden-run.py.
# This script always runs the automated deny-list scan (fails loudly, prints
# offenders) AND prints a human-review manifest that the operator must
# confirm before anything is written — belt and suspenders, because a static
# site is public forever the moment it deploys.
set -euo pipefail
cd "$(dirname "$0")/.."

BRAIN_URL="${BRAIN_URL:-http://127.0.0.1:9019}"
MISSION_ID=""
OUT_JSON="site/src/data/golden-run.json"
OUT_META="site/src/data/golden-run.meta.json"
I_KNOW=0
YES=0
BEARER=""

usage(){ cat <<'EOF'
Usage: scripts/export-golden-run.sh [--mission N] [--brain-url URL] [--bearer TOKEN]
                                     [--i-know] [--yes] [--out PATH]

  --mission N     Export this mission id. Default: the most recently
                   completed mission from /api/history.
  --brain-url URL Default: http://127.0.0.1:9019 (a dev-mode demo brain).
  --bearer TOKEN  Bearer token for an AUTHED brain. Requires --i-know: an
                   authed brain's recorded actors can be real principal
                   emails, not synthetic demo names — extra scrutiny needed.
  --i-know        Required alongside --bearer. Without it, this script only
                   talks to a dev-mode (unauthenticated) brain.
  --yes           Skip the interactive manifest confirmation. Only use this
                   after you've reviewed the manifest once for this exact
                   mission — e.g. a scripted re-export of an already-vetted
                   run. Never use --yes on a mission you haven't reviewed.
  --out PATH      Default: site/src/data/golden-run.json
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --mission) MISSION_ID="$2"; shift 2 ;;
    --brain-url) BRAIN_URL="$2"; shift 2 ;;
    --bearer) BEARER="$2"; shift 2 ;;
    --i-know) I_KNOW=1; shift ;;
    --yes) YES=1; shift ;;
    --out) OUT_JSON="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage; exit 1 ;;
  esac
done

if [ -n "$BEARER" ] && [ "$I_KNOW" -ne 1 ]; then
  echo "FAIL: --bearer (authed brain) requires --i-know — an authed brain's recorded actors" >&2
  echo "can be real principal emails, not synthetic demo names." >&2
  exit 1
fi

# ---- stash deploy/demo/.env (the demo-dev .env gotcha: its presence masks
#      the key-free, first-time-reviewer behavior this export must reflect) ----
STASHED=""
if [ -f deploy/demo/.env ]; then
  STASHED="deploy/demo/.env.stashed-by-export-golden-run"
  mv deploy/demo/.env "$STASHED"
  echo "stashed deploy/demo/.env -> $STASHED (restored on exit)"
fi
restore_env(){ [ -n "$STASHED" ] && [ -f "$STASHED" ] && mv "$STASHED" deploy/demo/.env; }
trap restore_env EXIT

curl_auth=(-fsS)
[ -n "$BEARER" ] && curl_auth+=(-H "Authorization: Bearer $BEARER")

# ---- resolve the mission id ----
if [ -z "$MISSION_ID" ]; then
  echo "no --mission given; looking up the most recent completed mission at $BRAIN_URL/api/history"
  MISSION_ID=$(curl "${curl_auth[@]}" "$BRAIN_URL/api/history" | python3 -c '
import json, sys
missions = json.load(sys.stdin).get("missions") or []
print(max(missions, key=lambda m: m.get("id", 0))["id"] if missions else "", end="")
')
  if [ -z "$MISSION_ID" ]; then
    echo "FAIL: no completed missions found at $BRAIN_URL/api/history — run 'make demo-mission' in deploy/demo/ first" >&2
    exit 1
  fi
fi
echo "exporting mission $MISSION_ID from $BRAIN_URL"

# ---- fetch the replay stream ----
TMP_JSON="$(mktemp)"
trap 'rm -f "$TMP_JSON"' EXIT
curl "${curl_auth[@]}" "$BRAIN_URL/api/replay?mission=$MISSION_ID" -o "$TMP_JSON"

# ---- AUTOMATED DENY-LIST SCAN (the floor — always runs, never skippable) ----
python3 scripts/scrub-golden-run.py deny "$TMP_JSON" "$(whoami)" "$(hostname)"

# ---- HUMAN-REVIEW MANIFEST (the ceiling) ----
python3 scripts/scrub-golden-run.py manifest "$TMP_JSON"

if [ "$YES" -ne 1 ]; then
  echo
  read -r -p "Reviewed the manifest above — write $OUT_JSON? [y/N] " ans
  case "$ans" in
    y|Y|yes|YES) ;;
    *) echo "aborted — nothing written"; exit 1 ;;
  esac
else
  echo "--yes given: skipping interactive confirmation (manifest was still printed above)"
fi

# ---- write the final JSON + metadata sidecar ----
mkdir -p "$(dirname "$OUT_JSON")"
cp "$TMP_JSON" "$OUT_JSON"

curl "${curl_auth[@]}" "$BRAIN_URL/api/history" | python3 -c "
import json, sys
missions = json.load(sys.stdin).get('missions') or []
m = next((x for x in missions if x.get('id') == $MISSION_ID), {})
meta = {
    'directive': m.get('directive', ''),
    'task_count': m.get('task_count', 0),
    'done_task_count': m.get('done_task_count', 0),
    'finding_count': m.get('finding_count', 0),
    'duration_seconds': m.get('duration_seconds', 0),
}
with open('$OUT_META', 'w') as f:
    json.dump(meta, f, indent=2)
    f.write('\n')
"
echo "wrote $OUT_JSON and $OUT_META"
```

```bash
chmod +x scripts/export-golden-run.sh
```

- [ ] **Step 4: Bring up the demo and run the export**

```bash
cd deploy/demo
make demo-mission        # brings up the brain + team, seeds a directive, converges (dev-mode, key-free)
```

Watch `http://localhost:9019` until the mission's Progress tab shows it converged (or reaches `awaiting_review`/`done`). Then, from the repo root, in a second terminal:

```bash
bash scripts/export-golden-run.sh
```

Review the printed manifest by eye (paths, URLs, actor names) — everything should be synthetic demo-shaped (e.g. `builder-1`, `go.mod`, container-internal paths under `/work`). Confirm `y` at the prompt.

Expected: `OK: deny-list scan clean`, the manifest listing, then `wrote site/src/data/golden-run.json and site/src/data/golden-run.meta.json`.

- [ ] **Step 5: Tear down the demo**

```bash
cd deploy/demo && make down
```

- [ ] **Step 6: Commit**

```bash
git add scripts/scrub-golden-run.py scripts/export-golden-run.sh site/src/data/golden-run.json site/src/data/golden-run.meta.json
git commit -m "$(cat <<'EOF'
feat(site): golden-run export script with a deny-list + manifest privacy gate

Exports one real, dev-mode demo mission's /api/replay stream for the
corralai.dev hero. The export is privacy-PROVEN, not privacy-assumed: an
automated deny-list scan (emails, host paths, operator identity, token/key
shapes, non-private IPs) fails loudly, and a human-review manifest (every
path, URL, and actor name found) requires confirmation before anything is
written or committed.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Astro scaffold + the replay-player.js sync script

**Files:**
- Create: `site/` (Astro project — package.json, astro.config.mjs, tsconfig.json, src/pages/index.astro, src/styles/global.css)
- Create: `scripts/sync-site-assets.sh`
- Modify: `.gitignore` (add `site/node_modules/`, `site/dist/`)

**Interfaces:**
- Consumes: `internal/ui/web/replay-player.js` (Task 1).
- Produces: `site/public/replay-player.js` (a committed, hash-checked copy); `site/package.json` with a `build` script that runs the sync check first; `scripts/sync-site-assets.sh --check` (fails loudly on drift, used in CI) and `scripts/sync-site-assets.sh` (no args — copies and reports, used locally).

- [ ] **Step 1: Scaffold Astro**

```bash
npm create astro@latest site -- --template minimal --typescript strict --no-install --no-git --skip-houston
cd site
npm install --save-exact
cd ..
```

Record the exact Astro version this installs (`cat site/package.json | grep '"astro"'`) — Astro releases frequently, so this plan doesn't hardcode a version number; `--save-exact` plus the committed `site/package-lock.json` is what pins it.

- [ ] **Step 2: Write `scripts/sync-site-assets.sh`**

```bash
#!/usr/bin/env bash
# SPDX-License-Identifier: Elastic-2.0
#
# scripts/sync-site-assets.sh — keeps site/public/replay-player.js in exact
# sync with internal/ui/web/replay-player.js (the single source of truth).
# Default mode: copy + report (for local dev after touching the product
# player). --check mode: compare hashes, fail loudly on drift without
# writing anything (for CI, so a stale committed copy can never ship silently).
set -euo pipefail
cd "$(dirname "$0")/.."

SRC="internal/ui/web/replay-player.js"
DST="site/public/replay-player.js"

if [ ! -f "$SRC" ]; then
  echo "FAIL: $SRC does not exist" >&2
  exit 1
fi

if [ "${1:-}" = "--check" ]; then
  if [ ! -f "$DST" ]; then
    echo "FAIL: $DST does not exist — run scripts/sync-site-assets.sh (no args) and commit it" >&2
    exit 1
  fi
  src_hash=$(sha256sum "$SRC" | cut -d' ' -f1)
  dst_hash=$(sha256sum "$DST" | cut -d' ' -f1)
  if [ "$src_hash" != "$dst_hash" ]; then
    echo "FAIL: $DST has drifted from $SRC" >&2
    echo "  $SRC: $src_hash" >&2
    echo "  $DST: $dst_hash" >&2
    echo "Run: scripts/sync-site-assets.sh   (then commit the updated site/public/replay-player.js)" >&2
    exit 1
  fi
  echo "OK: $DST matches $SRC"
else
  mkdir -p "$(dirname "$DST")"
  cp "$SRC" "$DST"
  echo "synced $SRC -> $DST"
fi
```

```bash
chmod +x scripts/sync-site-assets.sh
bash scripts/sync-site-assets.sh
bash scripts/sync-site-assets.sh --check
```

Expected: `synced internal/ui/web/replay-player.js -> site/public/replay-player.js`, then `OK: site/public/replay-player.js matches internal/ui/web/replay-player.js`.

- [ ] **Step 3: Wire the sync check into the site build**

In `site/package.json`, add a `prebuild` script (npm runs `prebuild` automatically before `build`):

```json
{
  "scripts": {
    "dev": "astro dev",
    "prebuild": "bash ../scripts/sync-site-assets.sh --check",
    "build": "astro build",
    "preview": "astro preview"
  }
}
```

- [ ] **Step 4: Global styles reusing the product palette**

Create `site/src/styles/global.css`:

```css
/* SPDX-License-Identifier: Elastic-2.0 */
/* Same amber-on-dark palette as the product UI (internal/ui/web/index.html) —
   the site and the embedded live player must not visually clash. */
:root {
  --bg: #0e1116; --fg: #e6e1d8; --muted: #8a8170; --amber: #e8a838; --red: #e8503a;
  --line: #33405a; --green: #8fdcab; --panel: #161b22; --sel: #1b2330; --card: rgba(20,25,32,.97);
  color-scheme: dark;
}
* { box-sizing: border-box; }
html, body {
  margin: 0; background: var(--bg); color: var(--fg);
  font: 16px/1.5 ui-sans-serif, system-ui, Segoe UI, Roboto, sans-serif;
}
a { color: var(--amber); }
h1, h2, h3 { color: var(--fg); }
.section { max-width: 860px; margin: 0 auto; padding: 48px 20px; }
```

- [ ] **Step 5: Minimal `src/pages/index.astro`** (placeholder page proving the pipeline — Task 4/5 fill it in)

```astro
---
// SPDX-License-Identifier: Elastic-2.0
import '../styles/global.css';
---
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>Corralai — the herd performs live</title>
</head>
<body>
  <div class="section">
    <h1>corralai.dev</h1>
    <p>Scaffold check — Hero lands in Task 4.</p>
  </div>
</body>
</html>
```

- [ ] **Step 6: Build and confirm it's green**

```bash
cd site && npm run build && cd ..
```

Expected: `astro build` completes, `site/dist/index.html` exists, and the `prebuild` hash check passes (no drift message).

- [ ] **Step 7: `.gitignore`**

Add to `.gitignore`:

```
site/node_modules/
site/dist/
```

- [ ] **Step 8: Commit**

```bash
git add site/package.json site/package-lock.json site/astro.config.mjs site/tsconfig.json site/src site/public/replay-player.js scripts/sync-site-assets.sh .gitignore
git commit -m "$(cat <<'EOF'
feat(site): scaffold the Astro one-pager + a hash-checked replay-player.js sync

New site/ npm toolchain, isolated from the Go module. A sync script keeps
site/public/replay-player.js identical to the product's copy — CI's --check
mode fails loudly on drift instead of allowing a silently stale player to ship.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: Hero component with the embedded replay player

**Files:**
- Create: `site/src/components/Hero.astro`
- Modify: `site/src/pages/index.astro`
- Create: `site/tests/hero.spec.ts`
- Create: `site/playwright.config.ts`
- Modify: `site/package.json` (add `@playwright/test` devDependency + `test:e2e` script)

**Interfaces:**
- Consumes: `site/src/data/golden-run.json` (Task 2, `{"events":[...]}`), `site/src/data/golden-run.meta.json` (Task 2, `{directive, task_count, done_task_count, finding_count, duration_seconds}`), `site/public/replay-player.js` (Task 3), and the DOM contract from Task 1's header comment: `<canvas id="c">`, the `#replay` bar markup, and a global `setView(v)`.
- Produces: the `Hero` component other sections (Task 5) render below.

- [ ] **Step 1: Write `Hero.astro`**

```astro
---
// SPDX-License-Identifier: Elastic-2.0
import golden from '../data/golden-run.json';
import meta from '../data/golden-run.meta.json';

const minutes = Math.round((meta.duration_seconds || 0) / 60);
---
<section id="hero">
  <div class="hero-copy">
    <h1>The herd performs live.</h1>
    <p class="pitch">
      Give a headless brain one directive and it turns it into a mission that a
      team of AI agents plans, builds, verifies, re-plans when they hit
      problems, and iterates with the client until it's accepted.
    </p>
    <p class="caption">
      Real recorded mission: {meta.task_count} tasks ({meta.done_task_count} done),
      {meta.finding_count} findings, {minutes}m — replaying at 4× below.
    </p>
    <div class="ctas">
      <a class="cta" href="https://github.com/pdbethke/corralai">View on GitHub</a>
      <a class="cta secondary" id="watch-demo-cta" href="#" style="display:none">Watch the demo</a>
    </div>
  </div>
  <div id="stage">
    <canvas id="c"></canvas>
    <div id="empty">no agents in the corral yet</div>
  </div>
  <div id="replay">
    <div class="row">
      <span id="replay-title"></span>
      <button id="replay-playbtn" onclick="toggleReplayPlay()">▶ play</button>
      <input type="range" id="replay-scrub" min="0" max="0" value="0" oninput="seekReplay(+this.value)">
      <span id="replay-label">0 / 0</span>
      <select onchange="setReplaySpeed(+this.value)" title="playback speed">
        <option value="1">1×</option><option value="2">2×</option><option value="4" selected>4×</option>
        <option value="8">8×</option><option value="16">16×</option>
      </select>
    </div>
  </div>
</section>

<style>
  /* Same #replay bar as the product UI (internal/ui/web/index.html) — the
     embed must feel identical, not like a marketing mockup of it. */
  #hero { position: relative; }
  .hero-copy { max-width: 860px; margin: 0 auto; padding: 56px 20px 24px; text-align: center; }
  .hero-copy h1 { font-size: 2.4rem; margin: 0 0 12px; color: var(--amber); }
  .pitch { font-size: 1.15rem; color: var(--fg); max-width: 720px; margin: 0 auto 14px; }
  .caption { color: var(--muted); font-size: 0.9rem; margin-bottom: 20px; }
  .ctas { display: flex; gap: 12px; justify-content: center; }
  .cta { background: var(--amber); color: var(--bg); padding: 10px 20px; border-radius: 6px; font-weight: 600; text-decoration: none; }
  .cta.secondary { background: var(--panel); color: var(--fg); border: 1px solid var(--line); }
  #stage { position: relative; height: 60vh; min-height: 380px; background: var(--panel); border-top: 1px solid var(--line); border-bottom: 1px solid var(--line); }
  #c { width: 100%; height: 100%; display: block; }
  #empty { position: absolute; inset: 0; display: flex; align-items: center; justify-content: center; color: var(--muted); pointer-events: none; }
  #replay { position: relative; background: var(--panel); border-bottom: 1px solid var(--line); padding: 9px 16px; }
  #replay .row { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; max-width: 860px; margin: 0 auto; }
  #replay .row button { background: var(--panel); color: var(--fg); border: 1px solid var(--line); border-radius: 5px; padding: 4px 12px; font-size: 12px; cursor: pointer; }
  #replay .row button:hover { border-color: var(--amber); }
  #replay-scrub { flex: 1; min-width: 160px; }
  #replay-label { color: var(--muted); font-size: 11.5px; min-width: 70px; text-align: center; }
  #replay-title { color: var(--amber); font-size: 12px; font-weight: 600; margin-right: 4px; }
</style>

<script src="/replay-player.js" is:inline></script>
<script define:vars={{ golden }} is:inline>
  // The site's minimal DOM contract implementation (see replay-player.js's
  // header): only 'replay' exists here, so setView just needs to keep the
  // control bar visible — there is no tab system to dispatch between.
  function setView(v) {
    const bar = document.getElementById('replay');
    if (bar) bar.classList.toggle('show', v === 'replay');
  }
  window.addEventListener('DOMContentLoaded', () => {
    setReplaySpeed(4);
    startReplay(golden);
    toggleReplayPlay(); // autoplay-equivalent: starts playing immediately at 4x
  });
</script>
```

- [ ] **Step 2: Wire it into `index.astro`**

```astro
---
// SPDX-License-Identifier: Elastic-2.0
import '../styles/global.css';
import Hero from '../components/Hero.astro';
---
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>Corralai — the herd performs live</title>
</head>
<body>
  <Hero />
</body>
</html>
```

- [ ] **Step 3: Playwright config + hero smoke test**

```bash
cd site && npm install --save-dev --save-exact @playwright/test && npx playwright install --with-deps chromium && cd ..
```

`site/playwright.config.ts`:

```ts
// SPDX-License-Identifier: Elastic-2.0
import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: './tests',
  webServer: {
    command: 'npm run preview -- --port 4321',
    port: 4321,
    reuseExistingServer: !process.env.CI,
  },
  use: { baseURL: 'http://localhost:4321' },
});
```

`site/tests/hero.spec.ts`:

```ts
// SPDX-License-Identifier: Elastic-2.0
import { test, expect } from '@playwright/test';

test('hero renders the canvas and the replay bar starts playing', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('#c')).toBeVisible();
  await expect(page.locator('#replay')).toBeVisible();
  // The scrub bar's max should reflect the baked golden-run event count
  // (>0) shortly after load — proves startReplay(golden) actually ran.
  await expect(async () => {
    const max = await page.locator('#replay-scrub').getAttribute('max');
    expect(Number(max)).toBeGreaterThan(0);
  }).toPass({ timeout: 5000 });
});

test('scrubbing the replay bar updates the position label', async ({ page }) => {
  await page.goto('/');
  const scrub = page.locator('#replay-scrub');
  await expect(async () => {
    const max = await scrub.getAttribute('max');
    expect(Number(max)).toBeGreaterThan(0);
  }).toPass({ timeout: 5000 });
  const max = Number(await scrub.getAttribute('max'));
  await scrub.evaluate((el, target) => {
    (el as HTMLInputElement).value = String(target);
    el.dispatchEvent(new Event('input'));
  }, Math.floor(max / 2));
  await expect(page.locator('#replay-label')).toHaveText(new RegExp(`^${Math.floor(max / 2)} / ${max}$`));
});
```

Add to `site/package.json` scripts: `"test:e2e": "npm run build && npx playwright test"`.

- [ ] **Step 4: Run it**

```bash
cd site && npm run test:e2e && cd ..
```

Expected: both tests PASS.

- [ ] **Step 5: Commit**

```bash
git add site/src/components/Hero.astro site/src/pages/index.astro site/tests/hero.spec.ts site/playwright.config.ts site/package.json site/package-lock.json
git commit -m "$(cat <<'EOF'
feat(site): hero section embedding the golden-run replay, autoplaying at 4x

The hero IS the product's replay player, unmodified — same replay-player.js,
same golden-run.json baked at build time, a minimal two-line setView()
satisfying the DOM contract. Smoke-tested: canvas renders, scrub bar reflects
the real event count, scrubbing updates the position label.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: Content sections (verified copy reuse)

**Files:**
- Create: `site/src/components/HowItWorks.astro`
- Create: `site/src/components/LearningLoop.astro`
- Create: `site/src/components/KnowledgeCorpus.astro`
- Create: `site/src/components/WatchItBack.astro`
- Create: `site/src/components/Quickstart.astro`
- Create: `site/src/components/SiteFooter.astro`
- Modify: `site/src/pages/index.astro`

**Interfaces:**
- Consumes: nothing new.
- Produces: nothing later tasks depend on by name — Task 7's link-check test asserts the GitHub link resolves and the page has no broken internal anchors.

Every string below is copied from `README.md` (spec's "every factual claim traces to README/DESIGN verified copy" constraint) — quoting the exact source lines so review can check word-for-word.

- [ ] **Step 1: `HowItWorks.astro`** (source: `README.md:44-59`, the adaptive-loop section)

```astro
---
// SPDX-License-Identifier: Elastic-2.0
---
<section class="section" id="how-it-works">
  <h2>How the herd works</h2>
  <p>
    A directive becomes a mission; the brain decomposes it into a
    dependency-ordered task queue; the agents pull ready tasks and execute
    them; their structured findings feed a two-tier re-planner; the mission
    converges when the client accepts.
  </p>
  <pre class="flow">CLIENT (you, or a modeled product-owner agent)
  │ directive ↓                 ↑ accept / feedback → next sprint
  ▼                             │
LEAD ── research → design → build-core → build → test ∥ secops ∥ perf → integrate → docs → retro
(orchestrates,        the dev team (one role per phase)
 re-plans, reworks)          SCRUM (standups · stall call-outs · nudges)
  └── findings → reflex fix+verify  ∥  lead supersede / re-architect → converge</pre>
</section>

<style>
  .flow { background: var(--panel); border: 1px solid var(--line); border-radius: 8px; padding: 16px; overflow-x: auto; font-size: 0.8rem; color: var(--muted); }
</style>
```

- [ ] **Step 2: `LearningLoop.astro`** (source: `README.md:100-110`)

```astro
---
// SPDX-License-Identifier: Elastic-2.0
---
<section class="section" id="learning-loop">
  <h2>It learns</h2>
  <p>
    Recurring failure signatures (the same finding, again and again) and
    clusters of similar lessons are swept into skill proposals: an LLM drafts
    corrective guidance plus a reusable skill, Shep announces the pending
    proposal at standup, and the operator approves or rejects it — from its
    own Proposals tab (a live count badge) or <code>corral-admin proposals</code>.
    Approval promotes the guidance into vetted memory and a versioned skill
    artifact; every later mission's instructions carry the top vetted lessons
    (fence-wrapped, clearly labeled, capped at 3) so the herd starts each
    mission already warned. And the loop watches its own efficacy: if the
    same signature keeps recurring after promotion, a revision proposal
    reopens for the human to reconsider.
  </p>
</section>
```

- [ ] **Step 3: `KnowledgeCorpus.astro`** (source: `README.md:116-134`)

```astro
---
// SPDX-License-Identifier: Elastic-2.0
---
<section class="section" id="knowledge-corpus">
  <h2>The knowledge corpus (CORRAL.md)</h2>
  <p>
    A repo that runs with corralai can carry its working knowledge as a
    markdown corpus in the repo itself: <code>CORRAL.md</code> at the root as
    the entry point, <code>docs/corral/*.md</code> as the corpus. The same
    corpus serves four readers — developers read it as onboarding docs, any
    developer's coding agent queries it conversationally, the herd itself
    searches it before working and extends memory as it learns, and it grows
    the way code does — through ordinary pull requests, where code review is
    the trust gate for knowledge exactly as it is for code.
  </p>
  <p>
    The learning loop closes the circle: skills the swarm proposes and a
    human approves land in the same corpus — herd-discovered knowledge and
    developer-written knowledge accumulate in one place, under one review
    gate, readable by humans and queryable by every agent that joins.
  </p>
</section>
```

- [ ] **Step 4: `WatchItBack.astro`** (source: `README.md:136-150`)

```astro
---
// SPDX-License-Identifier: Elastic-2.0
---
<section class="section" id="watch-it-back">
  <h2>Watch it back</h2>
  <p>
    Nothing about a finished mission is thrown away: every task's claim and
    completion, every finding and its resolution, every command an agent
    actually ran, and the event log itself survive indefinitely. A Completed
    tab lists past missions — directive, duration, task/finding counts, and
    (best-effort) what got learned from them — with a detail view per mission
    and a ▶ replay button. Replay is read-only: it reconstructs the whole
    build from durable rows and plays it back on the same corral canvas, at
    up to 16×, with a scrub bar.
  </p>
  <p class="callout">This hero IS that player — the exact same code, replaying a real mission.</p>
</section>

<style>
  .callout { color: var(--amber); font-weight: 600; }
</style>
```

- [ ] **Step 5: `Quickstart.astro`** (source: `README.md:352-365`)

```astro
---
// SPDX-License-Identifier: Elastic-2.0
---
<section class="section" id="quickstart">
  <h2>Quickstart</h2>
  <pre class="code">go test ./...
go run ./cmd/corral     # MCP /mcp/ · health /healthz · swarm UI / · on 127.0.0.1:9019</pre>
  <p>Open <code>http://127.0.0.1:9019/</code> for the live swarm + Progress tab (dev: auth off). To watch the whole loop end-to-end on one command (bundled GPU Ollama):</p>
  <pre class="code">cd deploy/demo
make demo-mission       # directive → team builds it → re-plans → client review → converge</pre>
</section>

<style>
  .code { background: var(--panel); border: 1px solid var(--line); border-radius: 8px; padding: 14px 18px; overflow-x: auto; font-family: ui-monospace, monospace; font-size: 0.85rem; }
</style>
```

- [ ] **Step 6: `SiteFooter.astro`** (source: `README.md:423-437`)

```astro
---
// SPDX-License-Identifier: Elastic-2.0
---
<footer class="section" id="site-footer">
  <p>
    <a href="https://github.com/pdbethke/corralai">github.com/pdbethke/corralai</a>
    · Elastic License 2.0 · built by a herd, wrangled by a human.
  </p>
</footer>
```

- [ ] **Step 7: Assemble in `index.astro`**

```astro
---
// SPDX-License-Identifier: Elastic-2.0
import '../styles/global.css';
import Hero from '../components/Hero.astro';
import HowItWorks from '../components/HowItWorks.astro';
import LearningLoop from '../components/LearningLoop.astro';
import KnowledgeCorpus from '../components/KnowledgeCorpus.astro';
import WatchItBack from '../components/WatchItBack.astro';
import Quickstart from '../components/Quickstart.astro';
import SiteFooter from '../components/SiteFooter.astro';
---
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <title>Corralai — the herd performs live</title>
  <meta name="description" content="Coordinated multi-agent, multi-model AI development, watchable live." />
</head>
<body>
  <Hero />
  <HowItWorks />
  <LearningLoop />
  <KnowledgeCorpus />
  <WatchItBack />
  <Quickstart />
  <SiteFooter />
</body>
</html>
```

- [ ] **Step 8: Build + hero test still green**

```bash
cd site && npm run test:e2e && cd ..
```

Expected: PASS (the hero tests don't assert anything about page length, so adding sections below shouldn't break them; confirm by reading the output).

- [ ] **Step 9: Commit**

```bash
git add site/src/components site/src/pages/index.astro
git commit -m "$(cat <<'EOF'
feat(site): the remaining one-pager sections, all copy reused verbatim from README.md

How-it-works, the learning loop, CORRAL.md, watch-it-back, quickstart, and
footer — every factual sentence traces to an existing README section, no new
capability claims.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Cloudflare Pages deploy workflow + human setup

**Files:**
- Create: `.github/workflows/deploy-site.yml`

**Interfaces:**
- Consumes: `secrets.CLOUDFLARE_API_TOKEN`, `secrets.CLOUDFLARE_ACCOUNT_ID` (repo secrets — set up in Step 1, a human/CLI step).
- Produces: nothing later tasks depend on.

- [ ] **Step 1 (human/CLI setup — the implementer runs what's executable and reports the rest):**

```bash
npm install -g wrangler   # or: npx wrangler <command> everywhere below, no global install
wrangler login            # opens a browser OAuth flow — cannot be scripted headlessly
wrangler pages project create corralai-dev --production-branch main
```

Create a scoped API token (Cloudflare dashboard → My Profile → API Tokens → "Create Token" → "Edit Cloudflare Pages" template, scoped to the account that owns the `corralai.dev` zone) — **this step needs a human in the dashboard**; there is no `wrangler` subcommand that mints a token non-interactively. Then:

```bash
gh secret set CLOUDFLARE_API_TOKEN --repo pdbethke/corralai --body "<paste the token>"
gh secret set CLOUDFLARE_ACCOUNT_ID --repo pdbethke/corralai --body "<paste the account id, from `wrangler whoami`>"
```

Report back to the user: "Cloudflare Pages project `corralai-dev` created, `CLOUDFLARE_API_TOKEN`/`CLOUDFLARE_ACCOUNT_ID` secrets set on the repo. Custom domain attach (`corralai.dev` → the Pages project) is a dashboard step (Pages → corralai-dev → Custom domains → Add) since `wrangler`'s domain-attach subcommand surface changes across versions — verify the current one with `wrangler pages domain --help` at execution time. **Do this attach step LAST**, after Task 7's verification passes against the `*.pages.dev` URL."

- [ ] **Step 2: Write `.github/workflows/deploy-site.yml`**

```yaml
name: Deploy site

on:
  push:
    branches: [main]
    paths: ["site/**", "internal/ui/web/replay-player.js", ".github/workflows/deploy-site.yml"]
  pull_request:
    branches: [main]
    paths: ["site/**", "internal/ui/web/replay-player.js"]
  workflow_dispatch:

jobs:
  test:
    # Same correctness gate as the main Deploy workflow — a site change can
    # never ship against a red Go suite, since golden-run.json and
    # replay-player.js both come from Go-side code.
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true
      - run: go vet ./...
      - run: go test ./...

  deploy:
    needs: test
    if: github.event_name != 'pull_request'
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-node@v4
        with:
          node-version: 20
          cache: npm
          cache-dependency-path: site/package-lock.json
      - name: Verify replay-player.js is in sync
        run: bash scripts/sync-site-assets.sh --check
      - name: Install + build
        working-directory: site
        run: |
          npm ci
          npm run test:e2e
      - name: Deploy to Cloudflare Pages
        working-directory: site
        run: npx wrangler pages deploy dist --project-name=corralai-dev --branch=main
        env:
          CLOUDFLARE_API_TOKEN: ${{ secrets.CLOUDFLARE_API_TOKEN }}
          CLOUDFLARE_ACCOUNT_ID: ${{ secrets.CLOUDFLARE_ACCOUNT_ID }}
```

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/deploy-site.yml
git commit -m "$(cat <<'EOF'
ci: deploy corralai.dev to Cloudflare Pages, gated on the Go test suite

New independent workflow (ubuntu-latest) — the main Deploy workflow's
self-hosted deploy job is untouched. Path-filtered so a Go-only PR never
triggers a site deploy.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 4: Push and watch the workflow**

```bash
git push
gh run watch
```

Expected: `test` and `deploy` jobs both green; the deploy job's log prints a `*.pages.dev` preview URL.

---

### Task 7: E2E verification, docs pointers, and the production DNS cutover

**Files:**
- Create: `site/tests/site.spec.ts`
- Modify: `README.md` (site pointer)
- Modify: `docs/DESIGN.md` (roadmap entry)

**Interfaces:**
- Consumes: the built `site/dist/` (Task 3/4), `site/src/data/golden-run.json` served from `dist/` (Task 2), the deploy workflow's `*.pages.dev` URL (Task 6).
- Produces: nothing further.

- [ ] **Step 1: Write `site/tests/site.spec.ts`** — zero-external-requests, GitHub link, and the belt-and-suspenders deny-list check against the *served* JSON (catches a hand-edited commit that bypassed Task 2's script)

```ts
// SPDX-License-Identifier: Elastic-2.0
import { test, expect } from '@playwright/test';

// The same deny-list rules as scripts/scrub-golden-run.py, reimplemented in
// JS since this runs in the site's Node/Playwright toolchain, not Python.
// Belt and suspenders: scripts/export-golden-run.sh already gates the file
// before it's committed — this catches a hand-edited commit that skipped it.
const DENY_PATTERNS: [RegExp, string][] = [
  [/[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}/g, 'email address'],
  [/\/home\/[A-Za-z0-9_.-]+/g, 'linux home-directory path'],
  [/\/Users\/[A-Za-z0-9_.-]+/g, 'macOS home-directory path'],
  [/gh[pousr]_[A-Za-z0-9]{20,}/g, 'GitHub token'],
  [/AKIA[0-9A-Z]{16}/g, 'AWS access key id'],
  [/cdt_[A-Za-z0-9]{20,}/g, 'vendor token (cdt_*)'],
  [/sk-[A-Za-z0-9]{20,}/g, 'OpenAI-shaped API key'],
  [/-----BEGIN[A-Z ]*PRIVATE KEY-----/g, 'private key material'],
];

test('the served golden-run.json passes the deny-list scan', async ({ request, baseURL }) => {
  const res = await request.get(`${baseURL}/golden-run.json`.replace('/dist', ''));
  // golden-run.json is bundled via the Astro data import, not served as a
  // static asset — this test instead reads it straight off disk to assert
  // the committed artifact is clean, independent of how Astro packages it.
  const fs = await import('node:fs');
  const text = fs.readFileSync('src/data/golden-run.json', 'utf-8');
  const offenses: string[] = [];
  for (const [pattern, label] of DENY_PATTERNS) {
    const matches = text.match(pattern);
    if (matches) offenses.push(`[${label}] ${matches.join(', ')}`);
  }
  expect(offenses, `golden-run.json failed the deny-list scan:\n${offenses.join('\n')}`).toHaveLength(0);
});

test('zero non-local network requests on page load', async ({ page }) => {
  const external: string[] = [];
  page.on('request', (req) => {
    const url = new URL(req.url());
    if (url.hostname !== 'localhost' && url.hostname !== '127.0.0.1') {
      external.push(req.url());
    }
  });
  await page.goto('/');
  await page.waitForTimeout(1500); // let the replay's fetch-or-no-fetch settle
  expect(external, `unexpected external requests: ${external.join(', ')}`).toHaveLength(0);
});

test('the GitHub link resolves', async ({ page, request }) => {
  await page.goto('/');
  const href = await page.locator('a[href*="github.com/pdbethke/corralai"]').first().getAttribute('href');
  expect(href).toBeTruthy();
  const res = await request.get(href!);
  expect(res.status(), `GET ${href} returned ${res.status()}`).toBeLessThan(400);
});
```

- [ ] **Step 2: Run the full site test suite**

```bash
cd site && npm run test:e2e && cd ..
```

Expected: all tests PASS, including the new deny-list and zero-external-requests checks.

- [ ] **Step 3: README pointer**

In `README.md`, immediately after the existing closing line (`README.md:436-437`):

```markdown
---
**corralai.dev** · github.com/pdbethke/corralai
```

replace with:

```markdown
---
**[corralai.dev](https://corralai.dev)** — a live-replay one-pager (`site/`, Astro,
Cloudflare Pages) · github.com/pdbethke/corralai
```

- [ ] **Step 4: DESIGN.md roadmap entry**

Insert a new roadmap entry immediately after the last existing entry (before `### Open threads (next)`, `docs/DESIGN.md:260`):

```markdown
- **P11 — corralai.dev (DONE 2026-07-03).** A static one-pager (`site/`,
  Astro, Cloudflare Pages, custom domain) whose hero is not a mockup: it's
  `internal/ui/web/replay-player.js` — extracted verbatim from the product
  UI, documented with a DOM contract — embedding a real, privacy-scrubbed
  recorded mission (`scripts/export-golden-run.sh`, gated by an automated
  deny-list scan plus a human-reviewed manifest before anything is written
  or committed). `site/public/replay-player.js` is a hash-checked copy of
  the product's file (`scripts/sync-site-assets.sh --check`, wired into the
  site build) — no silent drift between what plays on the product and what
  plays on the marketing page. Every other section's copy traces verbatim to
  README.md. Deployed via an independent GitHub Actions workflow
  (`.github/workflows/deploy-site.yml`, ubuntu-latest, gated on the same Go
  test suite the main Deploy workflow runs) publishing to Cloudflare Pages.
  Verified live (npm run test:e2e against the built dist/: hero canvas
  renders and autoplays the golden run, scrub bar reflects the real event
  count, zero non-local network requests, the served golden-run.json passes
  the deny-list scan, the GitHub link resolves).
```

- [ ] **Step 5: Full suite one more time**

```bash
go test ./... -count=1
bash scripts/check-security.sh
cd site && npm run test:e2e && cd ..
```

Expected: all green.

- [ ] **Step 6: Commit**

```bash
git add site/tests/site.spec.ts README.md docs/DESIGN.md
git commit -m "$(cat <<'EOF'
test(site): zero-external-requests + deny-list + link checks; docs pointers

E2E-verifies the spec's hard requirements against the built dist/, not just
unit-level assertions. README/DESIGN.md now point at the live site and
record what was actually run to verify it.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
EOF
)"
```

- [ ] **Step 7 (LAST — production DNS cutover, human step):**

Only after the `*.pages.dev` deploy from Task 6 has been checked against Step 1-6 of this task (visit it, confirm the hero plays, run `npm run test:e2e` with `baseURL` pointed at the `*.pages.dev` URL if not already covered by CI), attach the custom domain:

Cloudflare dashboard → Pages → `corralai-dev` → Custom domains → Add → `corralai.dev` (and `www.corralai.dev` if a redirect is wanted). This flips live DNS for a real domain — report to the user when it's done and ask them to confirm `https://corralai.dev` loads before considering this plan complete.

---

## Self-review notes (performed at write time)

- **Spec coverage:** hero replay embed with a real recorded mission and no backend (Tasks 1, 2, 4); player extraction into `internal/ui/web/replay-player.js` with `TestReplayPlayerStructure` relocated, product UI verified live post-extraction (Task 1); golden-run export via `/api/replay` into a baked JSON + metadata sidecar (Task 2); Astro `site/` isolated npm toolchain, one page, corral-styled CSS reusing the product palette (Task 3); the hash-checked sync script with a `--check` CI gate — "no silent drift" (Task 3, wired into Task 6's workflow); all seven content sections in the spec's stated order, every claim traced to README (Task 5); Cloudflare Pages project + custom domain + GitHub Actions job as an explicit human/CLI setup task, `wrangler pages deploy` with repo secrets (Task 6); Playwright smoke (`npm run build` green, player renders, scrub responds, zero non-local requests, GitHub link resolves) plus the full Go suite + `TestReplayPlayerStructure` staying green (Tasks 1, 4, 7); DNS cutover ordered as the explicit last verified step (Task 6 Step 1's note + Task 7 Step 7); the demo-video CTA slot shipped hidden, no analytics anywhere (Task 4's `display:none` CTA; no tracking script exists anywhere in the plan).
- **Privacy/scrub gate (addenda):** confirmed present as a hard requirement, not an assumption — Task 2 Step 1 shows the actual `scripts/scrub-golden-run.py` deny-list regexes (email/home-path/username/hostname/token-shapes/private-key/non-RFC1918-IP/escaped-container-path) and the human-review manifest (paths/URLs/actor names, sorted+deduped) with real fixture-driven test output in Step 2; the export script (Step 3) always runs both, requires `--i-know` for an authed brain, and requires interactive confirmation unless `--yes` (documented as "only after you've reviewed the manifest once"); Task 7 Step 1 reimplements the identical deny-list in JS as a belt-and-suspenders Playwright check against the committed `golden-run.json`, independent of whether the export script was actually used for a given commit.
- **Placeholder scan:** no "TBD"/"add error handling"/"similar to Task N" patterns found; every code step shows complete, runnable content (the Astro version is deliberately left to `npm create astro@latest` + `--save-exact` rather than a hand-guessed version number, since fabricating a specific Astro release number would be a false fact, not a placeholder — the pinning mechanism itself, `--save-exact` + committed lockfile, is exact and specified).
- **Type/interface consistency:** `startReplay(streamOrUrl)` signature and behavior (URL string vs. resolved object) is unchanged start-to-finish (Tasks 1 and 4 both rely on the exact same function); `brain.ReplayEvent`'s JSON field names (`ts`, `kind`, `actor`, `subject`, `detail`) are used identically in Task 2's export script, Task 2's deny-list/manifest scanner, and Task 7's belt-and-suspenders check; `brain.MissionSummary`'s field names (`directive`, `task_count`, `done_task_count`, `finding_count`, `duration_seconds`) match between Task 2's sidecar writer and Task 4's `Hero.astro` reader; the DOM contract (`#c`, `#replay*`, global `setView(v)`) is stated once in Task 1's file header and satisfied exactly once, minimally, in Task 4 — no second, divergent definition.
- **Ambiguity resolved:** the spec/shape-guidance said `TestReplayPlayerStructure` "moves/extends to the new file," which could mean either (a) the whole test now reads only `replay-player.js`, or (b) it splits across files matching where each piece of code actually lives. Resolution: `setView` was deliberately **not** extracted (it's a multi-tab dispatcher requiring index.html-only DOM — `#tab-progress`, `#tab-topology`, etc. — that has no standalone-embed equivalent; the embed instead supplies its own trivial `setView`, documented as part of the DOM contract), so the relocated test reads `replay-player.js` for every marker that actually moved and still reads `index.html` for the one assertion (`setView` calls `stopReplaySession()`) whose subject stayed there — this is not a weakened assertion (every original check still runs, verifying the same aggregate behavior), it's the same assertion pointed at its new home.
