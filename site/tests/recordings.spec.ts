// SPDX-License-Identifier: Elastic-2.0
import { test, expect } from '@playwright/test';

test('the gallery renders a card per recording, plays one, shows analytics, and stays on-domain', async ({ page }) => {
  const external: string[] = [];
  const backendApiCalls: string[] = [];
  page.on('request', (req) => {
    const url = new URL(req.url());
    if (url.hostname !== 'localhost' && url.hostname !== '127.0.0.1') external.push(req.url());
    if (url.pathname.startsWith('/api/')) backendApiCalls.push(req.url());
  });

  await page.goto('/recordings/');
  const cards = page.locator('.card');
  expect(await cards.count(), 'expected at least one recording card').toBeGreaterThanOrEqual(1);

  await cards.first().click();
  await expect(async () => {
    const max = await page.locator('#replay-scrub').getAttribute('max');
    expect(Number(max)).toBeGreaterThan(0);
  }).toPass({ timeout: 5000 });

  await expect(page.locator('#analytics table').first()).toBeVisible();
  await expect(page.locator('#analytics .bar').first()).toBeVisible();

  expect(external, `unexpected external requests: ${external.join(', ')}`).toHaveLength(0);
  expect(backendApiCalls, `unexpected /api/* calls from a backend-free page: ${backendApiCalls.join(', ')}`).toHaveLength(0);
});

test('a recording with an .analysis.md shows the affordance and reveals the analysis on selection', async ({ page }) => {
  const fs = await import('node:fs');
  const slugs = fs
    .readdirSync('src/data/recordings')
    .filter((f) => f.endsWith('.analysis.md'))
    .map((f) => f.replace(/\.analysis\.md$/, ''));
  test.skip(slugs.length === 0, 'no analysis sidecars committed');

  await page.goto('/recordings/');
  const card = page.locator(`.card[data-slug="${slugs[0]}"]`);
  await expect(card.locator('.has-analysis')).toBeVisible();

  const panel = page.locator(`.analysis-panel[data-analysis-slug="${slugs[0]}"]`);
  await expect(panel).toBeHidden();
  await card.click();
  await expect(panel).toBeVisible();
  await expect(panel.locator('h2').first()).toContainText('Analysis');
});

test('every recording card corresponds to a committed stream + meta pair', async () => {
  const fs = await import('node:fs');
  const files = fs.readdirSync('src/data/recordings');
  const streamFiles = files.filter((f) => f.endsWith('.json') && !f.endsWith('.meta.json'));
  for (const f of streamFiles) {
    const metaName = f.replace(/\.json$/, '.meta.json');
    expect(files, `${f} is missing its ${metaName} sidecar`).toContain(metaName);
    const meta = JSON.parse(fs.readFileSync(`src/data/recordings/${metaName}`, 'utf-8'));
    expect(Array.isArray(meta.models), `${metaName} must carry a models array (may be empty for pre-model-threading recordings)`).toBe(true);
  }
});
