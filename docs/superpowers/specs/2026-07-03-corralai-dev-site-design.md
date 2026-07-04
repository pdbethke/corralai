# corralai.dev: the one-pager where the herd performs live

**Status:** approved design, 2026-07-03
**User decisions baked in:** Astro (explicit pick — anticipates docs/blog growth); Cloudflare Pages on the existing account; custom domain `corralai.dev` (owned, zone present); one-pager scope; the hero is an **embedded interactive replay of a real golden run** (user: "replay of builds on the demo site"), with the demo-video slot secondary (the video ships later and targets LinkedIn); CTAs: View on GitHub + watch the demo.

## Why the hero works with no backend

The replay player (shipped in mission-history-replay) was built embed-friendly on purpose: `startReplay(streamOrUrl)` accepts a plain stream object, structurally tested. The site serves a **baked stream JSON** — a real recorded mission exported once from `/api/replay?mission=N` — and the player animates the corral canvas from it, scrub bar and speeds included. Static files only; a strict "no external requests" posture holds trivially.

## Architecture

- `site/` in the corralai repo: an Astro project (npm, isolated from the Go module; CI treats it as its own build). One page (`src/pages/index.astro`), components per section, global corral-styled CSS reusing the product palette (amber on dark; grass texture optional, tasteful).
- **Player extraction (the one product-side change):** the replay player + the minimal canvas renderer it drives (nodes/links/bursts/buzzes draw path, force layout, the SKINS vocabulary it needs) move from inline `index.html` script into `internal/ui/web/replay-player.js`, loaded by the product page with a `<script src>` (embedded FS already serves the directory) and copied into `site/public/` at site build time by a small sync script that fails the build if the two ever diverge (hash check — no silent drift). The product UI keeps working identically; `TestReplayPlayerStructure` moves/extends to the new file. Fallback if extraction turns out gnarly: a lean site-local renderer subset, accepted duplication with a comment linking both sites.
- **Golden run:** `scripts/export-golden-run.sh` — runs against a demo brain, exports `/api/replay?mission=N` to `site/src/data/golden-run.json` plus a small metadata sidecar (directive, task/finding counts, duration) rendered as the hero caption. The site build embeds it; recording a fresh golden run is a documented manual step, not CI.

## Page content (top to bottom)

1. **Hero:** headline + one-sentence pitch (corral voice), the replay embed playing the golden run on load (muted-autoplay equivalent: starts at 4×, controls visible), caption with the run's real stats, CTAs: View on GitHub / Watch the demo (video slot: hidden until the video exists).
2. **How the herd works:** mission → plan → herd builds → findings → verify gate → review, four or five tight steps with the existing UI screenshots.
3. **It learns:** the learning loop pitch (proposals → human gate → skills fleet-wide) reusing README's verified copy.
4. **The knowledge corpus (CORRAL.md):** the pattern pitch, reused from the README section.
5. **Watch it back:** history + replay blurb — and "this hero IS that player."
6. **Quickstart:** the README's key-free demo commands, verbatim.
7. **Footer:** GitHub, license (Elastic-2.0), "built by a herd, wrangled by a human."

## Deploy

Cloudflare Pages project `corralai-dev`, production branch `main`, build `cd site && npm ci && npm run build`, output `site/dist`. Custom domain `corralai.dev` (+ `www` redirect). A GitHub Actions job (extending the existing Deploy workflow) builds and publishes via `wrangler pages deploy` with a CF API token stored as a repo secret — set up at implementation time (the repo currently has no secrets; creating the token/secret is a documented human-or-CLI step). DNS via the existing zone.

## Testing

- Site: `npm run build` green in CI; a Playwright smoke against the built `dist/` (serve statically): hero player renders, scrub responds, zero non-local network requests (request interception assert), GitHub link resolves.
- Product side: full Go suite green after the player extraction; `TestReplayPlayerStructure` (relocated) still pins embed rules; a live check that the product replay still works post-extraction (screenshot).
- Copy: every factual claim traces to the README/DESIGN verified claims — no new capability claims invented for marketing.

## Deliberately out of scope

- Docs/blog sections (Astro makes them cheap later — that's why it was chosen).
- The demo video and its slot content (separate effort; the slot ships hidden).
- Analytics/tracking of any kind (static, quiet page for now).
- Replay ambience richness (arrives with ticket #22; the hero replays what v1 records).
