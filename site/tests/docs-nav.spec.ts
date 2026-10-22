// SPDX-License-Identifier: Elastic-2.0
// The docs nav is on every page, so any doc is one click away wherever a
// reader is (components/DocsSidebar.astro, drawn by Header.astro). From
// 1024px it is a fixed column the page moves over for; narrower, the header's
// Docs button opens it as a drawer. The /ledger/ pages are rendered by Go and
// get it with their copy of the header (integrations/ledger-chrome.mjs), so
// they are tested here like any other page.
import { test, expect, type Page } from '@playwright/test';
import { readdirSync } from 'node:fs';

const entry = readdirSync('dist/ledger/entry').find((f) => f.endsWith('.html'));
const PAGES = ['/', '/docs/getting-started/', '/field-notes/fugu/', '/field-notes/', '/recordings/', '/warehouse/', '/ledger/', `/ledger/entry/${entry}`, '/no-such-page/'];

const noSideScroll = async (page: Page) => {
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow, 'the page must not scroll sideways').toBeLessThanOrEqual(0);
};

for (const path of PAGES) {
  test(`wide: ${path} shows the docs nav, and nothing on the page sits under it`, async ({ page }) => {
    await page.setViewportSize({ width: 1400, height: 900 });
    await page.goto(path);
    const nav = page.getByRole('navigation', { name: 'Docs' });
    await expect(nav.getByRole('link', { name: 'Getting started' })).toBeVisible();
    await expect(page.getByRole('button', { name: 'Docs menu' })).toBeHidden();
    const side = (await page.locator('aside.site-side').boundingBox())!;
    expect(side.x).toBe(0);
    // Every element after the header and the nav starts right of the nav.
    const lefts = await page.evaluate(() =>
      [...document.body.children]
        .filter((el) => !el.matches('header.site-header, aside.site-side, script, style, dialog'))
        .map((el) => el.getBoundingClientRect())
        .filter((r) => r.width > 0 && r.height > 0)
        .map((r) => r.left)
    );
    expect(lefts.length).toBeGreaterThan(0);
    for (const left of lefts) expect(left, 'content under the docs nav').toBeGreaterThanOrEqual(side.x + side.width - 1);
    // The header spans the page, over the nav's column.
    const header = (await page.locator('header.site-header').boundingBox())!;
    expect(header.x).toBe(0);
    expect(header.width).toBe(1400);
    await noSideScroll(page);
  });
}

for (const path of ['/', '/ledger/']) {
  test(`phone: ${path} opens the docs nav from the header and closes it again`, async ({ page }) => {
    await page.setViewportSize({ width: 390, height: 844 });
    await page.goto(path);
    const link = page.getByRole('navigation', { name: 'Docs' }).getByRole('link', { name: 'Configuration' });
    const button = page.getByRole('button', { name: 'Docs menu' });
    await expect(link).toBeHidden();
    await button.click();
    await expect(button).toHaveAttribute('aria-expanded', 'true');
    await expect(link).toBeVisible();
    await page.keyboard.press('Escape');
    await expect(link).toBeHidden();
    await expect(button).toHaveAttribute('aria-expanded', 'false');
    await button.click();
    await link.click();
    await expect(page).toHaveURL(/\/docs\/configuration\/$/);
    await noSideScroll(page);
  });
}

test('the docs nav marks the page being read, and only on docs pages', async ({ page }) => {
  await page.goto('/docs/configuration/');
  await expect(page.locator('aside.site-side a[aria-current="page"]')).toHaveText('Configuration');
  await page.goto('/ledger/');
  await expect(page.locator('aside.site-side a[aria-current="page"]')).toHaveCount(0);
});

test('a record page carries only the chrome\'s own CSS from the home page', async ({ page }) => {
  // ledger-chrome copies rules by the scope attributes on the header and the
  // nav. A scope pattern that matched every component (as one did until
  // 2026-10-22) would bring the front page's own rules, the hero's among them.
  const html = await (await page.request.get('/ledger/')).text();
  const home = await (await page.request.get('/')).text();
  const heroScope = home.match(/<section[^>]*id="hero"[^>]*(data-astro-cid-[a-z0-9]+)/)?.[1];
  expect(heroScope, 'the hero has a scope attribute to look for').toBeTruthy();
  expect(html).not.toContain(heroScope!);
});

for (const theme of ['light', 'dark']) {
  test(`${theme}: a record page is on the same background as the rest of the site`, async ({ page }) => {
    // The record pages keep their own palette (white cards); ledger-chrome
    // gives them the site's --bg so they are not a lighter sheet among the
    // site's cream ones.
    await page.addInitScript((t) => localStorage.setItem('corralai-site-theme', t), theme);
    const bg = async (path: string) => {
      await page.goto(path);
      return page.evaluate(() => getComputedStyle(document.body).backgroundColor);
    };
    const home = await bg('/');
    expect(await bg('/ledger/')).toBe(home);
    expect(await bg(`/ledger/entry/${entry}`)).toBe(home);
  });
}
