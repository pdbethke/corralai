// SPDX-License-Identifier: Elastic-2.0
// The entries table of /results/ renders what Derive says about each entry's
// signature level: its sentence, its colour, and for a keyless entry the
// workflow and trigger. A keyless level cannot be produced in the browser
// without a real Fulcio signature, so this feeds the page's own renderer
// (entriesTable, from src/lib/results-view.ts, loaded as the page loads it)
// synthetic derived entries. No signature is faked: the renderer never
// verifies, it prints what Derive carried.
import { test, expect } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
import { stripTypeScriptTypes } from 'node:module';

const SITE = path.join(import.meta.dirname, '..');
const HARNESS = 'http://localhost:4321/__results-entries-harness';

const SENTENCE: Record<string, string> = {
  'keyless-unpinned': 'signed keylessly by a GitHub Actions workflow of this repository, on the ref it names; any branch could have signed, because no workflow is pinned',
  'keyless-pinned-unchecked': "signed keylessly by the workflow and ref this ledger's pin names; the pin was not checked against a root key you named",
  'keyless-pinned': "signed keylessly by the workflow and ref this ledger's root pinned",
  key: "signed by a key this ledger's root holds or registered",
};

const entry = (level: string, extra: object = {}) => ({
  file: `${level}.json`, hash: 'h', kind: '', keyid: 'sigstore', status: 'verified', level, level_sentence: SENTENCE[level], ...extra,
});

async function render(page: import('@playwright/test').Page, entries: object[]) {
  await page.route(HARNESS, (r) => r.fulfill({ status: 200, contentType: 'text/html', body: '<!doctype html><meta charset="utf-8"><title>harness</title><div id="root"></div>' }));
  await page.goto(HARNESS);
  // The renderer's imports (loader, charts, links) are not what is under
  // test: drop them, keep the module.
  const src = stripTypeScriptTypes(fs.readFileSync(path.join(SITE, 'src', 'lib', 'results-view.ts'), 'utf8')).replace(/^import .* from '\.\/[a-z-]+';$/gm, '');
  await page.addScriptTag({ type: 'module', content: `${src}\nwindow.__entriesTable = entriesTable;` });
  await page.waitForFunction(() => (window as any).__entriesTable !== undefined);
  await page.evaluate((es) => {
    const d = { entries: es, summary: { totals: { entries_verified: es.length, entries_unsigned: 0 } } };
    const t = (window as any).__entriesTable(d);
    t.open = true;
    document.getElementById('root')!.appendChild(t);
  }, entries);
}

test('keyless-unpinned and keyless-pinned-unchecked are warns carrying their sentence, workflow and trigger', async ({ page }) => {
  const wf = 'https://github.com/o/r/.github/workflows/corral.yml@refs/heads/attacker';
  await render(page, [
    entry('keyless-unpinned', { workflow: wf, trigger: 'push' }),
    entry('keyless-pinned-unchecked', { workflow: wf, trigger: 'schedule' }),
    entry('keyless-pinned', { workflow: wf }),
    entry('key', { keyid: 'ed25519:abc' }),
  ]);
  const rows = page.locator('details.entries tbody tr');
  await expect(rows).toHaveCount(4);
  for (const [i, level] of ['keyless-unpinned', 'keyless-pinned-unchecked'].entries()) {
    const lv = rows.nth(i).locator('.entry-level');
    await expect(lv).toHaveText(SENTENCE[level]);
    await expect(lv).toHaveClass(new RegExp(`level-${level}`));
  }
  await expect(rows.nth(0).locator('.entry-level')).toContainText('no workflow is pinned');
  await expect(rows.nth(0).locator('.entry-workflow')).toHaveText(`workflow ${wf}, trigger push`);
  await expect(rows.nth(1).locator('.entry-workflow')).toHaveText(`workflow ${wf}, trigger schedule`);
  await expect(rows.nth(2).locator('.entry-workflow')).toHaveText(`workflow ${wf}`);
  await expect(rows.nth(3).locator('.entry-workflow')).toHaveCount(0);
  await expect(rows.nth(3).locator('.entry-level')).toHaveText(SENTENCE.key);
});

test('a workflow string is shown as text, never as markup', async ({ page }) => {
  await render(page, [entry('keyless-unpinned', { workflow: '<img src=x onerror=alert(1)>', trigger: '<b>t</b>' })]);
  await expect(page.locator('details.entries tbody tr .entry-workflow')).toHaveText('workflow <img src=x onerror=alert(1)>, trigger <b>t</b>');
  await expect(page.locator('details.entries img, details.entries b')).toHaveCount(0);
});

// The harness has no page stylesheet, so the colour is asserted where it is
// defined: both weak levels share the amber rule, and the strong ones do not.
test('the page draws keyless-unpinned and keyless-pinned-unchecked amber, and no strong level', () => {
  const css = fs.readFileSync(path.join(SITE, 'src', 'pages', 'results', 'index.astro'), 'utf8');
  const rule = css.split('\n').find((l) => l.includes('var(--amber)') && l.includes('.level-'))!;
  expect(rule).toContain('.level-keyless-unpinned');
  expect(rule).toContain('.level-keyless-pinned-unchecked');
  expect(rule).not.toMatch(/\.level-(keyless-pinned|key)[,\s{]/);
});
