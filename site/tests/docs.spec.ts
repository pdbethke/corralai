// SPDX-License-Identifier: Elastic-2.0
import { test, expect } from '@playwright/test';

test('/docs mounts, renders the sidebar, and stays on-domain', async ({ page }) => {
  const external: string[] = [];
  page.on('request', (req) => {
    const url = new URL(req.url());
    if (url.hostname !== 'localhost' && url.hostname !== '127.0.0.1') external.push(req.url());
  });
  await page.goto('/docs/getting-started/');
  // The outer <nav aria-label="Main"> wraps a fixed-position sidebar pane
  // and collapses to zero height itself (a Starlight layout quirk, not a
  // rendering bug), so assert it's attached and check visibility on the
  // actual link inside it instead of on the wrapping <nav>.
  await expect(page.getByRole('navigation', { name: 'Main' })).toBeAttached();
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
    // The 6 cli/* entries are deliberate raw `link:` sidebar items pointing
    // at pages Task 4 generates mechanically from --help output — they
    // don't exist yet by design (see astro.config.mjs), so a 404 there is
    // expected until Task 4 lands, not a dead-link regression in this task.
    if (href.startsWith('/docs/cli/')) continue;
    const res = await page.request.get(href);
    expect(res.status(), `${href} returned ${res.status()}`).toBeLessThan(400);
  }
});

test('a full /docs session — navigate, search via Pagefind, click a result — never leaves localhost', async ({
  page,
}) => {
  // The zero-external-requests guarantee (site.spec.ts covers the landing
  // page) must hold for the docs shell too, including its search UI: Astro
  // Starlight's Pagefind index and worker are static local assets, not a
  // hosted search API, so a full search-and-click round trip should never
  // fire a single non-local request.
  const external: string[] = [];
  page.on('request', (req) => {
    const url = new URL(req.url());
    if (url.hostname !== 'localhost' && url.hostname !== '127.0.0.1') external.push(req.url());
  });

  await page.goto('/docs/getting-started/');

  await page.getByRole('button', { name: 'Search' }).click();
  const dialog = page.locator('dialog[aria-label="Search"]');
  await expect(dialog).toBeVisible();

  const searchInput = dialog.locator('input.pagefind-ui__search-input');
  await searchInput.fill('mission');

  const firstResult = dialog.locator('a.pagefind-ui__result-link').first();
  await expect(firstResult).toBeVisible({ timeout: 5000 });

  const resultHref = await firstResult.getAttribute('href');
  expect(resultHref).toBeTruthy();
  await firstResult.click();

  await expect(page).toHaveURL(new RegExp(String(resultHref)));
  await expect(page.getByRole('navigation', { name: 'Main' })).toBeAttached();

  expect(
    external,
    `unexpected external requests during a /docs search session: ${external.join(', ')}`
  ).toHaveLength(0);
});
