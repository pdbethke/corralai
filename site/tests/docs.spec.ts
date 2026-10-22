// SPDX-License-Identifier: Elastic-2.0
// /docs: the site's own pages since 2026-10-22 (src/pages/docs), with the
// site header, a sidebar from src/data/docs-sidebar.mjs, and the header's
// site-wide Pagefind search.
import { test, expect, type Page } from '@playwright/test';

const offsite = (page: Page) => {
  const external: string[] = [];
  page.on('request', (req) => {
    const url = new URL(req.url());
    if (url.hostname !== 'localhost' && url.hostname !== '127.0.0.1') external.push(req.url());
  });
  return external;
};

const search = async (page: Page, term: string) => {
  await page.getByRole('button', { name: 'Search the site' }).click();
  const dialog = page.locator('dialog[aria-label="Search"]');
  await expect(dialog).toBeVisible();
  await dialog.locator('input.pagefind-ui__search-input').fill(term);
  return dialog;
};

test('/docs mounts, renders the sidebar, and stays on-domain', async ({ page }) => {
  const external = offsite(page);
  await page.goto('/docs/getting-started/');
  const side = page.getByRole('navigation', { name: 'Docs' });
  await expect(side.getByRole('link', { name: 'Getting started' })).toBeVisible();
  await expect(side.getByRole('link', { name: 'Getting started' })).toHaveAttribute('aria-current', 'page');
  await expect(page.locator('header.site-header nav.sh-nav a[aria-current="page"]')).toHaveText('Docs');
  expect(external, `unexpected external requests from /docs: ${external.join(', ')}`).toHaveLength(0);
});

test('the docs sidebar has no dead links across every listed section', async ({ page }) => {
  await page.goto('/docs/getting-started/');
  const hrefs = await page.getByRole('navigation', { name: 'Docs' }).locator('a').evaluateAll((els) =>
    els.map((el) => el.getAttribute('href')!)
  );
  expect(hrefs.length).toBeGreaterThan(30);
  for (const href of new Set(hrefs)) {
    const res = await page.request.get(href);
    expect(res.status(), `${href} returned ${res.status()}`).toBeLessThan(400);
  }
});

test('a docs page links to the pages before and after it in sidebar order', async ({ page }) => {
  await page.goto('/docs/getting-started/');
  const pager = page.getByRole('navigation', { name: 'Previous and next' });
  await expect(pager.locator('a[rel="prev"]')).toHaveAttribute('href', '/docs/how-it-works/');
  await expect(pager.locator('a[rel="next"]')).toHaveAttribute('href', '/docs/first-audit/');
});

test('a collapsed sidebar group opens on its own pages only', async ({ page }) => {
  const group = (p: Page) =>
    p.locator('nav[aria-label="Docs"] details', { has: p.locator('summary', { hasText: 'The optional daemon' }) });
  await page.goto('/docs/getting-started/');
  await expect(group(page)).not.toHaveAttribute('open', '');
  await page.goto('/docs/mcp-tools/');
  await expect(group(page)).toHaveAttribute('open', '');
});

test('on a phone the sidebar folds into a Docs menu above the page', async ({ page }) => {
  await page.setViewportSize({ width: 375, height: 800 });
  await page.goto('/docs/getting-started/');
  const menu = page.locator('details.docs-side');
  await expect(menu.locator('> summary')).toBeVisible();
  await expect(page.getByRole('link', { name: 'Configuration' })).toBeHidden();
  await menu.locator('> summary').click();
  await expect(page.getByRole('link', { name: 'Configuration' })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth)).toBeLessThanOrEqual(0);
});

test('a docs note renders as a callout with its title', async ({ page }) => {
  await page.goto('/docs/mcp-tools/');
  const note = page.locator('aside.callout-note').first();
  await expect(note.locator('.callout-title')).toHaveText('The optional daemon — frozen, and not read by any audit');
  await expect(note.locator('strong', { hasText: 'brain' })).toBeVisible();
});

test('a full /docs session — navigate, search via Pagefind, click a result — never leaves localhost', async ({
  page,
}) => {
  // Pagefind's index and worker are static files under /pagefind/, not a
  // hosted search API, so a search-and-click round trip fires no request
  // off this host.
  const external = offsite(page);
  await page.goto('/docs/getting-started/');
  const dialog = await search(page, 'mission');
  const firstResult = dialog.locator('a.pagefind-ui__result-link').first();
  await expect(firstResult).toBeVisible({ timeout: 5000 });
  const resultHref = await firstResult.getAttribute('href');
  expect(resultHref).toBeTruthy();
  await firstResult.click();
  await expect(page).toHaveURL(new RegExp(String(resultHref).split('#')[0]));
  expect(external, `unexpected external requests during a /docs search session: ${external.join(', ')}`).toHaveLength(0);
});

test('Pagefind search finds "claims" and navigates to the claims & leases page', async ({ page }) => {
  const external = offsite(page);
  await page.goto('/docs/getting-started/');
  const dialog = await search(page, 'claims');
  const result = dialog.locator('a.pagefind-ui__result-link', { hasText: /Claims/i }).first();
  await expect(result).toBeVisible({ timeout: 5000 });
  await result.click();
  await expect(page).toHaveURL(/claims-and-leases/);
  expect(external, `Pagefind search made an external request: ${external.join(', ')}`).toHaveLength(0);
});

// The search is the whole site's, not the docs': field notes and the
// Go-rendered ledger pages are in the index, and the ledger's copy of the
// header can open it.
test('search from the home page finds a field note', async ({ page }) => {
  await page.goto('/');
  const dialog = await search(page, 'fugu');
  await expect(dialog.locator('a.pagefind-ui__result-link[href^="/field-notes/"]').first()).toBeVisible({ timeout: 5000 });
});

test('search opens on a ledger page and finds ledger pages', async ({ page }) => {
  await page.goto('/ledger/');
  const dialog = await search(page, 'proven');
  await expect(dialog.locator('a.pagefind-ui__result-link[href^="/ledger/"]').first()).toBeVisible({ timeout: 5000 });
});

test('the header search does not index the header itself', async ({ page }) => {
  // "Ledger" is a link in every page's header. Indexed, it would match every
  // page; as content it is on the ledger pages and the few that discuss it.
  // Asked through Pagefind's own API, so the count is not the UI's first page.
  // What holds this today is Pagefind's default of skipping <header>, <nav>
  // and <footer> (taking data-pagefind-ignore off the header leaves the count
  // unchanged, checked 2026-10-22); the attribute keeps it if the markup moves.
  await page.goto('/docs/getting-started/');
  const { hits, pages } = await page.evaluate(async () => {
    const pf = await import('/pagefind/pagefind.js');
    const entry = await (await fetch('/pagefind/pagefind-entry.json')).json();
    const hit = await pf.search('ledger');
    return { hits: hit.results.length, pages: entry.languages.en.page_count as number };
  });
  expect(pages).toBeGreaterThan(100);
  expect(hits).toBeGreaterThan(0);
  expect(hits).toBeLessThan(pages - 20);
});

test('the UI-tour pages have no obvious accessibility footguns', async ({ page }) => {
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
