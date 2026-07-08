// SPDX-License-Identifier: Elastic-2.0
import { test, expect } from '@playwright/test';

// The field-notes section (/field-notes and /field-notes/<id>) is marketing-side
// like / and /recordings, so it inherits the same house rules: it renders, it
// carries the shared chrome, it makes zero non-local requests, and its committed
// source leaks nothing an operator would regret publishing.

test('the field-notes index lists at least one note and links into it', async ({ page }) => {
  await page.goto('/field-notes/');
  await expect(page).toHaveTitle(/Field notes/);
  const h1 = page.locator('h1');
  await expect(h1).toHaveCount(1);
  const cards = page.locator('.fn-card a');
  expect(await cards.count(), 'expected at least one field note card').toBeGreaterThanOrEqual(1);
  // the first card points at a real /field-notes/<id>/ route
  const href = await cards.first().getAttribute('href');
  expect(href).toMatch(/^\/field-notes\/[a-z0-9-]+\/$/);
});

test('a note page renders the MDX body with exactly one h1 (the title)', async ({ page }) => {
  await page.goto('/field-notes/fugu/');
  await expect(page).toHaveTitle(/Fugu.*Corralai field notes/);
  // Single h1 (the note title) — the body prose starts at h2, so the a11y rule holds.
  await expect(page.locator('h1')).toHaveCount(1);
  await expect(page.locator('h1')).toContainText('Fugu');
  // The MDX actually rendered: a body heading and the closing line are present.
  // exact match: the same phrase is a substring of the h1 title, so scope to the
  // body h2 to avoid a two-element strict-mode match.
  await expect(
    page.locator('.note-body').getByRole('heading', { name: 'The knife it leaves unwashed', exact: true }),
  ).toBeVisible();
  await expect(page.locator('.note-body')).toContainText('We built the licensed kitchen first.');
  // The back-link home to the index exists.
  await expect(page.locator('.note-back a')).toHaveAttribute('href', '/field-notes/');
});

test('the header carries the Field notes nav item, marked current on note pages', async ({ page }) => {
  await page.goto('/field-notes/');
  const nav = page.locator('.sh-nav a', { hasText: 'Field notes' });
  await expect(nav).toHaveAttribute('aria-current', 'page');
});

test('zero non-local network requests on a field note page', async ({ page }) => {
  const external: string[] = [];
  page.on('request', (req) => {
    const url = new URL(req.url());
    if (url.hostname !== 'localhost' && url.hostname !== '127.0.0.1') external.push(req.url());
  });
  await page.goto('/field-notes/fugu/');
  await page.waitForLoadState('networkidle');
  expect(external, `unexpected external requests: ${external.join(', ')}`).toHaveLength(0);
});

test('committed field-notes source leaks no operator paths, emails, or tokens', async () => {
  const fs = await import('node:fs');
  const dir = 'src/content/field-notes';
  const files = fs.readdirSync(dir).filter((f) => f.endsWith('.md') || f.endsWith('.mdx'));
  expect(files.length, 'expected at least one field note').toBeGreaterThanOrEqual(1);
  const DENY: [RegExp, string][] = [
    [/\/home\/[A-Za-z0-9_.-]+/g, 'linux home path'],
    [/\/Users\/[A-Za-z0-9_.-]+/g, 'macOS home path'],
    [/[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}/g, 'email address'],
    [/gh[pousr]_[A-Za-z0-9]{20,}/g, 'GitHub token'],
    [/sk-[A-Za-z0-9]{20,}/g, 'API key'],
  ];
  for (const f of files) {
    const text = fs.readFileSync(`${dir}/${f}`, 'utf-8');
    for (const [re, label] of DENY) {
      const m = text.match(re);
      expect(m, `${f} leaks ${label}: ${m?.join(', ')}`).toBeNull();
    }
  }
});
