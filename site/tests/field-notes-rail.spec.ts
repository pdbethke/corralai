// SPDX-License-Identifier: Elastic-2.0
import { test, expect, type Page } from '@playwright/test';

// The field-notes rail: a column of the newest notes, on the right of the
// front page and of every note page on a wide screen. On a narrow screen it
// moves out of the way of the content it sits beside: on the front page it
// comes straight after the hero, cut to three notes, so the notes are still
// the first thing below the fold; on a note page it comes after the article,
// so the note is read first.

const WIDE = { width: 1400, height: 900 };
const NARROW = { width: 390, height: 844 };

async function noSideScroll(page: Page) {
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow, 'the page must not scroll sideways').toBeLessThanOrEqual(0);
}

test('front page, wide: the rail lists the newest notes to the right of the content', async ({ page }) => {
  await page.setViewportSize(WIDE);
  await page.goto('/');
  const rail = page.locator('aside.fn-rail');
  await expect(rail).toBeVisible();
  const links = rail.locator('li a');
  expect(await links.count()).toBeGreaterThanOrEqual(5);
  await expect(links.first()).toHaveAttribute('href', /^\/field-notes\/[a-z0-9-]+\/$/);
  // Newest first: the dates are non-increasing.
  const dates = await rail.locator('li time').evaluateAll((ts) => ts.map((t) => t.getAttribute('datetime') ?? ''));
  expect([...dates].sort().reverse()).toEqual(dates);
  const r = await rail.boundingBox();
  const m = await page.locator('#ci-gate').boundingBox();
  expect(r!.x, 'the rail sits right of the main column').toBeGreaterThanOrEqual(m!.x + m!.width - 1);
  await expect(rail.locator('a', { hasText: 'All field notes' })).toHaveAttribute('href', '/field-notes/');
  // In the first screen, beside the hero, not below it: the rail is there to
  // call attention to the notes.
  expect(r!.y, 'the rail starts in the first screen').toBeLessThan(WIDE.height / 2);
  const hero = await page.locator('#hero').boundingBox();
  expect(r!.x).toBeGreaterThanOrEqual(hero!.x + hero!.width - 1);
  await expect(page.locator('h1')).toHaveCount(1);
  await noSideScroll(page);
});

test('front page, narrow: the rail follows the hero, three notes long', async ({ page }) => {
  await page.setViewportSize(NARROW);
  await page.goto('/');
  const rail = page.locator('aside.fn-rail');
  await expect(rail).toBeVisible();
  const r = await rail.boundingBox();
  const hero = await page.locator('#hero').boundingBox();
  const next = await page.locator('#the-videos').boundingBox();
  expect(r!.y).toBeGreaterThanOrEqual(hero!.y + hero!.height - 1);
  expect(r!.y + r!.height).toBeLessThanOrEqual(next!.y + 1);
  await expect(rail.locator('li:visible')).toHaveCount(3);
  await noSideScroll(page);
});

test('note page, wide: the rail sits beside the note and leaves the note out', async ({ page }) => {
  await page.setViewportSize(WIDE);
  await page.goto('/field-notes/fugu/');
  const rail = page.locator('aside.fn-rail');
  await expect(rail).toBeVisible();
  await expect(rail.locator('a[href="/field-notes/fugu/"]')).toHaveCount(0);
  const r = await rail.boundingBox();
  const note = await page.locator('article.note').boundingBox();
  expect(r!.x).toBeGreaterThanOrEqual(note!.x + note!.width - 1);
  await expect(page.locator('h1')).toHaveCount(1);
  await noSideScroll(page);
});

test('note page, narrow: the note comes first, the rail after it', async ({ page }) => {
  await page.setViewportSize(NARROW);
  await page.goto('/field-notes/fugu/');
  const r = await page.locator('aside.fn-rail').boundingBox();
  const note = await page.locator('article.note').boundingBox();
  expect(r!.y).toBeGreaterThanOrEqual(note!.y + note!.height - 1);
  await noSideScroll(page);
});
