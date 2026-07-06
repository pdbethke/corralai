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
