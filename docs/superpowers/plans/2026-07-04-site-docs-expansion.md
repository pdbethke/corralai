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

Plan complete and saved to `docs/superpowers/plans/2026-07-04-site-docs-expansion.md`. Two execution options:

**1. Subagent-Driven (recommended)** - I dispatch a fresh subagent per task, review between tasks, fast iteration

**2. Inline Execution** - Execute tasks in this session using executing-plans, batch execution with checkpoints

**Which approach?**
