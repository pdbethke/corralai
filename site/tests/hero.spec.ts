// SPDX-License-Identifier: Elastic-2.0
import { test, expect } from '@playwright/test';

test('hero renders the canvas and the replay bar starts playing', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('#c')).toBeVisible();
  await expect(page.locator('#replay')).toBeVisible();
  // The scrub bar's max should reflect the baked golden-run event count
  // (>0) shortly after load — proves startReplay(golden) actually ran.
  await expect(async () => {
    const max = await page.locator('#replay-scrub').getAttribute('max');
    expect(Number(max)).toBeGreaterThan(0);
  }).toPass({ timeout: 5000 });
});

test('scrubbing the replay bar updates the position label', async ({ page }) => {
  await page.goto('/');
  const scrub = page.locator('#replay-scrub');
  await expect(async () => {
    const max = await scrub.getAttribute('max');
    expect(Number(max)).toBeGreaterThan(0);
  }).toPass({ timeout: 5000 });
  const max = Number(await scrub.getAttribute('max'));
  await scrub.evaluate((el, target) => {
    (el as HTMLInputElement).value = String(target);
    el.dispatchEvent(new Event('input'));
  }, Math.floor(max / 2));
  await expect(page.locator('#replay-label')).toHaveText(new RegExp(`^${Math.floor(max / 2)} / ${max}$`));
});
