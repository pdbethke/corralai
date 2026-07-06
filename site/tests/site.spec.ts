// SPDX-License-Identifier: Elastic-2.0
import { test, expect } from '@playwright/test';

// The same deny-list rules as scripts/scrub-golden-run.py's scan_deny(),
// reimplemented in JS since this runs in the site's Node/Playwright
// toolchain, not Python. Belt and suspenders: scripts/export-golden-run.sh
// already gates the file before it's committed — this catches a hand-edited
// commit that skipped it.
const DENY_PATTERNS: [RegExp, string][] = [
  [/[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}/g, 'email address'],
  [/\/home\/[A-Za-z0-9_.-]+/g, 'linux home-directory path'],
  [/\/Users\/[A-Za-z0-9_.-]+/g, 'macOS home-directory path'],
  [/gh[pousr]_[A-Za-z0-9]{20,}/g, 'GitHub token'],
  [/AKIA[0-9A-Z]{16}/g, 'AWS access key id'],
  [/cdt_[A-Za-z0-9]{20,}/g, 'vendor token (cdt_*)'],
  [/sk-[A-Za-z0-9]{20,}/g, 'OpenAI-shaped API key'],
  [/-----BEGIN[A-Z ]*PRIVATE KEY-----/g, 'private key material'],
  // Windows-shaped paths (drive-letter and backslash-home), mirroring the
  // two extra deny rules scrub-golden-run.py carries that scripts/export
  // didn't originally quote in Task 2's excerpt.
  [/\b[A-Za-z]:(?:\\{1,2}[A-Za-z0-9._-]+)+/g, 'Windows drive-letter path'],
  [/\\{1,2}(?:Users|home)\\{1,2}[A-Za-z0-9._-]+/g, 'Windows backslash home path'],
];

// scan_deny in scrub-golden-run.py also checks two more rules that this port
// intentionally DOES carry (parity, Important-1 below), plus two it
// deliberately does NOT: $(whoami) and $(hostname) matches. Those two are
// unportable to CI on purpose — a GitHub Actions runner's username/hostname
// is an ephemeral, meaningless container identity (e.g. "runner" /
// "fv-az123-456"), not an operator's real identity the way it is when a
// human runs the Python export tool interactively on their own machine.
// There is nothing for a CI assertion to meaningfully check there.

const IPV4_RE = /\b(?:\d{1,3}\.){3}\d{1,3}\b/g;
// Absolute paths outside the demo containers' own internal roots — mirrors
// scan_deny's GENERIC absolute-path rule, including its glued-slash
// carve-out (see below).
const PATHLIKE_RE = /(?:\/[A-Za-z0-9._-]+){2,}/g;
const SAFE_PATH_PREFIXES = ['/work', '/tmp', '/root'];

function isPrivateOrLocalIPv4(ip: string): boolean {
  const octets = ip.split('.').map(Number);
  if (octets.length !== 4 || octets.some((o) => Number.isNaN(o) || o < 0 || o > 255)) {
    return true; // not a real IPv4 (e.g. a version string like "1.26.4") — not our problem, mirrors the Python ValueError catch
  }
  const [a, b] = octets;
  if (a === 10) return true; // 10.0.0.0/8
  if (a === 172 && b >= 16 && b <= 31) return true; // 172.16.0.0/12
  if (a === 192 && b === 168) return true; // 192.168.0.0/16
  if (a === 127) return true; // loopback
  if (a === 169 && b === 254) return true; // link-local
  return false;
}

function scanDeny(text: string): string[] {
  const offenses: string[] = [];
  for (const [pattern, label] of DENY_PATTERNS) {
    const matches = text.match(pattern);
    if (matches) offenses.push(`[${label}] ${matches.join(', ')}`);
  }
  for (const m of text.matchAll(IPV4_RE)) {
    if (!isPrivateOrLocalIPv4(m[0])) offenses.push(`[non-private/non-localhost IP] ${m[0]}`);
  }
  for (const m of text.matchAll(PATHLIKE_RE)) {
    const path = m[0];
    const idx = m.index ?? 0;
    // Only a path whose leading '/' actually STARTS a token is an absolute
    // filesystem path. A '/' glued to a preceding word character is the
    // tail of a domain or Go module path (github.com/yourusername/stack)
    // — not a host path, so not a deny (same carve-out scan_deny documents
    // in scripts/scrub-golden-run.py).
    const precedingChar = idx > 0 ? text[idx - 1] : '';
    const precededByWord = idx > 0 && /[A-Za-z0-9._-]/.test(precedingChar);
    if (!precededByWord && !SAFE_PATH_PREFIXES.some((prefix) => path.startsWith(prefix))) {
      offenses.push(`[absolute path outside demo-container roots] ${path}`);
    }
  }
  return offenses;
}

test('the deny-list scan flags the Important-1 parity rules (non-private IPv4, absolute paths outside demo roots)', () => {
  // RED-first fixtures: each of these strings should trip a rule that this
  // JS port previously lacked. Run against inline samples, not the golden
  // file, so this test proves the rule itself fires independent of whether
  // the committed fixture happens to contain a violation today.
  const publicIp = scanDeny('reachable over the vpn at 203.0.113.42 during the incident');
  expect(publicIp.some((o) => o.includes('non-private/non-localhost IP'))).toBe(true);

  const hostPath = scanDeny('the secret lives at /etc/secrets/prod.env on the box');
  expect(hostPath.some((o) => o.includes('absolute path outside demo-container roots'))).toBe(true);

  // Negative cases: private/local IPs and the demo containers' own roots
  // must NOT false-positive.
  const privateIps = scanDeny('internal traffic stays on 10.0.0.5, 172.20.3.1, 192.168.1.1, and 127.0.0.1');
  expect(privateIps).toHaveLength(0);

  const safePaths = scanDeny('demo artifacts land in /work/output, /tmp/scratch, and /root/.cache');
  expect(safePaths).toHaveLength(0);

  // Glued-slash carve-out: a '/' immediately preceded by a word character
  // is a domain/module-path tail, not a host path, and must NOT fire —
  // ported faithfully from scan_deny's own documented edge case.
  const gluedSlash = scanDeny('see github.com/yourusername/stack for the source');
  expect(gluedSlash).toHaveLength(0);
});

test('every committed recording passes the deny-list scan', async () => {
  const fs = await import('node:fs');
  // Streams AND the human-written .analysis.md sidecars — anything under
  // recordings/ ships to the public page, so everything gets the same scan.
  const files = fs
    .readdirSync('src/data/recordings')
    .filter((f) => (f.endsWith('.json') && !f.endsWith('.meta.json')) || f.endsWith('.analysis.md'));
  expect(files.length, 'expected at least one committed recording').toBeGreaterThanOrEqual(1);
  for (const f of files) {
    const text = fs.readFileSync(`src/data/recordings/${f}`, 'utf-8');
    const offenses = scanDeny(text);
    expect(offenses, `${f} failed the deny-list scan:\n${offenses.join('\n')}`).toHaveLength(0);
  }
});

test('zero non-local network requests across the whole page session, including same-origin /api/*', async ({ page }) => {
  const external: string[] = [];
  const backendApiCalls: string[] = [];
  page.on('request', (req) => {
    const url = new URL(req.url());
    if (url.hostname !== 'localhost' && url.hostname !== '127.0.0.1') {
      external.push(req.url());
    }
    // A relative fetch('/api/...') is same-origin, so it passes the
    // hostname check above as a false "local" pass — but it's the exact bug
    // class the inReplay guard in replay-player.js exists to prevent (a
    // static embed calling back to a backend that doesn't exist here). Track
    // it separately so this test actually catches that regression class.
    if (url.pathname.startsWith('/api/')) {
      backendApiCalls.push(req.url());
    }
  });

  await page.goto('/');
  // Let the replay's fetch-or-no-fetch settle and autoplay begin.
  await expect(async () => {
    const max = await page.locator('#replay-scrub').getAttribute('max');
    expect(Number(max)).toBeGreaterThan(0);
  }).toPass({ timeout: 5000 });

  // Scrub all the way to the END of the stream (not just the midpoint) so
  // the interception above observes the WHOLE 639-event golden run, not
  // half of it — seekReplay() walks every event from index 0 up to the
  // target synchronously, so scrubbing to `max` is what actually exercises
  // every event in the recording under interception.
  const scrub = page.locator('#replay-scrub');
  const max = Number(await scrub.getAttribute('max'));
  await scrub.evaluate((el, target) => {
    (el as HTMLInputElement).value = String(target);
    el.dispatchEvent(new Event('input'));
  }, max);

  // Confirm the full event count actually rendered (renderReplayScrub()
  // writes "idx / max" into #replay-label), not just that we set the input
  // value — this is the proof the whole stream played, not just that we
  // asked it to.
  await expect(page.locator('#replay-label')).toHaveText(`${max} / ${max}`, { timeout: 5000 });

  // A further settle window in case autoplay's timer loop fires one more
  // scheduled step after the seek.
  await page.waitForTimeout(500);

  expect(external, `unexpected external requests: ${external.join(', ')}`).toHaveLength(0);
  expect(
    backendApiCalls,
    `unexpected same-origin /api/* requests during a backend-free static replay: ${backendApiCalls.join(', ')}`
  ).toHaveLength(0);
});

test('replay-player.js does not clobber the site title', async ({ page }) => {
  // Regression guard for Minor-3: setSkin() in replay-player.js sets
  // document.title to the product UI's own title ("CorralAI — the corral")
  // at script-load time. Hero.astro's inline script restores the site's
  // title afterward — assert the restored value wins, not the clobbered one.
  await page.goto('/');
  await expect(page).toHaveTitle('Corralai — a headless brain for a herd of AI agents');
});

test('the landing hero shows the full cockpit AND the skin/theme selector, defaulting to ranch', async ({ page }) => {
  // The hero renders the full cockpit shell (tasks/agents/findings/exec, shared
  // with /recordings via internal/ui/web/cockpit-shell.html — see
  // site/src/lib/cockpitShell.ts), not a bare canvas, so those four panels are
  // expected. The visual skin/theme selector now ships in the cockpit HUD on
  // the hero too (the previously-suppressed #skinsel is restored) so the demo
  // exposes the matrix view devs asked for; it defaults to the clean ranch skin.
  await page.goto('/');
  const skinsel = page.locator('#skinsel');
  await expect(skinsel).toBeVisible();
  await expect(skinsel).toHaveValue('ranch');
  await expect(skinsel.locator('option')).toHaveCount(4); // ranch, flock, matrix, hive
  await expect(page.locator('#exec, #tasks, #agents, #findings')).toHaveCount(4);
});

test('the hero skin selector applies a real visual palette (matrix → green phosphor) and keeps the page title', async ({ page }) => {
  await page.goto('/');
  const skinsel = page.locator('#skinsel');
  await expect(skinsel).toBeVisible();

  // Switching to matrix sets data-skin on <html>, which re-themes the cockpit
  // via the --stage-* token overrides (see site/src/styles/global.css).
  await skinsel.selectOption('matrix');
  await expect(page.locator('html')).toHaveAttribute('data-skin', 'matrix');
  const bg = await page.evaluate(() =>
    getComputedStyle(document.documentElement).getPropertyValue('--stage-bg').trim(),
  );
  expect(bg, 'matrix must repaint the stage near-black').toMatch(/#000|rgb\(0, ?[0-9], ?0\)|^#00/);
  const accent = await page.evaluate(() =>
    getComputedStyle(document.documentElement).getPropertyValue('--stage-amber').trim(),
  );
  expect(accent.toLowerCase(), 'matrix accent must be phosphor green').toContain('#39ff14');

  // The picked skin repaints but must not steal the branded page title.
  await expect(page).toHaveTitle('Corralai — a headless brain for a herd of AI agents');
});

test('the GitHub link resolves and points at the real repo, everywhere it appears', async ({ page, request }) => {
  await page.goto('/');
  const links = page.locator('a[href*="github.com/pdbethke/corralai"]');
  const count = await links.count();
  // Both the hero CTA and the footer carry this link — asserting >=2 here
  // (rather than just "first() is truthy") catches a future edit that
  // silently drops one of them.
  expect(count, 'expected the GitHub link to appear in both the hero CTA and the footer').toBeGreaterThanOrEqual(2);

  const hrefs = await links.evaluateAll((els) => els.map((el) => el.getAttribute('href')));
  for (const href of hrefs) {
    expect(href).toBeTruthy();
  }

  // Only actually fetch the URL once — hitting github.com per matched link
  // is redundant network chatter for an identical href.
  const href = hrefs[0]!;
  const res = await request.get(href);
  expect(res.status(), `GET ${href} returned ${res.status()}`).toBeLessThan(400);
});

test('the page has no obvious accessibility footguns', async ({ page }) => {
  await page.goto('/');
  // Quick, dependency-free a11y pass (no @axe-core/playwright in this
  // toolchain) rather than a full audit: a <title>, a single <h1>, and an
  // accessible name on every link/button/img, which covers the failure
  // modes most likely from hand-authored marketing markup.
  await expect(page).toHaveTitle(/.+/);

  const h1Count = await page.locator('h1').count();
  expect(h1Count, 'expected exactly one <h1> on the page').toBe(1);

  const images = page.locator('img');
  const imgCount = await images.count();
  for (let i = 0; i < imgCount; i++) {
    const alt = await images.nth(i).getAttribute('alt');
    expect(alt, `image ${i} is missing an alt attribute`).not.toBeNull();
  }

  const links = page.locator('a');
  const linkCount = await links.count();
  for (let i = 0; i < linkCount; i++) {
    const link = links.nth(i);
    const text = (await link.innerText()).trim();
    const ariaLabel = await link.getAttribute('aria-label');
    expect(
      text.length > 0 || (ariaLabel && ariaLabel.length > 0),
      `link ${i} has no visible text and no aria-label`
    ).toBeTruthy();
  }
});

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

  // Decode the actual pixel dimensions from the PNG header so a bad
  // recapture fails CI, not just human review. A PNG's first chunk is
  // always IHDR: 8-byte signature + 4-byte length + 4-byte type, then
  // width and height as big-endian uint32s at offsets 16 and 20.
  const signature = bytes.subarray(0, 8).toString('hex');
  expect(signature, 'og-image.png is not a PNG').toBe('89504e470d0a1a0a');
  expect(bytes.subarray(12, 16).toString('ascii'), 'first PNG chunk should be IHDR').toBe('IHDR');
  const width = bytes.readUInt32BE(16);
  const height = bytes.readUInt32BE(20);
  expect(width, 'og:image must be exactly 1200px wide (matches og:image:width)').toBe(1200);
  expect(height, 'og:image must be exactly 630px tall (matches og:image:height)').toBe(630);
});

test('the hero canvas zooms at the cursor, pans on drag, and resets on double-click', async ({ page }) => {
  await page.goto('/');
  await expect(async () => {
    const max = await page.locator('#replay-scrub').getAttribute('max');
    expect(Number(max)).toBeGreaterThan(0);
  }).toPass({ timeout: 5000 });

  const canvas = page.locator('#c');
  // The hero canvas can render below the fold on the default 1280x720
  // viewport; mouse.move/wheel operate in viewport coordinates, so scroll
  // it fully into view first or the synthetic wheel event lands on nothing.
  await canvas.scrollIntoViewIfNeeded();
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

test('the on-screen zoom controls drive the same transform and the hint fades', async ({ page }) => {
  await page.goto('/');
  await expect(async () => {
    const max = await page.locator('#replay-scrub').getAttribute('max');
    expect(Number(max)).toBeGreaterThan(0);
  }).toPass({ timeout: 5000 });

  const zoomIn = page.getByRole('button', { name: 'Zoom in' });
  const zoomOut = page.getByRole('button', { name: 'Zoom out' });
  const reset = page.getByRole('button', { name: 'Reset zoom' });
  await expect(zoomIn).toBeVisible();
  await expect(zoomOut).toBeVisible();
  await expect(reset).toBeVisible();

  // the hint is visible on first paint, and clicking a control dismisses it
  // immediately (not just after its timeout) — same discoverability affordance
  // a wheel/drag interaction dismisses.
  const hint = page.locator('.view-hint');
  await expect(hint).toBeVisible();
  await expect(hint).toHaveText(/scroll or pinch to zoom.*drag to pan/);

  await zoomIn.click();
  const afterIn = await page.evaluate(() => (window as any).getViewTransform());
  expect(afterIn.scale, 'the + button must zoom in past 1x').toBeGreaterThan(1);
  await expect(hint).toHaveClass(/hide/);

  await zoomIn.click(); await zoomIn.click(); await zoomIn.click(); await zoomIn.click(); await zoomIn.click();
  const clamped = await page.evaluate(() => (window as any).getViewTransform());
  expect(clamped.scale, '+ must clamp at 4x same as the wheel').toBeLessThanOrEqual(4);

  await zoomOut.click();
  const afterOut = await page.evaluate(() => (window as any).getViewTransform());
  expect(afterOut.scale, 'the - button must zoom back out').toBeLessThan(clamped.scale);

  await reset.click();
  const afterReset = await page.evaluate(() => (window as any).getViewTransform());
  expect(afterReset).toEqual({ scale: 1, x: 0, y: 0 });
});

test('a drag-pan released OFF-canvas does not eat the next legit click', async ({ page }) => {
  // Regression: viewJustPanned() used to be cleared only inside a canvas
  // 'click' listener, which never fires when the pan's mouseup lands off the
  // canvas (routine — mousemove/mouseup are window-level, and the zoom
  // controls sit at the edge). The flag leaked and silently swallowed the
  // NEXT click. index.html's real consumer opens an agent window; here we
  // install the identical guard (skip when viewJustPanned()) as a probe.
  await page.goto('/');
  await expect(async () => {
    const max = await page.locator('#replay-scrub').getAttribute('max');
    expect(Number(max)).toBeGreaterThan(0);
  }).toPass({ timeout: 5000 });

  const canvas = page.locator('#c');
  await canvas.scrollIntoViewIfNeeded();
  const box = (await canvas.boundingBox())!;
  const cx = box.x + box.width / 2, cy = box.y + box.height / 2;

  await page.evaluate(() => {
    (window as any).__honored = 0;
    (window as any).__ateAfterPan = 0;   // clicks the guard correctly suppressed
    document.getElementById('c')!.addEventListener('click', () => {
      if ((window as any).viewJustPanned()) { (window as any).__ateAfterPan++; return; }
      (window as any).__honored++;
    });
  });
  await page.evaluate(() => (window as any).resetView());

  // drag past the threshold and RELEASE OFF the canvas (to its right).
  await page.mouse.move(cx, cy);
  await page.mouse.down();
  await page.mouse.move(cx + 60, cy + 20, { steps: 4 });
  await page.mouse.move(box.x + box.width + 40, cy, { steps: 4 });
  await page.mouse.up();

  // the flag must have cleared on the global mouseup even with no canvas
  // click to clear it — otherwise the next click is eaten.
  await expect(async () => {
    expect(await page.evaluate(() => (window as any).viewJustPanned())).toBe(false);
  }).toPass({ timeout: 1000 });

  // now a plain click on the canvas must be honored (probe fires 1/1).
  await page.mouse.click(cx, cy);
  const honored = await page.evaluate(() => (window as any).__honored);
  expect(honored, 'the click after an off-canvas pan release must be honored').toBe(1);

  // complementary guarantee: a pan RELEASED ON-canvas still has its own
  // trailing click suppressed (the guard sees the flag before the timer).
  await page.evaluate(() => { (window as any).__honored = 0; (window as any).resetView(); });
  await page.mouse.move(cx, cy);
  await page.mouse.down();
  await page.mouse.move(cx + 70, cy + 25, { steps: 5 });
  await page.mouse.up();   // releases on-canvas → fires a trailing click
  await page.waitForTimeout(20);
  const suppressed = await page.evaluate(() => (window as any).__ateAfterPan);
  const honoredOnCanvas = await page.evaluate(() => (window as any).__honored);
  expect(suppressed, "the on-canvas pan's own trailing click must be suppressed").toBeGreaterThanOrEqual(1);
  expect(honoredOnCanvas, "the pan's trailing click must not be honored").toBe(0);
});
