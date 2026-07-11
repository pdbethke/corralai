<!-- SPDX-License-Identifier: Elastic-2.0 -->
# The daemon/client architecture — one hosted UI bundle, thin rendering clients

**Date:** 2026-07-10
**Status:** approved (brainstorm) → spec for review
**One line:** Make the brain a true headless **daemon** (CLI-configured, MCP + JSON API, serves
no webapp) that **hosts** a single versioned UI bundle; the **clients** (`corral-observe`,
`corral-admin`, `corral-desktop`) fetch that one bundle, render it locally, and forward
`/api | /mcp | /events` to the daemon. Client-server for the UI; one UI codebase; DRY.

## Why

The thing is a **"headless brain."** Today it isn't headless: `internal/ui` embeds the SPA
(`//go:embed web`) and serves it at `/` via a `FileServer` — the brain *is* a webapp. The
console clients (`internal/console`) are pure reverse proxies that forward the brain's pages
back out with a token injected. So the UI is emitted by the daemon, and the clients are dumb
transports. That contradicts the daemon's identity, and — more importantly for the pivot —
it's the wrong shape for an **accountability data/API backbone** that a distributed dev shop
connects to.

The target is the old backup-system model: a **daemon** on the server does the work and
speaks a protocol; separate **consoles/clients** on operators' machines render the UI. The
daemon **hosts** its console (ships it to clients) but never **spawns** or renders it. The
driver is **DRY**: instead of N copies of the SPA (one baked into each client binary, plus
the brain's embed, plus the site's synced copy), there is **one UI codebase, hosted once,
rendered by every client, always version-matched.** The instinct already exists in the repo —
`scripts/sync-site-assets.sh` is a DRY drift-gate keeping the site's `replay-player.js` in
lockstep with `internal/ui/web` as the source of truth. This makes that principle the
architecture instead of a script.

## What exists (grounded)

- **Brain serves the SPA:** `internal/ui/ui.go` — `//go:embed web` (`webFS`, line 43-44) +
  `mux.Handle("/", http.FileServer(http.FS(sub)))` (line 173), alongside ~30 JSON routes,
  `/events` (SSE), and `/mcp` (registered in `cmd/corral/main.go`'s mux).
- **Console = reverse proxy:** `internal/console/console.go` `New(brainRaw, token, readOnly)`
  → a `httputil.NewSingleHostReverseProxy` to the brain that injects the bearer and forwards
  `/`, `/api/*`, `/events`; read-only refuses non-GET before it reaches the brain.
- **Clients:** `corral-observe` (read-only console), `corral-admin` (`--open` runs the console +
  opens a browser), `corral-desktop` (launches Chrome/Chromium/etc. in `--app` mode at the
  brain URL with the token).
- **DRY drift-gate:** `scripts/sync-site-assets.sh` syncs `site/public/replay-player.js` ⇐
  `internal/ui/web/replay-player.js` (source of truth), `--check` fails on drift.

## The architecture

```
        operator machine                              server
  ┌──────────────────────────┐              ┌──────────────────────────┐
  │  client (observe/admin/  │   fetch      │  DAEMON (brain)          │
  │  desktop)                │  bundle ───▶ │  • CLI/env/drop-in config│
  │  • fetch + cache the     │◀── (versioned│  • MCP  /mcp             │
  │    ONE UI bundle         │    resource) │  • JSON API  /api/*      │
  │  • serve it locally      │              │  • events  /events       │
  │  • inject bearer + fwd    │──/api|/mcp──▶│  • HOSTS the UI bundle    │
  │    /api|/mcp|/events      │   /events    │    (serves, never renders)│
  │  • open browser/webview   │              │  • serves NO webapp at /  │
  └──────────────────────────┘              └──────────────────────────┘
        renders the UI                            hosts + does the work
```

### 1. The daemon HOSTS a versioned console bundle (does not render it)
- The brain keeps the SPA source (`internal/ui/web`) embedded (single-binary ethos preserved),
  but serves it as a **versioned bundle resource** to authenticated clients — not as a
  browser-facing app. Endpoints (auth-required, bearer):
  - `GET /console/manifest.json` → `{ version, assets: [{path, sha256}], entry: "index.html" }`.
    `version` = the daemon's build version, so the bundle is **version-matched to the API**.
  - `GET /console/asset/<path>` → the asset bytes (from the embed).
- **The brain stops serving the SPA at `/`.** `/` returns a plain daemon-identity response
  (e.g. `200 text/plain: "corral daemon — connect a client (corral-observe|admin|desktop)"`),
  never HTML for a browser to render. The daemon owns no browser session and no login flow.
- The brain keeps `/api/*`, `/events`, `/mcp` exactly as today.

### 2. The clients FETCH + host + render the one bundle
`internal/console` changes from a dumb reverse proxy into a **thin console host**:
- On start: authenticate to the daemon, `GET /console/manifest.json`, and ensure the bundle is
  cached locally keyed by `version` (e.g. `~/.cache/corral/console/<version>/`) — fetch+verify
  (sha256) any missing assets. If the daemon has a newer version than the cache, fetch it. The
  UI is thus **always matched** to the daemon it's talking to.
- Serve the cached bundle from a **local** HTTP server (`127.0.0.1:<port>`) — same-origin, so
  no CORS: the browser talks only to `localhost`.
- **Forward `/api/*`, `/events`, `/mcp`** from the local server to the daemon with the bearer
  injected (the existing proxy logic, minus proxying `/`). Read-only still refuses non-GET
  locally.
- Open the operator's browser / webview at the local server.
- `corral-observe` = read-only host; `corral-admin` = read-write host; `corral-desktop` =
  a webview/app-mode-browser pointed at the same local host.

### 3. One UI codebase — the DRY core
- `internal/ui/web` is the single source of truth. It becomes the **hosted bundle**; every
  runtime client renders it — no per-client SPA copies. A client binary carries no UI, only
  the fetch-cache-host logic.
- The marketing **site** is static (Cloudflare Pages) and can't fetch from a daemon at runtime,
  so its embedded pieces (`replay-player.js`) stay a **build-time** synced copy — the existing
  `sync-site-assets.sh` drift-gate remains the DRY guarantee there. (Runtime DRY via the daemon
  bundle; build-time DRY via the drift gate. One source of truth either way.)

### 4. Trust/security seam
- The bundle is served only to **authenticated** clients (bearer), so the daemon is not an open
  web server handing HTML to anyone; the browser only ever touches the operator's `localhost`.
- No new auth surface on the daemon — the bundle endpoints validate the same bearer JWT as
  `/api/*`. Read-only `corral-observe` still refuses non-GET at the client, so the read-only
  guarantee is unchanged.
- The daemon no longer renders for a browser → no XSS/session surface on the daemon itself; the
  presentation host (client) owns that, on the operator's own machine.

## Migration — incremental, cockpit never breaks
1. **Add the bundle endpoints** (`/console/manifest.json`, `/console/asset/*`) to the brain,
   served from the existing embed. `/` still serves the SPA (no behavior change yet).
2. **Switch the console client** to fetch+cache+host the bundle and forward only `/api|/mcp|/events`
   (stop proxying `/`). Verify `corral-observe`/`corral-admin`/`corral-desktop` render the cached
   bundle identically. (The records dashboard is the proof: it already talks to `/api/builds`.)
3. **Flip the daemon's `/`** to the daemon-identity response; the SPA is now reached only through
   a client. Remove the `FileServer("/")` app-serving.
Each step is independently shippable and testable; the cockpit works throughout.

## Out of scope (later)
- **Signing/verifying the bundle** (so a client can prove the UI it fetched is the daemon's
  genuine console) — a nice future tie-in to the certify machinery; not v1.
- **Offline client cache** as a first-class feature (v1 caches by version but assumes the daemon
  is reachable, which a client needs anyway).
- **A native (non-browser) desktop shell** — v1 `corral-desktop` stays an app-mode browser.
- **MotherDuck-federated multi-daemon client** (a client aggregating many daemons) — the
  federation spec.

## Testing
- Brain: `GET /console/manifest.json` returns a version + asset list with correct sha256s; each
  `GET /console/asset/<path>` returns the embedded bytes; both require the bearer; `/` returns the
  daemon-identity response, not HTML.
- Console host: given a fake daemon serving a manifest + assets, the client caches by version,
  re-fetches on a version bump, serves the cached bundle locally, and forwards `/api|/events|/mcp`
  with the bearer; read-only refuses non-GET; a sha256 mismatch on a fetched asset is rejected.
- End-to-end (like the records smoke test): a client against a real daemon renders the cockpit
  from the cached bundle, and `/api/builds` still drives the Records view.

## Decisions (defaulted; revisit in review)
- **Bundle stays embedded in the daemon** (single-binary ethos) but is served as a versioned
  resource, never rendered by the daemon.
- **Version = daemon build version**; clients cache by version → always matched, update-once.
- **The client is the presentation host** (local server + browser/webview); the daemon hosts,
  never spawns.
- **Site keeps a build-time synced copy** (static host can't fetch a daemon at runtime); the
  drift-gate remains the DRY guarantee there.
- **Incremental migration** (bundle endpoints → client hosts bundle → daemon `/` goes headless),
  each step shippable.
