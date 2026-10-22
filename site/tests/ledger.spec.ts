// SPDX-License-Identifier: Elastic-2.0
// The record pages are rendered by internal/recordview into public/ledger
// before the site is built (scripts/gen-ledger-site.sh; deploy-site.yml
// runs it before test:e2e). At phone width, no page may scroll sideways:
// wide tables scroll inside their own box instead.
import { test, expect } from '@playwright/test';
import { readdirSync } from 'node:fs';

const entries = readdirSync('public/ledger/entry');
const firstOf = (want: RegExp) => entries.find((f) => want.test(f));

test.use({ viewport: { width: 375, height: 800 } });

const overflow = () => document.documentElement.scrollWidth - document.documentElement.clientWidth;

for (const path of ['/ledger/', '/ledger/chain-1.html', '/ledger/audits-1.html', '/ledger/reviews-1.html', '/ledger/verdicts.html']) {
  test(`${path} does not scroll sideways at 375px`, async ({ page }) => {
    const res = await page.goto(path);
    expect(res?.status(), `${path} must exist: run scripts/gen-ledger-site.sh first`).toBe(200);
    expect(await page.evaluate(overflow), `${path} overflow in px`).toBeLessThanOrEqual(0);
  });
}

test('an entry page does not scroll sideways at 375px', async ({ page }) => {
  const f = firstOf(/\.html$/);
  expect(f, 'no entry pages: run scripts/gen-ledger-site.sh first').toBeTruthy();
  await page.goto(`/ledger/entry/${f}`);
  expect(await page.evaluate(overflow), `entry ${f} overflow in px`).toBeLessThanOrEqual(0);
});

// The pages a PR or issue link lands on must lead back to the site, and look
// like it: the ledger-chrome integration copies the home page's own header
// onto them, so the header here must match the home page's link for link and
// be drawn in the same colors, with the Ledger link marked current.
test('an entry page carries the same site header as the home page', async ({ page }) => {
  const f = firstOf(/\.html$/);
  expect(f, 'no entry pages: run scripts/gen-ledger-site.sh first').toBeTruthy();
  const header = async (path: string) => {
    await page.goto(path);
    const h = page.locator('header.site-header');
    await expect(h).toHaveCount(1);
    return h.evaluate((el) => {
      const link = el.querySelector('.sh-nav a:not([aria-current])')!;
      const css = (e: Element, p: string) => getComputedStyle(e).getPropertyValue(p);
      return {
        links: [...el.querySelectorAll('nav a')].map((a) => [a.textContent, a.getAttribute('href')]),
        current: [...el.querySelectorAll('[aria-current="page"]')].map((a) => a.getAttribute('href')),
        bg: css(el, 'background-color'),
        position: css(el, 'position'),
        link: [css(link, 'color'), css(link, 'font-size'), css(link, 'padding')],
        mark: css(el.querySelector('.cai-mark')!, 'color'),
      };
    });
  };
  const home = await header('/');
  const entry = await header(`/ledger/entry/${f}`);
  expect(entry.links).toEqual(home.links);
  expect(home.current).toEqual(['/']);
  expect(entry.current).toEqual(['/ledger/']);
  expect({ ...entry, current: [] }).toEqual({ ...home, current: [] });
});
