// SPDX-License-Identifier: Elastic-2.0
import { test, expect } from '@playwright/test';

test('the homepage distinguishes variable discovery from a recorded proof', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('#hero .hero-sub')).toContainText('Not every survivor gets proven');
  for (const selector of ['#ci-gate', '#the-videos']) {
    await expect(page.locator(selector)).toContainText('fresh run');
    await expect(page.locator(selector)).toContainText('vary');
    await expect(page.locator(selector)).not.toContainText('does not drift');
  }
  await expect(page.locator('#how-it-works')).toContainText('do not guarantee independence');
  await expect(page.locator('#the-record')).toContainText('tail');
  await expect(page.locator('#the-record')).toContainText('external record of the expected head');
});

test('the mutation guide explains assumptions, related work and bundle limits', async ({ page }) => {
  await page.goto('/docs/concepts/mutation-testing/');
  const article = page.locator('.sl-markdown-content');
  await expect(article).toContainText('independent observations from a representative sample');
  await expect(article).toContainText('not a probability that your code is correct');
  await expect(article).toContainText('authored_together');
  await expect(article).toContainText('does not change the individual proven count');
  await expect(article.locator('a[href="https://arxiv.org/abs/2501.12862"]')).toHaveCount(1);
  await expect(article.locator('a[href="https://doi.org/10.1145/3832098"]')).toHaveCount(1);
  // These fragments are linked from the README and other instructional pages.
  await expect(page.locator('#what-the-confidence-interval-does-and-does-not-mean')).toHaveCount(1);
  await expect(page.locator('#related-work-and-corrals-integration')).toHaveCount(1);
});
