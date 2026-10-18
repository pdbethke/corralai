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
