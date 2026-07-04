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
  // Windows-shaped paths (drive-letter and backslash-home), mirroring the
  // two extra deny rules scrub-golden-run.py carries that scripts/export
  // didn't originally quote in Task 2's excerpt.
  [/\b[A-Za-z]:(?:\\{1,2}[A-Za-z0-9._-]+)+/g, 'Windows drive-letter path'],
  [/\\{1,2}(?:Users|home)\\{1,2}[A-Za-z0-9._-]+/g, 'Windows backslash home path'],
];

test('the committed golden-run.json passes the deny-list scan', async () => {
  // golden-run.json is bundled via the Astro data import, not served as a
  // static asset — this test reads the committed source artifact straight
  // off disk, independent of how Astro packages it into dist/.
  const fs = await import('node:fs');
  const text = fs.readFileSync('src/data/golden-run.json', 'utf-8');
  const offenses: string[] = [];
  for (const [pattern, label] of DENY_PATTERNS) {
    const matches = text.match(pattern);
    if (matches) offenses.push(`[${label}] ${matches.join(', ')}`);
  }
  expect(offenses, `golden-run.json failed the deny-list scan:\n${offenses.join('\n')}`).toHaveLength(0);
});

test('zero non-local network requests across the whole page session', async ({ page }) => {
  const external: string[] = [];
  page.on('request', (req) => {
    const url = new URL(req.url());
    if (url.hostname !== 'localhost' && url.hostname !== '127.0.0.1') {
      external.push(req.url());
    }
  });

  await page.goto('/');
  // Let the replay's fetch-or-no-fetch settle and autoplay begin.
  await expect(async () => {
    const max = await page.locator('#replay-scrub').getAttribute('max');
    expect(Number(max)).toBeGreaterThan(0);
  }).toPass({ timeout: 5000 });

  // Interact with the scrub bar mid-session — the request interception
  // above stays wired for this whole block, not just the initial `load`
  // event, so a lazily-fetched asset triggered by scrubbing would still
  // be caught. This is the "whole page session" hardening over the
  // load-only version of this check.
  const scrub = page.locator('#replay-scrub');
  const max = Number(await scrub.getAttribute('max'));
  await scrub.evaluate((el, target) => {
    (el as HTMLInputElement).value = String(target);
    el.dispatchEvent(new Event('input'));
  }, Math.floor(max / 2));

  // Give autoplay a further window to keep running post-scrub.
  await page.waitForTimeout(2000);

  expect(external, `unexpected external requests: ${external.join(', ')}`).toHaveLength(0);
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
