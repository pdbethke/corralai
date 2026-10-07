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

// The hero LEADS with what a developer gets: the bugs their tests miss, and
// a proven test for each. This replaced the refusal as the h1 on 2026-10-07,
// a deliberate repositioning, so it is pinned: an h1 that drifts by accident
// is the failure this guards.
test('the hero leads with the proven missing tests', async ({ page }) => {
  await page.goto('/');
  const h1 = page.locator('#hero h1');
  await expect(h1).toContainText('Your tests pass.');
  await expect(h1).toContainText('the tests that catch them');
  await expect(page.locator('#hero .ctas .cta').first()).toHaveAttribute('href', '/docs/getting-started/');
});

// The exhibit is one real pair from the hero tape: a bug that survived the
// suite, and the test corral wrote that catches it. Both are read from the
// tape at build time, so this reads the tape too and checks the page shows
// exactly what the run recorded, never a hand-typed copy.
test('the hero exhibit shows a real surviving bug and the test written for it, from the tape', async ({ page }) => {
  const fs = await import('node:fs');
  const meta = JSON.parse(fs.readFileSync('src/data/recordings/corral-audits-corral.meta.json', 'utf-8'));
  const tape = JSON.parse(fs.readFileSync('src/data/recordings/corral-audits-corral.json', 'utf-8'));
  const id: string = meta.hero_exhibit.survivor;
  const verdict = tape.events.find((e: any) => e.kind === 'pool_verdict').detail;
  const test = tape.events.find((e: any) => e.kind === 'task_done' && e.subject === `test-writer/${id}`).detail.result as string;
  const testName = /func (Test\w+)/.exec(test)![1];
  await page.goto('/');
  const ex = page.locator('#hero .exhibit');
  await expect(ex).toBeVisible();
  await expect(ex.locator('.ex-test')).toContainText(`func ${testName}(`);
  await expect(ex.locator('.ex-bug .add')).toContainText('return stmt, true, nil');
  await expect(ex.locator('.ex-numbers')).toContainText(`${verdict.mutants_total} bugs planted`);
  await expect(ex.locator('.ex-numbers')).toContainText(`${verdict.survivors} slipped past our tests`);
  await expect(ex.locator('.ex-numbers')).toContainText(`${verdict.proven_missed} now have a test`);
  await expect(ex.locator(`a[href*="/pull/${meta.hero_exhibit.landed}"]`)).toHaveCount(1);
  // What "proven" means, said on the page: the suite passed with the bug in,
  // and the written test fails with it and passes without it.
  await expect(ex.locator('.ex-bug .ex-result')).toContainText('our own tests still passed');
  await expect(ex.locator('.ex-test .ex-result')).toContainText('Fails with the bug in. Passes on the real code.');
});

// The comic and the refusal are the reason why, so they follow the exhibit.
test('the same-model comic and the refusal follow the exhibit', async ({ page }) => {
  await page.goto('/');
  const comic = page.locator('#hero img.hero-comic');
  await expect(comic).toBeVisible();
  await expect(comic).toHaveAttribute('alt', /same frontier model/);
  const ex = await page.locator('#hero .exhibit').boundingBox();
  const c = await comic.boundingBox();
  expect(c!.y).toBeGreaterThan(ex!.y + ex!.height - 1);
  await expect(page.locator('#hero .why h2')).toContainText('Corral is the one that refuses to build');
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
