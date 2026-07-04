# corralai.dev v2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Turn corralai.dev from a one-pager into a daylight-restyled, conversion-ready landing page plus a full `/docs` site (Starlight) with a UI tab tour and a CI-enforced, never-hand-written CLI reference — so a cold LinkedIn reader can be sold on the product, see it actually works via receipts (a real recorded replay), and self-serve the docs to run it.

**Architecture:** Part A restyles `site/` to a light default (keeping the replay hero as a dark-framed viewport) and adds an aggressive-but-honest above-the-fold conversion pass plus OpenGraph/Twitter card metadata so a shared link renders a real product screenshot, not a blank card. Part B mounts Astro's Starlight framework at `/docs` inside the same `site/` project, restructuring README/DESIGN/docs/corral content into a sidebar-navigated concepts library plus a screenshot-illustrated tab-by-tab UI tour, all captured from seeded scratch brains (never a personal/live brain) via the same scratch-DB-path + throwaway-seed-program pattern used for the v1 hero. Part C adds `scripts/gen-cli-docs.sh`, which builds the six `cmd/` binaries, captures their real `-h` output and env-var doc-comment blocks, and emits generated markdown into both `docs/cli/` and the Starlight tree, with a `--check` drift gate wired into CI beside the existing player-sync check. Two of the six binaries (`corral-agent`, `corral-harness`) currently have no `-h`/usage output at all; this plan adds one, TDD, because a docs generator cannot document text a binary refuses to print.

**Tech Stack:** Astro 7.0.6 (exact-pinned, existing `site/` toolchain), `@astrojs/starlight` (new, `--save-exact`), Playwright 1.61.1 (existing `site/tests/` e2e toolchain), Go 1.26.4 (existing, for the two usage-text additions), bash (`scripts/gen-cli-docs.sh`, following the existing `scripts/sync-site-assets.sh` and `scripts/export-golden-run.sh` conventions), Python 3 (existing `scripts/scrub-golden-run.py`, reused unmodified for screenshot-session privacy review where applicable).

## Global Constraints

- SPDX header `// SPDX-License-Identifier: Elastic-2.0` (or the language's comment-equivalent) on every new source file where comments are possible (`.js`, `.ts`, `.sh`, `.py`, `.go`, `.astro`, `.md` front matter) — not on `.json`.
- Zero external requests from the site is test-enforced: no external fonts, no CDN scripts/styles, no analytics/tracking of any kind, on the landing page AND on every `/docs` page. Starlight's Pagefind search runs fully locally — verify at build time that it stays that way. Extend the existing network-interception e2e pattern (`site/tests/site.spec.ts`'s zero-external-requests test) to cover a full `/docs` session, including a Pagefind search.
- Verified-copy discipline: every factual claim traces to README.md/docs/DESIGN.md/docs/corral/*.md/source code — no new capability claims invented for marketing or docs. Reframing true facts for a stronger pitch is in scope; inventing facts is not.
- Corral voice everywhere in user-facing copy — never bee/hive/swarm (memory `corralai-metaphor`). The product UI's own DOM still literally labels its default tab `swarm` (`internal/ui/web/index.html:325`, `id="tab-swarm"`) — that is existing, unchanged product surface, not something this plan touches; all NEW site/docs copy describing that tab calls it "the corral (canvas view)," never "swarm."
- No personal infrastructure details anywhere in docs — no productbinder hostnames, no Hetzner specifics, no real IPs/paths. The "Running it" / deploying-behind-a-tunnel content stays a generic pattern write-up.
- Screenshots (hero frame, OG image, UI tour images) come from SEEDED SCRATCH BRAINS ONLY, using scratch `CORRALAI_*_DB` paths and a throwaway seed program deleted before commit (the pattern established in `.superpowers/sdd/corralai-dev-site/task-1-report.md`) — never a personal/live brain.
- Every commit message ends with `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`.
- Green before any task is considered done: `go test ./... -count=1`, `go vet ./...`, `cd site && npm run build`, `cd site && npm run test:e2e`, `bash scripts/sync-site-assets.sh --check`, `bash scripts/gen-cli-docs.sh --check` (from Task 4 onward).
- CI (`.github/workflows/deploy-site.yml`) carries drift gates for BOTH the synced replay player (`scripts/sync-site-assets.sh --check`, already wired) and the generated CLI docs (`scripts/gen-cli-docs.sh --check`, added in Task 4).
- Astro stays exact-pinned (`"astro": "7.0.6"` in `site/package.json`, `--save-exact` on any new dependency, e.g. `@astrojs/starlight`).

---

### Task 1: Daylight restyle + dark-framed hero

**Files:**
- Modify: `site/src/styles/global.css`
- Modify: `site/src/components/Hero.astro`
- Modify: `site/src/components/HowItWorks.astro`, `LearningLoop.astro`, `KnowledgeCorpus.astro`, `WatchItBack.astro`, `Quickstart.astro`, `SiteFooter.astro` (token-only touch-ups — no copy changes here, Task 2 handles hero copy)
- Test: `site/tests/site.spec.ts` (add a contrast-check test)

**Interfaces:**
- Consumes: the existing CSS custom properties contract (`--bg`, `--fg`, `--muted`, `--amber`, `--red`, `--line`, `--green`, `--panel`, `--sel`, `--card`) every component already reads.
- Produces: the same property names, new light values, PLUS two new dark-scoped properties (`--stage-bg`, `--stage-fg`) that `#hero #stage` and its children use so the hero viewport stays dark regardless of the page-wide light tokens. Later tasks (2, 7) style new sections using the same `--bg`/`--fg`/`--amber`/`--panel`/`--line`/`--muted` names — the values change here, the names don't.

- [ ] **Step 1: Write the contrast-check test first (fails red)**

```ts
// Append to site/tests/site.spec.ts
test('body text meets WCAG AA contrast against the page background', async ({ page }) => {
  await page.goto('/');
  // Cheap, dependency-free contrast check (no axe-core in this toolchain):
  // relative-luminance contrast ratio between the paragraph's computed color
  // and the page's computed background, per WCAG 2.1 formula. AA for normal
  // body text requires >= 4.5.
  const ratio = await page.evaluate(() => {
    function luminance(rgb: [number, number, number]): number {
      const [r, g, b] = rgb.map((c) => {
        const s = c / 255;
        return s <= 0.03928 ? s / 12.92 : Math.pow((s + 0.055) / 1.055, 2.4);
      });
      return 0.2126 * r + 0.7152 * g + 0.0722 * b;
    }
    function parseRgb(css: string): [number, number, number] {
      const m = css.match(/\d+/g)!.map(Number);
      return [m[0], m[1], m[2]];
    }
    const p = document.querySelector('.pitch') as HTMLElement;
    const fg = parseRgb(getComputedStyle(p).color);
    const bg = parseRgb(getComputedStyle(document.body).backgroundColor);
    const lFg = luminance(fg) + 0.05;
    const lBg = luminance(bg) + 0.05;
    return lFg > lBg ? lFg / lBg : lBg / lFg;
  });
  expect(ratio, `contrast ratio ${ratio.toFixed(2)} is below WCAG AA's 4.5 minimum`).toBeGreaterThanOrEqual(4.5);
});
```

- [ ] **Step 2: Run it to confirm it fails against the current dark theme**

```bash
cd site && npm run build && npx playwright test -g "WCAG AA" && cd ..
```

Expected: FAIL or PASS-by-accident is possible either way against the current all-dark palette (`--fg: #e6e1d8` on `--bg: #0e1116` is already high-contrast) — the point of running it now is to have a baseline number logged before the palette changes, not to prove redness. Note the printed ratio.

- [ ] **Step 3: Rewrite `global.css` to daylight tokens**

```css
/* SPDX-License-Identifier: Elastic-2.0 */
/* Daylight palette: warm cream/parchment ground, dark-ink text, the same
   amber accent read in daylight instead of on a terminal-dark background.
   --stage-* stay dark — the replay hero is a product-shown-in-a-dark-window
   viewport, framed against the light page, not itself restyled. Kept as
   custom properties (not hand-picked per component) so a future
   prefers-color-scheme dark variant is a token swap, not a rewrite. */
:root {
  --bg: #faf3e6; --fg: #241f18; --muted: #6b6152; --amber: #b5780a; --red: #b8341f;
  --line: #ddd0b3; --green: #3f7a52; --panel: #f2e9d6; --sel: #ece0c4; --card: rgba(255,251,242,.97);
  --stage-bg: #0e1116; --stage-fg: #e6e1d8; --stage-muted: #8a8170; --stage-amber: #e8a838;
  --stage-line: #33405a; --stage-panel: #161b22;
  color-scheme: light;
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

Note: `--amber: #b5780a` and `--green: #3f7a52` are the *same hues* as the dark-mode `#e8a838`/`#8fdcab`, darkened for AA contrast against the cream ground (`#faf3e6`) rather than the near-black `#0e1116` they were tuned for originally — same accent family, daylight-legible values.

- [ ] **Step 4: Re-frame the hero as a dark viewport on the light page**

Edit `site/src/components/Hero.astro`'s `<style>` block — replace every `var(--bg|--fg|--muted|--amber|--panel|--line)` reference *inside `#stage` and `#replay`* with the new `--stage-*` tokens, and add a visible frame so the dark canvas reads as an intentional window rather than a leftover dark section:

```astro
<style>
  #hero { position: relative; }
  .hero-copy { max-width: 860px; margin: 0 auto; padding: 56px 20px 24px; text-align: center; }
  .hero-copy h1 { font-size: 2.4rem; margin: 0 0 12px; color: var(--amber); }
  .pitch { font-size: 1.15rem; color: var(--fg); max-width: 720px; margin: 0 auto 14px; }
  .caption { color: var(--muted); font-size: 0.9rem; margin-bottom: 20px; }
  .ctas { display: flex; gap: 12px; justify-content: center; }
  .cta { background: var(--amber); color: #fff; padding: 10px 20px; border-radius: 6px; font-weight: 600; text-decoration: none; }
  .cta.secondary { background: var(--panel); color: var(--fg); border: 1px solid var(--line); }
  /* The framed dark viewport: grass, rain, and glowing nodes need a dark
     canvas even on a light page — bordered and shadowed so it reads as a
     window onto the corral, not an unstyled leftover section. */
  #stage-frame { max-width: 1040px; margin: 0 auto; padding: 0 20px; }
  #stage {
    position: relative; height: 60vh; min-height: 380px;
    background: var(--stage-panel); border: 1px solid var(--stage-line);
    border-radius: 12px; box-shadow: 0 18px 44px rgba(30,20,0,.22);
    overflow: hidden;
  }
  #c { width: 100%; height: 100%; display: block; }
  #empty { position: absolute; inset: 0; display: flex; align-items: center; justify-content: center; color: var(--stage-muted); pointer-events: none; }
  #replay {
    position: relative; background: var(--stage-panel); color: var(--stage-fg);
    border: 1px solid var(--stage-line); border-top: none;
    border-bottom-left-radius: 12px; border-bottom-right-radius: 12px;
    padding: 9px 16px; max-width: 1040px; margin: 0 auto;
  }
  #replay .row { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
  #replay .row button { background: var(--stage-panel); color: var(--stage-fg); border: 1px solid var(--stage-line); border-radius: 5px; padding: 4px 12px; font-size: 12px; cursor: pointer; }
  #replay .row button:hover { border-color: var(--stage-amber); }
  #replay-scrub { flex: 1; min-width: 160px; }
  #replay-label { color: var(--stage-muted); font-size: 11.5px; min-width: 70px; text-align: center; }
  #replay-title { color: var(--stage-amber); font-size: 12px; font-weight: 600; margin-right: 4px; }
</style>
```

Wrap the existing `<div id="stage">…</div>` markup in a new `<div id="stage-frame">` so the border-radius/shadow have a sizing anchor independent of `#replay`'s own width:

```astro
<div id="stage-frame">
  <div id="stage">
    <canvas id="c"></canvas>
    <div id="empty">no agents in the corral yet</div>
  </div>
</div>
<div id="replay">
  ...
</div>
```

(Leave `#replay`'s inner markup exactly as it is today — only the enclosing `<style>` block and the new `#stage-frame` wrapper change.)

- [ ] **Step 5: Run the contrast test again**

```bash
cd site && npm run build && npx playwright test -g "WCAG AA" && cd ..
```

Expected: PASS. If it fails, the printed ratio tells you which token pair is too close — `--fg`/`--bg` (`#241f18` on `#faf3e6`) computes to ~13.9:1, comfortably above 4.5, so a failure here means a component overrode `.pitch`'s color with something else; check `Hero.astro` didn't leave a stray inline `color:` behind.

- [ ] **Step 6: Full site build + e2e suite**

```bash
cd site && npm run test:e2e && cd ..
```

Expected: all existing tests plus the new contrast test PASS. The zero-external-requests test and the GitHub-link test are untouched by a token change, but re-run them for the same reason Task 1 of the v1 plan re-ran the product UI check: restyle-shaped changes are exactly where an unrelated regression hides.

- [ ] **Step 7: Screenshot for the human**

```bash
cd site && npm run preview -- --port 4321 &
sleep 2
```

Use the Playwright browser tool to navigate to `http://localhost:4321/`, wait for the hero replay to start (scrub bar shows a nonzero max), and take one full-page screenshot (e.g. `/tmp/corralai-daylight-restyle.png`) showing the cream/parchment page with the dark-framed hero — this is the "does it actually look right" check no automated test covers. Then:

```bash
kill %1
```

- [ ] **Step 8: Commit**

```bash
git add site/src/styles/global.css site/src/components/Hero.astro site/tests/site.spec.ts
git commit -m "$(cat <<'EOF'
style(site): daylight restyle with a dark-framed hero viewport

Light-default is the norm for major OSS sites; dark-default was a
terminal-brand affectation. Page tokens move to a warm cream/parchment
ground with dark-ink text and a darkened amber/green accent pair tuned for
AA contrast on the new background. The replay hero keeps its own dark
--stage-* tokens and gets a border/shadow frame — grass, rain, and glowing
nodes still need a dark canvas, now presented as a window onto the corral
rather than an unstyled leftover dark section. New WCAG AA contrast e2e
check guards the swap going forward.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 2: Social preview card + above-the-fold conversion pass

**Files:**
- Modify: `site/src/pages/index.astro` (head meta tags)
- Modify: `site/src/components/Hero.astro` (copy)
- Create: `site/public/og-image.png` (produced by a capture script, not hand-drawn)
- Create: `scripts/capture-og-image.sh`
- Create: `cmd/seedreplay/main.go` (throwaway — deleted at the end of this task, not committed)
- Test: `site/tests/site.spec.ts` (add meta-tag + OG-image-resolves tests)

**Interfaces:**
- Consumes: Task 1's `--amber`/`--stage-*` tokens; the seeded-scratch-brain pattern from `.superpowers/sdd/corralai-dev-site/task-1-report.md` (scratch `CORRALAI_DB`/`CORRALAI_MEMORY_DB`/`CORRALAI_PRINCIPALS_DB`/`CORRALAI_GATEWAY_DB`/`CORRALAI_ARTIFACTS_DB` env vars pointed at a `mktemp -d` directory, `go run ./cmd/corral`, a throwaway seed program mirroring `internal/brain/replay_test.go`'s `seedReplayMission` helper against real on-disk stores).
- Produces: `site/public/og-image.png` (1200×630, committed) that Task 7's UI-tour task does NOT reuse (each screenshot-taking task seeds its own scratch brain independently — see Ambiguities below) — no other task depends on this file by name.

- [ ] **Step 1: Write the failing meta-tag test**

```ts
// Append to site/tests/site.spec.ts
test('the page carries OpenGraph + Twitter card metadata for link previews', async ({ page }) => {
  await page.goto('/');
  const og = async (prop: string) => page.locator(`meta[property="${prop}"]`).getAttribute('content');
  expect((await og('og:title'))?.length, 'og:title missing').toBeGreaterThan(0);
  expect((await og('og:title'))!.length, 'og:title should fit a LinkedIn card title (<=60 chars)').toBeLessThanOrEqual(60);
  expect(await og('og:description')).toBeTruthy();
  expect(await og('og:url')).toBe('https://corralai.dev/');
  const ogImage = await og('og:image');
  expect(ogImage).toBe('https://corralai.dev/og-image.png');
  expect(await page.locator('meta[name="twitter:card"]').getAttribute('content')).toBe('summary_large_image');
  expect(await page.locator('link[rel="canonical"]').getAttribute('href')).toBe('https://corralai.dev/');
});

test('the OG image asset exists and is a real local file, not a placeholder', async ({ page, request }) => {
  await page.goto('/');
  const res = await request.get('/og-image.png');
  expect(res.status()).toBe(200);
  const bytes = await res.body();
  expect(bytes.length, 'og-image.png looks empty/placeholder').toBeGreaterThan(10_000);
});
```

- [ ] **Step 2: Run it to confirm it fails**

```bash
cd site && npm run build && npx playwright test -g "OpenGraph|OG image" && cd ..
```

Expected: FAIL — no meta tags, no `og-image.png` exist yet.

- [ ] **Step 3: Write the head meta tags in `index.astro`**

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

const title = 'Corralai — the herd performs live';
const description = 'Give one directive; watch a herd of AI agents plan, build, verify, and re-plan a real mission live — every run recorded and replayable.';
const ogTitle = 'Corralai — watch AI agents build, live';
const url = 'https://corralai.dev/';
---
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <link rel="icon" type="image/svg+xml" href="/favicon.svg" />
  <title>{title}</title>
  <meta name="description" content={description} />
  <link rel="canonical" href={url} />

  <meta property="og:type" content="website" />
  <meta property="og:title" content={ogTitle} />
  <meta property="og:description" content={description} />
  <meta property="og:url" content={url} />
  <meta property="og:image" content="https://corralai.dev/og-image.png" />
  <meta property="og:image:width" content="1200" />
  <meta property="og:image:height" content="630" />

  <meta name="twitter:card" content="summary_large_image" />
  <meta name="twitter:title" content={ogTitle} />
  <meta name="twitter:description" content={description} />
  <meta name="twitter:image" content="https://corralai.dev/og-image.png" />
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

(`ogTitle` is 34 characters — comfortably under the 60-char ceiling the test enforces; `title` for the browser tab stays the existing longer string.)

- [ ] **Step 4: Above-the-fold conversion copy in `Hero.astro`**

Replace the `.hero-copy` block's contents (h1/pitch/caption/ctas) — everything else in the file (the `#stage-frame`, `#replay`, `<style>`, `<script>` blocks from Task 1) is unchanged:

```astro
<div class="hero-copy">
  <h1>Watch a herd of AI agents build software. Live. On the record.</h1>
  <p class="pitch">
    This hero is a real recorded mission — not a demo reel. Give a headless
    brain one directive and it turns it into a mission that a team of AI
    agents plans, builds, verifies, re-plans when they hit problems, and
    iterates with the client until it's accepted. Every run is recorded and
    replayable, exactly like the one playing below.
  </p>
  <p class="proof-row">
    <span>Runs free on a local 7B model — no API keys</span>
    <span aria-hidden="true">·</span>
    <span>Agents propose, you approve — nothing merges without a human</span>
  </p>
  <p class="caption">
    Real recorded mission: {meta.task_count} tasks ({meta.done_task_count} done), {meta.finding_count} findings, {minutes}m — replaying at 4× below.
  </p>
  <div class="ctas">
    <a class="cta" href="https://github.com/pdbethke/corralai">View on GitHub</a>
    <a class="cta secondary" id="watch-demo-cta" href="#" style="display:none">Watch the demo</a>
  </div>
  <p class="star-invite">
    Free, source-available, and getting better with every run —
    <a href="https://github.com/pdbethke/corralai">a star helps other people find it</a>.
  </p>
</div>
```

Add the two new classes to the `<style>` block (alongside the existing `.hero-copy`/`.pitch`/`.caption`/`.ctas` rules from Task 1 — do not remove those):

```css
.proof-row { display: flex; gap: 10px; justify-content: center; flex-wrap: wrap; color: var(--fg); font-size: 0.95rem; font-weight: 600; margin: 0 0 10px; }
.proof-row span[aria-hidden] { color: var(--muted); font-weight: 400; }
.star-invite { color: var(--muted); font-size: 0.85rem; margin-top: 14px; }
```

No external badge/shield service — the star-invite is a plain styled link, per the zero-external-requests rule.

- [ ] **Step 5: Write the throwaway seed program**

```go
// cmd/seedreplay/main.go — THROWAWAY, not committed. Deleted at the end of
// this task (Step 8). Mirrors internal/brain/replay_test.go's
// seedReplayMission helper against real on-disk stores instead of
// t.TempDir(), so a real `go run ./cmd/corral` process serves a mission with
// visible in-progress agents for a screenshot capture.
package main

import (
	"fmt"
	"os"

	"github.com/pdbethke/corralai/internal/brain"
	"github.com/pdbethke/corralai/internal/coord"
)

func main() {
	dbPath := os.Getenv("CORRALAI_DB")
	if dbPath == "" {
		fmt.Fprintln(os.Stderr, "CORRALAI_DB must be set to a scratch path")
		os.Exit(1)
	}
	store, err := coord.Open(dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open store:", err)
		os.Exit(1)
	}
	defer store.Close()
	if err := brain.SeedDemoMission(store, "add rate limiting to the ingest endpoint"); err != nil {
		fmt.Fprintln(os.Stderr, "seed:", err)
		os.Exit(1)
	}
	fmt.Println("seeded a scratch mission with active agents into", dbPath)
}
```

If `internal/brain` does not export a `SeedDemoMission` helper (check with `grep -rn "func SeedDemoMission\|func seedReplayMission" internal/brain/`) — it will not, since `seedReplayMission` in `replay_test.go` is a test-only, unexported helper — copy that function's body into `cmd/seedreplay/main.go` verbatim (it is test code being reused as a throwaway seed script, not product code; that's why this whole file is deleted before commit rather than kept as a real tool).

- [ ] **Step 6: Seed a scratch brain and capture the OG image**

```bash
export SCRATCH=$(mktemp -d)
export CORRALAI_DB="$SCRATCH/coord.sqlite3"
export CORRALAI_MEMORY_DB="$SCRATCH/memory.duckdb"
export CORRALAI_PRINCIPALS_DB="$SCRATCH/principals.sqlite3"
export CORRALAI_GATEWAY_DB="$SCRATCH/gateway.sqlite3"
export CORRALAI_ARTIFACTS_DB="$SCRATCH/artifacts.sqlite3"
export CORRALAI_OIDC_ISSUER=""   # dev mode, no auth — this is a scratch brain, never a real one
go run ./cmd/seedreplay
go run ./cmd/corral &
sleep 2
```

Write `scripts/capture-og-image.sh`:

```bash
#!/usr/bin/env bash
# SPDX-License-Identifier: Elastic-2.0
#
# scripts/capture-og-image.sh — captures a 1200x630 frame of a seeded scratch
# brain's live canvas for use as the site's OG/Twitter card image. Takes the
# brain URL as $1 (default the local dev brain). MUST only ever be pointed at
# a seeded scratch brain (see Task 2 of the site-docs-expansion plan) — never
# a personal/live one; this script has no way to verify that itself, so the
# human running it is the privacy gate here, same as the golden-run export.
set -euo pipefail
cd "$(dirname "$0")/.."
BRAIN_URL="${1:-http://127.0.0.1:9019}"
OUT="site/public/og-image.png"
node --experimental-vm-modules -e "
const { chromium } = require('site/node_modules/playwright-core');
(async () => {
  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1200, height: 630 } });
  await page.goto('$BRAIN_URL/');
  await page.waitForTimeout(4000); // let agents render and start moving
  await page.screenshot({ path: '$OUT' });
  await browser.close();
})();
"
echo "wrote $OUT — REVIEW IT BY EYE before committing: confirm it shows only synthetic demo agent names/paths, nothing from a personal brain."
```

```bash
chmod +x scripts/capture-og-image.sh
bash scripts/capture-og-image.sh
```

Expected: `wrote site/public/og-image.png — REVIEW IT BY EYE...`. Open the file and confirm it shows the canvas with visible agent nodes (not a blank/empty-state screen) and no personal-brain content — this is a seeded scratch brain, so by construction it can only contain the synthetic directive from Step 5, but eyeball it anyway, same discipline as the golden-run manifest review.

- [ ] **Step 7: Tear down the scratch brain**

```bash
kill %1
rm -rf "$SCRATCH"
```

- [ ] **Step 8: Delete the throwaway seed program**

```bash
rm -rf cmd/seedreplay
```

- [ ] **Step 9: Run the tests**

```bash
cd site && npm run test:e2e && cd ..
```

Expected: all tests including the two new ones from Step 1 PASS.

- [ ] **Step 10: Commit**

```bash
git add site/src/pages/index.astro site/src/components/Hero.astro site/public/og-image.png scripts/capture-og-image.sh site/tests/site.spec.ts
git commit -m "$(cat <<'EOF'
feat(site): OpenGraph/Twitter card + above-the-fold conversion pass

Cold LinkedIn traffic needs a real card, not a blank one: og:title/
og:description/og:image (a real seeded-scratch-brain screenshot, reviewed by
eye, never a personal brain)/og:url/twitter:card, plus a canonical link and
search meta description. Hero copy leads with the receipts angle (a real
recorded run, not a demo reel), the key-free local-model claim, and the
human-gate claim — all verbatim-or-tightened from existing README/DESIGN
claims, nothing new invented. No external badge services (zero-external-
requests rule) — the star invite is a plain styled link.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 3: Starlight mount at `/docs` + zero-external verification + nav skeleton

**Files:**
- Modify: `site/astro.config.mjs`
- Modify: `site/package.json` (add `@astrojs/starlight`)
- Create: `site/src/content/docs/index.mdx` (docs landing/redirect stub)
- Create: `site/src/content.config.ts` (Starlight's content collection, if the scaffold doesn't generate one)
- Test: `site/tests/docs.spec.ts`

**Interfaces:**
- Consumes: nothing from earlier tasks (independent subsystem mounted alongside the existing landing page).
- Produces: the `site/src/content/docs/` directory and the sidebar-config shape in `astro.config.mjs` that Tasks 5, 6, and 7 add pages/groups to (`starlight({ sidebar: [...] })`, each entry `{ label, items: [{ label, slug }] }` or `{ label, autogenerate: { directory } }`).

- [ ] **Step 1: Install Starlight**

```bash
cd site && npx astro add starlight --yes && cd ..
```

Astro's own `astro add` wires the integration into `astro.config.mjs` automatically. Immediately re-pin it exact (mirroring how `astro` itself is pinned):

```bash
cd site && npm install --save-exact "@astrojs/starlight@$(node -p "require('./package.json').dependencies['@astrojs/starlight']")" && cd ..
```

Record the installed version the same way Task 3 of the v1 plan recorded Astro's: `grep '"@astrojs/starlight"' site/package.json`.

- [ ] **Step 2: Confirm the generated config, then set the sidebar skeleton**

`npx astro add` typically writes something close to this — reconcile whatever it actually produced with this exact shape (the `sidebar` array is what Tasks 5–7 append to):

```js
// site/astro.config.mjs
// SPDX-License-Identifier: Elastic-2.0
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

export default defineConfig({
  site: 'https://corralai.dev',
  integrations: [
    starlight({
      title: 'Corralai docs',
      description: 'Getting started, concepts, running it, and the CLI reference for corralai.',
      social: {
        github: 'https://github.com/pdbethke/corralai',
      },
      // Starlight ships Pagefind (fully local, no network calls) and system
      // fonts by default — no <link> to a Google/Adobe font host anywhere in
      // its default theme. Verified in Task 3 Step 5 and re-verified in
      // Task 8's e2e docs-session network interception.
      customCss: ['./src/styles/starlight-tokens.css'],
      sidebar: [
        { label: 'Getting started', slug: 'getting-started' },
        {
          label: 'Concepts',
          items: [
            { label: 'Mission lifecycle', slug: 'concepts/mission-lifecycle' },
            { label: 'The task queue + verify gate', slug: 'concepts/queue-and-verify' },
            { label: 'Claims & leases', slug: 'concepts/claims-and-leases' },
            { label: 'Memory tiers + the learning loop', slug: 'concepts/memory-and-learning-loop' },
            { label: 'Mission history + replay', slug: 'concepts/history-and-replay' },
            { label: 'Multi-model herds', slug: 'concepts/multi-model-herds' },
            { label: 'The knowledge corpus', slug: 'concepts/knowledge-corpus' },
            { label: 'Trust & security', slug: 'concepts/trust-and-security' },
          ],
        },
        { label: 'Running it', slug: 'running-it' },
        {
          label: 'The UI, tab by tab',
          items: [
            { label: 'The corral (canvas view)', slug: 'ui-tour/corral' },
            { label: 'Progress', slug: 'ui-tour/progress' },
            { label: 'Topology', slug: 'ui-tour/topology' },
            { label: 'Memory', slug: 'ui-tour/memory' },
            { label: 'Proposals', slug: 'ui-tour/proposals' },
            { label: 'Completed + replay + agent windows', slug: 'ui-tour/completed-and-replay' },
          ],
        },
        {
          label: 'CLI reference',
          items: [
            { label: 'corral', slug: 'cli/corral' },
            { label: 'corral-admin', slug: 'cli/corral-admin' },
            { label: 'corral-agent', slug: 'cli/corral-agent' },
            { label: 'corral-harness', slug: 'cli/corral-harness' },
            { label: 'corral-observe', slug: 'cli/corral-observe' },
            { label: 'corral-top', slug: 'cli/corral-top' },
          ],
        },
      ],
    }),
  ],
});
```

- [ ] **Step 3: Daylight-match Starlight's theme tokens**

```css
/* SPDX-License-Identifier: Elastic-2.0 */
/* site/src/styles/starlight-tokens.css — repoint Starlight's own CSS custom
   properties at the same daylight palette as the rest of the site (Task 1),
   so /docs doesn't look like an unrelated skin bolted onto the landing page. */
:root {
  --sl-color-bg: #faf3e6;
  --sl-color-text: #241f18;
  --sl-color-text-accent: #b5780a;
  --sl-color-bg-nav: #f2e9d6;
  --sl-color-bg-sidebar: #f2e9d6;
  --sl-color-hairline: #ddd0b3;
  --sl-color-accent: #b5780a;
}
```

- [ ] **Step 4: Stub content pages so the sidebar resolves (Tasks 5–7 fill these in)**

```bash
mkdir -p site/src/content/docs/concepts site/src/content/docs/ui-tour site/src/content/docs/cli
```

For each of the 15 non-CLI slugs referenced in Step 2's sidebar (`getting-started`, `running-it`, the 8 `concepts/*`, the 6 `ui-tour/*`), create a stub with real, minimal, non-placeholder front matter (Starlight requires `title`; a stub page IS valid content, not a TODO — it just says less than the final page will):

```mdx
---
title: Getting started
description: Install corralai, run the key-free demo, and kick off a first mission.
---

Content for this page lands in a later task of the corralai.dev v2 plan.
```

(Repeat for the other 14 slugs, substituting each sidebar entry's own `label` as the `title` and a one-line description of that entry's topic — e.g. for `concepts/claims-and-leases`: `title: Claims & leases`, `description: How agents claim files and branches without stepping on each other.`.) The 6 `cli/*` pages are NOT stubbed here — Task 4 generates them mechanically and they must not pre-exist as hand-written stubs a generator would then have to overwrite silently.

- [ ] **Step 5: Build and grep for anything that would violate zero-external-requests**

```bash
cd site && npm run build
grep -rn "https://fonts\.\|https://cdn\.\|googleapis\.com\|jsdelivr\|unpkg\.com" dist/ || echo "clean: no external font/CDN references in dist/"
cd ..
```

Expected: `clean: no external font/CDN references in dist/`. If the grep finds a hit, it is almost certainly a Starlight default Google Fonts `<link>` — the fix is `customCss`/`starlight()`'s font override (consult the installed version's own docs under `site/node_modules/@astrojs/starlight/`) to force system fonts, matching `global.css`'s existing `font: 16px/1.5 ui-sans-serif, system-ui, ...` stack; re-run this step until clean.

- [ ] **Step 6: Write `site/tests/docs.spec.ts`**

```ts
// SPDX-License-Identifier: Elastic-2.0
import { test, expect } from '@playwright/test';

test('/docs mounts, renders the sidebar, and stays on-domain', async ({ page }) => {
  const external: string[] = [];
  page.on('request', (req) => {
    const url = new URL(req.url());
    if (url.hostname !== 'localhost' && url.hostname !== '127.0.0.1') external.push(req.url());
  });
  await page.goto('/docs/getting-started/');
  await expect(page.locator('nav')).toBeVisible();
  await expect(page.getByRole('link', { name: 'Getting started' })).toBeVisible();
  expect(external, `unexpected external requests from /docs: ${external.join(', ')}`).toHaveLength(0);
});

test('the docs sidebar has no dead links across every listed section', async ({ page }) => {
  await page.goto('/docs/getting-started/');
  const hrefs = await page.locator('nav a[href^="/docs/"]').evaluateAll((els) =>
    els.map((el) => el.getAttribute('href')!)
  );
  expect(hrefs.length).toBeGreaterThan(10);
  for (const href of new Set(hrefs)) {
    const res = await page.request.get(href);
    expect(res.status(), `${href} returned ${res.status()}`).toBeLessThan(400);
  }
});
```

- [ ] **Step 7: Run the tests**

```bash
cd site && npm run test:e2e -- --grep-invert "hero|OpenGraph|OG image" && npx playwright test docs.spec.ts && cd ..
```

Expected: both new tests PASS.

- [ ] **Step 8: Full build + full e2e**

```bash
cd site && npm run test:e2e && cd ..
```

Expected: PASS (landing-page tests + docs tests together).

- [ ] **Step 9: Commit**

```bash
git add site/astro.config.mjs site/package.json site/package-lock.json site/src/content site/src/styles/starlight-tokens.css site/tests/docs.spec.ts
git commit -m "$(cat <<'EOF'
feat(site): mount Starlight at /docs with a daylight-matched theme + nav skeleton

Sidebar skeleton for getting-started, 8 concepts pages, running-it, the
6-tab UI tour, and the 6-binary CLI reference — content lands in Tasks 4-7.
Starlight's Pagefind search runs fully locally; verified at build (grep for
external font/CDN hosts in dist/) and via a new docs.spec.ts e2e that
intercepts every request during a /docs session and asserts none leave
localhost, preserving the site's zero-external-requests guarantee.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 4: `scripts/gen-cli-docs.sh` + generated CLI reference + CI drift gate

**Files:**
- Modify: `cmd/corral-agent/main.go` (add `-h`/`--help`)
- Modify: `cmd/corral-harness/main.go` (add `-h`/`--help`)
- Test: `cmd/corral-agent/usage_test.go` (new)
- Test: `cmd/corral-harness/usage_test.go` (new)
- Create: `scripts/gen-cli-docs.sh`
- Create (generated, committed): `docs/cli/corral.md`, `docs/cli/corral-admin.md`, `docs/cli/corral-agent.md`, `docs/cli/corral-harness.md`, `docs/cli/corral-observe.md`, `docs/cli/corral-top.md`
- Create (generated, committed): `site/src/content/docs/cli/corral.md`, `corral-admin.md`, `corral-agent.md`, `corral-harness.md`, `corral-observe.md`, `corral-top.md`
- Modify: `.github/workflows/deploy-site.yml`

**Interfaces:**
- Consumes: the six `cmd/*` binaries as built by `go build`; each `main.go`'s top-of-file doc comment (the `// Env:` / `//\tCORRALAI_...` block pattern already used by `cmd/corral`, `cmd/corral-harness`, `cmd/corral-observe`, `cmd/corral-top`).
- Produces: `scripts/gen-cli-docs.sh [--check]` — no-arg mode regenerates all 12 files (6 repo + 6 Starlight copies) and exits 0; `--check` mode regenerates into a temp dir, diffs against the committed files, and exits 1 with a diff on drift. Task 3's sidebar `cli/*` slugs are exactly the filenames this script writes.

Two of the six binaries currently print nothing useful for `-h`: `corral-agent` only recognizes `--version`/`-version`/`version`/`-v` (`cmd/corral-agent/main.go:128-133`) and otherwise proceeds to try to connect to a brain; `corral-harness` has no `-h` handling at all and only prints an error if `HARNESS_CMD` is unset. Per the spec, thin usage text is a product bug, not a doc-generation problem to paper over — fixed here, TDD, before the generator ever runs against them.

- [ ] **Step 1: Write the failing test for `corral-agent`'s new `printUsage()`**

```go
// cmd/corral-agent/usage_test.go
// SPDX-License-Identifier: Elastic-2.0
package main

import "testing"

func TestPrintUsageMentionsEveryEnvVar(t *testing.T) {
	out := usageText()
	for _, want := range []string{
		"CORRAL_BRAIN", "AGENT_ROLE", "AGENT_NAME", "AGENT_WORKSPACE",
		"MODEL_BACKEND", "AGENT_MODEL", "CLOBBER",
	} {
		if !contains(out, want) {
			t.Errorf("usageText() missing env var %q\n---\n%s", want, out)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (func() bool {
		for i := 0; i+len(substr) <= len(s); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}
```

- [ ] **Step 2: Run it to confirm it fails**

```bash
go test ./cmd/corral-agent/... -run TestPrintUsageMentionsEveryEnvVar -v
```

Expected: FAIL — `undefined: usageText`.

- [ ] **Step 3: Add `usageText()` and wire `-h`/`--help` into `corral-agent`**

Add near the top of `cmd/corral-agent/main.go` (after the existing `env` helper):

```go
func usageText() string {
	return `corral-agent — reference LLM-driven agent for the demo (local Ollama by default)

Usage:
  corral-agent            connect to the brain and work the queue
  corral-agent --version  print the build version and exit
  corral-agent -h         print this help and exit

Env:
  CORRAL_BRAIN       brain URL (default http://127.0.0.1:9019/mcp/)
  AGENT_ROLE         builder | tester | pentester | reviewer (default builder)
  AGENT_NAME         display name in the swarm UI (default same as AGENT_ROLE)
  AGENT_WORKSPACE    working directory for edits (default $TMPDIR/corral-demo-ws)
  MODEL_BACKEND      ollama (default) | openai (Gemini/OpenRouter/local, any OpenAI-compatible endpoint)
  AGENT_MODEL        model name passed to the backend (default qwen2.5-coder:7b)
  CLOBBER            set "1" to ignore coordination conflicts and edit anyway (demo of what NOT coordinating looks like)
`
}
```

Replace the existing version-flag loop (`cmd/corral-agent/main.go:128-133`) with:

```go
	for _, a := range os.Args[1:] {
		if a == "--version" || a == "-version" || a == "version" || a == "-v" {
			fmt.Println("corral-agent", version)
			return
		}
		if a == "-h" || a == "--help" || a == "help" {
			fmt.Print(usageText())
			return
		}
	}
```

- [ ] **Step 4: Run the test to confirm it passes**

```bash
go test ./cmd/corral-agent/... -run TestPrintUsageMentionsEveryEnvVar -v
```

Expected: PASS.

- [ ] **Step 5: Same TDD cycle for `corral-harness`**

```go
// cmd/corral-harness/usage_test.go
// SPDX-License-Identifier: Elastic-2.0
package main

import "testing"

func TestUsageTextMentionsEveryEnvVar(t *testing.T) {
	out := usageText()
	for _, want := range []string{
		"CORRAL_BRAIN", "BEE_NAME", "BEE_ROLE", "BEE_WORKSPACE", "HARNESS_CMD",
		"HARNESS_DESC", "BEE_ROUNDS", "HARNESS_TIMEOUT_SECONDS", "HARNESS_IDLE_SECONDS",
		"BEE_PROMPT_FILE",
	} {
		if !contains(out, want) {
			t.Errorf("usageText() missing env var %q\n---\n%s", want, out)
		}
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (func() bool {
		for i := 0; i+len(substr) <= len(s); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}
```

Run it (expect FAIL: `undefined: usageText`), then add to `cmd/corral-harness/main.go`, reusing the exact env-var list already documented in its top-of-file doc comment (lines 14-27):

```go
func usageText() string {
	return `corral-harness — loops a headless coding agent (Claude Code, Gemini CLI, Codex, ...) as a swarm bee over MCP

Usage:
  corral-harness         claim one task, work it, complete it, exit
  corral-harness -h      print this help and exit

Env:
  CORRAL_BRAIN   brain URL (default http://localhost:9019)
  BEE_NAME       swarm name (default Harness)
  BEE_ROLE       role to serve (default builder)
  BEE_WORKSPACE  working directory for the harness (default .)
  HARNESS_CMD    command template; placeholders: {prompt} {mcp_config} {brain}
                 e.g. claude -p {prompt} --mcp-config {mcp_config} \
                      --allowedTools "mcp__corral__*,Read,Write,Edit,Bash" \
                      --permission-mode acceptEdits
  HARNESS_DESC   how to announce this harness (default derived from HARNESS_CMD)
  BEE_ROUNDS     max tasks to run, 0 = forever (default 0)
  HARNESS_TIMEOUT_SECONDS  per-invocation kill deadline (default 900)
  HARNESS_IDLE_SECONDS     backoff when the queue is empty (default 30)
  BEE_PROMPT_FILE optional file replacing the built-in bee prompt; the same
                 placeholders are substituted into it
`
}
```

At the top of `func main()` (`cmd/corral-harness/main.go:129`), before the existing `HARNESS_CMD` check, add:

```go
	for _, a := range os.Args[1:] {
		if a == "-h" || a == "--help" || a == "help" {
			fmt.Print(usageText())
			return
		}
	}
```

- [ ] **Step 6: Run both usage tests plus the full package tests**

```bash
go test ./cmd/corral-agent/... ./cmd/corral-harness/... -v
```

Expected: PASS.

- [ ] **Step 7: Write `scripts/gen-cli-docs.sh`**

```bash
#!/usr/bin/env bash
# SPDX-License-Identifier: Elastic-2.0
#
# scripts/gen-cli-docs.sh [--check] — the CLI reference generator. Builds
# every cmd/* binary, captures its REAL -h output (stdout+stderr combined —
# some binaries print usage to one, some to the other, and the docs must not
# silently miss whichever a given binary picked), pulls the env-var doc-
# comment block out of its main.go header, and emits markdown for each into
# BOTH docs/cli/ (repo) and site/src/content/docs/cli/ (Starlight tree).
#
# --check: regenerate into a scratch dir and diff against the committed
# files instead of overwriting them; exits 1 with the diff on any drift.
# Docs that lie about a flag fail CI (wired into .github/workflows/deploy-site.yml).
set -euo pipefail
cd "$(dirname "$0")/.."

BINARIES=(corral corral-admin corral-agent corral-harness corral-observe corral-top)
CHECK=0
[ "${1:-}" = "--check" ] && CHECK=1

WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

echo "building ${BINARIES[*]}..."
for b in "${BINARIES[@]}"; do
  go build -o "$WORKDIR/$b" "./cmd/$b"
done

extract_env_block() {
  # Pulls the doc-comment lines between "// Env:" and the next blank
  # doc-comment line (or the "package main" line, whichever comes first)
  # out of a main.go, stripping the leading "//" and one optional tab/space.
  local mainfile="$1"
  awk '
    /^\/\/ Env:/ { inenv=1; next }
    /^package main/ { inenv=0 }
    inenv && /^\/\/\t?/ {
      line=$0
      sub(/^\/\/\t?/, "", line)
      if (line == "" ) { blank++; if (blank>1) { inenv=0; next } }
      else { blank=0 }
      print line
    }
    inenv && !/^\/\// { inenv=0 }
  ' "$mainfile"
}

capture_help() {
  local bin="$1"
  # Combine stdout+stderr — corral-admin/corral-agent/corral-harness print to
  # stderr, corral-top/corral-observe use Go's default flag.Usage (also
  # stderr); this generator must not assume which.
  "$WORKDIR/$bin" -h 2>&1 || true
}

gen_one() {
  local b="$1" out="$2"
  local help env_block
  help="$(capture_help "$b")"
  env_block="$(extract_env_block "cmd/$b/main.go")"
  {
    echo "---"
    echo "title: $b"
    echo "description: Generated CLI reference for $b — never hand-written; see scripts/gen-cli-docs.sh."
    echo "---"
    echo
    echo "> Generated by \`scripts/gen-cli-docs.sh\` from $b's own \`-h\` output and its main.go doc comment. Do not hand-edit — run \`scripts/gen-cli-docs.sh\` and commit the result."
    echo
    echo "## Usage"
    echo
    echo '```'
    echo "$help"
    echo '```'
    if [ -n "$env_block" ]; then
      echo
      echo "## Environment variables"
      echo
      echo '```'
      echo "$env_block"
      echo '```'
    fi
  } > "$out"
}

if [ "$CHECK" -eq 1 ]; then
  CHECK_DIR="$WORKDIR/check"
  mkdir -p "$CHECK_DIR/docs" "$CHECK_DIR/site"
  fail=0
  for b in "${BINARIES[@]}"; do
    gen_one "$b" "$CHECK_DIR/docs/$b.md"
    cp "$CHECK_DIR/docs/$b.md" "$CHECK_DIR/site/$b.md"
    if ! diff -u "docs/cli/$b.md" "$CHECK_DIR/docs/$b.md" >/dev/null 2>&1; then
      echo "FAIL: docs/cli/$b.md has drifted from $b's real -h output:" >&2
      diff -u "docs/cli/$b.md" "$CHECK_DIR/docs/$b.md" >&2 || true
      fail=1
    fi
    if ! diff -u "site/src/content/docs/cli/$b.md" "$CHECK_DIR/site/$b.md" >/dev/null 2>&1; then
      echo "FAIL: site/src/content/docs/cli/$b.md has drifted from $b's real -h output:" >&2
      diff -u "site/src/content/docs/cli/$b.md" "$CHECK_DIR/site/$b.md" >&2 || true
      fail=1
    fi
  done
  if [ "$fail" -ne 0 ]; then
    echo "Run: scripts/gen-cli-docs.sh   (then commit the regenerated docs)" >&2
    exit 1
  fi
  echo "OK: generated CLI docs match every binary's real -h output"
else
  mkdir -p docs/cli site/src/content/docs/cli
  for b in "${BINARIES[@]}"; do
    gen_one "$b" "docs/cli/$b.md"
    cp "docs/cli/$b.md" "site/src/content/docs/cli/$b.md"
    echo "wrote docs/cli/$b.md and site/src/content/docs/cli/$b.md"
  done
fi
```

```bash
chmod +x scripts/gen-cli-docs.sh
```

- [ ] **Step 8: Generate the docs and spot-check the densest page**

```bash
bash scripts/gen-cli-docs.sh
cat docs/cli/corral-admin.md
```

Expected: six `wrote ...` lines, then `corral-admin.md` showing the full verb list (`corral-admin ui`, `instruct`, `status`, `whoami`, `mint-observer`, `member`, `mission`, `review`, `findings`, `resolve-findings`, `reference`, `analyze`, `proposals list|show|approve|reject`, and the `Global flags:` line) inside a fenced `Usage` block — a human-readability pass: confirm every verb reads clearly on its own line with no wrapping artifacts from the capture. If corral-admin has no `## Environment variables` section (it has no top-of-file `// Env:` doc comment — it uses `--brain`/`--token` flags instead, already covered by its own usage text), that's correct, not a bug.

- [ ] **Step 9: Run `--check` to confirm it's now clean**

```bash
bash scripts/gen-cli-docs.sh --check
```

Expected: `OK: generated CLI docs match every binary's real -h output`.

- [ ] **Step 10: Wire the drift gate into CI**

Add a step to the `deploy` job of `.github/workflows/deploy-site.yml`, immediately after the existing "Check the player hasn't drifted from the herd" step (both need Go, so add a `setup-go` step to the `deploy` job — it currently only sets up Node):

```yaml
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true
      - name: Check the player hasn't drifted from the herd
        run: bash scripts/sync-site-assets.sh --check
      - name: Check the CLI reference hasn't drifted from the real binaries
        run: bash scripts/gen-cli-docs.sh --check
```

(Insert the new `setup-go` step before the existing `setup-node` step or after — order between the two doesn't matter, neither depends on the other.)

- [ ] **Step 11: Full verification**

```bash
go test ./... -count=1
bash scripts/check-security.sh
cd site && npm run test:e2e && cd ..
bash scripts/gen-cli-docs.sh --check
```

Expected: all green.

- [ ] **Step 12: Commit**

```bash
git add cmd/corral-agent/main.go cmd/corral-agent/usage_test.go cmd/corral-harness/main.go cmd/corral-harness/usage_test.go scripts/gen-cli-docs.sh docs/cli site/src/content/docs/cli .github/workflows/deploy-site.yml
git commit -m "$(cat <<'EOF'
feat(cli,site): generated CLI reference with a CI drift gate

corral-agent and corral-harness gained real -h/--help output (TDD) — a docs
generator can't document text a binary refuses to print, and "even I don't
know how to run the thing properly" was the whole reason for Part C.
scripts/gen-cli-docs.sh builds all six cmd/ binaries, captures each one's
real -h output (stdout+stderr, since binaries disagree on which) plus its
main.go env-var doc-comment block, and emits markdown into both docs/cli/
and the Starlight tree. --check mode diffs regenerated output against the
committed pages and is now wired into deploy-site.yml beside the existing
player-sync check — a stale or dishonest CLI doc fails CI.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 5: Concepts pages, batch 1 (getting started, mission lifecycle, queue/verify, claims)

**Files:**
- Modify: `site/src/content/docs/getting-started.mdx`
- Modify: `site/src/content/docs/concepts/mission-lifecycle.mdx`
- Modify: `site/src/content/docs/concepts/queue-and-verify.mdx`
- Modify: `site/src/content/docs/concepts/claims-and-leases.mdx`
- Test: `site/tests/docs.spec.ts` (extend the dead-link test's implicit coverage — no new test file needed; the existing "no dead links across every listed section" test already walks these four pages once they're linked from the sidebar, which Task 3 already did)

**Interfaces:**
- Consumes: `docs/corral/mission-lifecycle.md`, `docs/corral/verify-gate.md`, `docs/corral/claims-and-leases.md`, `README.md`'s quickstart section (source-of-truth text this task copies/tightens, never invents).
- Produces: nothing later tasks depend on by name.

- [ ] **Step 1: `getting-started.mdx`** (source: `README.md`'s install/demo/quickstart section — verbatim-or-tightened)

```mdx
---
title: Getting started
description: Install corralai, run the key-free demo, and kick off a first mission.
---

## Install

```bash
go install github.com/pdbethke/corralai/cmd/corral@latest
```

Or clone and build from source:

```bash
git clone https://github.com/pdbethke/corralai
cd corralai
go build ./...
```

## The key-free demo

The fastest way to see a mission run is the bundled demo compose profile —
it runs the brain plus a small herd of agents against a local Ollama model,
no API keys required:

```bash
cd deploy/demo
make demo-mission
```

Watch progress at `http://localhost:9019` — the corral (canvas) tab shows
agents moving live; the progress tab shows the task queue draining.

## Your first mission

Against a brain you're running yourself (dev mode — no `CORRALAI_OIDC_ISSUER`
set, so auth is off):

```bash
go run ./cmd/corral
```

Then, from another terminal:

```bash
go run ./cmd/corral-admin mission create "add rate limiting to the ingest endpoint"
```

Open `http://127.0.0.1:9019` to watch it plan, build, verify, and converge.
See [Running it](/docs/running-it/) for env vars and auth-on setup, and
[the CLI reference](/docs/cli/corral-admin/) for every `corral-admin` verb.
```

- [ ] **Step 2: `concepts/mission-lifecycle.mdx`** (source: `docs/corral/mission-lifecycle.md`)

```mdx
---
title: Mission lifecycle
description: Directive in, converged mission out — planning, sprints, review, and re-planning.
---

A **directive** — one sentence of intent — becomes a **mission**. The brain
decomposes it into a dependency-ordered task queue, one phase per role
(research → design → build-core → build → test ∥ secops ∥ perf → integrate →
docs → retro). Agents pull ready tasks (no unmet dependencies, no active
claim) and execute them.

Missions run in **sprints**: a batch of tasks reaches a checkpoint, the lead
reviews outcomes, and either the mission converges (the client accepts) or a
new sprint starts — informed by findings from the sprint just finished.

The **client** role — a human, or a modeled product-owner agent in the demo
— accepts or requests changes at each checkpoint. Nothing converges without
that acceptance step: this is the same human-gate principle that governs
[skill proposals](/docs/concepts/memory-and-learning-loop/) and [trust
scope](/docs/concepts/trust-and-security/) elsewhere in the system.

See `corral-admin mission list|status <id>|create <directive...>` in the
[CLI reference](/docs/cli/corral-admin/) to drive missions from the command
line, and [Mission history + replay](/docs/concepts/history-and-replay/) for
how a finished mission is recorded and replayed afterward.
```

- [ ] **Step 3: `concepts/queue-and-verify.mdx`** (source: `docs/corral/verify-gate.md`)

```mdx
---
title: The task queue + verify gate
description: How tasks move from queued to done, and why nothing completes without a verify command passing.
---

Every task carries an optional **Verify** command. A task cannot be marked
done until its Verify command exits 0 — an agent claiming "I finished this"
is not evidence; the command's exit code is. Tasks with no Verify command
are trusted on the agent's own completion report (used for genuinely
unverifiable work — a design write-up, a research summary).

Tasks are pulled from the queue in dependency order: a task with unmet
dependencies, or one already under an active [claim](/docs/concepts/claims-and-leases/)
by another agent, is never handed out twice. When a Verify command fails,
the task returns to the queue with the failure captured as a **finding** —
which feeds the [re-planning loop](/docs/concepts/mission-lifecycle/)
described above, not a silent retry.
```

- [ ] **Step 4: `concepts/claims-and-leases.mdx`** (source: `docs/corral/claims-and-leases.md`)

```mdx
---
title: Claims & leases
description: How agents claim files and branches without stepping on each other's work.
---

Before editing, an agent **claims** the paths (and, for repo-work missions,
the branch) it's about to touch. A claim is a time-boxed lease, not a
permanent lock: it expires automatically if the agent stalls or crashes, so
one dead agent can't freeze a whole mission's queue.

A second agent that tries to claim an already-leased path is refused and
told who holds it and for how much longer — it backs off and picks a
different ready task instead of racing the first agent's edit. This is what
`CLOBBER=1` on [`corral-agent`](/docs/cli/corral-agent/) deliberately
disables, as a demo of what uncoordinated multi-agent editing looks like:
watch two agents pile onto the same file with claims turned off, then
compare against the coordinated run.

`corral-admin status` shows active claims alongside active agents and recent
work — see the [CLI reference](/docs/cli/corral-admin/).
```

- [ ] **Step 5: Build and run the docs link-check test**

```bash
cd site && npm run build && npx playwright test docs.spec.ts && cd ..
```

Expected: PASS — all four new pages resolve and the sidebar has no dead links pointing at them.

- [ ] **Step 6: Commit**

```bash
git add site/src/content/docs/getting-started.mdx site/src/content/docs/concepts/mission-lifecycle.mdx site/src/content/docs/concepts/queue-and-verify.mdx site/src/content/docs/concepts/claims-and-leases.mdx
git commit -m "$(cat <<'EOF'
docs(site): concepts batch 1 — getting started, mission lifecycle, queue/verify, claims

Content sourced verbatim-or-tightened from README.md and docs/corral/
{mission-lifecycle,verify-gate,claims-and-leases}.md — no new capability
claims. Cross-links to the CLI reference (Task 4) and forward to the
concepts pages Task 6 fills in.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 6: Concepts pages, batch 2 (memory/learning loop, history/replay, multi-model, corpus, trust)

**Files:**
- Modify: `site/src/content/docs/concepts/memory-and-learning-loop.mdx`
- Modify: `site/src/content/docs/concepts/history-and-replay.mdx`
- Modify: `site/src/content/docs/concepts/multi-model-herds.mdx`
- Modify: `site/src/content/docs/concepts/knowledge-corpus.mdx`
- Modify: `site/src/content/docs/concepts/trust-and-security.mdx`
- Modify: `site/src/content/docs/running-it.mdx`

**Interfaces:**
- Consumes: `docs/corral/memory-etiquette.md`, `docs/DESIGN.md` (learning loop, trust model, history/replay sections), `site/src/components/LearningLoop.astro`/`KnowledgeCorpus.astro` (already-verified copy from the v1 site, safe to reuse/expand), `docs/corral/demo-map.md`, `cmd/corral/main.go`'s env-var doc block (Task 4's generated `/docs/cli/corral/` page is the canonical env reference — this page cross-links to it rather than duplicating 40 env vars by hand).
- Produces: nothing later tasks depend on by name.

- [ ] **Step 1: `concepts/memory-and-learning-loop.mdx`** (source: `docs/corral/memory-etiquette.md` + `LearningLoop.astro`'s existing verified copy)

```mdx
---
title: Memory tiers + the learning loop
description: Advisory-to-vetted memory, skill proposals, and the human gate that promotes one into the other.
---

## Memory tiers

Memory entries start **advisory** — written by any agent, searched by
everyone, but not yet trusted enough to shape behavior automatically.
Promotion to **vetted** requires the human gate described below; only vetted
guidance is injected into every later mission's instructions.

## The learning loop

Recurring failure signatures (the same finding, again and again) and
clusters of similar lessons are swept into **skill proposals**: an LLM drafts
corrective guidance plus a reusable skill, Shep announces the pending
proposal at standup, and the operator approves or rejects it — from the
Proposals tab (a live count badge) or `corral-admin proposals`. Approval
promotes the guidance into vetted memory and a versioned skill artifact;
every later mission's instructions carry the top vetted lessons
(fence-wrapped, clearly labeled, capped at 3) so the herd starts each
mission already warned. The loop watches its own efficacy: if the same
signature keeps recurring after promotion, a revision proposal reopens for
the human to reconsider.

See `corral-admin proposals list|show|approve|reject` in the
[CLI reference](/docs/cli/corral-admin/).
```

- [ ] **Step 2: `concepts/history-and-replay.mdx`** (source: `internal/brain/history.go`, `internal/brain/replay.go` — the same structs the site's own hero replay is built from)

```mdx
---
title: Mission history + replay
description: Every mission is recorded; any finished mission can be replayed exactly as it happened.
---

Every mission's timeline is recorded as a stream of events (`/api/replay?mission=N`)
— agent actions, task transitions, findings, re-plans — with no positions
baked in; the replay client recomputes layout itself, so the same recording
looks right at any window size. `/api/history` lists every mission with its
directive, status, task/finding counts, and duration.

This is not a demo-only feature: the landing page's hero **is** a real
recorded mission, replaying through the identical player the product UI
uses on its own Completed tab — see [the UI tour's Completed
page](/docs/ui-tour/completed-and-replay/) for the same replay bar inside
the actual product.

`corral-top` (a read-only terminal viewport) and `corral-observe` (a
credentialed reverse proxy for remote viewing) both read from this same
state surface — see their pages in the [CLI reference](/docs/cli/corral-top/).
```

- [ ] **Step 3: `concepts/multi-model-herds.mdx`** (source: `README.md`'s multi-model section, `cmd/corral-agent/main.go`'s `MODEL_BACKEND`)

```mdx
---
title: Multi-model herds
description: Role-based model policy, harness workers bringing their own auth, and model comparison.
---

Different roles can run different models: a `CORRALAI_ROLE_MODELS` policy
maps role → model, so (for example) a reviewer role can run a stronger model
than a builder role doing routine work — see the full variable in
[corral's env reference](/docs/cli/corral/).

`corral-harness` workers bring their own model AND their own auth (e.g. a
Claude Max subscription instead of per-call API billing) — the harness
contract is nothing but MCP tool calls against the brain
(bootstrap → claim_task → work → complete_task); `corral-agent` is merely
the reference implementation of the same contract, wired to a local Ollama
model by default (`MODEL_BACKEND=ollama`, `AGENT_MODEL=qwen2.5-coder:7b`) or
any OpenAI-compatible endpoint (`MODEL_BACKEND=openai`, e.g. Gemini or
OpenRouter). `corral-admin analyze` can report on `model_comparison` across
a mission's agents once more than one model has done work in it.
```

- [ ] **Step 4: `concepts/knowledge-corpus.mdx`** (source: `KnowledgeCorpus.astro`'s existing verified copy)

```mdx
---
title: The knowledge corpus (CORRAL.md)
description: A repo's own working knowledge, read by developers, coding agents, and the herd alike.
---

A repo that runs with corralai can carry its working knowledge as a
markdown corpus in the repo itself: `CORRAL.md` at the root as the entry
point, `docs/corral/*.md` as the corpus. The same corpus serves four
readers — developers read it as onboarding docs, any developer's coding
agent queries it conversationally, the herd itself searches it before
working and extends memory as it learns, and it grows the way code does —
through ordinary pull requests, where code review is the same gate that
[vetted memory](/docs/concepts/memory-and-learning-loop/) passes through.

This site's own docs are built the same way this page describes: sourced
from `README.md`, `docs/DESIGN.md`, and `docs/corral/*.md`, restructured for
a sidebar rather than duplicated by hand.
```

- [ ] **Step 5: `concepts/trust-and-security.mdx`** (source: `docs/DESIGN.md`'s trust-model/honesty sections)

```mdx
---
title: Trust & security
description: The trust model, the human gate, observer tokens, delegation, and sandbox jails — with honest scope framing.
---

## The human gate

Nothing that changes what future missions do — a memory promotion, a skill
proposal, a mission's final acceptance — happens without a human approving
it. Agents propose; a human approves. This is the same gate described in
[mission lifecycle](/docs/concepts/mission-lifecycle/) and [the learning
loop](/docs/concepts/memory-and-learning-loop/), stated here as the security
property it actually is: an agent cannot unilaterally make itself, or a
later agent, more trusted.

## Observer tokens and delegation

A **read-only observer token**, minted via `mint_observer` (or
`corral-admin mint-observer`), grants view-only access to a brain's live
state — `corral-observe` uses exactly this token shape, and as defense in
depth refuses non-GET methods locally even if handed a non-read-only token
by mistake. Full write access is scoped to authenticated members and
superusers via the brain's own principal store, seeded day-0 from
`CORRALAI_ALLOWED_PRINCIPALS`/`CORRALAI_ADMIN_PRINCIPALS` and canonical
in the database after.

## Sandbox jails

Agent-executed commands run inside a sandbox boundary (see `internal/sandbox`
in the source) rather than directly against the operator's own filesystem —
the demo's `CLOBBER=1` mode (see [multi-model herds](/docs/concepts/multi-model-herds/))
still runs inside this same sandbox; it only disables coordination claims,
not the sandbox itself.

## Honest scope

Corralai coordinates agents and enforces process gates — it does not itself
guarantee the correctness of any given agent's code, model output, or
judgment. The verify gate, findings, and human review exist because of that
limit, not instead of it.
```

- [ ] **Step 6: `running-it.mdx`** (source: `cmd/corral/main.go`'s env doc block via cross-link, `deploy/demo/README.md`, generic tunnel pattern — NO personal infrastructure details)

```mdx
---
title: Running it
description: Dev mode vs auth-on, the demo compose profiles, and deploying a brain behind a tunnel.
---

## Dev mode vs auth-on

With `CORRALAI_OIDC_ISSUER` unset, the brain runs with auth disabled — any
caller is trusted. This is for local development only. Setting
`CORRALAI_OIDC_ISSUER` to any OIDC provider (Keycloak, Auth0, Okta, Dex,
Authentik, or others) turns auth on: callers must present a valid token for
that issuer and the configured `CORRALAI_OIDC_AUDIENCE`. See the full env
reference on [corral's CLI page](/docs/cli/corral/) — every variable there
comes straight from the binary's own source comment, generated, not
hand-copied.

## The demo compose profiles

`deploy/demo/` runs the brain plus a small herd of agents against a local
model with `make demo-mission` — see [Getting started](/docs/getting-started/).

## Deploying a brain behind a tunnel

The brain listens on `CORRALAI_ADDR` (default `127.0.0.1:9019`) and expects
to be reached through a reverse tunnel or proxy that terminates TLS and
forwards to that local address — the general pattern is: run `corral`
bound to loopback, run a tunnel client (any provider) pointed at that same
loopback address, and set `CORRALAI_ALLOWED_HOSTS` to the public hostname(s)
the tunnel exposes so the brain's Host-header check accepts them. Corralai
supports built-in TLS via `CORRALAI_TLS_CERT`/`CORRALAI_TLS_KEY`, or
`CORRALAI_TLS_AUTOCERT_DOMAINS` for automatic Let's Encrypt certificates,
as alternatives to a tunnel for a brain with a public IP. This page
describes the pattern only — specific tunnel providers, hostnames, and
ports are a deployment's own operational detail, not part of this doc.
```

- [ ] **Step 7: Build and run the docs link-check test**

```bash
cd site && npm run build && npx playwright test docs.spec.ts && cd ..
```

Expected: PASS.

- [ ] **Step 8: Commit**

```bash
git add site/src/content/docs/concepts/memory-and-learning-loop.mdx site/src/content/docs/concepts/history-and-replay.mdx site/src/content/docs/concepts/multi-model-herds.mdx site/src/content/docs/concepts/knowledge-corpus.mdx site/src/content/docs/concepts/trust-and-security.mdx site/src/content/docs/running-it.mdx
git commit -m "$(cat <<'EOF'
docs(site): concepts batch 2 — memory/learning loop, history/replay, multi-model, corpus, trust, running it

Sourced from docs/corral/memory-etiquette.md, docs/DESIGN.md, and the
existing verified LearningLoop.astro/KnowledgeCorpus.astro copy. Running It
describes the tunnel-deployment pattern generically — no hostnames, no
provider names, no personal infrastructure details, per the site's standing
privacy rule. Cross-links to the generated CLI reference (Task 4) instead of
duplicating the ~40-variable env block by hand.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 7: UI tab tour — seeded brain, screenshots, alt text

**Files:**
- Modify: `site/src/content/docs/ui-tour/corral.mdx`
- Modify: `site/src/content/docs/ui-tour/progress.mdx`
- Modify: `site/src/content/docs/ui-tour/topology.mdx`
- Modify: `site/src/content/docs/ui-tour/memory.mdx`
- Modify: `site/src/content/docs/ui-tour/proposals.mdx`
- Modify: `site/src/content/docs/ui-tour/completed-and-replay.mdx`
- Create: `site/src/assets/ui-tour/corral.png`, `progress.png`, `topology.png`, `memory.png`, `proposals.png`, `completed.png`, `replay-bar.png`, `agent-window.png` (8 images)
- Create: `cmd/seedtour/main.go` (throwaway — deleted at the end of this task)

**Interfaces:**
- Consumes: the same seeded-scratch-brain pattern as Task 2, run independently here (see Ambiguities — each screenshot-taking task seeds and tears down its own brain rather than sharing a live session across non-adjacent tasks).
- Produces: nothing later tasks depend on by name.

- [ ] **Step 1: Write the throwaway seed program**

This one needs a richer mission than Task 2's single-directive seed: enough state to populate proposals, memory entries, and a completed mission alongside an in-progress one, so every tab has something real to screenshot.

```go
// cmd/seedtour/main.go — THROWAWAY, not committed. Deleted at the end of
// this task (Step 9). Seeds a scratch brain with: one in-progress mission
// (for corral/progress/topology tabs), one pending skill proposal (for the
// proposals tab), a few memory entries (for the memory tab), and one
// completed mission with a full replay stream (for the completed tab).
package main

import (
	"fmt"
	"os"

	"github.com/pdbethke/corralai/internal/brain"
	"github.com/pdbethke/corralai/internal/coord"
	"github.com/pdbethke/corralai/internal/memory"
)

func main() {
	dbPath := os.Getenv("CORRALAI_DB")
	memPath := os.Getenv("CORRALAI_MEMORY_DB")
	if dbPath == "" || memPath == "" {
		fmt.Fprintln(os.Stderr, "CORRALAI_DB and CORRALAI_MEMORY_DB must both be set to scratch paths")
		os.Exit(1)
	}
	store, err := coord.Open(dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open coord store:", err)
		os.Exit(1)
	}
	defer store.Close()
	memStore, err := memory.Open(memPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "open memory store:", err)
		os.Exit(1)
	}
	defer memStore.Close()

	if err := brain.SeedDemoMission(store, "add rate limiting to the ingest endpoint"); err != nil {
		fmt.Fprintln(os.Stderr, "seed in-progress mission:", err)
		os.Exit(1)
	}
	if err := brain.SeedDemoMission(store, "migrate the queue storage to duckdb"); err != nil {
		fmt.Fprintln(os.Stderr, "seed completed mission:", err)
		os.Exit(1)
	}
	// mark the second mission done so it shows on the Completed tab with a
	// full replay stream, matching the pattern task-1-report.md used.
	if err := coord.SetMissionStatus(store, 2, "done"); err != nil {
		fmt.Fprintln(os.Stderr, "mark mission 2 done:", err)
		os.Exit(1)
	}
	if err := memStore.AddAdvisoryEntry("build", "prefer table-driven tests for the queue package"); err != nil {
		fmt.Fprintln(os.Stderr, "seed memory entry:", err)
		os.Exit(1)
	}
	if err := memStore.AddAdvisoryEntry("test", "always run go vet before go test in CI"); err != nil {
		fmt.Fprintln(os.Stderr, "seed memory entry:", err)
		os.Exit(1)
	}
	if err := brain.SeedPendingProposal(store, "retries without backoff", "add exponential backoff to the ingest retry helper"); err != nil {
		fmt.Fprintln(os.Stderr, "seed proposal:", err)
		os.Exit(1)
	}
	fmt.Println("seeded a tour brain into", dbPath, "and", memPath)
}
```

If any of `brain.SeedDemoMission`, `coord.SetMissionStatus`, `memory.Open(...).AddAdvisoryEntry`, or `brain.SeedPendingProposal` don't exist under those exact names (check with `grep -rn "func SeedDemoMission\|func SetMissionStatus\|func AddAdvisoryEntry\|func SeedPendingProposal\|func SeedPending" internal/brain internal/coord internal/memory`), replace the call with whichever real, already-tested helper in that package produces the equivalent state — the test files under each of those three packages (`internal/brain/*_test.go`, `internal/coord/*_test.go`, `internal/memory/*_test.go`) are the source of truth for what's actually callable; this is throwaway glue code, not new product surface, so match existing test helpers rather than inventing new store methods.

- [ ] **Step 2: Seed and start the scratch brain**

```bash
export SCRATCH=$(mktemp -d)
export CORRALAI_DB="$SCRATCH/coord.sqlite3"
export CORRALAI_MEMORY_DB="$SCRATCH/memory.duckdb"
export CORRALAI_PRINCIPALS_DB="$SCRATCH/principals.sqlite3"
export CORRALAI_GATEWAY_DB="$SCRATCH/gateway.sqlite3"
export CORRALAI_ARTIFACTS_DB="$SCRATCH/artifacts.sqlite3"
export CORRALAI_OIDC_ISSUER=""   # dev mode — this is a scratch brain, never a real one
go run ./cmd/seedtour
go run ./cmd/corral &
sleep 2
```

- [ ] **Step 3: Capture the six tab screenshots + the replay bar + an agent window**

Use the Playwright browser tool against `http://127.0.0.1:9019/`:

1. Navigate to `/`, wait for the canvas to render agents, screenshot → `site/src/assets/ui-tour/corral.png` (the default "corral" canvas view, `#tab-swarm` in the DOM).
2. Click `#tab-progress`, wait for the task list to render, screenshot → `progress.png`.
3. Click `#tab-topology`, screenshot → `topology.png`.
4. Click `#tab-memory`, confirm the two seeded advisory entries are visible, screenshot → `memory.png`.
5. Click `#tab-proposals`, confirm the seeded pending proposal and its badge count are visible, screenshot → `proposals.png`.
6. Click `#tab-completed`, confirm the seeded completed mission ("migrate the queue storage to duckdb") is listed, screenshot → `completed.png`.
7. From the Completed tab, open that mission's replay (click into it, then start replay), screenshot the `#replay` control bar specifically (clipped to that element) → `replay-bar.png`.
8. Click an agent node on the corral canvas to open its floating agent-detail window (`#windows` → the per-agent draggable window), screenshot clipped to that window → `agent-window.png`.

Save all eight under `site/src/assets/ui-tour/`. Review each by eye before the next step — confirm every visible agent name, task title, and file path is the synthetic seed content from Step 1, nothing else (this is the same manual-review discipline as `scrub-golden-run.py`'s manifest step, applied by eye since these are PNGs, not JSON).

- [ ] **Step 4: Tear down and delete the seed program**

```bash
kill %1
rm -rf "$SCRATCH"
rm -rf cmd/seedtour
```

- [ ] **Step 5: Write the six UI-tour pages with real alt text**

```mdx
---
title: The corral (canvas view)
description: The default tab — every agent, live, moving through the mission.
---

The canvas is the default view: every active agent renders as a moving node,
connected by lines to the files and paths it's currently touching. Click any
agent to open its floating detail window (see below) — ask it a question, or
just watch its status update in place.

![The corral canvas view showing several agent nodes connected to file-path nodes during an active mission](../../../assets/ui-tour/corral.png)

![A floating agent-detail window opened from the canvas, showing one agent's current task and status](../../../assets/ui-tour/agent-window.png)
```

```mdx
---
title: Progress
description: The task queue, phase by phase, with done/in-progress/blocked status per task.
---

The Progress tab lists every task in the mission's queue, grouped by phase
(research, design, build-core, build, test, secops, perf, integrate, docs,
retro), each with its current status. This is the queue [claims and
verify](/docs/concepts/queue-and-verify/) act on — a task shown here as
"in progress" has an active claim; one shown as "blocked" is waiting on a
dependency.

![The Progress tab showing the task queue grouped by phase, with per-task status](../../../assets/ui-tour/progress.png)
```

```mdx
---
title: Topology
description: The dependency graph between tasks — what's blocking what.
---

The Topology tab renders the mission's task-dependency graph directly,
rather than inferring it from the flat Progress list — useful for seeing at
a glance why a particular task hasn't started yet.

![The Topology tab showing the task dependency graph for the active mission](../../../assets/ui-tour/topology.png)
```

```mdx
---
title: Memory
description: The advisory and vetted memory entries the herd has written and searched.
---

The Memory tab lists the brain's own memory store — see [memory tiers and
the learning loop](/docs/concepts/memory-and-learning-loop/) for how an
entry moves from advisory to vetted.

![The Memory tab listing advisory memory entries written by agents during past missions](../../../assets/ui-tour/memory.png)
```

```mdx
---
title: Proposals
description: Pending skill proposals awaiting the human gate.
---

The Proposals tab shows every pending skill proposal, with the live count
badge that also appears on the tab itself. Approving here (or via
`corral-admin proposals approve <id>`) promotes the proposal's guidance into
vetted memory — see [the learning loop](/docs/concepts/memory-and-learning-loop/).
This tab's label itself is skin-aware — some visual skins rename it (e.g.
"anomalies") while keeping the same underlying approve/reject flow.

![The Proposals tab showing one pending skill proposal with its approve/reject controls](../../../assets/ui-tour/proposals.png)
```

```mdx
---
title: Completed + replay + agent windows
description: Finished missions, and the replay bar that plays any of them back exactly as they happened.
---

The Completed tab lists every finished mission. Opening one starts its
replay — the same replay bar and player that this site's own landing-page
hero embeds (see [mission history + replay](/docs/concepts/history-and-replay/)).

![The Completed tab listing a finished mission](../../../assets/ui-tour/completed.png)

![The replay control bar: play/pause, a scrub bar, a position label, and a speed selector](../../../assets/ui-tour/replay-bar.png)
```

- [ ] **Step 6: Build and run the docs link-check test**

```bash
cd site && npm run build && npx playwright test docs.spec.ts && cd ..
```

Expected: PASS — all six pages resolve and every `<img>` has real alt text (Task 8's a11y-style check, extended to `/docs`, will assert this explicitly).

- [ ] **Step 7: Commit**

```bash
git add site/src/content/docs/ui-tour site/src/assets/ui-tour
git commit -m "$(cat <<'EOF'
docs(site): UI tab tour with seeded-scratch-brain screenshots

One page per tab (corral, progress, topology, memory, proposals, completed)
plus the replay bar and a floating agent-detail window — eight screenshots
total, all captured from a throwaway seeded scratch brain (cmd/seedtour,
deleted before this commit), never a personal/live one. Every image has
descriptive alt text.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 8: e2e extension (docs search + contrast) + final verification + README pointer refresh

**Files:**
- Modify: `site/tests/docs.spec.ts` (add the Pagefind search test and an a11y pass over `/docs`)
- Modify: `README.md` (point at the live docs site)

**Interfaces:**
- Consumes: everything from Tasks 1-7.
- Produces: nothing — this is the closing verification task.

- [ ] **Step 1: Write the Pagefind search test**

```ts
// Append to site/tests/docs.spec.ts
test('Pagefind search works fully offline and returns a real result', async ({ page }) => {
  const external: string[] = [];
  page.on('request', (req) => {
    const url = new URL(req.url());
    if (url.hostname !== 'localhost' && url.hostname !== '127.0.0.1') external.push(req.url());
  });
  await page.goto('/docs/getting-started/');
  // Starlight's default search trigger is a button in the header that opens
  // a dialog containing the Pagefind-backed input.
  await page.getByRole('button', { name: /search/i }).click();
  const searchInput = page.getByRole('searchbox');
  await searchInput.fill('claims');
  await expect(page.getByRole('link', { name: /Claims & leases/i })).toBeVisible({ timeout: 5000 });
  await page.getByRole('link', { name: /Claims & leases/i }).click();
  await expect(page).toHaveURL(/claims-and-leases/);
  expect(external, `Pagefind search made an external request: ${external.join(', ')}`).toHaveLength(0);
});

test('the docs pages have no obvious accessibility footguns', async ({ page }) => {
  await page.goto('/docs/ui-tour/corral/');
  const h1Count = await page.locator('h1').count();
  expect(h1Count, 'expected exactly one <h1> on the docs page').toBe(1);
  const images = page.locator('img');
  const imgCount = await images.count();
  expect(imgCount, 'expected the corral UI-tour page to have screenshots').toBeGreaterThan(0);
  for (let i = 0; i < imgCount; i++) {
    const alt = await images.nth(i).getAttribute('alt');
    expect(alt, `image ${i} is missing alt text`).toBeTruthy();
  }
});
```

- [ ] **Step 2: Run the new tests**

```bash
cd site && npm run build && npx playwright test docs.spec.ts && cd ..
```

Expected: PASS. If the search test fails on the button/role selector, inspect the installed Starlight version's actual header markup (`site/node_modules/@astrojs/starlight/`) and adjust the `getByRole` calls to match — Starlight's search trigger accessible name is stable across versions but not guaranteed identical wording; match what's actually rendered.

- [ ] **Step 3: Point the README at the live docs**

Find the README's existing link to the site (search for `corralai.dev` in `README.md`) and add a docs pointer immediately after it:

```markdown
See it live: [corralai.dev](https://corralai.dev) — a real recorded mission replaying on the landing page. Full docs, concepts, a UI tour, and the CLI reference: [corralai.dev/docs](https://corralai.dev/docs).
```

- [ ] **Step 4: Full final verification — every gate green**

```bash
go test ./... -count=1
go vet ./...
bash scripts/check-security.sh
bash scripts/sync-site-assets.sh --check
bash scripts/gen-cli-docs.sh --check
cd site && npm run build && npm run test:e2e && cd ..
```

Expected: every command exits 0.

- [ ] **Step 5: Full-page screenshots for the human**

```bash
cd site && npm run preview -- --port 4321 &
sleep 2
```

Use the Playwright browser tool to capture: the daylight landing page (`/`), the `/docs/getting-started/` page (sidebar + content), and one UI-tour page (`/docs/ui-tour/corral/`, confirming the seeded screenshot renders inline) — three screenshots for the record (e.g. `/tmp/corralai-v2-landing.png`, `/tmp/corralai-v2-docs.png`, `/tmp/corralai-v2-ui-tour.png`). Then:

```bash
kill %1
```

- [ ] **Step 6: Commit**

```bash
git add site/tests/docs.spec.ts README.md
git commit -m "$(cat <<'EOF'
test(site): docs search + a11y e2e coverage; point README at the live docs

Pagefind search is verified fully offline (network-intercepted, same
discipline as the landing page's zero-external-requests test) and a quick
a11y pass runs over the UI-tour page specifically, since it's the page with
the most images. README now points readers at corralai.dev/docs for the
full concepts library, UI tour, and generated CLI reference this plan added.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 9: Recordings gallery + build-time DuckDB analytics

**Files:**
- Modify: `internal/brain/replay.go` (ReplayEvent gains `Model`; findings/telemetry loops populate it)
- Modify: `internal/telemetry/store.go:154-176` (`EventsForMission` selects the `model` column)
- Test: `internal/brain/replay_test.go` (new `TestBuildReplayStreamCarriesModel`)
- Modify: `scripts/scrub-golden-run.py` (new `models` subcommand)
- Modify: `scripts/export-golden-run.sh` (`--slug`, `--rederive-meta`, `models` field in the meta sidecar, new default `--out`)
- Move: `site/src/data/golden-run.json` → `site/src/data/recordings/golden-run.json` (and `.meta.json` alongside)
- Modify: `site/src/components/Hero.astro` (import paths only)
- Modify: `site/tests/site.spec.ts` (deny-list test globs every recording)
- Create: `site/scripts/build-analytics.mjs`
- Modify: `site/package.json` (`@duckdb/node-api` devDependency; `prebuild`/`predev` chain)
- Modify: `.gitignore` (add `site/src/data/analytics.json`)
- Create: `site/src/pages/recordings.astro`
- Modify: `site/src/components/WatchItBack.astro` ("more recordings" link)
- Modify: `site/src/components/SiteFooter.astro` (truthful built-on line)
- Modify: `site/src/content/docs/concepts/multi-model-herds.mdx` (fleet-oracle/MotherDuck paragraph)
- Test: `site/tests/recordings.spec.ts`

**Interfaces:**
- Consumes: `startReplay(streamOrUrl)` from `replay-player.js` (already accepts a resolved `{events:[...]}` object — no player change); Task 1's `--stage-*` tokens for the full-width player frame; the export pipeline from the v1 plan's Task 2 (`scripts/export-golden-run.sh` + `scripts/scrub-golden-run.py`, flags as shipped: `--mission/--brain-url/--bearer/--i-know/--yes/--out`, meta sidecar derived from `--out`).
- Produces: `ReplayEvent.Model string` (JSON `model,omitempty`) plus `Detail["backend"]` on queue-derived finding events (Task 10's recording carries these); `scripts/export-golden-run.sh --slug NAME` (writes `site/src/data/recordings/NAME.json` + `.meta.json`, meta now including `"models": [...]`) and `--rederive-meta PATH.json` (recomputes `models` for an already-committed stream); `python3 scripts/scrub-golden-run.py models FILE` (prints a sorted JSON array of `backend:model` labels); `site/src/data/analytics.json` (build-generated, gitignored: `{findings_by_severity:[{slug,severity,n}], findings_by_model:[{model,findings}], task_durations:[{slug,tasks,avg_seconds,max_seconds}]}`); the `/recordings/` page Task 10 populates and screenshots — and Task 12 later upgrades into the full cockpit (this task ships it canvas-only; the cockpit panel DOM ids are added by Task 12, whose renderers null-guard so the dependency is safe in both directions).

Ground truth this task is built on (verified in source): `ReplayEvent` (`internal/brain/replay.go:16-22`) has NO model field today; `telemetry.Event` HAS a `Model` column (`internal/telemetry/store.go:30`) but `EventsForMission` (`store.go:156`) doesn't select it, so it never reaches the stream; queue findings carry `ReporterModel`/`ReporterBackend` (`internal/queue/findings.go:39`) but `BuildReplayStream`'s findings loop drops both. So the spec's "derive models from the stream" is impossible against the code as shipped — this task fixes the product (TDD) so streams are self-describing, which the build-time analytics REQUIRE anyway (the site build sees only committed JSON, never the brain's telemetry DB).

- [ ] **Step 1: Write the failing Go test**

Append to `internal/brain/replay_test.go`:

```go
// TestBuildReplayStreamCarriesModel: model identity must ride through the
// stream from BOTH sources that know it — the queue's findings table
// (reporter_model/reporter_backend) and the telemetry event log's model
// column — so an exported recording is self-describing about what models
// built it (site Part D derives meta.models and per-model analytics from
// the committed stream alone; it never sees the brain's telemetry DB).
func TestBuildReplayStreamCarriesModel(t *testing.T) {
	q, tel, mid := seedReplayMission(t)
	if _, err := q.AddFinding(queue.Finding{MissionID: mid, Reporter: "bee2", Type: "bug",
		Severity: "high", Target: "y.go", ReporterModel: "qwen2.5-coder:7b", ReporterBackend: "ollama"}); err != nil {
		t.Fatal(err)
	}
	if err := tel.Record(telemetry.Event{MissionID: mid, Kind: "finding_reported",
		Actor: "bee2", Subject: "y.go", Model: "qwen2.5-coder:7b"}); err != nil {
		t.Fatal(err)
	}
	events, err := BuildReplayStream(q, tel, mid)
	if err != nil {
		t.Fatal(err)
	}
	var queueSide, telSide bool
	for _, ev := range events {
		if ev.Kind != "finding_reported" || ev.Subject != "y.go" || ev.Model != "qwen2.5-coder:7b" {
			continue
		}
		if b, _ := ev.Detail["backend"].(string); b == "ollama" {
			queueSide = true // queue-derived: has reporter_backend in detail
		} else {
			telSide = true // telemetry-derived: model column only
		}
	}
	if !queueSide {
		t.Error("queue-derived finding_reported must carry Model + detail.backend from reporter_model/reporter_backend")
	}
	if !telSide {
		t.Error("telemetry-derived finding_reported must carry Model from the telemetry model column")
	}
}
```

- [ ] **Step 2: Run it to confirm it fails**

```bash
go test ./internal/brain/... -run TestBuildReplayStreamCarriesModel -v
```

Expected: FAIL at compile — `ev.Model undefined (type ReplayEvent has no field or method Model)` — which is the point.

- [ ] **Step 3: Thread model through the stream**

In `internal/brain/replay.go`, add the field to the struct:

```go
type ReplayEvent struct {
	TS      float64        `json:"ts"`
	Kind    string         `json:"kind"`
	Actor   string         `json:"actor,omitempty"`
	Subject string         `json:"subject,omitempty"`
	Model   string         `json:"model,omitempty"` // model that filed this beat, when known (findings, telemetry) — Part D's recordings derive meta.models and per-model analytics from this
	Detail  map[string]any `json:"detail,omitempty"`
}
```

Replace the findings loop's first append (`replay.go:63-64`) with:

```go
	for _, f := range findings {
		fev := ReplayEvent{TS: f.CreatedTS, Kind: "finding_reported", Actor: f.Reporter, Subject: f.Target,
			Model:  f.ReporterModel,
			Detail: map[string]any{"type": f.Type, "severity": f.Severity}}
		if f.ReporterBackend != "" {
			fev.Detail["backend"] = f.ReporterBackend
		}
		out = append(out, fev)
		if f.ResolvedTS > 0 {
			out = append(out, ReplayEvent{TS: f.ResolvedTS, Kind: "finding_resolved", Subject: f.Target,
				Detail: map[string]any{"status": f.Status}})
		}
	}
```

In the telemetry loop, add `Model: e.Model`:

```go
		for _, e := range evs {
			out = append(out, ReplayEvent{TS: e.TS, Kind: e.Kind, Actor: e.Actor, Subject: e.Subject, Model: e.Model, Detail: e.Detail})
		}
```

In `internal/telemetry/store.go`, `EventsForMission` (lines 154-176) — add the model column to the SELECT and the scan:

```go
	rows, err := s.db.Query(
		`SELECT ts, kind, COALESCE(actor,''), COALESCE(subject,''), COALESCE(model,''), COALESCE(detail,'') FROM events WHERE mission_id=? ORDER BY ts ASC`,
		missionID)
```

```go
		if err := rows.Scan(&e.TS, &e.Kind, &e.Actor, &e.Subject, &e.Model, &detail); err != nil {
			return nil, err
		}
```

- [ ] **Step 4: Run the test + the full Go suite**

```bash
go test ./internal/brain/... -run TestBuildReplayStream -v
go test ./... -count=1
```

Expected: PASS (the golden-order fixture test compares `(kind,subject)` pairs only, so the new field can't break it).

- [ ] **Step 5: Add the `models` subcommand to `scripts/scrub-golden-run.py`**

Add this function alongside `cmd_deny` (line 105) and `cmd_manifest` (line 116):

```python
def cmd_models(path):
    """Prints a sorted JSON array of distinct model labels seen in the stream
    (backend:model when the event carries a backend in detail, bare model
    otherwise). A bare label that is the suffix of a qualified one is the SAME
    model seen from the telemetry side (no backend column there) — collapsed,
    so one model never lists twice."""
    data = json.load(open(path, encoding='utf-8'))
    models = set()
    for ev in data.get('events', []):
        m = (ev.get('model') or '').strip()
        if not m:
            continue
        backend = ((ev.get('detail') or {}).get('backend') or '').strip()
        models.add(backend + ':' + m if backend else m)
    models = {m for m in models if not any(o != m and o.endswith(':' + m) for o in models)}
    print(json.dumps(sorted(models)))
```

And wire it into the dispatcher at the bottom, after the existing `elif cmd == 'manifest':` branch (line 150):

```python
    elif cmd == 'models':
        cmd_models(sys.argv[2])
```

Quick check against fixtures:

```bash
printf '{"events":[{"ts":1,"kind":"finding_reported","model":"qwen2.5-coder:7b","detail":{"backend":"ollama"}},{"ts":2,"kind":"finding_reported","model":"qwen2.5-coder:7b"},{"ts":3,"kind":"finding_reported","model":"llama3.2:3b","detail":{"backend":"ollama"}}]}' > /tmp/models-test.json
python3 scripts/scrub-golden-run.py models /tmp/models-test.json
```

Expected: `["ollama:llama3.2:3b", "ollama:qwen2.5-coder:7b"]` — the bare duplicate collapsed into the qualified label.

- [ ] **Step 6: Extend `scripts/export-golden-run.sh`**

Four edits, exact:

(a) The defaults block — new default `--out` (the golden run moves into `recordings/` in Step 7) plus the two new flag variables:

```bash
BRAIN_URL="${BRAIN_URL:-http://127.0.0.1:9019}"
MISSION_ID=""
OUT_JSON="site/src/data/recordings/golden-run.json"
SLUG=""
REDERIVE=""
I_KNOW=0
YES=0
BEARER=""
```

(b) Usage text — add these lines to the `usage()` heredoc after the `--out PATH` entry:

```
  --slug NAME     Shorthand for --out site/src/data/recordings/NAME.json —
                   the recordings-gallery layout. The meta sidecar travels
                   with it as always.
  --rederive-meta PATH.json
                   Offline mode: no brain contact, no export. Recomputes the
                   models field of PATH's existing .meta.json sidecar from
                   the committed stream (for recordings exported before the
                   models field existed). All other meta fields are kept.
```

(c) Arg parsing — add two cases to the `while` loop before `-h|--help`:

```bash
    --slug) SLUG="$2"; shift 2 ;;
    --rederive-meta) REDERIVE="$2"; shift 2 ;;
```

And immediately after the loop (before the `OUT_META=` derivation):

```bash
[ -n "$SLUG" ] && OUT_JSON="site/src/data/recordings/$SLUG.json"

if [ -n "$REDERIVE" ]; then
  META="${REDERIVE%.json}.meta.json"
  [ -f "$REDERIVE" ] && [ -f "$META" ] || { echo "FAIL: need both $REDERIVE and $META" >&2; exit 1; }
  MODELS_JSON="$(python3 scripts/scrub-golden-run.py models "$REDERIVE")"
  python3 - "$META" "$MODELS_JSON" <<'PYEOF'
import json, sys
meta_path, models = sys.argv[1], json.loads(sys.argv[2])
meta = json.load(open(meta_path, encoding='utf-8'))
meta['models'] = models
with open(meta_path, 'w', encoding='utf-8') as f:
    json.dump(meta, f, indent=2)
    f.write('\n')
print('rederived models for', meta_path, '->', models)
PYEOF
  exit 0
fi
```

(d) The meta-writing python at the end of the script — derive `models` from the just-exported stream and include it (insert the `MODELS_JSON=` line before the existing `curl .../api/history` meta pipeline, and add the `'models'` key to the `meta = {...}` dict; everything else unchanged):

```bash
MODELS_JSON="$(python3 scripts/scrub-golden-run.py models "$TMP_JSON")"

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
    'models': json.loads('''$MODELS_JSON'''),
}
with open('$OUT_META', 'w') as f:
    json.dump(meta, f, indent=2)
    f.write('\n')
"
```

- [ ] **Step 7: Move the golden run into `recordings/` and rederive its meta**

```bash
mkdir -p site/src/data/recordings
git mv site/src/data/golden-run.json site/src/data/recordings/golden-run.json
git mv site/src/data/golden-run.meta.json site/src/data/recordings/golden-run.meta.json
bash scripts/export-golden-run.sh --rederive-meta site/src/data/recordings/golden-run.json
```

Expected: `rederived models for site/src/data/recordings/golden-run.meta.json -> []` — the existing golden run was recorded BEFORE Step 3's model threading, so its stream honestly carries no model info; an empty list is the truthful value, not a bug (the card simply omits the models line, and analytics bucket its findings under `(not recorded)`, mirroring `model_comparison`'s own `(no model)` convention in `internal/telemetry/store.go:188`).

Update `site/src/components/Hero.astro`'s two imports (paths only, nothing else changes in that file):

```astro
import golden from '../data/recordings/golden-run.json';
import meta from '../data/recordings/golden-run.meta.json';
```

Update `site/tests/site.spec.ts`'s deny-list test to cover EVERY committed recording (replace the whole `'the committed golden-run.json passes the deny-list scan'` test):

```ts
test('every committed recording passes the deny-list scan', async () => {
  const fs = await import('node:fs');
  const files = fs.readdirSync('src/data/recordings').filter((f) => f.endsWith('.json') && !f.endsWith('.meta.json'));
  expect(files.length, 'expected at least one committed recording').toBeGreaterThanOrEqual(1);
  for (const f of files) {
    const text = fs.readFileSync(`src/data/recordings/${f}`, 'utf-8');
    const offenses = scanDeny(text);
    expect(offenses, `${f} failed the deny-list scan:\n${offenses.join('\n')}`).toHaveLength(0);
  }
});
```

- [ ] **Step 8: The DuckDB build-analytics step**

```bash
cd site && npm install --save-dev --save-exact @duckdb/node-api && cd ..
```

Create `site/scripts/build-analytics.mjs`:

```js
// SPDX-License-Identifier: Elastic-2.0
// site/scripts/build-analytics.mjs — build-time DuckDB aggregates over every
// committed recording stream in src/data/recordings/, written to
// src/data/analytics.json (generated + gitignored; regenerated by the
// predev/prebuild npm hooks, never hand-edited).
//
// DuckDB binding choice (the spec said "CLI or node binding — decide by what
// installs cleanly in CI's ubuntu image"): the official Node binding,
// @duckdb/node-api. It installs through the same `npm ci` as every other
// site dependency (prebuilt binaries, exact-pinned via package-lock), so CI
// needs no extra curl/apt step the way the standalone CLI would. MotherDuck
// is deliberately NOT involved here — it stays product-side (fleet sync +
// the cross-brain oracle, credentialed); this script reads only committed
// local JSON, preserving the site's zero-external posture at build time too.
import { DuckDBInstance } from '@duckdb/node-api';
import fs from 'node:fs';
import path from 'node:path';
import os from 'node:os';

const RECORDINGS_DIR = path.join(import.meta.dirname, '..', 'src', 'data', 'recordings');
const OUT = path.join(import.meta.dirname, '..', 'src', 'data', 'analytics.json');

// ---- flatten every stream to one row per event ----
const rows = [];
const seenFindings = new Set();
for (const f of fs.readdirSync(RECORDINGS_DIR).sort()) {
  if (!f.endsWith('.json') || f.endsWith('.meta.json')) continue;
  const slug = f.replace(/\.json$/, '');
  const { events = [] } = JSON.parse(fs.readFileSync(path.join(RECORDINGS_DIR, f), 'utf-8'));
  for (const ev of events) {
    const d = ev.detail || {};
    // A finding appears TWICE in a stream — once from the queue's findings
    // table, once from the telemetry event log (BuildReplayStream merges
    // both sources). Dedupe by (slug, subject, severity, second-rounded ts)
    // so analytics count findings, not stream beats. Stream order puts the
    // queue-derived beat (which carries detail.backend) first, so the
    // surviving row is the backend-qualified one — deterministic.
    if (ev.kind === 'finding_reported') {
      const key = `${slug}|${ev.subject || ''}|${d.severity || ''}|${Math.round(ev.ts)}`;
      if (seenFindings.has(key)) continue;
      seenFindings.add(key);
    }
    rows.push({
      slug, ts: ev.ts, kind: ev.kind || '', subject: ev.subject || '',
      model: ev.model || '', backend: String(d.backend || ''), severity: String(d.severity || ''),
    });
  }
}
if (rows.length === 0) {
  console.error('no recordings found under', RECORDINGS_DIR);
  process.exit(1);
}
const ndjson = path.join(os.tmpdir(), `corralai-analytics-${process.pid}.ndjson`);
fs.writeFileSync(ndjson, rows.map((r) => JSON.stringify(r)).join('\n'));

// ---- DuckDB aggregates ----
const instance = await DuckDBInstance.create(':memory:');
const conn = await instance.connect();
await conn.run(`CREATE TABLE ev AS SELECT * FROM read_json_auto('${ndjson}', format='newline_delimited')`);

async function q(sql) {
  const reader = await conn.runAndReadAll(sql);
  // DuckDB counts come back as BigInt; JSON.stringify rejects BigInt.
  return reader.getRowObjects().map((row) =>
    Object.fromEntries(Object.entries(row).map(([k, v]) => [k, typeof v === 'bigint' ? Number(v) : v]))
  );
}

const findings_by_severity = await q(`
  SELECT slug, COALESCE(NULLIF(severity,''),'(none)') AS severity, count(*) AS n
  FROM ev WHERE kind='finding_reported' GROUP BY slug, severity ORDER BY slug, severity`);
const findings_by_model = await q(`
  SELECT CASE WHEN model='' THEN '(not recorded)'
              WHEN backend='' THEN model
              ELSE backend || ':' || model END AS model,
         count(*) AS findings
  FROM ev WHERE kind='finding_reported' GROUP BY 1 ORDER BY findings DESC, model`);
const task_durations = await q(`
  WITH claimed AS (SELECT slug, subject, min(ts) AS t0 FROM ev WHERE kind='task_claimed' GROUP BY slug, subject),
       done    AS (SELECT slug, subject, max(ts) AS t1 FROM ev WHERE kind='task_done'    GROUP BY slug, subject)
  SELECT claimed.slug AS slug, count(*) AS tasks,
         round(avg(t1 - t0), 1) AS avg_seconds, round(max(t1 - t0), 1) AS max_seconds
  FROM claimed JOIN done USING (slug, subject)
  WHERE t1 >= t0 GROUP BY claimed.slug ORDER BY claimed.slug`);

fs.writeFileSync(OUT, JSON.stringify({
  generated_note: 'built by site/scripts/build-analytics.mjs (DuckDB) — do not hand-edit',
  findings_by_severity, findings_by_model, task_durations,
}, null, 2) + '\n');
fs.rmSync(ndjson);
console.log('wrote', OUT, `(${rows.length} events across ${new Set(rows.map((r) => r.slug)).size} recording(s))`);
```

Wire it into `site/package.json`'s scripts (analytics must exist before both `astro build` AND `astro dev`):

```json
  "predev": "node scripts/build-analytics.mjs",
  "prebuild": "bash ../scripts/sync-site-assets.sh --check && node scripts/build-analytics.mjs",
```

Add to `.gitignore`:

```
site/src/data/analytics.json
```

Run it once and inspect:

```bash
cd site && node scripts/build-analytics.mjs && cat src/data/analytics.json && cd ..
```

Expected: `wrote .../analytics.json (NNN events across 1 recording(s))`; `findings_by_model` shows one `(not recorded)` row (the pre-model-threading golden run); `findings_by_severity` and `task_durations` show real numbers for `golden-run`.

- [ ] **Step 9: The `/recordings` page**

Create `site/src/pages/recordings.astro`:

```astro
---
// SPDX-License-Identifier: Elastic-2.0
import '../styles/global.css';
import analytics from '../data/analytics.json';

// Enumerate committed recordings by glob — no manifest file to drift.
const streamModules = import.meta.glob('../data/recordings/*.json', { eager: true });
const streams: Record<string, unknown> = {};
const cards: { slug: string; directive: string; models: string[]; task_count: number; done_task_count: number; finding_count: number; minutes: number }[] = [];
for (const [p, mod] of Object.entries(streamModules)) {
  if (p.endsWith('.meta.json')) continue;
  const slug = p.split('/').pop()!.replace(/\.json$/, '');
  const metaMod = streamModules[p.replace(/\.json$/, '.meta.json')] as any;
  if (!metaMod) throw new Error(`recording ${slug} has no .meta.json sidecar`);
  const meta = metaMod.default ?? metaMod;
  streams[slug] = (mod as any).default ?? mod;
  cards.push({
    slug,
    directive: meta.directive || '(no directive recorded)',
    models: meta.models || [],
    task_count: meta.task_count || 0,
    done_task_count: meta.done_task_count || 0,
    finding_count: meta.finding_count || 0,
    minutes: Math.round((meta.duration_seconds || 0) / 60),
  });
}
cards.sort((a, b) => a.slug.localeCompare(b.slug));
const maxModelFindings = Math.max(1, ...analytics.findings_by_model.map((r: any) => r.findings));
---
<html lang="en">
<head>
  <meta charset="utf-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
  <link rel="icon" type="image/svg+xml" href="/favicon.svg" />
  <title>Corralai — recordings</title>
  <meta name="description" content="A gallery of real recorded corralai missions — different directives, different model mixes, every one replayable." />
</head>
<body>
  <div class="section">
    <h1>Recordings</h1>
    <p>
      Real recorded missions — every one exported through the same deny-list +
      human-manifest privacy gate as the landing page's hero. Pick a card to
      replay it on the corral canvas below.
    </p>
    <div class="cards">
      {cards.map((c) => (
        <button class="card" data-slug={c.slug}>
          <span class="directive">{c.directive}</span>
          {c.models.length > 0 && <span class="models">{c.models.join(' + ')}</span>}
          <span class="stats">{c.task_count} tasks ({c.done_task_count} done) · {c.finding_count} findings · {c.minutes}m</span>
        </button>
      ))}
    </div>
    <p class="back"><a href="/">← back to the corral</a></p>
  </div>

  <div id="stage-frame">
    <div id="stage">
      <canvas id="c"></canvas>
      <div id="empty">pick a recording above</div>
    </div>
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

  <div class="section" id="analytics">
    <h2>Across the recordings</h2>
    <p class="how">
      Computed at build time with DuckDB over the committed streams above —
      the public face of the product's <code>model_comparison</code> report.
      MotherDuck stays product-side (fleet sync + the cross-brain oracle,
      credentialed) and is never wired into this site.
    </p>

    <h3>Findings by model</h3>
    <table>
      <thead><tr><th>model</th><th>findings</th><th></th></tr></thead>
      <tbody>
        {analytics.findings_by_model.map((r: any) => (
          <tr>
            <td>{r.model}</td>
            <td class="num">{r.findings}</td>
            <td class="barcell"><div class="bar" style={`width:${Math.round((r.findings / maxModelFindings) * 100)}%`}></div></td>
          </tr>
        ))}
      </tbody>
    </table>

    <h3>Findings by severity, per recording</h3>
    <table>
      <thead><tr><th>recording</th><th>severity</th><th>count</th></tr></thead>
      <tbody>
        {analytics.findings_by_severity.map((r: any) => (
          <tr><td>{r.slug}</td><td>{r.severity}</td><td class="num">{r.n}</td></tr>
        ))}
      </tbody>
    </table>

    <h3>Task durations, per recording</h3>
    <table>
      <thead><tr><th>recording</th><th>tasks</th><th>avg (s)</th><th>max (s)</th></tr></thead>
      <tbody>
        {analytics.task_durations.map((r: any) => (
          <tr><td>{r.slug}</td><td class="num">{r.tasks}</td><td class="num">{r.avg_seconds}</td><td class="num">{r.max_seconds}</td></tr>
        ))}
      </tbody>
    </table>
  </div>
</body>
</html>

<style>
  .cards { display: grid; grid-template-columns: repeat(auto-fill, minmax(260px, 1fr)); gap: 14px; margin: 20px 0; }
  .card {
    display: flex; flex-direction: column; gap: 8px; text-align: left; cursor: pointer;
    background: var(--panel); border: 1px solid var(--line); border-radius: 10px; padding: 16px;
    font: inherit; color: var(--fg);
  }
  .card:hover, .card.active { border-color: var(--amber); }
  .directive { font-weight: 600; }
  .models { color: var(--amber); font-size: 0.85rem; }
  .stats { color: var(--muted); font-size: 0.85rem; }
  .back { margin-top: 8px; }
  /* full-width dark player viewport — same framing tokens as the hero (Task 1) */
  #stage-frame { max-width: 1040px; margin: 0 auto; padding: 0 20px; }
  #stage {
    position: relative; height: 60vh; min-height: 380px;
    background: var(--stage-panel); border: 1px solid var(--stage-line);
    border-radius: 12px; box-shadow: 0 18px 44px rgba(30,20,0,.22); overflow: hidden;
  }
  #c { width: 100%; height: 100%; display: block; }
  #empty { position: absolute; inset: 0; display: flex; align-items: center; justify-content: center; color: var(--stage-muted); pointer-events: none; }
  #replay {
    position: relative; background: var(--stage-panel); color: var(--stage-fg);
    border: 1px solid var(--stage-line); border-top: none;
    border-bottom-left-radius: 12px; border-bottom-right-radius: 12px;
    padding: 9px 16px; max-width: 1040px; margin: 0 auto;
  }
  #replay .row { display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
  #replay .row button { background: var(--stage-panel); color: var(--stage-fg); border: 1px solid var(--stage-line); border-radius: 5px; padding: 4px 12px; font-size: 12px; cursor: pointer; }
  #replay .row button:hover { border-color: var(--stage-amber); }
  #replay-scrub { flex: 1; min-width: 160px; }
  #replay-label { color: var(--stage-muted); font-size: 11.5px; min-width: 70px; text-align: center; }
  #replay-title { color: var(--stage-amber); font-size: 12px; font-weight: 600; margin-right: 4px; }
  #analytics table { width: 100%; border-collapse: collapse; margin: 12px 0 28px; }
  #analytics th, #analytics td { text-align: left; padding: 6px 10px; border-bottom: 1px solid var(--line); }
  #analytics .num { text-align: right; font-variant-numeric: tabular-nums; }
  #analytics .barcell { width: 40%; }
  #analytics .bar { height: 12px; background: var(--amber); border-radius: 3px; min-width: 2px; }
  #analytics .how { color: var(--muted); font-size: 0.9rem; }
</style>

<script src="/replay-player.js" is:inline></script>
<script define:vars={{ streams }} is:inline>
  // Same minimal DOM contract as Hero.astro (see replay-player.js's header).
  window.setView = function (v) {
    const bar = document.getElementById('replay');
    if (bar) bar.classList.toggle('show', v === 'replay');
  };
  window.addEventListener('DOMContentLoaded', () => {
    setReplaySpeed(4);
    for (const btn of document.querySelectorAll('.card')) {
      btn.addEventListener('click', () => {
        document.querySelectorAll('.card.active').forEach((c) => c.classList.remove('active'));
        btn.classList.add('active');
        // startReplay already accepts a resolved {events:[...]} object — no
        // player change needed; this is the same call shape as the hero.
        startReplay(streams[btn.dataset.slug]).then(() => {
          // replayPlaying is a top-level `let` in replay-player.js (a classic
          // script, so it lands on window) — guard so switching recordings
          // mid-play never toggles playback OFF.
          if (!window.replayPlaying) toggleReplayPlay();
          document.getElementById('stage-frame').scrollIntoView({ behavior: 'smooth' });
        });
      });
    }
  });
  // replay-player.js's setSkin() clobbers document.title at script-load time
  // (same as on the landing page) — restore this page's own title.
  document.title = 'Corralai — recordings';
</script>
```

- [ ] **Step 10: Landing links + the truthful built-on line**

In `site/src/components/WatchItBack.astro`, after the existing `.callout` paragraph (`<p class="callout">This hero IS that player — the exact same code, replaying a real mission.</p>`), add:

```astro
  <p><a href="/recordings/">More recordings → a gallery of real missions, different model mixes, all replayable.</a></p>
```

Replace `site/src/components/SiteFooter.astro`'s footer block with:

```astro
<footer class="section" id="site-footer">
  <p>
    <a href="https://github.com/pdbethke/corralai">github.com/pdbethke/corralai</a>
    · Elastic License 2.0 · built by a herd, wrangled by a human.
  </p>
  <p class="built-on">
    Built on DuckDB (mission telemetry, and the analytics on the
    <a href="/recordings/">recordings page</a>) and MotherDuck (fleet sync +
    the cross-brain oracle, product-side).
  </p>
</footer>

<style>
  .built-on { color: var(--muted); font-size: 0.85rem; }
</style>
```

(Both claims verified in source: `internal/telemetry/store.go` opens `sql.Open("duckdb", ...)`; `CORRALAI_MOTHERDUCK` is the fleet-sync target in `cmd/corral/main.go`'s env block. Text only — no external assets, no badge services.)

In `site/src/content/docs/concepts/multi-model-herds.mdx`, append (the spec's MotherDuck amendment requires the fleet-oracle story on this page):

```mdx
## The fleet oracle (DuckDB + MotherDuck)

Mission telemetry is a DuckDB event log per brain; `CORRALAI_MOTHERDUCK`
points fleet sync at a MotherDuck database so multiple brains contribute to
one cross-brain ledger — the fleet oracle that `model_comparison` and the
other `corral-admin analyze` reports read from. This surface is
product-side and credentialed: it is never wired into the public site,
whose [recordings-page analytics](/recordings/) are computed at build time
with plain DuckDB over the committed recording streams instead.
```

- [ ] **Step 11: Write `site/tests/recordings.spec.ts`**

```ts
// SPDX-License-Identifier: Elastic-2.0
import { test, expect } from '@playwright/test';

test('the gallery renders a card per recording, plays one, shows analytics, and stays on-domain', async ({ page }) => {
  const external: string[] = [];
  const backendApiCalls: string[] = [];
  page.on('request', (req) => {
    const url = new URL(req.url());
    if (url.hostname !== 'localhost' && url.hostname !== '127.0.0.1') external.push(req.url());
    if (url.pathname.startsWith('/api/')) backendApiCalls.push(req.url());
  });

  await page.goto('/recordings/');
  const cards = page.locator('.card');
  expect(await cards.count(), 'expected at least one recording card').toBeGreaterThanOrEqual(1);

  await cards.first().click();
  await expect(async () => {
    const max = await page.locator('#replay-scrub').getAttribute('max');
    expect(Number(max)).toBeGreaterThan(0);
  }).toPass({ timeout: 5000 });

  await expect(page.locator('#analytics table').first()).toBeVisible();
  await expect(page.locator('#analytics .bar').first()).toBeVisible();

  expect(external, `unexpected external requests: ${external.join(', ')}`).toHaveLength(0);
  expect(backendApiCalls, `unexpected /api/* calls from a backend-free page: ${backendApiCalls.join(', ')}`).toHaveLength(0);
});

test('every recording card corresponds to a committed stream + meta pair', async () => {
  const fs = await import('node:fs');
  const files = fs.readdirSync('src/data/recordings');
  const streamFiles = files.filter((f) => f.endsWith('.json') && !f.endsWith('.meta.json'));
  for (const f of streamFiles) {
    const metaName = f.replace(/\.json$/, '.meta.json');
    expect(files, `${f} is missing its ${metaName} sidecar`).toContain(metaName);
    const meta = JSON.parse(fs.readFileSync(`src/data/recordings/${metaName}`, 'utf-8'));
    expect(Array.isArray(meta.models), `${metaName} must carry a models array (may be empty for pre-model-threading recordings)`).toBe(true);
  }
});
```

- [ ] **Step 12: Full verification**

```bash
go test ./... -count=1
bash scripts/check-security.sh
cd site && npm run test:e2e && cd ..
bash scripts/sync-site-assets.sh --check && bash scripts/gen-cli-docs.sh --check
```

Expected: all green — including the moved-golden-run deny-list glob, the hero (unchanged behavior, new import paths), and the two new recordings tests. Note: this task never touches `internal/ui/web/replay-player.js` (the player never reads `ev.model`), so the sync check is trivially unaffected — run it anyway as part of the standing gate.

- [ ] **Step 13: Commit**

```bash
git add internal/brain/replay.go internal/brain/replay_test.go internal/telemetry/store.go scripts/scrub-golden-run.py scripts/export-golden-run.sh site/src/data/recordings site/src/components/Hero.astro site/src/components/WatchItBack.astro site/src/components/SiteFooter.astro site/src/content/docs/concepts/multi-model-herds.mdx site/src/pages/recordings.astro site/scripts/build-analytics.mjs site/package.json site/package-lock.json site/tests/site.spec.ts site/tests/recordings.spec.ts .gitignore
git commit -m "$(cat <<'EOF'
feat(site,brain): recordings gallery + build-time DuckDB analytics

Model identity now rides through the replay stream (ReplayEvent.Model +
detail.backend, from reporter_model/reporter_backend and the telemetry model
column — TDD'd), so exported recordings are self-describing about what
built them. The exporter gains --slug (recordings/ layout), a models field
in the meta sidecar, and --rederive-meta for pre-threading recordings (the
golden run's honest value: []). /recordings renders a card grid from an
Astro glob (no manifest to drift), replays any recording through the
unchanged player, and shows build-time DuckDB aggregates (official node
binding — installs via npm ci, no extra CI step): findings by severity per
recording, per-model finding counts (the public face of model_comparison),
task-duration profiles — hand-rolled inline bars, zero external requests,
e2e-enforced. Landing gains the more-recordings link and the truthful
built-on-DuckDB+MotherDuck line; the multi-model docs page tells the
fleet-oracle story (MotherDuck stays product-side, never wired into the site).

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 10: Record the mixed-model run + populate the gallery

**Files:**
- Create: `site/src/data/recordings/mixed-herd.json` (produced by the export pipeline, never hand-written)
- Create: `site/src/data/recordings/mixed-herd.meta.json` (produced alongside)

**Interfaces:**
- Consumes: Task 9's entire pipeline — `ReplayEvent.Model` threading (the brain image the demo builds MUST include Task 9's commit, or the new recording carries no model info and this task's verification fails), `--slug`, the `models` meta field, the glob-driven gallery, and `build-analytics.mjs`. Also `deploy/demo/Makefile`'s `demo-models` target (verified as shipped): `MODELS_BACKEND_A ?= ollama` / `MODELS_MODEL_A ?= qwen2.5-coder:7b` / `MODELS_BACKEND_B ?= ollama` / `MODELS_MODEL_B ?= llama3.2:3b` (Makefile lines 8-13), wired as `MODELS_ROLE_MODELS = pentester=A,reviewer=B,builder=ollama:qwen2.5-coder:7b,tester=ollama:qwen2.5-coder:7b` into `CORRALAI_ROLE_MODELS` at lines 17 and 68.
- Produces: the second gallery entry — a genuinely different model mix than the all-qwen golden run — plus the final screenshots. Nothing later depends on it; this is the closing content task.

- [ ] **Step 1: Stash the demo `.env` (the demo-dev gotcha)**

```bash
[ -f deploy/demo/.env ] && mv deploy/demo/.env deploy/demo/.env.stashed-by-task10 && echo stashed
```

`deploy/demo/.env`'s presence masks the key-free, first-time-reviewer behavior this recording must reflect (memory `corralai-demo-dev-env`). The export script stashes it itself, but the demo BRING-UP must also run without it. Restore in Step 7.

- [ ] **Step 2: Run the mixed-model demo mission**

```bash
cd deploy/demo
make demo-models
```

This is the Makefile's own A/B split — pentester on `ollama:qwen2.5-coder:7b` (Group A), reviewer on `ollama:llama3.2:3b` (Group B), builder/tester on qwen — key-free, and a genuinely different mix than the all-qwen golden run. First run pulls both models (~4.7 GB + ~2.0 GB, allow ~5-15 min; watch `docker compose -f docker-compose.yml logs -f ollama-pull models-ollama-pull`). Watch `http://localhost:9019` until the mission converges (`awaiting_review`/`done` on the Progress tab).

For the recording to demonstrate a real multi-model comparison, BOTH finding-filing roles (pentester AND reviewer) must have filed at least one finding — model identity rides on finding events, so a run where only one of them filed anything yields a one-model `models` list no matter what was configured. Check the Topology tab's `model_comparison` table shows **two model rows** before exporting. If it shows only one, the mission didn't exercise both filers: create a second mission against the same running brain (`go run ./cmd/corral-admin mission create "..."` from the repo root) or `make down && make demo-models` and let a fresh mission run, until two rows appear.

- [ ] **Step 3: Export through the FULL gate**

From the repo root, in a second terminal:

```bash
bash scripts/export-golden-run.sh --slug mixed-herd
```

The deny-list scan runs automatically (the floor); the human-review manifest prints (the ceiling) — **the controller must surface this manifest to the human** and only answer `y` after the human has reviewed every path, URL, and actor name by eye (everything should be synthetic demo-shaped: builder/tester/pentester/reviewer names, `/work`-rooted paths). Never `--yes` on a first export of a mission.

Expected: `OK: deny-list scan clean`, the manifest, the confirmation, then `wrote site/src/data/recordings/mixed-herd.json and site/src/data/recordings/mixed-herd.meta.json`.

- [ ] **Step 4: Verify the meta carries the real mix**

```bash
cat site/src/data/recordings/mixed-herd.meta.json
```

Expected: a `"models"` array with **at least two distinct entries** — e.g. `["ollama:llama3.2:3b", "ollama:qwen2.5-coder:7b"]`. If it has fewer than two, the stream didn't capture findings from both models (see Step 2's two-rows check) — do NOT ship a "mixed" recording whose own data can't show the mix; go back to Step 2.

- [ ] **Step 5: Rebuild analytics and verify the multi-model comparison is real**

```bash
cd site && node scripts/build-analytics.mjs && cat src/data/analytics.json && cd ..
```

Expected: `... across 2 recording(s)`; `findings_by_model` now shows at least two real model rows (plus the golden run's `(not recorded)` bucket); `findings_by_severity` and `task_durations` each list both slugs.

- [ ] **Step 6: Full e2e + gate suite**

```bash
cd site && npm run test:e2e && cd ..
go test ./... -count=1
bash scripts/sync-site-assets.sh --check && bash scripts/gen-cli-docs.sh --check
```

Expected: all green — the recordings deny-list glob now covers both streams; the gallery e2e sees two cards.

- [ ] **Step 7: Tear down + restore**

```bash
cd deploy/demo && make down && cd ../..
[ -f deploy/demo/.env.stashed-by-task10 ] && mv deploy/demo/.env.stashed-by-task10 deploy/demo/.env && echo restored
```

- [ ] **Step 8: Final screenshots for the human**

```bash
cd site && npm run preview -- --port 4321 &
sleep 2
```

Playwright browser tool: capture `/recordings/` showing both cards + the analytics tables with the real two-model comparison (`/tmp/corralai-v2-recordings.png`), and the mixed-herd recording mid-replay after clicking its card (`/tmp/corralai-v2-mixed-replay.png`). Then:

```bash
kill %1
```

- [ ] **Step 9: Commit**

```bash
git add site/src/data/recordings/mixed-herd.json site/src/data/recordings/mixed-herd.meta.json
git commit -m "$(cat <<'EOF'
feat(site): mixed-herd recording — the gallery's first real multi-model run

Recorded via the demo's own demo-models A/B split (pentester on
qwen2.5-coder:7b, reviewer on llama3.2:3b, key-free), exported through the
full deny-list + human-manifest gate, meta.models carrying both models
derived from the stream itself. The recordings-page analytics now show a
real per-model comparison instead of a single (not recorded) bucket.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 11: Canvas zoom/pan in the shared renderer

**Files:**
- Modify: `internal/ui/web/replay-player.js` (view transform state, wheel-zoom, drag-pan, reset, coordinate helpers, draw-loop transform)
- Modify: `internal/ui/web/index.html:941-962` (the two hit-test handlers switch to world coordinates)
- Modify: `internal/ui/ui_test.go` (`TestReplayPlayerStructure` gains zoom/pan markers)
- Modify: `site/public/replay-player.js` (hash-synced copy, refreshed in the same commit)
- Modify: `site/tests/site.spec.ts` (deterministic zoom/pan/reset e2e)
- Create: `cmd/seedreplay/main.go` (throwaway — same program as Task 2 Step 5, re-created for the product-side verification and deleted again before commit)

**Interfaces:**
- Consumes: the shared canvas machinery in `replay-player.js` — `cv`/`ctx` (line 49), `CW`/`CH` + the per-frame base transform `ctx.setTransform(devicePixelRatio,0,0,devicePixelRatio,0,0)` (`resize()`, line 57), the one `draw()` loop (line 436: `clearRect` → optional `drawRain(dt)` → `ctx.drawImage(bgCv, 0, 0, CW, CH)` → links/nodes/bursts/buzzes, ALL in world coordinates), and the ONLY canvas-coordinate consumers anywhere: `index.html`'s `cv.addEventListener('click', ...)` (line 941, agent/file hit-test via `ev.clientX-rect.left`) and `cv.addEventListener('dblclick', ...)` (line 957, rename-bee hit-test). Audited: neither file has a canvas mousemove/hover hit-test; bubbles (`buzzes`), `bursts`, trails, and the queen are all placed from node world coordinates inside `draw()`, so they inherit the transform for free; the `#empty` caption is a DOM overlay, unaffected.
- Produces (test-asserted by the extended `TestReplayPlayerStructure`, used by `index.html` and the e2e): `zoomAt(sx, sy, factor)`, `screenToWorld(sx, sy) -> {x,y}`, `canvasToWorld(ev) -> {x,y}` (event → world, the helper both `index.html` handlers call), `resetView()`, `getViewTransform() -> {scale, x, y}` (read-only — a top-level `function` declaration, so it lands on `window` for the Playwright e2e; the `let` state variables deliberately don't).

Scope note: this changes the SHARED renderer — product live view, product replay, and the site hero/recordings pages all inherit it through the one file + the hash sync. It has zero SSE/API dependencies (pure view math), so it behaves identically in all three modes by construction.

- [ ] **Step 1: Extend `TestReplayPlayerStructure` first (fails red)**

In `internal/ui/ui_test.go`, add to the existing `playerMarkers` slice:

```go
		// zoom/pan (Task 11 of the site-docs-expansion plan): the view
		// transform lives in the SHARED renderer so live, replay, and the
		// static site embed all get it; hit-tests consume canvasToWorld.
		`function zoomAt(sx, sy, factor)`,
		`function screenToWorld(sx, sy)`,
		`function canvasToWorld(ev)`,
		`function resetView()`,
		`function getViewTransform()`,
		`cv.addEventListener('wheel'`,
```

And to the existing `indexMarkers` slice:

```go
		// both canvas hit-tests must read world coordinates, not raw
		// canvas-local pixels — otherwise clicking a zoomed/panned agent
		// opens the wrong node (or nothing).
		`canvasToWorld(ev)`,
```

Also add, after the existing marker loops (inside the same test function):

```go
	// The draw loop must apply the world transform once per frame (not
	// per-node math): scale+offset baked into one setTransform call that
	// multiplies devicePixelRatio.
	if !strings.Contains(player, "devicePixelRatio*viewScale") {
		t.Error("draw() must apply the view transform via ctx.setTransform(devicePixelRatio*viewScale, ...) once per frame")
	}
```

- [ ] **Step 2: Run it to confirm it fails**

```bash
go test ./internal/ui/... -run TestReplayPlayerStructure -v
```

Expected: FAIL on every new marker.

- [ ] **Step 3: Add the view transform to `replay-player.js`**

Insert immediately after the `resize()` wiring (after line 61, `resize();`):

```js
// ---- view transform: wheel-zoom + drag-pan over the corral ----
// screen = world*viewScale + viewOffset. All node physics/layout stay in
// world coordinates (CW/CH space); draw() applies the transform once per
// frame via ctx.setTransform, and every consumer of a canvas mouse event
// must go through canvasToWorld() for hit-testing (see index.html).
let viewScale = 1, viewOX = 0, viewOY = 0;
const VIEW_MIN = 0.4, VIEW_MAX = 4;
let viewDidPan = false;   // set by a completed drag-pan; eats the click that follows
function screenToWorld(sx, sy){ return { x: (sx - viewOX)/viewScale, y: (sy - viewOY)/viewScale }; }
function canvasToWorld(ev){
  const r = cv.getBoundingClientRect();
  return screenToWorld(ev.clientX - r.left, ev.clientY - r.top);
}
function getViewTransform(){ return { scale: viewScale, x: viewOX, y: viewOY }; }
function resetView(){ viewScale = 1; viewOX = 0; viewOY = 0; }
function zoomAt(sx, sy, factor){
  const ns = Math.min(VIEW_MAX, Math.max(VIEW_MIN, viewScale * factor));
  // keep the world point under the cursor fixed: it maps to the same screen
  // px before and after the scale change, so the offset absorbs the delta.
  viewOX = sx - (sx - viewOX) * (ns / viewScale);
  viewOY = sy - (sy - viewOY) * (ns / viewScale);
  viewScale = ns;
}
cv.addEventListener('wheel', ev => {
  ev.preventDefault();
  const r = cv.getBoundingClientRect();
  zoomAt(ev.clientX - r.left, ev.clientY - r.top, Math.exp(-ev.deltaY * 0.0015));
}, { passive: false });
// drag-to-pan: only engages after a small movement threshold, so plain
// clicks (agent windows, inspector) keep working untouched; a completed pan
// eats the click that the browser fires after mouseup (capture phase runs
// before index.html's bubble-phase hit-test).
let panFrom = null;
cv.addEventListener('mousedown', ev => { panFrom = { x: ev.clientX, y: ev.clientY, ox: viewOX, oy: viewOY }; });
addEventListener('mousemove', ev => {
  if(!panFrom) return;
  const dx = ev.clientX - panFrom.x, dy = ev.clientY - panFrom.y;
  if(!viewDidPan && Math.hypot(dx, dy) < 4) return;   // threshold: not a pan yet
  viewDidPan = true;
  viewOX = panFrom.ox + dx; viewOY = panFrom.oy + dy;
});
addEventListener('mouseup', () => { panFrom = null; });
cv.addEventListener('click', ev => {
  if(viewDidPan){ viewDidPan = false; ev.stopImmediatePropagation(); ev.stopPropagation(); }
}, true);
// double-click on EMPTY space resets to the 1x fit; a double-click on an
// agent stays the rename shortcut (index.html's own dblclick hit-test) —
// the two are disjoint by the same hit radius the rename handler uses.
// (No keyboard binding: neither file has canvas keyboard idioms to match.)
cv.addEventListener('dblclick', ev => {
  const p = canvasToWorld(ev);
  for(const n of nodes.values()){
    if(n.kind !== 'agent') continue;
    if(Math.hypot(n.x - p.x, n.y - p.y) < 22) return;  // rename territory — leave it alone
  }
  resetView();
});
```

Note the ordering constraint this placement satisfies: the `dblclick` handler references `nodes`, which is declared lower in the file (line 115) — safe because handlers run on user events, long after the whole script has evaluated. The `wheel`/`mousedown` handlers only touch the `let`s declared right here.

- [ ] **Step 4: Apply the transform in `draw()`**

Replace the top of `draw()` (lines 436-441):

```js
function draw(){
  step();
  // clear in DEVICE space (identity-ish base transform), THEN apply the
  // world transform once for everything that lives in the corral — the
  // background included (natural zoom: the pasture is part of the world;
  // beyond its edge the panel color shows, which reads as "the pasture
  // ends", not as a glitch). Per-node math stays untouched.
  ctx.setTransform(devicePixelRatio, 0, 0, devicePixelRatio, 0, 0);
  ctx.clearRect(0, 0, CW, CH);
  const frameT = Date.now()/1000, dt = Math.min(0.1, frameT-lastFrameT); lastFrameT = frameT;
  ctx.setTransform(devicePixelRatio*viewScale, 0, 0, devicePixelRatio*viewScale, devicePixelRatio*viewOX, devicePixelRatio*viewOY);
  if(skin().bg==='rain') drawRain(dt);
  if(bgCv.width>1) ctx.drawImage(bgCv, 0, 0, CW, CH);
```

Everything below that point in `draw()` (links, queen, nodes, bursts, buzzes) is already world-space and needs no change — the whole audit of screen-space leaks came up empty (see Interfaces). `resize()`'s own `setTransform` stays as-is; `draw()` overrides it every frame anyway.

- [ ] **Step 5: Convert `index.html`'s two hit-tests to world coordinates**

Replace the coordinate lines only. Click handler (line 941-943):

```js
cv.addEventListener('click', ev => {
  // node coords are WORLD coords; the view may be zoomed/panned, so convert
  // through the shared inverse transform, not raw canvas-local pixels.
  const p = canvasToWorld(ev), mx = p.x, my = p.y;
```

Dblclick handler (line 957-958):

```js
cv.addEventListener('dblclick', ev => {
  const p = canvasToWorld(ev), mx = p.x, my = p.y;
```

(Bodies of both handlers unchanged — `mx`/`my` keep their names, so the hit-test math below each line is untouched.)

- [ ] **Step 6: Sync the site copy + run the structural test**

```bash
bash scripts/sync-site-assets.sh
go test ./internal/ui/... -run TestReplayPlayerStructure -v
go test ./... -count=1
```

Expected: `synced internal/ui/web/replay-player.js -> site/public/replay-player.js`, then PASS, PASS.

- [ ] **Step 7: Deterministic e2e on the site build**

Append to `site/tests/site.spec.ts`:

```ts
test('the hero canvas zooms at the cursor, pans on drag, and resets on double-click', async ({ page }) => {
  await page.goto('/');
  await expect(async () => {
    const max = await page.locator('#replay-scrub').getAttribute('max');
    expect(Number(max)).toBeGreaterThan(0);
  }).toPass({ timeout: 5000 });

  const canvas = page.locator('#c');
  const box = (await canvas.boundingBox())!;
  const cx = box.x + box.width / 2, cy = box.y + box.height / 2;

  // wheel-zoom in at the center — getViewTransform is a top-level function
  // declaration in the classic-script player, so it lands on window.
  await page.mouse.move(cx, cy);
  await page.mouse.wheel(0, -400);
  const zoomed = await page.evaluate(() => (window as any).getViewTransform());
  expect(zoomed.scale, 'wheel up must zoom in past 1x').toBeGreaterThan(1);
  expect(zoomed.scale, 'zoom must clamp at 4x').toBeLessThanOrEqual(4);

  // drag-to-pan: offset moves by the drag delta
  await page.mouse.move(cx, cy);
  await page.mouse.down();
  await page.mouse.move(cx + 80, cy + 40, { steps: 5 });
  await page.mouse.up();
  const panned = await page.evaluate(() => (window as any).getViewTransform());
  expect(Math.round(panned.x - zoomed.x), 'drag must pan the view horizontally').toBe(80);
  expect(Math.round(panned.y - zoomed.y), 'drag must pan the view vertically').toBe(40);

  // double-click empty space (the far corner, away from any node) resets
  await page.mouse.dblclick(box.x + 10, box.y + 10);
  const reset = await page.evaluate(() => (window as any).getViewTransform());
  expect(reset).toEqual({ scale: 1, x: 0, y: 0 });
});
```

```bash
cd site && npm run test:e2e && cd ..
```

Expected: PASS, including all pre-existing tests (the pan click-eater must not have broken the existing hero interactions — the zero-external and scrub tests exercise the replay bar, which sits outside the canvas and is untouched).

- [ ] **Step 8: Product-side live verification (seeded scratch brain) + screenshots**

Re-create `cmd/seedreplay/main.go` exactly as written in Task 2 Step 5 (throwaway, same fallback rule if helper names differ), then:

```bash
export SCRATCH=$(mktemp -d)
export CORRALAI_DB="$SCRATCH/coord.sqlite3"
export CORRALAI_MEMORY_DB="$SCRATCH/memory.duckdb"
export CORRALAI_PRINCIPALS_DB="$SCRATCH/principals.sqlite3"
export CORRALAI_GATEWAY_DB="$SCRATCH/gateway.sqlite3"
export CORRALAI_ARTIFACTS_DB="$SCRATCH/artifacts.sqlite3"
export CORRALAI_OIDC_ISSUER=""
go run ./cmd/seedreplay
go run ./cmd/corral &
sleep 2
```

Playwright browser tool against `http://127.0.0.1:9019/`:
1. Wheel-zoom into a cluster of agents on the live canvas; screenshot the close-up → `/tmp/corralai-task11-product-zoom.png`.
2. While zoomed, CLICK a zoomed agent and confirm its floating agent-detail window opens (this is the inverse-transform correctness check — the whole point of `canvasToWorld`).
3. Drag-pan, then double-click empty grass and confirm the view snaps back to 1x.

Then the site build:

```bash
cd site && npm run preview -- --port 4321 &
sleep 2
```

Navigate to `http://localhost:4321/`, let the replay start, wheel-zoom into a cluster mid-replay, screenshot → `/tmp/corralai-task11-site-zoom.png`. Then tear down everything:

```bash
kill %1
cd .. && kill %1
rm -rf "$SCRATCH" cmd/seedreplay
```

- [ ] **Step 9: Full gate + commit**

```bash
go test ./... -count=1
bash scripts/check-security.sh
bash scripts/sync-site-assets.sh --check
bash scripts/gen-cli-docs.sh --check
cd site && npm run test:e2e && cd ..
```

Expected: all green — the sync check passes because Step 6 refreshed the copy, committed together below.

```bash
git add internal/ui/web/replay-player.js internal/ui/web/index.html internal/ui/ui_test.go site/public/replay-player.js site/tests/site.spec.ts
git commit -m "$(cat <<'EOF'
feat(ui): wheel-zoom + drag-pan on the corral canvas, shared across live/replay/site

Zoom anchors at the cursor (screen = world*scale + offset; the offset
absorbs the scale delta so the point under the wheel stays fixed), clamped
0.4x-4x. Drag-to-pan engages past a 4px threshold and eats the trailing
click in capture phase, so click-to-open-agent-window is untouched.
Double-click on empty space resets to 1x (double-click on an agent stays
the rename shortcut — disjoint by the same hit radius). Both index.html
hit-tests now go through the shared canvasToWorld inverse transform — the
correctness core, verified live by clicking a zoomed agent. The background
zooms with the world (natural: the pasture is part of the corral; its edge
reads as the pasture ending). Pure view math, no SSE/API dependencies —
identical in live mode, replay, and the static embed; site copy hash-synced
in this commit; TestReplayPlayerStructure extended; deterministic
zoom/pan/reset e2e via the read-only getViewTransform() probe.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
EOF
)"
```

---

### Task 12: The replay cockpit — replay events drive the full viewport

**Files:**
- Modify: `internal/ui/web/replay-player.js` (replay-side panel state + renderers, wired into the existing replay choke points)
- Modify: `internal/ui/web/index.html` (move `sevColor`/`SEV_RANK` out — lines 750-751 — nothing else)
- Modify: `internal/ui/ui_test.go` (`TestReplayPlayerStructure` gains cockpit markers)
- Modify: `site/public/replay-player.js` (hash-synced copy, refreshed in the same commit)
- Modify: `site/src/pages/recordings.astro` (cockpit DOM + CSS around Task 9's player)
- Modify: `site/tests/recordings.spec.ts` (cockpit e2e: console lines appear, count tracks scrub position)

**Interfaces:**
- Consumes: the replay pipeline's existing choke points in `replay-player.js` — `startReplay()` (state reset at line 619: `nodes.clear(); links.length = 0; ...`), `seekReplay()` (same reset at line 671, then a rebuild-from-0 walk), `applyReplayEvent(ev)` (the one translation point for every beat), `renderReplayScrub()` (called by startReplay/replayStep/seekReplay — the single per-step render funnel), `stopReplaySession()` (line 636, where live SSE resumes and its "fresh snapshot immediately on connect" repaints the live panels), and the `inReplay` flag (line 607). Shared helpers already in the file: `esc()`, `roleColor()`, `displayName()`. Stream shapes (verified in `internal/brain/replay.go` + Task 9's additions): `execution` beats carry `actor`, `subject`=command, `detail.{ok, exit_code, role}` (NO `timed_out`/`summary` — those are live-state-only fields the tape doesn't record); `task_created` carries `subject`=key + `detail.{role,title}`; `task_claimed` adds `actor`; `task_done/cancelled/superseded` carry `actor`+`subject`; `finding_reported` carries `actor`, `subject`=target, `detail.{type,severity}` (+ `ev.model`/`detail.backend` after Task 9); `finding_resolved` carries `subject` + `detail.status`.
- Produces (test-asserted, consumed by the product page implicitly and the recordings page explicitly): `renderReplayPanels()`, `renderReplayConsole()`, `renderReplayTasks()`, `renderReplayFindings()`, `resetReplayPanels()` — all null-guarded on optional DOM ids `#exec`, `#tasks`, `#findings` (the same optional-contract pattern as `#empty`/`#stat`), all no-ops unless `inReplay` is true. The DOM-contract header comment gains these three ids in its "Optional, null-guarded" list.

**Extract-vs-mirror decision (judged per the coordinator's instruction):** MIRROR with shared helpers, not extract. The live renderers (`renderExec` at `index.html:783`, `renderTasks` at 830, `renderFindings` at 754) are entangled with live-only features the tape cannot feed: the console's filter chips + `recent_activity` tool-call stream + `lastExecKey` burst wiring + multi-line `summary` output; the task list's supersede-lineage arrows keyed on live `t.id`/`t.supersedes`; the findings panel's `recurring`/`status==='open'` fields. Extracting them would either drag `lastState` shapes into the shared file (entangling the embed with a brain it doesn't have) or fork every renderer internally on a mode flag (worse than two small renderers). Instead the replay renderers reuse the shared helpers and the SAME CSS vocabulary (`feedhdr`, `xblk`/`xcmdline`/`xprompt`/`xcmd`/`xbadge`, `trow`/`tdot`, `frow`/`fsev`) so the cockpit is visually the product console, while the live renderers stay untouched. `sevColor`/`SEV_RANK` move from `index.html` into `replay-player.js` (both files' renderers need them; leaving a copy in each would throw `Identifier 'SEV_RANK' has already been declared` at load, since both scripts share the page's top-level scope).

- [ ] **Step 1: Extend `TestReplayPlayerStructure` first (fails red)**

Add to the `playerMarkers` slice in `internal/ui/ui_test.go`:

```go
		// replay cockpit (Task 12): the tape drives console/tasks/findings
		// panels through null-guarded optional DOM ids, same contract as
		// #empty/#stat — present in the product and the site cockpit,
		// absent in the canvas-only hero.
		`function renderReplayPanels()`,
		`function renderReplayConsole()`,
		`function renderReplayTasks()`,
		`function renderReplayFindings()`,
		`function resetReplayPanels()`,
		`const SEV_RANK`,
		`function sevColor(sev)`,
```

And after the marker loops, add:

```go
	// The cockpit renderers must be null-guarded (optional DOM) and
	// inReplay-gated (never clobber the live panels while SSE owns them),
	// and every replay entry/reset path must reset the panel state.
	for _, guard := range []string{
		`document.getElementById('exec')`,
		`document.getElementById('tasks')`,
		`document.getElementById('findings')`,
	} {
		if !strings.Contains(player, guard) {
			t.Errorf("replay-player.js cockpit missing optional-DOM lookup %q", guard)
		}
	}
	if !strings.Contains(player, "if(!inReplay) return;") {
		t.Error("renderReplayPanels must be inReplay-gated — a live page's panels belong to apply()/SSE, not the tape")
	}
	for _, fn := range []string{"function startReplay(streamOrUrl){", "function seekReplay(target){", "function stopReplaySession(){"} {
		fi := strings.Index(player, fn)
		if fi < 0 {
			t.Fatalf("could not locate %s in replay-player.js", fn)
		}
		end := strings.Index(player[fi:], "\n}")
		if end < 0 {
			t.Fatalf("could not locate the end of %s", fn)
		}
		if !strings.Contains(player[fi:fi+end], "resetReplayPanels(") {
			t.Errorf("%s must reset the cockpit panel state (resetReplayPanels) — seek/restart/stop each rebuild or relinquish the panels", fn)
		}
	}
```

- [ ] **Step 2: Run it to confirm it fails**

```bash
go test ./internal/ui/... -run TestReplayPlayerStructure -v
```

Expected: FAIL on every new marker.

- [ ] **Step 3: Move `sevColor`/`SEV_RANK` into the shared file**

Delete `index.html` lines 750-751:

```js
function sevColor(sev){ return (sev==='critical'||sev==='high')?C.red:(sev==='medium'?C.amber:'#6b6452'); }
const SEV_RANK = {critical:3, high:2, medium:1, low:0};
```

Add them to `replay-player.js` immediately after `roleColor` (line 337) — identical text, plus a one-line comment:

```js
// severity color/rank — shared by the live findings panel (index.html) and
// the replay cockpit's findings renderer below.
function sevColor(sev){ return (sev==='critical'||sev==='high')?C.red:(sev==='medium'?C.amber:'#6b6452'); }
const SEV_RANK = {critical:3, high:2, medium:1, low:0};
```

- [ ] **Step 4: The cockpit state + renderers (inside the replay block, so the existing no-POST scan covers them)**

Insert after the `let inReplay = false;` declaration (line 607):

```js
// ---- replay cockpit: the tape drives the console/tasks/findings panels ----
// The live panels render from lastState via apply() (SSE) — which is paused
// during replay, so in the product they used to freeze on stale live content
// while the canvas played the tape. These replay-side renderers accumulate
// state from applyReplayEvent and paint the SAME panel DOM (#exec, #tasks,
// #findings — all optional, null-guarded) from the tape instead. People want
// to see action: the whole viewport replays, not just the canvas.
// Honest limits of the tape: execution beats carry ok/exit_code but not the
// live feed's timed_out flag or multi-line output summary, and there is no
// recent_activity tool-call stream — the cockpit shows what was recorded,
// never invents the rest.
let replayExecLines = [];   // {agent, role, command, ok, exitCode}
let replayTasks = new Map(); // key -> {key, title, role, status, claimedBy}
let replayFindings = [];    // {reporter, target, type, severity, model, resolved}
let replaySeenBeats = new Set(); // dedupe: findings ride the tape twice (queue+telemetry merge)
function resetReplayPanels(){
  replayExecLines = []; replayTasks = new Map(); replayFindings = []; replaySeenBeats = new Set();
}
function clearReplayPanelDOM(){
  for(const id of ['exec','tasks','findings']){
    const el = document.getElementById(id);
    if(el) el.innerHTML = '';
  }
}
function renderReplayConsole(){
  const ep = document.getElementById('exec');
  if(!ep) return;
  const tail = replayExecLines.slice(-24);
  ep.innerHTML = '<div class="feedhdr">console · replaying the tape · ' + replayExecLines.length + '</div>' +
    (tail.length ? '' : '<div class="xempty">▌ no commands on the tape yet…</div>') +
    tail.map(e => {
      const badge = e.ok
        ? '<span class="xbadge" style="color:var(--green)" title="exit 0">✓</span>'
        : '<span class="xbadge" style="color:var(--red)" title="exit ' + esc(String(e.exitCode)) + '">✗' + esc(String(e.exitCode)) + '</span>';
      return '<div class="xblk"><div class="xcmdline"><span class="xprompt">❯</span> <b style="color:' + roleColor(e.role) + '">' + esc(displayName(e.agent)) + '</b> <code class="xcmd">' + esc(e.command || '') + '</code> ' + badge + '</div></div>';
    }).join('') + '<div class="xcursor">▌</div>';
  ep.scrollTop = ep.scrollHeight; // tail like the live console
}
function renderReplayTasks(){
  const tp = document.getElementById('tasks');
  if(!tp) return;
  const tasks = Array.from(replayTasks.values());
  if(!tasks.length){ tp.innerHTML = ''; return; }
  const order = {claimed:0, queued:1, done:2, superseded:3, cancelled:4};
  const counts = tasks.reduce((m,t)=>{ m[t.status]=(m[t.status]||0)+1; return m; }, {});
  const hdr = ['claimed','queued','done','superseded','cancelled'].filter(s=>counts[s]).map(s=>counts[s]+' '+s).join(' · ');
  tp.innerHTML = '<div class="feedhdr">tasks · ' + tasks.length + (hdr ? ' &nbsp; ' + hdr : '') + '</div>' +
    tasks.slice().sort((a,b)=>(order[a.status]??9)-(order[b.status]??9)).slice(0,50).map(t => {
      const gone = (t.status==='cancelled' || t.status==='superseded');
      const dot = t.status==='done' ? C.green : (t.status==='claimed' ? '#5b9bd5' : '#6b6452');
      const who = t.claimedBy && !gone ? ' <span style="color:' + roleColor(t.role) + '">← ' + esc(displayName(t.claimedBy)) + '</span>' : '';
      const titleStyle = gone ? 'color:var(--muted);text-decoration:line-through' : 'color:var(--fg)';
      return '<div class="trow"' + (gone ? ' style="opacity:.6"' : '') + '><span class="tdot" style="background:' + dot + '"></span><b style="' + titleStyle + '">' + esc(t.title || t.key) + '</b> <span style="color:var(--muted)">' + esc(t.role || '') + '</span>' + who + '</div>';
    }).join('');
}
function renderReplayFindings(){
  const fp = document.getElementById('findings');
  if(!fp) return;
  if(!replayFindings.length){ fp.innerHTML = ''; return; }
  const open = replayFindings.filter(f => !f.resolved);
  const crit = open.filter(f => f.severity==='critical' || f.severity==='high').length;
  const hdr = 'findings · ' + open.length + ' open' + (crit ? ' &nbsp; <b style="color:var(--red)">⚠ ' + crit + ' high</b>' : '');
  fp.innerHTML = '<div class="feedhdr">' + hdr + '</div>' +
    open.slice().sort((a,b)=>(SEV_RANK[b.severity]??0)-(SEV_RANK[a.severity]??0)).slice(0,30).map(f => {
      const hi = (f.severity==='critical' || f.severity==='high') ? ' hi' : '';
      const tgt = f.target ? ' <span style="color:var(--fg)">' + esc(f.target) + '</span>' : '';
      const mdl = f.model ? ' <span style="color:#5a7a8a;font-size:10px;font-family:ui-monospace,monospace">[' + esc(f.model) + ']</span>' : '';
      return '<div class="frow' + hi + '"><span class="fsev" style="color:' + sevColor(f.severity) + '">' + esc(f.severity) + '</span> <span style="color:var(--muted)">' + esc(f.type) + '</span>' + tgt + ' <span style="color:#6b6452">· ' + esc(displayName(f.reporter)) + '</span>' + mdl + '</div>';
    }).join('');
}
function renderReplayPanels(){
  if(!inReplay) return; // the live page's panels belong to apply()/SSE
  renderReplayConsole();
  renderReplayTasks();
  renderReplayFindings();
}
```

- [ ] **Step 5: Accumulate cockpit state in `applyReplayEvent`**

Add the accumulation at the TOP of `applyReplayEvent(ev)`'s body (before the existing `switch` — the canvas cases below stay byte-identical):

```js
function applyReplayEvent(ev){
  // ---- cockpit accumulation (panels) — before the canvas switch below ----
  const d = ev.detail || {};
  switch(ev.kind){
    case 'task_created':
      if(ev.subject) replayTasks.set(ev.subject, {key: ev.subject, title: d.title || '', role: d.role || '', status: 'queued', claimedBy: ''});
      break;
    case 'task_claimed': {
      if(ev.subject){
        const t = replayTasks.get(ev.subject) || {key: ev.subject, title: d.title || '', role: d.role || ''};
        t.status = 'claimed'; t.claimedBy = ev.actor || ''; t.role = d.role || t.role;
        replayTasks.set(ev.subject, t);
      }
      break;
    }
    case 'task_done': case 'task_cancelled': case 'task_superseded': {
      if(ev.subject){
        const t = replayTasks.get(ev.subject);
        if(t) t.status = ev.kind.slice(5); // done | cancelled | superseded
      }
      break;
    }
    case 'execution': {
      // dedupe on (actor, command, second-rounded ts) — cheap insurance in
      // case a stream ever carries a beat from both merge sources.
      const k = 'x|' + (ev.actor||'') + '|' + (ev.subject||'') + '|' + Math.round(ev.ts);
      if(!replaySeenBeats.has(k)){
        replaySeenBeats.add(k);
        replayExecLines.push({agent: ev.actor || '', role: d.role || '', command: ev.subject || '', ok: !!d.ok, exitCode: d.exit_code == null ? '' : d.exit_code});
        if(replayExecLines.length > 200) replayExecLines.shift();
      }
      break;
    }
    case 'finding_reported': {
      // findings ride the tape TWICE (queue + telemetry merge — same dedupe
      // convention as Task 9's analytics flattener).
      const k = 'f|' + (ev.subject||'') + '|' + (d.severity||'') + '|' + Math.round(ev.ts);
      if(!replaySeenBeats.has(k)){
        replaySeenBeats.add(k);
        replayFindings.push({reporter: ev.actor || '', target: ev.subject || '', type: d.type || '', severity: d.severity || '', model: ev.model || '', resolved: false});
      }
      break;
    }
    case 'finding_resolved': {
      for(let i = replayFindings.length - 1; i >= 0; i--){
        if(replayFindings[i].target === ev.subject && !replayFindings[i].resolved){ replayFindings[i].resolved = true; break; }
      }
      break;
    }
  }
  // ---- canvas translation (unchanged below this line) ----
  const now = Date.now()/1000; // same clock draw()'s bursts/buzzes compare against
```

(The rest of the function — `agentId`/`pathId`/the canvas `switch` — is untouched.)

- [ ] **Step 6: Wire reset + render into the existing choke points**

In `startReplay()`, extend the reset line (619):

```js
    nodes.clear(); links.length = 0; bursts.length = 0; buzzes.length = 0;
    resetReplayPanels();
```

In `seekReplay()`, extend the reset line (671) identically:

```js
  nodes.clear(); links.length = 0; bursts.length = 0; buzzes.length = 0;
  resetReplayPanels();
```

In `renderReplayScrub()` — the single funnel every replay state change already flows through (startReplay, replayStep, seekReplay) — add as its FIRST line:

```js
function renderReplayScrub(){
  renderReplayPanels();
```

(Seek performance note: `seekReplay`'s rebuild walk calls `applyReplayEvent` in a tight loop and only then calls `renderReplayScrub()` once — so a 600-event scrub does 600 cheap accumulations and ONE panel paint, same pattern the canvas already uses.)

In `stopReplaySession()`, before the SSE-resume line (642), add:

```js
  // Relinquish the panels: clear replay content so the live snapshot (pushed
  // immediately on SSE reconnect) repaints from a blank panel, never leaving
  // a flash of stale TAPE content posing as live state. inReplay is already
  // false at this point, so no replay render can race the repaint.
  resetReplayPanels();
  clearReplayPanelDOM();
```

And update the DOM-contract header comment (the "Optional, null-guarded" list near the top of the file) to include the three new ids:

```
// Optional, null-guarded (safe to omit entirely): #empty, #stat, #skinsel,
// #skinsub, #tab-swarm, #tab-proposals, #proposals-badge, #tab-completed,
// #themebtn — and the replay-cockpit panels #exec, #tasks, #findings (when
// present, replay populates them from the tape; the canvas-only hero omits
// them and loses nothing).
```

- [ ] **Step 7: Sync + Go suite**

```bash
bash scripts/sync-site-assets.sh
go test ./internal/ui/... -run TestReplayPlayerStructure -v
go test ./... -count=1
```

Expected: sync copy refreshed, PASS, PASS. (The new renderers live inside the replay block, so the existing "no POST/mutating fetch" scan now covers them automatically — they are DOM-only by construction.)

- [ ] **Step 8: The site cockpit on `/recordings`**

In `site/src/pages/recordings.astro`, replace Task 9's player block (`#stage-frame` + `#replay`) with the cockpit layout — the canvas keeps its dark frame; the three panels join it inside the same dark stage. The hero (`Hero.astro`) is deliberately NOT touched: it stays canvas-only, and the null guards are what make that free.

```astro
  <div id="stage-frame">
    <div id="cockpit">
      <aside id="tasks"></aside>
      <div id="stage">
        <canvas id="c"></canvas>
        <div id="empty">pick a recording above</div>
      </div>
      <aside id="findings"></aside>
    </div>
    <div id="exec"></div>
  </div>
  <div id="replay">
    ...(Task 9's replay-bar markup, unchanged)...
  </div>
```

Add to the page's `<style>` (alongside Task 9's rules; `#stage` loses its own border-radius corners where it now sits inside the cockpit grid — replace Task 9's `#stage-frame`/`#stage` rules with these):

```css
  #stage-frame {
    max-width: 1240px; margin: 0 auto; padding: 0 20px;
  }
  #cockpit {
    display: grid; grid-template-columns: 220px 1fr 260px;
    background: var(--stage-bg); border: 1px solid var(--stage-line);
    border-top-left-radius: 12px; border-top-right-radius: 12px;
    box-shadow: 0 18px 44px rgba(30,20,0,.22); overflow: hidden;
  }
  @media (max-width: 900px) { #cockpit { grid-template-columns: 1fr; } #tasks, #findings { max-height: 160px; } }
  #stage { position: relative; height: 56vh; min-height: 360px; background: var(--stage-panel); }
  #tasks, #findings {
    background: var(--stage-bg); color: var(--stage-fg); overflow-y: auto;
    padding: 10px 12px; font-size: 12px; border-right: 1px solid var(--stage-line);
  }
  #findings { border-right: none; border-left: 1px solid var(--stage-line); }
  #exec {
    background: var(--stage-bg); color: var(--stage-fg); border: 1px solid var(--stage-line); border-top: none;
    height: 180px; overflow-y: auto; padding: 10px 14px;
    font-size: 12px;
  }
  /* the cockpit panels reuse the product console's class vocabulary — same
     names the shared renderers emit, styled for the dark stage */
  .feedhdr { color: var(--stage-muted); font-size: 11px; letter-spacing: .4px; margin-bottom: 6px; }
  .xblk { margin-bottom: 3px; }
  .xcmdline { display: flex; align-items: baseline; gap: 7px; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; line-height: 1.5; }
  .xprompt { color: var(--stage-amber); }
  .xcmd { font-family: ui-monospace, monospace; color: var(--stage-fg); }
  .xbadge { font-weight: 700; }
  .xempty, .xcursor { color: var(--stage-muted); }
  .trow { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; padding: 2px 0; }
  .tdot { display: inline-block; width: 7px; height: 7px; border-radius: 50%; margin-right: 6px; }
  .frow { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; padding: 2px 0; }
  .frow.hi { font-weight: 600; }
  .fsev { font-weight: 700; }
```

One correction to the CSS custom properties the renderers emit: the shared renderers use `var(--green)`, `var(--red)`, `var(--muted)`, `var(--fg)` inline — on the recordings page those resolve to the LIGHT page tokens inside a dark stage. Scope the dark values onto the cockpit in the same `<style>`:

```css
  #cockpit, #exec {
    --fg: var(--stage-fg); --muted: var(--stage-muted);
    --green: #8fdcab; --red: #e8503a; --amber: var(--stage-amber);
  }
```

(Custom properties inherit, so the cockpit subtree sees dark-canvas values while the rest of the page keeps daylight tokens — no renderer change needed, which is the point of emitting `var()` references instead of literals.)

- [ ] **Step 9: Cockpit e2e**

Append to `site/tests/recordings.spec.ts`:

```ts
test('the cockpit panels replay the tape: console lines appear and track the scrub position', async ({ page }) => {
  await page.goto('/recordings/');
  await page.locator('.card').first().click();
  await expect(async () => {
    const max = await page.locator('#replay-scrub').getAttribute('max');
    expect(Number(max)).toBeGreaterThan(0);
  }).toPass({ timeout: 5000 });

  const scrub = page.locator('#replay-scrub');
  const max = Number(await scrub.getAttribute('max'));
  const seek = (target: number) =>
    scrub.evaluate((el, t) => { (el as HTMLInputElement).value = String(t); el.dispatchEvent(new Event('input')); }, target);

  // Mid-tape: the console has lines, tasks and findings headers render.
  await seek(Math.floor(max / 2));
  const midConsole = await page.locator('#exec .xblk').count();
  expect(midConsole, 'expected console lines mid-tape').toBeGreaterThan(0);
  await expect(page.locator('#tasks .feedhdr')).toContainText('tasks ·');

  // The console header's total count grows with the scrub position — the
  // rendered tail is capped at 24 rows, so assert on the header's running
  // total, which reflects every execution beat accumulated so far.
  const headerCount = async () => {
    const txt = await page.locator('#exec .feedhdr').innerText();
    return Number(txt.split('·').pop()!.trim());
  };
  const midTotal = await headerCount();
  await seek(max);
  const endTotal = await headerCount();
  expect(endTotal, 'console total must grow from mid-tape to end-of-tape').toBeGreaterThan(midTotal);

  // Seek BACK rebuilds from zero: an early position must show fewer than the end.
  await seek(Math.floor(max / 10));
  const earlyTotal = await headerCount();
  expect(earlyTotal, 'seeking back must rebuild the panels from zero, not keep accumulating').toBeLessThan(endTotal);

  // Findings accumulate by end-of-tape (the golden run has real findings).
  await seek(max);
  await expect(page.locator('#findings .feedhdr')).toContainText('findings ·');
});
```

```bash
cd site && npm run test:e2e && cd ..
```

Expected: PASS, including every earlier test — the hero specs prove the canvas-only embed still works with the cockpit ids absent (that IS the null-guard test, running against real DOM).

- [ ] **Step 10: Product-side live verification (seeded scratch brain)**

Re-create `cmd/seedreplay/main.go` per Task 2 Step 5 (throwaway; the Task 7 variant with a completed mission is the better fit here — the Completed tab needs a finished mission to replay), start the scratch brain as in Task 11 Step 8, then with the Playwright browser tool against `http://127.0.0.1:9019/`:

1. Open the completed tab → open the finished mission → press ▶ replay; confirm the CONSOLE (`#exec`) fills with tape lines (❯-prefixed, ✓/✗ badges) and the tasks/findings asides track the tape while it plays — screenshot → `/tmp/corralai-task12-product-cockpit.png`.
2. Scrub backward and confirm the console total DROPS (rebuild-from-0, not accumulation).
3. Exit replay (✕) and confirm the panels repaint LIVE state — the console header reads `live console ·` (renderExec's header, not the cockpit's `console · replaying the tape ·`) with no flash of leftover tape rows — screenshot → `/tmp/corralai-task12-product-live-restored.png`.

Tear down: kill the brain, `rm -rf "$SCRATCH" cmd/seedreplay`.

- [ ] **Step 11: Full gate + commit**

```bash
go test ./... -count=1
bash scripts/check-security.sh
bash scripts/sync-site-assets.sh --check
bash scripts/gen-cli-docs.sh --check
cd site && npm run test:e2e && cd ..
```

Expected: all green.

```bash
git add internal/ui/web/replay-player.js internal/ui/web/index.html internal/ui/ui_test.go site/public/replay-player.js site/src/pages/recordings.astro site/tests/recordings.spec.ts
git commit -m "$(cat <<'EOF'
feat(ui,site): the replay cockpit — the tape drives the full viewport

Replay used to animate only the canvas while the console/tasks/findings
panels froze on stale live state (SSE paused). Now applyReplayEvent
accumulates replay-side panel state (execution beats -> console lines with
ok/fail badges; task lifecycle beats -> a live task list; finding beats ->
an accumulating findings panel with severity ranking and per-model tags)
and null-guarded renderers paint the same optional panel DOM (#exec,
#tasks, #findings — same contract pattern as #empty/#stat). Seek rebuilds
the panels from zero exactly like the canvas; exit clears them so the SSE
snapshot repaints live state with no stale-tape flash. Mirrored renderers
with shared helpers (esc/roleColor/displayName + sevColor/SEV_RANK moved
to the shared file) rather than extracting the live renderers — those are
entangled with live-only shapes the tape doesn't carry (activity stream,
filter chips, summaries, supersede lineage), and the tape's honest limits
are documented, not papered over. The recordings page becomes the full
cockpit (tasks | canvas | findings + console, dark-stage-scoped tokens);
the landing hero stays canvas-only. Structural markers, cockpit e2e
(console count tracks scrub position both directions), product Playwright
check, site copy hash-synced.

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
EOF
)"
```

---

## Self-Review

**1. Spec coverage.**
- Part A (daylight restyle, dark-framed hero) → Task 1. Covered.
- Social/conversion addendum (OG/Twitter meta, above-the-fold copy, canonical, meta description, no external badges) → Task 2. Covered.
- Part B (Starlight at `/docs`, zero-external preserved, all 7 content sections, sidebar order, ≤~150 lines/page, UI tab tour with real screenshots + alt text from seeded brains) → Tasks 3, 5, 6, 7. Covered — each concepts page is well under 150 lines.
- Part C (`gen-cli-docs.sh`, both output locations, `--check` drift gate wired into CI, thin-usage-text fixed as product change with TDD) → Task 4. Covered.
- Verification section (restyle screenshots + contrast check, docs build green + offline search e2e + claim traceability, CLI-ref check green + human readability pass on corral-admin, existing e2e suite stays green, deny-list/sync-check unchanged) → Tasks 1 (contrast+screenshot), 3 (build+network e2e), 4 (`--check` + readability pass), 8 (full final verification + docs a11y + search e2e). Covered.
- Global constraints header (SPDX, zero-external test-enforced including a `/docs` Pagefind session, verified-copy, corral voice, no personal infra, seeded-brain-only screenshots, trailer, full green gate list, CI drift gates for both player and CLI docs) — stated verbatim at the top and each task's steps satisfy them individually (SPDX on every new file, seeded-brain-only in Tasks 2/7, no personal infra in Task 6's Running It, both drift gates green in Task 4/8).
- Deliberately-out-of-scope items (dark-mode variant, versioned docs, i18n, search analytics, rewriting usage strings beyond what the generator exposes as inadequate, blog) — none of the eight tasks touch any of these; Task 4 only fixes the two binaries whose usage text was genuinely absent, not a general rewrite.

**2. Placeholder scan.** No "TBD"/"fill in"/"similar to Task N" patterns. The one spot requiring judgment at execution time — Task 5/7's fallback instructions for `SeedDemoMission`/`SeedPendingProposal`/etc. if those exact helper names don't exist — is not a placeholder: it gives a concrete fallback command (`grep` the real test helpers) rather than deferring the decision with no path forward, matching how Task 1 of the v1 plan handled the same class of "verify this name is real" risk for `apply()`/`esc()`.

**3. Type/name consistency.** `usageText()` (Task 4) is used identically in both `corral-agent` and `corral-harness` and in both new test files. The sidebar slugs defined in Task 3 Step 2 (`concepts/mission-lifecycle`, `concepts/queue-and-verify`, `concepts/claims-and-leases`, `concepts/memory-and-learning-loop`, `concepts/history-and-replay`, `concepts/multi-model-herds`, `concepts/knowledge-corpus`, `concepts/trust-and-security`, `ui-tour/corral`, `ui-tour/progress`, `ui-tour/topology`, `ui-tour/memory`, `ui-tour/proposals`, `ui-tour/completed-and-replay`, `cli/*`) match the exact filenames created/modified in Tasks 4-7. `scripts/gen-cli-docs.sh`'s two output paths (`docs/cli/$b.md`, `site/src/content/docs/cli/$b.md`) match Task 3's sidebar slugs and Task 4's own file list. The `--stage-*` CSS custom properties introduced in Task 1 are the only new names Task 1 relies on; no later task references them, so no drift risk there.

**Ambiguities resolved during planning:**
- **Spec said "5 binaries"; ground truth is 6** (`corral`, `corral-admin`, `corral-agent`, `corral-harness`, `corral-observe`, `corral-top` — `corral-observe` wasn't named in the spec prose). Resolution: enumerated via `ls cmd/` as the spec itself instructed ("enumerate from cmd/"); all 6 are covered by Task 4 and get their own CLI-reference/sidebar page.
- **Spec said the existing e2e suite has "8 tests"; ground truth is 6** in `site/tests/site.spec.ts` today. Resolution: no plan step depended on the number 8; Tasks 1/2 simply append new tests to the real file, so this is a spec-prose correction with no plan impact.
- **UI tab naming: spec calls the first tab "corral (canvas)"; the literal DOM id/label is `tab-swarm`/"swarm."** Resolution, per the Global Constraints' corral-voice rule: all NEW site copy (Task 7's `ui-tour/corral.mdx`, Task 3's sidebar label) calls it "the corral (canvas view)" — the existing product DOM id/label is untouched (out of scope; relabeling shipped product UI is not part of this plan).
- **The addendum's og:image capture note says it "can reuse the UI-tour screenshot task's seeded brain session (one setup, many captures)."** Resolution: kept Task 2 (OG image) and Task 7 (UI tour) as two independent seeded-brain sessions instead, because Task 2 is sequenced immediately after Task 1 (per the addendum's own sequencing instruction) while Task 7 runs much later after the concepts pages — sharing one live brain process across five non-adjacent tasks would violate each task's "produces a self-contained, independently testable deliverable" requirement (writing-plans' Task Right-Sizing rule) and would leave a scratch brain process spanning a long, interruptible span of work. The setup cost (seed program + `go run ./cmd/corral` + teardown) is a few minutes each time and is deliberately cheap by design (throwaway seed program, `mktemp -d` scratch paths) — paying it twice is the right trade against holding a background process alive across four intervening tasks.
- **corral-admin's usage text was already judged adequate**, not thin — the spec's "if a binary's usage text is too thin, fix it" clause is satisfied by fixing `corral-agent` and `corral-harness` (Task 4), the two binaries with zero structured `-h` output; `corral-top`/`corral-observe`'s Go-default `flag.Usage` output was judged sufficient since both already carry a full synopsis in their doc comments that the generator surfaces separately as the "Usage" fenced block's context via the page's own generated header line — no code change needed for those two.
- **Exact Astro/Starlight version pins were not hardcoded** in Task 3 (mirroring the v1 plan's own precedent for `astro` itself) — `--save-exact` plus the committed `package-lock.json` is the pinning mechanism, since Starlight (like Astro) releases frequently and a plan-embedded version number would go stale immediately.

## Self-Review Addendum (Tasks 9–12, appended per the Part D, zoom, and cockpit amendments)

**1. Spec coverage (Part D + the zoom request).**
- Recordings data layout (`site/src/data/recordings/<slug>{.json,.meta.json}`, Astro glob, no manifest file) → Task 9 Steps 7/9. Covered.
- Meta `models` field derived from the stream → Task 9 Steps 5/6 — with the prerequisite product fix (Step 1-4), since the stream as shipped carries no model info at all (see ambiguities below). Covered.
- Card grid → full-width `startReplay(streamObject)` player → Task 9 Step 9 (no player change, per the spec's own expectation — verified: `startReplay` already branches on `typeof streamOrUrl`). Covered.
- Hero unchanged / "more recordings" link / truthful DuckDB+MotherDuck line (text only) → Task 9 Steps 7 (import paths only) and 10. Covered.
- Build-time DuckDB analytics (three aggregates, hand-rolled bars, no CDN lib, CLI-vs-binding decision documented, runs in CI ubuntu) → Task 9 Step 8 (binding: `@duckdb/node-api`, choice documented in the script header). Covered.
- MotherDuck stays product-side + fleet-oracle story on the multi-model docs page → Task 9 Step 10. Covered. Public MotherDuck share explicitly NOT planned (separate follow-up spec per the amendment).
- Two contrasting runs through the full gate → golden run (entry #1, Task 9 Step 7) + mixed-herd (Task 10, `make demo-models`, full deny + human manifest). Covered.
- E2E: gallery renders / recording opens and plays / analytics present / zero-external holds → Task 9 Step 11; deny-list extended to every recording → Task 9 Step 7. Covered.
- Zoom/pan (Task 11): cursor-anchored wheel-zoom with clamp, threshold drag-pan that preserves click-to-open, empty-space double-click reset, inverse-transform hit-tests (full audit: the only canvas-coordinate consumers are `index.html:941/957`), one `setTransform` per frame, background zooms with the world (choice noted), works in live/replay/static embed, `TestReplayPlayerStructure` extended, Playwright on both product (scratch brain, zoomed-click correctness) and site dist (cluster close-up screenshots), sync refreshed in the same commit. Covered.
- Replay cockpit (Task 12): extract-vs-mirror judged explicitly (mirror with shared helpers; reasons documented in the task), null-guarded optional DOM ids on the same contract pattern as `#empty`/`#stat`, panels populated from the tape (console with ok/fail coloring, task list reflecting current replay state, findings accumulating with severity ranking), seek/rebuild-from-0 resets panels (test-asserted per choke-point function), product stale-panels gap fixed with an explicit no-stale-flash teardown (clear-then-let-the-snapshot-repaint, verified live in Step 10), recordings page becomes the full cockpit while the hero stays canvas-only, structural + site e2e (console count tracks scrub position in BOTH directions) + product Playwright check, sync/zero-external/SPDX/trailer all per Global Constraints. Covered.

**2. Placeholder scan (new tasks).** Complete code in every step; the one execution-time judgment (Task 11 Step 8 re-creates Task 2's throwaway seed program) points at the exact prior step rather than saying "similar to" — the code is fully spelled out there and the fallback rule travels with it.

**3. Type/name consistency (new tasks).** `models` (meta field) is produced by Task 9 Step 6, consumed by Step 9's cards, Step 11's sidecar test, and Task 10 Step 4. `scripts/scrub-golden-run.py models` is invoked identically in Steps 5, 6(c), and 6(d). `viewScale`/`viewOX`/`viewOY` in Task 11 Step 3 match Step 4's `setTransform` args and Step 1's structural marker (`devicePixelRatio*viewScale`); `canvasToWorld(ev)` matches Step 5's index.html usage and Step 1's `indexMarkers` entry; `getViewTransform()` matches Step 7's e2e probe. `analytics.json`'s three keys (`findings_by_severity`, `findings_by_model`, `task_durations`) match between Step 8's writer and Step 9's page.

**Ambiguities resolved (Tasks 9–11):**
- **Model info does NOT ride through the replay stream as shipped** — `telemetry.Event` has a `Model` column but `EventsForMission` (`internal/telemetry/store.go:156`) doesn't SELECT it, and `BuildReplayStream`'s findings loop drops `ReporterModel`/`ReporterBackend`. The coordinator's either/or ("stream if it carries model, else the mission_history/model_comparison surface") resolves to a third, better option: thread model through the stream as a TDD'd product change (Task 9 Steps 1-4). Required regardless of preference — the build-time analytics see only committed JSON, never the brain's telemetry DB, so per-model finding counts are impossible without a self-describing stream.
- **The existing golden run predates model threading**, so its rederived `models` is honestly `[]` (`--rederive-meta` added for exactly this) — the card omits the models line and analytics bucket its findings as `(not recorded)`, mirroring `model_comparison`'s own `(no model)` convention. No fabricated "all-qwen" claim for a stream that can't prove it.
- **The golden-run move into `recordings/` landed in Task 9, not Task 10** (the coordinator's summary put "golden run becomes entry #1" under Task 10): Task 9's e2e needs at least one committed recording to render a card and play it, and the move is a mechanical refactor (git mv + two import paths + one test glob), while Task 10 stays purely record-the-mix + populate + verify.
- **One finding, two stream beats** — `BuildReplayStream` merges the queue's findings table AND the telemetry log, so every finding appears twice; raw counting would double the analytics. The flattener dedupes by `(slug, subject, severity, second-rounded ts)`, deterministically keeping the queue-derived (backend-qualified) beat. Same duplication surfaces as a bare-vs-qualified model label in `models` derivation — collapsed by the suffix rule in `cmd_models`.
- **DuckDB CLI vs node binding**: the official node binding `@duckdb/node-api` — installs through the same `npm ci` as every other site dependency (prebuilt binaries, exact-pinned via package-lock), so CI's ubuntu image needs no extra curl/apt step the way the standalone CLI would. Documented in `build-analytics.mjs`'s header per the spec.
- **`demo-models`, not hand-rolled env** — the Makefile already ships the A/B target (`make demo-models`: pentester=qwen2.5-coder:7b, reviewer=llama3.2:3b, key-free); Task 10 uses it as-is instead of exporting MODELS_* by hand. Because model identity rides on finding events, Task 10 gates the export on the Topology tab's `model_comparison` showing two rows first — a "mixed" run where only one role filed findings cannot honestly demonstrate the mix.
- **Zoom reset affordance kept minimal**: double-click on empty space only, no keyboard binding — neither `replay-player.js` nor `index.html` has any canvas keyboard idiom to match, and the coordinator's "keyboard 0 maybe" was explicitly optional. Double-click-on-agent stays the existing rename shortcut; the two are disjoint by the rename handler's own 22px hit radius.
- **Background zooms with the world** (the "natural" option): the pasture/comb/rain is part of the corral, and its visible edge when panned reads as the pasture ending, not a glitch. Noted in the draw() comment per the coordinator's instruction; the cheaper fixed-background variant is a two-line change (move the `drawImage` above the second `setTransform`) if it looks wrong in Step 8's live check.
- **Pan-vs-click disambiguation** uses a 4px movement threshold plus a capture-phase click-eater in the player itself — chosen over patching `index.html`'s click handler with pan-awareness, because the site embed (which has no `index.html` handlers) must get the same behavior from the shared file alone.
- **Cockpit: mirror, not extract** — `renderExec`/`renderTasks`/`renderFindings` read live-state shapes the tape doesn't carry (`recent_activity` tool calls, `summary` output, `timed_out`, `recurring`, `t.id`/`t.supersedes` lineage) and carry live-only UX (filter chips, `lastExecKey` burst wiring); extraction would either drag `lastState` into the brain-less embed or mode-fork every renderer. The replay renderers share helpers (`esc`/`roleColor`/`displayName`, plus `sevColor`/`SEV_RANK` moved into the shared file — they CANNOT be duplicated: both scripts share the page's top-level scope, so a second `const SEV_RANK` throws at load) and the same CSS class vocabulary, so the cockpit looks like the product console while the live renderers stay untouched. The tape's honest limits (no timed-out badge, no output summaries, no tool-call stream) are documented in the code comment, not padded with invented content.
- **Cockpit reuses the product's panel ids (`#exec`/`#tasks`/`#findings`) rather than replay-specific ones** — in the product, replay painting the very panels that used to sit stale is precisely the gap being fixed; `inReplay` gating + the SSE-resume snapshot are what make the shared ids safe (replay never renders while live owns them, and teardown clears the panels before live repaints — no stale-tape flash, verified live in Task 12 Step 10).
- **Task 12 sequenced after Task 9; dependency noted both ways** — Task 9 ships `/recordings` canvas-only (its Produces note says so); Task 12 adds the cockpit DOM there. Run out of order, the null guards make Task 12's shared-file changes inert on a Task 9-less tree, and Task 9 without Task 12 simply has no panels. The hero deliberately never gets the cockpit (canvas-only stays cleaner), which doubles as the permanent real-DOM exercise of the null guards.
- **Cockpit token scoping on the site**: the shared renderers emit `var(--green)`/`var(--red)`/`var(--fg)`/`var(--muted)` inline; on the daylight recordings page those would resolve to light-page values inside the dark stage, so the page re-scopes those custom properties on the cockpit subtree (`#cockpit, #exec { --fg: var(--stage-fg); ... }`) — CSS inheritance does the work, no renderer fork.

Plan complete and saved to `docs/superpowers/plans/2026-07-04-site-docs-expansion.md`. Two execution options:

**1. Subagent-Driven (recommended)** - I dispatch a fresh subagent per task, review between tasks, fast iteration

**2. Inline Execution** - Execute tasks in this session using executing-plans, batch execution with checkpoints

**Which approach?**
