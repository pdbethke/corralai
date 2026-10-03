// SPDX-License-Identifier: Elastic-2.0
import { test, expect } from '@playwright/test';

test('hero renders the canvas and the replay bar starts playing', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('#c')).toBeVisible();
  await expect(page.locator('#replay')).toBeVisible();
  // The scrub bar's max should reflect the baked hero recording's event
  // count (>0) shortly after load — proves startReplay(golden) actually ran.
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

test('the same-model comic opens the hero', async ({ page }) => {
  await page.goto('/');
  const comic = page.locator('#hero img.hero-comic');
  await expect(comic).toBeVisible();
  await expect(comic).toHaveAttribute('alt', /same frontier model/);
});

// The hero LEADS with the refusal: corral is the harness that does not build.
// This replaced the house question as the h1 on 2026-10-01, a deliberate
// repositioning rather than a copy tweak, so it is pinned the same way the
// house question was — an h1 that drifts by accident is the failure this guards.
test('the hero leads with the refusal', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('#hero h1')).toContainText('Corral is the one that refuses to build');
});

// The house question was demoted from the h1, not deleted: it is the same
// argument (no one may be judge in their own cause) made concrete, and it now
// opens the lead. Pinned so a later edit cannot quietly drop it.
test('the hero keeps the house question in the lead', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('#hero p.lead')).toContainText('Would you trust your house');
});

// Pin: the hero's default recording is now corral-audits-corral (corral
// auditing its own signing code, NEEDS-REVIEW) — the founder-requested
// featured tape. The pool caption is data-driven off the tape's own
// pool_verdict event, so this also proves that event renders correctly.
test('the hero replays corral-audits-corral by default and its pool caption renders', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('#hero .verdict-strip .vs-status')).toHaveText('NEEDS-REVIEW');
  await expect(page.locator('#hero .verdict-strip .vs-file code')).toContainText('certify.go');
  await expect(page.locator('#hero .caption').first()).toContainText('NEEDS-REVIEW');
  await expect(page.locator('#hero .bd-link')).toHaveAttribute('href', '/recordings/#corral-audits-corral');
});
