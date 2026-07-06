// SPDX-License-Identifier: Elastic-2.0
import { test, expect } from '@playwright/test';

// The site chrome added for launch: a sticky header with the logo + primary
// nav on the hand-authored pages, and a matching logo in the /docs header.
// These are light render/behavior checks — the heavier a11y/network invariants
// live in site.spec.ts / docs.spec.ts and already cover the whole page.

test('the landing header carries the logo, primary nav, and a star CTA', async ({ page }) => {
  await page.goto('/');
  const header = page.locator('header.site-header');
  await expect(header).toBeVisible();

  // Sticky, so it stays as the page scrolls (part of "intentionally embedded",
  // not an orphaned demo).
  const position = await header.evaluate((el) => getComputedStyle(el).position);
  expect(position, 'the site header must be sticky').toBe('sticky');

  // Brand lockup links home and names itself for assistive tech.
  const brand = header.locator('a.sh-brand');
  await expect(brand).toHaveAttribute('href', '/');
  await expect(brand).toHaveAttribute('aria-label', /home/i);
  await expect(brand.locator('.cai-word')).toContainText('CorralAI');

  // Primary nav: Home, Docs, Recordings, GitHub.
  const nav = header.locator('nav.sh-nav');
  for (const label of ['Home', 'Docs', 'Recordings', 'GitHub']) {
    await expect(nav.getByRole('link', { name: label, exact: true })).toBeVisible();
  }
  // Home is marked as the current page on the landing route.
  await expect(nav.getByRole('link', { name: 'Home', exact: true })).toHaveAttribute('aria-current', 'page');

  // Star CTA points at the repo.
  await expect(header.locator('a.sh-star')).toHaveAttribute('href', /github\.com\/pdbethke\/corralai/);
});

test('the header nav links resolve on-domain (docs + recordings)', async ({ page }) => {
  await page.goto('/');
  const nav = page.locator('header.site-header nav.sh-nav');
  const docsHref = await nav.getByRole('link', { name: 'Docs', exact: true }).getAttribute('href');
  const recHref = await nav.getByRole('link', { name: 'Recordings', exact: true }).getAttribute('href');
  for (const href of [docsHref, recHref]) {
    expect(href).toBeTruthy();
    const res = await page.request.get(href!);
    expect(res.status(), `${href} returned ${res.status()}`).toBeLessThan(400);
  }
});

test('the header theme toggle flips the persisted site theme', async ({ page }) => {
  await page.goto('/');
  const html = page.locator('html');
  const before = await html.getAttribute('data-theme');
  await page.locator('#sh-theme-toggle').click();
  const after = await html.getAttribute('data-theme');
  expect(after, 'clicking the toggle must switch the theme').not.toBe(before);
  const persisted = await page.evaluate(() => localStorage.getItem('corralai-site-theme'));
  expect(persisted, 'the theme choice must persist').toBe(after);
});

test('the replay bar themes the DEMO WINDOW independently of the site; the HUD speed control persists', async ({ page }) => {
  await page.goto('/');
  const bottomTheme = page.locator('#replay-theme-toggle');
  const hudSpeed = page.locator('#hud-speed'); // the only replay-speed control now — top HUD, shown while replaying
  const headerTheme = page.locator('#sh-theme-toggle');

  await expect(bottomTheme).toBeVisible();
  await expect(hudSpeed).toBeVisible();

  const html = page.locator('html');
  // The replay-bar moon is the DEMO WINDOW's own control: it flips
  // data-cockpit-theme, persists corralai-cockpit-theme, and must NOT touch the
  // site's data-theme (that's the header moon's job).
  const siteBefore = await html.getAttribute('data-theme');
  const cockpitBefore = await html.getAttribute('data-cockpit-theme');
  await bottomTheme.click();
  const cockpitAfter = await html.getAttribute('data-cockpit-theme');
  expect(cockpitAfter, 'replay-bar toggle must switch data-cockpit-theme').not.toBe(cockpitBefore);
  expect(await page.evaluate(() => localStorage.getItem('corralai-cockpit-theme'))).toBe(cockpitAfter);
  expect(await html.getAttribute('data-theme'), 'demo-window toggle must NOT change the site theme').toBe(siteBefore);

  // And the header moon themes the site WITHOUT touching the demo window.
  await headerTheme.click();
  expect(await html.getAttribute('data-theme'), 'header toggle must switch the site theme').not.toBe(siteBefore);
  expect(await html.getAttribute('data-cockpit-theme'), 'site toggle must NOT change the demo window').toBe(cockpitAfter);

  // Speed lives only in the cockpit HUD now (the site header no longer carries
  // it); changing it persists.
  await hudSpeed.selectOption('8');
  await expect(hudSpeed).toHaveValue('8');
  expect(await page.evaluate(() => localStorage.getItem('corralai-replay-speed'))).toBe('8');
  // and the site header has no speed control anymore
  await expect(page.locator('#sh-replay-speed')).toHaveCount(0);
});

test('the recordings replay bar themes the demo window independently; speed still syncs', async ({ page }) => {
  await page.goto('/recordings/');
  await expect(page.locator('#replay-theme-toggle')).toBeVisible();
  // The HUD speed control shows only while a tape is playing; pick a card first.
  await page.locator('.card[data-slug]').first().click();
  const hudSpeed = page.locator('#hud-speed');
  await expect(hudSpeed).toBeVisible();

  const html = page.locator('html');
  const siteBefore = await html.getAttribute('data-theme');
  await page.locator('#replay-theme-toggle').click();
  expect(await html.getAttribute('data-cockpit-theme')).toBe('light');
  expect(await html.getAttribute('data-theme'), 'demo-window toggle must NOT change the site theme').toBe(siteBefore);

  await hudSpeed.selectOption('16');
  await expect(hudSpeed).toHaveValue('16');
  expect(await page.evaluate(() => localStorage.getItem('corralai-replay-speed'))).toBe('16');
});

test('the cockpit HUD speed control defaults to 2x and affects playback', async ({ page }) => {
  await page.goto('/');
  // the HUD speed control appears once the hero tape starts playing
  await expect(async () => {
    const max = await page.locator('#replay-scrub').getAttribute('max');
    expect(Number(max)).toBeGreaterThan(0);
  }).toPass({ timeout: 5000 });
  const speed = page.locator('#hud-speed');
  await expect(speed).toBeVisible();
  await expect(speed).toHaveValue('2');

  const startIdx = await page.evaluate(() => Number(document.getElementById('replay-scrub')!.value));
  await speed.selectOption('1');
  await page.waitForTimeout(450);
  const slowIdx = await page.evaluate(() => Number(document.getElementById('replay-scrub')!.value));
  const slowDelta = slowIdx - startIdx;

  await page.evaluate((idx) => {
    const scrub = document.getElementById('replay-scrub') as HTMLInputElement;
    scrub.value = String(idx);
    scrub.dispatchEvent(new Event('input'));
  }, startIdx);
  await speed.selectOption('16');
  await page.waitForTimeout(450);
  const fastIdx = await page.evaluate(() => Number(document.getElementById('replay-scrub')!.value));
  const fastDelta = fastIdx - startIdx;

  expect(fastDelta, '16× must advance the scrubber faster than 1×').toBeGreaterThan(slowDelta);

  await speed.selectOption('8');
  const persisted = await page.evaluate(() => localStorage.getItem('corralai-replay-speed'));
  expect(persisted).toBe('8');
});

test('the site header carries no replay-speed control (it lives in the cockpit HUD)', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('header.site-header')).toBeVisible();
  await expect(page.locator('#sh-replay-speed')).toHaveCount(0);
  await page.goto('/recordings/');
  await expect(page.locator('#sh-replay-speed')).toHaveCount(0);
});

test('the recordings page carries the same site header', async ({ page }) => {
  await page.goto('/recordings/');
  const header = page.locator('header.site-header');
  await expect(header).toBeVisible();
  await expect(header.locator('nav.sh-nav').getByRole('link', { name: 'Recordings', exact: true }))
    .toHaveAttribute('aria-current', 'page');
});

test('the docs header shows the CorralAI logo linking back to the site home', async ({ page }) => {
  await page.goto('/docs/getting-started/');
  const title = page.locator('a.docs-site-title');
  await expect(title).toBeVisible();
  await expect(title).toHaveAttribute('href', '/');
  await expect(title).toContainText('CorralAI');
});

test('the launch docs pages render (configuration, MCP tools, limitations)', async ({ page }) => {
  const pages: [string, RegExp][] = [
    ['/docs/configuration/', /Configuration/i],
    ['/docs/mcp-tools/', /MCP tools/i],
    ['/docs/limitations/', /Limitations/i],
  ];
  for (const [url, heading] of pages) {
    await page.goto(url);
    const h1 = page.locator('h1');
    await expect(h1, `${url} must have exactly one <h1>`).toHaveCount(1);
    await expect(h1).toContainText(heading);
    // reachable from the sidebar
    await expect(page.getByRole('navigation', { name: 'Main' })).toBeAttached();
  }
});
