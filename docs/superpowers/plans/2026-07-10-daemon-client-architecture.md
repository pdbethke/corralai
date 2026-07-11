# Daemon/Client Architecture Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the brain a headless daemon that HOSTS a single versioned UI bundle; the console clients FETCH, cache, and render that one bundle locally and forward `/api|/mcp|/events` to the daemon. One UI codebase, version-matched, DRY. The daemon serves no webapp.

**Architecture:** Three incremental, independently-shippable steps: (1) the brain serves the embedded SPA as a versioned bundle resource (`/console/manifest.json` + `/console/asset/*`) — additive, `/` unchanged; (2) `internal/console` flips from a dumb reverse-proxy into a thin bundle-host that fetches+caches the bundle by version, serves it locally, and forwards only `/api|/mcp|/events`; (3) the daemon's `/` stops serving the SPA (returns a daemon-identity response), and `corral-desktop` points at the local host. The cockpit works throughout.

**Tech Stack:** Go 1.26; `embed.FS` (existing `internal/ui` `//go:embed web`); `net/http` + `httputil.ReverseProxy` (existing console); `crypto/sha256`; the brain's existing `verifier.Wrap(authz(...))` auth.

## Global Constraints

- SPDX header `// SPDX-License-Identifier: Elastic-2.0` on every new Go file.
- TDD: failing test first, watch it fail, minimal code, watch it pass, commit.
- `go vet ./...` clean; full `go test ./...` green; `go build ./...`; `bash scripts/check-security.sh` green before each commit.
- The bearer token is a secret: never logged; the browser only ever talks to the client's `localhost` (the token stays in the client).
- **The daemon serves no browser-facing app and owns no browser session** (the whole point). It hosts the bundle as a resource; the client renders it.
- **DRY:** one UI source (`internal/ui/web`) → one hosted bundle → every client. No per-client SPA copy.
- **Read-only guarantee preserved:** `corral-observe` refuses non-GET at the client, unchanged.
- **The cockpit must keep working after every task** (incremental migration; each task ships).
- Corral metaphor in user copy; do not add bee terms (existing ones may remain).
- Branch: `arch/daemon-client-ui` (spec committed there).

## File Structure

- `internal/ui/ui.go` — add `Deps.Version`; add `/console/manifest.json` + `/console/asset/*` handlers (from `webFS`); (Task 3) flip `/` to the daemon-identity handler.
- `internal/ui/console_bundle.go` (+ test) — the manifest builder (walk `webFS`, sha256 each file).
- `cmd/corral/main.go` — wire `Deps.Version: version`.
- `internal/console/bundle.go` (+ test) — the client-side bundle cache (fetch manifest, fetch+verify+cache assets by version).
- `internal/console/console.go` (+ test) — `New` becomes a bundle-host: serve the cached bundle for `/`, forward `/api|/mcp|/events`.
- `cmd/corral-desktop/main.go` — point the app-mode browser at the local console host.

---

### Task 1: The daemon hosts the SPA as a versioned bundle resource (additive)

**Files:** Create `internal/ui/console_bundle.go`, `internal/ui/console_bundle_test.go`; modify `internal/ui/ui.go` (Deps + routes), `cmd/corral/main.go` (wire Version).

**Interfaces — Produces:**
```go
// in internal/ui
type BundleManifest struct {
    Version string         `json:"version"`          // = daemon build version
    Entry   string         `json:"entry"`            // "index.html"
    Assets  []BundleAsset  `json:"assets"`
}
type BundleAsset struct {
    Path   string `json:"path"`   // e.g. "index.html", "replay-player.js"
    SHA256 string `json:"sha256"` // hex
}
// buildManifest walks the embedded web/ FS and returns the manifest (computed once).
func buildManifest(webFS fs.FS, version string) (BundleManifest, error)
```
- `Deps` gains `Version string`. `main.go` sets `Version: version` in the `ui.Deps{…}` literal (main.go:1228; `version` is at main.go:436).
- In `ui.Handler` register (near the existing routes): `GET /console/manifest.json` → JSON of the (cached) manifest; `GET /console/manifest.sig` → the detached **Ed25519 signature over the canonical manifest bytes** (see signing below); `GET /console/asset/{path...}` → the file bytes from `webFS` (via `sub`, the `fs.Sub(webFS,"web")` already computed for the FileServer), with the content-type by extension. Reject `..`/absolute paths. These sit inside `ui.Handler`, so they inherit `verifier.Wrap(authz(...))` — **auth is automatic**, same bearer as `/api`.
- **Bundle signature (the trust anchor — see spec §4a).** The signature is NOT minted by the running daemon; it is produced at **build/release time** by signing the canonical manifest with the **corralai release key**, and embedded so the daemon just serves it (`//go:embed console.manifest.sig`, or a build var). Add `scripts/sign-console-bundle.sh` that: builds the canonical manifest (same `buildManifest`), signs it with `$CORRALAI_RELEASE_KEY` (Ed25519), writes `internal/ui/console.manifest.sig`. For **dev/tests**, a committed dev-keypair signs it; the pinned public key is overridable. If the embedded signature is absent/stale, `GET /console/manifest.sig` returns 404 and the manifest is served **unsigned** (clients that require a signature will refuse it — fail-closed at the client, never a fake-signed manifest).
- `/` is untouched in this task (still the FileServer) — additive, nothing breaks.

- [ ] **Step 1: Failing test** `console_bundle_test.go`: `buildManifest(testFS, "v1.2.3")` returns `Version=="v1.2.3"`, `Entry=="index.html"`, one `BundleAsset` per file with a correct hex sha256 (compute independently in the test); a known file's asset bytes round-trip.
- [ ] **Step 2: Run it, verify it fails.** `go test ./internal/ui/ -run Bundle`.
- [ ] **Step 3: Implement `buildManifest`** (walk `fs.WalkDir`, `sha256.Sum256` each file) and the two handlers in `ui.go`; add `Deps.Version`; wire `version` in main.go. Build the manifest once (at `Handler` construction) and serve the cached copy.
- [ ] **Step 4: Run** `go test ./internal/ui/ ./cmd/corral/` + `go vet` + `go build ./...` → green.
- [ ] **Step 5: Commit.**
```bash
git add internal/ui/ cmd/corral/main.go
git commit -m "feat(ui): serve the SPA as a versioned /console bundle resource (manifest + assets, auth'd)"
```

---

### Task 2: The console client becomes a thin bundle-host

**Files:** Create `internal/console/bundle.go`, `internal/console/bundle_test.go`; modify `internal/console/console.go` (+ test).

**Interfaces — Produces:**
```go
// fetchBundle fetches the daemon's manifest, ensures every asset is cached (by version,
// sha256-verified), and returns the local cache dir for that version. Idempotent: a fully
// cached version does no network beyond the manifest fetch.
func fetchBundle(brainRaw, token, cacheRoot string) (dir string, manifest BundleManifest, err error)
```
- `fetchBundle`: `GET {brain}/console/manifest.json` (bearer) → manifest bytes; **`GET
  {brain}/console/manifest.sig` (bearer) → the detached signature, and VERIFY it (Ed25519) over the
  canonical manifest bytes against the PINNED corralai release public key BEFORE trusting anything.**
  On signature failure (or a missing signature unless `--allow-unsigned-console` is set) → **refuse,
  render nothing** (a poisoned/forged bundle from a compromised or forged daemon dies here — spec §4a).
  The pinned key is a build-time constant `corralaiReleasePubKey` (hex), overridable via config for
  private forks/dev. Only after the manifest is signature-verified: `dir = cacheRoot/console/<version>/`;
  for each asset, if the cached file's sha256 != asset.SHA256 (or missing), `GET {brain}/console/asset/<path>`
  (bearer), verify sha256 (reject on mismatch), write `0644` under `dir`. `cacheRoot` defaults to
  `os.UserCacheDir()/corral`.
- `console.New(brainRaw, token, readOnly)` (same signature) is refactored:
  1. `dir, _, err := fetchBundle(brainRaw, token, defaultCacheRoot())` — fail fast if the daemon is unreachable (the client needs it anyway).
  2. Build the reverse proxy to the daemon as today (bearer injection, `FlushInterval=-1` for SSE).
  3. mux: **`/api/`, `/events`, `/mcp` (and `/mcp/`) → the proxy** (forward to the daemon); **everything else → `http.FileServer(http.Dir(dir))`** (serve the cached bundle locally). The SPA's relative asset + `/api` fetches resolve correctly against the local origin.
  4. read-only: wrap the **proxied** routes in `readOnlyGate` (unchanged guarantee); the local bundle is GET-only static content anyway.
  5. Keep `HealthPath`.
- **Console security controls (spec §4d — verified to OWASP ASVS):**
  - **CSRF / confused-deputy (ASVS V13/V50):** every proxied `/api` request MUST pass an
    `Origin`/`Referer` allowlist (the console's own localhost origin) AND carry a **per-session
    secret** (a random value minted at start, injected into the served bundle, required as a header
    on `/api` calls) — a drive-by site can't ride the injected bearer.
  - **Serve-time integrity (ASVS V12):** re-verify each cached asset's sha256 when serving it;
    cache dir `0700`.
  - **Rollback protection (TUF):** reject a manifest whose version is older than the last accepted
    for this daemon (persist the high-water mark).
  - **Size cap:** cap total bundle bytes fetched (DoS defense).
  - **No token to the browser (invariant):** the bearer stays in the client; never rendered/sent to
    the bundle.
- **Net:** the browser gets the UI from the local cache (the daemon's version-matched bundle), and every data call is proxied to the daemon with the token — the daemon never serves `/` to this browser.

- [ ] **Step 1: Failing test** `bundle_test.go` + `console_test.go`: stand up a fake daemon (`httptest`) serving `/console/manifest.json` + `/console/asset/*` (+ a stub `/api/ping`). `fetchBundle` caches all assets (verify the files + their sha256 on disk), does no re-download on a second call, and REJECTS a tampered asset (sha256 mismatch). `New(fakeURL, "tok", false)` → a handler that: `GET /` returns the cached `index.html`; `GET /api/ping` is proxied to the fake daemon **with `Authorization: Bearer tok`**; `readOnly=true` refuses `POST /api/x`; a version bump on the fake daemon causes a re-fetch into a new cache dir.
- [ ] **Step 2: Run it, verify it fails.**
- [ ] **Step 3: Implement** `fetchBundle` + the `New` refactor (bundle-host mux).
- [ ] **Step 4: Run** `go test ./internal/console/ ./cmd/corral-admin/ ./cmd/corral-observe/` + `go vet` + `go build ./...` + `bash scripts/check-security.sh` → green.
- [ ] **Step 5: Commit.**
```bash
git add internal/console/
git commit -m "feat(console): thin bundle-host — fetch+cache the daemon's versioned UI, serve locally, forward /api|/mcp|/events"
```

---

### Task 3: The daemon's `/` goes headless + `corral-desktop` points local + cleanup

**Files:** Modify `internal/ui/ui.go` (`/` handler), `cmd/corral-desktop/main.go`; (docs) `site/src/content/docs/docs/…` note if the UI-tour references the brain serving the UI.

**Interfaces:**
- `internal/ui/ui.go`: replace `mux.Handle("/", http.FileServer(http.FS(sub)))` with a **daemon-identity handler**: for any path not matched by an `/api`/`/console`/`/events`/other route, return `200 text/plain`:
  `corral daemon — headless. Connect a client: corral-observe (read-only) / corral-admin / corral-desktop.` The SPA is no longer served at `/`; it's reached only via `/console/*` (Task 1) rendered by a client (Task 2). Keep `/console/*`, `/api/*`, `/events` working.
- `cmd/corral-desktop/main.go`: today it launches a browser `--app` at the brain URL (main.go:343-364). Change it to **run the bundle-host console locally** (`console.New(brain, token, false)` on a `127.0.0.1:0` listener) and launch the app-mode browser at that **local** address — so desktop renders the daemon's bundle through the same client path as `corral-admin`, never pointing a browser at the daemon. (Reuse `console.New`; do not duplicate the host logic.)

- [ ] **Step 1: Failing test** `internal/ui` test: `GET /` (and an arbitrary non-route path) returns `200 text/plain` containing "daemon" and NOT the SPA's HTML; `GET /console/manifest.json` and a seeded `/api/*` route still work. (For `corral-desktop`, assert via a small unit that it constructs a local `console.New` host and targets the local addr — mirror how `corral-admin`'s wiring is exercised, or a focused test of the URL it hands the browser.)
- [ ] **Step 2: Run it, verify it fails** (today `/` serves HTML).
- [ ] **Step 3: Implement** the daemon-identity `/` handler (remove the `FileServer`); rewire `corral-desktop` to the local bundle-host.
- [ ] **Step 4: Run** full `go test ./...` + `go vet` + `go build ./...` + `bash scripts/check-security.sh`. **Manual/e2e sanity** (like the records smoke test): a real daemon + `corral-admin`/`corral-observe` render the cockpit from the cached bundle, and `/` on the daemon returns the identity string, not the app.
- [ ] **Step 5: Commit.**
```bash
git add internal/ui/ui.go cmd/corral-desktop/main.go site/
git commit -m "feat: daemon / goes headless (no webapp) — SPA reached only via a client hosting the /console bundle"
```

---

## Self-Review

**Spec coverage:** bundle endpoints (Task 1) + client bundle-host + version-matched cache + forward-only-API (Task 2) + daemon `/` headless + desktop-points-local (Task 3). Auth inherited via `ui.Handler`'s existing `verifier.Wrap(authz(...))`. DRY (one `internal/ui/web` → one bundle → all clients). Incremental (each task ships; cockpit never breaks). ✓

**Placeholder scan:** none — the manifest/handler/cache logic is concrete; the one NOTE (`corral-desktop` mirrors `corral-admin`'s wiring) points at existing code.

**Type consistency:** `BundleManifest`/`BundleAsset`/`buildManifest` (Task 1) consumed by `fetchBundle` (Task 2) — the client re-declares the same JSON shape (or imports `internal/ui`'s exported types; prefer a tiny shared struct in `internal/console` matching the JSON to avoid a `console → ui` import). `console.New` signature unchanged across Tasks 2, 3. `Deps.Version` (Tasks 1).

**Out of scope (per spec):** signing/verifying the bundle, offline-first client cache, a native (non-browser) desktop shell, a multi-daemon federated client.

**Risk note:** Task 2 is the load-bearing change (the client stops proxying `/` and serves the cache). Its test stands up a fake daemon and asserts the cache + sha256-verify + the `/api` forward + read-only gate. Task 3 is the only "breaking" flip (daemon `/`), and it lands *after* the clients no longer depend on the daemon serving `/` — so ordering keeps the cockpit alive throughout.
