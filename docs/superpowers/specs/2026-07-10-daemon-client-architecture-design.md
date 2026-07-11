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

### 4. Trust/security seam — the bundle is SIGNED; the daemon is not trusted to vouch for it
The client executes the bundle's JS in the operator's browser with the bearer token in the loop,
so a poisoned bundle = arbitrary code with API access. A per-asset sha256 in the manifest is only
**integrity-in-transit from a trusted source** — it does NOT stop a compromised or **forged
daemon** (open-source: anyone can run one) from serving a *consistent* malicious manifest+bundle.
Two independent layers close this, both anchored **externally** to the daemon:

**(a) Bundle authenticity — release-signed, client-pinned.** The **corralai release process**
signs the manifest (which covers every asset via its hash) with the **corralai release key**. The
**client pins the corralai release *public* key** (baked into the client binary at build) and
**verifies the manifest signature BEFORE rendering** — a poisoned/forged bundle isn't validly
signed → refused. The anchor is the *release* key, NOT the daemon's own key (daemon-signs would be
the circular-anchor bug). A running daemon only *serves* the pre-signed bundle it was built with;
it cannot mint a valid one. Being open-source is a *feature* (Kerckhoffs): security rests on the
signing key, not code secrecy.
- **v1:** a detached Ed25519 signature over the manifest; the client pins the corralai release
  public key (build-time constant, overridable via config for private forks/dev keys).
- **Hardening (dogfooding — reuse `internal/transparency`):** sign the bundle keyless via
  Sigstore/cosign + anchor to **Rekor**, verified against the TUF-rooted corralai identity — i.e.
  the UI bundle becomes an artifact `corral certify` itself would produce, verified the same way
  `corral certify verify` verifies a build. The accountability tool secures its own distribution
  with its own accountability.

**(b) Daemon authenticity — the client verifies *which* daemon it talks to.** A forged daemon
serving the *genuine* signed UI but a malicious *backend* is a separate threat. The client verifies
the daemon's identity — TLS cert pinning, and/or the brain's published identity key (`internal/attest`
Ed25519), and/or the operator-configured OIDC issuer — so a client only talks to a daemon the
operator explicitly trusts.

**(c) Hijacked daemon (the honest hard limit).** If the *legitimate* daemon is compromised in
place, the client — correctly identifying and trusting it — connects, sends its bearer, and reveals
its IP to the attacker now controlling it. **No client can hide from a server it must talk to; no
crypto fixes a compromised trusted server.** We do NOT claim otherwise. What the design guarantees
instead: (1) **the accountability MISSION survives** — records are Rekor-anchored and the client
verifies them **against the public log, not the daemon**, so a hijacked daemon **cannot forge a
record a client will accept** nor rewrite witnessed history; its worst is *denial*, not *forgery*
(trust the log, not the server — the whole thesis). (2) **Scoped, short-lived credentials** cap the
blast radius (`corral-observe` read-only; `cdt_` delegation tokens TTL-bound + identity-scoped) — a
harvested token is low-value and expiring. (3) **Identity pinning detects a key/cert swap.** (4) The
**headless split shrinks the hijack surface** — a daemon serving no webapp, no session, no rendered
page is a smaller target than one that does. (5) IP/connection metadata exposure is inherent to
client-server and is a network-layer concern (VPN/Tor), outside corral's remit — stated, not hidden.

**Baseline (unchanged):** the bundle endpoints require the same bearer as `/api/*` (no new auth
surface); the browser only ever touches the operator's `localhost`; read-only `corral-observe`
still refuses non-GET at the client; the daemon renders nothing → no XSS/session surface on the
daemon itself.

**(d) v1 hardening requirements — mapped to published standards.** The client/daemon MUST, in v1:
- **Rollback protection** — the client rejects a bundle whose version is *older* than the last it
  accepted from that daemon (anti-downgrade; a compromised daemon can't serve a known-vulnerable
  old-but-validly-signed bundle). *[TUF]*
- **Console CSRF / confused-deputy defense** — the local console enforces an **`Origin`/`Referer`
  allowlist** (its own localhost origin only) AND a **per-session secret** (a random value the
  served bundle carries and a drive-by site cannot obtain) on every proxied `/api` request, so a
  hostile site the operator visits can't ride the injected bearer via `fetch('http://127.0.0.1:…')`.
  *[OWASP ASVS; Top 10 CSRF]*
- **Serve-time integrity** — re-check each cached asset's sha256 at *serve* time (not only fetch);
  cache dir is `0700` (TOCTOU defense).
- **TLS enforced** — client→daemon is HTTPS with full cert verification (no `InsecureSkipVerify`);
  daemon-identity (§4b) rides on the verified cert.
- **Token scoping (defense in depth)** — read-only is BOTH the client gate AND a read-only-scoped
  token; a gate bug alone must not grant writes. The token is bound to its daemon (never sent to
  another).
- **No token to the browser** — the bearer stays server-side in the client; a test asserts it never
  crosses into the rendered bundle. *(invariant)*
- **Bundle size cap** — the client caps total bundle bytes (DoS defense vs. a malicious daemon).
- **`--allow-unsigned-console`** is a loud, warned, dev-only escape hatch, absent from release builds.

**Standards this design tracks:** SLSA (release/bundle build provenance), Sigstore + Rekor (keyless,
publicly-witnessed signing — the hardening), TUF (key rotation + rollback), OWASP ASVS (the console),
NIST SSDF (the dev process). See the companion spec *own-supply-chain hardening* for how corralai
holds *its own* releases to these + publishes the evidence.

## Client ecosystem — purpose-built windows into the daemon (the platform payoff)

Because the daemon is a headless API and the clients are thin, a **client is any purpose-built
consumer of the daemon's API** — not just "the cockpit." One backbone, many windows, each shipped
and versioned independently, each a lens for an audience:

- **CISO / audit client** (read-only, `corral-observe` lineage) — certified records, the
  verify/chain, attestations, and the security-posture evidence. The "prove it" console.
- **Dev client** (`corral-admin`/`corral-desktop`) — missions, builds, replay; the day-to-day.
- **Presentation client** — the scrubber/replay as a clean, narratable, slide-friendly "watch it
  back" surface for a board, a gov conference, a customer demo.
- **Prometheus exporter** — NOT a UI: a thin client that consumes the API and re-exposes it as a
  `/metrics` endpoint for Grafana/alerting. Proof that "client" generalizes past the browser.
- **Future windows** — Slack/Teams notifier, SIEM/webhook forwarder, a public trust/status page.

**Still DRY, at the right layer.** The shared core is **the daemon API + a component library**
(the record model, `replay-player.js`, the verify/chain renderer, the records table); each client
*composes* those for its audience — it does not re-implement them. So the "one bundle" of §1 is more
precisely **one component/data core, from which purpose-built bundles (and non-UI exporters) are
assembled.** The daemon serves the API (and, for UI clients, the bundle(s)); the windows multiply
without touching the daemon. This is the line between "our webapp" and "an accountability platform,"
and it maps straight onto the pivot: the CISO window is the accountability face, the exporter is the
ops integration, the presentation window is the gov-conference demo — all against one signed,
Rekor-witnessed backbone.

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
- **Rekor-anchored bundle signing (the dogfooding hardening)** — v1 signs the manifest with a
  detached Ed25519 signature verified against a pinned corralai release key; the keyless
  Sigstore/cosign + Rekor-anchored version (reusing `internal/transparency`, verified like `corral
  certify verify`) is the follow-up.
- **Full daemon-identity pinning UX** — v1 relies on TLS + the operator configuring a trusted
  daemon; first-class cert/identity-key pinning + trust-on-first-use is a follow-up.
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
