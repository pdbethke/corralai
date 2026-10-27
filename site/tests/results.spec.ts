// SPDX-License-Identifier: Elastic-2.0
// The live results pages, /results/?repo=owner/name (A, one repository's
// report card) and /results/ (B, every listed ledger), driven in Chromium
// against the built site with GitHub replaced by fixtures through page.route.
//
// Fixtures (written by beforeAll into the gitignored tests/fixtures/ledgers/,
// under page-* names so results-loader.spec.ts in another worker never
// touches them):
// - two-entries: internal/ledger/testdata/two-entries, two verified scans that
//   graded nothing (every count 0, caught share null);
// - tampered: the same with one byte of one entry changed and re-gzipped;
// - empty: a ledger branch whose scans/ holds no entries;
// - real-prefix: the first 84 entries of corral's own public ledger (the
//   corral/ledger branch, read with git as scripts/gen-ledger-site.sh does),
//   ending at the second flask scan. A name-ordered prefix of a hash-linked
//   chain is itself an intact chain, so it verifies against the published
//   LEDGER_PUBKEY: 2 scans, 20 proven missing tests, 80 planted, telemetry.
import { test, expect, type Page } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
import zlib from 'node:zlib';
import { execFileSync } from 'node:child_process';
import { stripTypeScriptTypes } from 'node:module';

const SITE = path.join(import.meta.dirname, '..');
const ROOT = path.join(SITE, '..');
const FIXTURES = path.join(SITE, 'tests', 'fixtures', 'ledgers');
const SOURCE_FIXTURE = path.join(ROOT, 'internal', 'ledger', 'testdata', 'two-entries');
const GOOD = path.join(FIXTURES, 'page-two-entries');
const TAMPERED = path.join(FIXTURES, 'page-tampered');
const EMPTY = path.join(FIXTURES, 'page-empty');
const PREFIX = path.join(FIXTURES, 'page-real-prefix');
const PREFIX_LAST = '20260908T154158Z-36e4a824f340-d65faf854374.json.gz';
const ALLOWED_HOSTS = new Set(['localhost', 'api.github.com', 'raw.githubusercontent.com']);
const API_HEADERS = {
  'access-control-allow-origin': '*',
  'access-control-expose-headers': 'X-RateLimit-Limit, X-RateLimit-Remaining, X-RateLimit-Reset, X-RateLimit-Used',
  'content-type': 'application/json',
};
const RAW_HEADERS = { 'access-control-allow-origin': '*', 'content-type': 'application/octet-stream' };
// Words that would be commentary on a project rather than a measurement.
const TAUNTS = /\b(worst|fail|shame|bad)/i;

function ledgerRef(): string {
  for (const ref of ['origin/corral/ledger', 'corral/ledger']) {
    try {
      execFileSync('git', ['rev-parse', '--verify', '--quiet', ref], { cwd: ROOT, stdio: 'ignore' });
      return ref;
    } catch {
      /* try the next */
    }
  }
  throw new Error('no corral/ledger ref for the real-prefix fixture (git fetch origin corral/ledger)');
}

test.beforeAll(() => {
  for (const d of [GOOD, TAMPERED, EMPTY, PREFIX]) fs.rmSync(d, { recursive: true, force: true });
  fs.cpSync(SOURCE_FIXTURE, GOOD, { recursive: true });
  fs.cpSync(SOURCE_FIXTURE, TAMPERED, { recursive: true });
  const victim = fs.readdirSync(path.join(TAMPERED, 'scans')).sort()[0];
  const file = path.join(TAMPERED, 'scans', victim);
  const json = zlib.gunzipSync(fs.readFileSync(file)).toString('utf8');
  const tampered = json.replace('"Cores": 0', '"Cores": 1');
  if (tampered === json || tampered.length !== json.length) throw new Error('tamper did not change exactly one byte');
  fs.writeFileSync(file, zlib.gzipSync(Buffer.from(tampered, 'utf8')));
  fs.mkdirSync(path.join(EMPTY, 'scans'), { recursive: true });
  fs.copyFileSync(path.join(SOURCE_FIXTURE, 'rootpub.hex'), path.join(EMPTY, 'rootpub.hex'));

  const ref = ledgerRef();
  const names = execFileSync('git', ['ls-tree', '--name-only', `${ref}:scans`], { cwd: ROOT, encoding: 'utf8' })
    .split('\n')
    .filter(Boolean)
    .sort();
  const upto = names.indexOf(PREFIX_LAST);
  if (upto < 0) throw new Error(`${PREFIX_LAST} is not on ${ref}`);
  fs.mkdirSync(path.join(PREFIX, 'scans'), { recursive: true });
  for (const n of names.slice(0, upto + 1)) {
    fs.writeFileSync(path.join(PREFIX, 'scans', n), execFileSync('git', ['show', `${ref}:scans/${n}`], { cwd: ROOT, maxBuffer: 1 << 26 }));
  }
  fs.writeFileSync(path.join(PREFIX, 'rootpub.hex'), execFileSync('git', ['show', `${ref}:LEDGER_PUBKEY`], { cwd: ROOT }));
});

type Ledger = { dir: string } | { treesStatus: number };

// serve answers GitHub for every repo in `ledgers` from its fixture (or with
// a bare status for the trees call), and any other repo with a 404 (no
// ledger branch). It returns the list of requests to hosts outside the
// allowlist.
async function serve(page: Page, ledgers: Record<string, Ledger>, listings?: object) {
  const offsite: string[] = [];
  page.on('request', (req) => {
    if (!ALLOWED_HOSTS.has(new URL(req.url()).hostname)) offsite.push(req.url());
  });
  if (listings !== undefined) {
    await page.route('**/results/listings.json', (route) =>
      route.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(listings) }),
    );
  }
  await page.route('https://api.github.com/**', async (route) => {
    const m = /^\/repos\/([^/]+\/[^/]+)\/git\/trees\/corral%2Fledger$/.exec(new URL(route.request().url()).pathname);
    const l = m ? ledgers[m[1]] : undefined;
    if (!l) return route.fulfill({ status: 404, headers: API_HEADERS, body: '{"message":"Not Found"}' });
    if ('treesStatus' in l) return route.fulfill({ status: l.treesStatus, headers: API_HEADERS, body: '{}' });
    const tree = [
      { path: 'LEDGER_PUBKEY', type: 'blob' },
      ...fs.readdirSync(path.join(l.dir, 'scans')).map((f) => ({ path: `scans/${f}`, type: 'blob' })),
    ];
    return route.fulfill({ status: 200, headers: API_HEADERS, body: JSON.stringify({ sha: 'x', tree, truncated: false }) });
  });
  await page.route('https://raw.githubusercontent.com/**', async (route) => {
    const m = /^\/([^/]+\/[^/]+)\/corral\/ledger\/(.+)$/.exec(new URL(route.request().url()).pathname);
    const l = m ? ledgers[m[1]] : undefined;
    if (!m || !l || 'treesStatus' in l) return route.fulfill({ status: 404, headers: RAW_HEADERS, body: '404' });
    const rel = decodeURIComponent(m[2]);
    const f = rel === 'LEDGER_PUBKEY' ? path.join(l.dir, 'rootpub.hex') : path.join(l.dir, rel);
    if (!fs.existsSync(f)) return route.fulfill({ status: 404, headers: RAW_HEADERS, body: '404' });
    return route.fulfill({ status: 200, headers: RAW_HEADERS, body: fs.readFileSync(f) });
  });
  return offsite;
}

const listing = (repo: string) => ({ repo, listed_at: '2026-10-22', signing: 'ed25519' });
const tile = (page: Page, key: string) => page.locator(`[data-tile="${key}"] .tile-value`);
const loaded = (page: Page) => expect(page.locator('#results[data-state="done"]')).toBeVisible({ timeout: 30_000 });

test('A on the fixture shows the tiles from summary.totals and 2/2 entries verified', async ({ page }) => {
  const offsite = await serve(page, { 'o/r': { dir: GOOD } }, { listings: [] });
  await page.goto('/results/?repo=o/r');
  await loaded(page);
  // two-entries derives to two scans that graded nothing: the counts are 0,
  // and the caught share is null, which reads as nothing graded, never 0%.
  await expect(tile(page, 'proven')).toHaveText('0');
  await expect(tile(page, 'caught')).toHaveText('nothing graded');
  await expect(tile(page, 'verified')).toHaveText('2/2');
  await expect(tile(page, 'planted')).toHaveText('0');
  await expect(tile(page, 'files')).toHaveText('0');
  await expect(tile(page, 'telemetry')).toHaveText('no telemetry recorded');
  await expect(page.getByText('The per-run charts need a run that graded a file')).toBeVisible();
  // The entries table is collapsed and names each key.
  const entries = page.locator('details.entries');
  await expect(entries).not.toHaveAttribute('open', /.*/);
  await entries.locator('summary').click();
  await expect(entries.locator('tbody tr')).toHaveCount(2);
  await expect(entries.locator('tbody tr').first()).toContainText('scan');
  await expect(entries.locator('tbody tr').first()).toContainText('ed25519:f0b74a4534f38901');
  await expect(entries.locator('tbody tr').first()).toContainText('verified');
  expect(offsite).toEqual([]);
});

test('A on the real prefix draws runs, gaps with their proving tests, telemetry and reviews', async ({ page }) => {
  await serve(page, { 'pdbethke/corralai': { dir: PREFIX } }, { listings: [listing('pdbethke/corralai')] });
  await page.goto('/results/?repo=pdbethke/corralai');
  await loaded(page);
  await expect(tile(page, 'proven')).toHaveText('20');
  await expect(tile(page, 'caught')).toHaveText('72.5%');
  await expect(tile(page, 'verified')).toHaveText('84/84');
  await expect(tile(page, 'planted')).toHaveText('80');
  await expect(tile(page, 'telemetry')).toHaveText('53');
  await expect(tile(page, 'reviews')).toHaveText('24');
  await expect(page.locator('.listed-note')).toContainText('Listed 2026-10-22');
  await expect(page.locator('figure.viz')).toHaveCount(2);
  await expect(page.locator('.viz-legend li')).toHaveText(['survived the suite', 'proven missing tests']);
  // Hovering the run chart shows one tooltip with every series.
  const plot = page.locator('figure.viz').first().locator('svg.viz-svg');
  const box = (await plot.boundingBox())!;
  await plot.hover({ position: { x: box.width - 30, y: box.height / 2 } });
  await expect(page.locator('figure.viz').first().locator('.viz-tip')).toContainText('proven missing tests');
  // Runs are numbered among every run on the ledger, not among the graded ones.
  await expect(page.locator('figure.viz').first().locator('.viz-tip')).toContainText('Run 2 of 2');
  // The table twin carries every value.
  await page.locator('figure.viz').first().getByRole('button', { name: 'Show table' }).click();
  await expect(page.locator('figure.viz').first().locator('.viz-table tbody tr')).toHaveCount(2);
  // One row per file in a run; every proven missing test is listed by its
  // mutant id, and the rows add up to the tile.
  await expect(page.locator('table.gaps .mutant-id')).toHaveCount(20);
  const perRow = await page.locator('table.gaps tbody td.num').allInnerTexts();
  expect(perRow.reduce((a, x) => a + Number(x), 0)).toBe(20);
  await expect(page.getByRole('heading', { name: 'Proven missing tests (20, across 2 runs)' })).toBeVisible();
  // The headline sums runs, and says so: a file audited twice counts twice.
  await expect(page.locator('[data-tile="proven"] .tile-caption')).toHaveText(
    'planted bugs the suite let through across 2 runs, each then caught by a test corral wrote and ran; a file audited more than once counts each run',
  );
  const first = page.locator('table.gaps tbody tr').first().locator('details');
  await first.locator('summary').click();
  await expect(first.locator('pre')).toContainText('def test');
  await expect(page.getByText('No file has been graded by two different mutant models at the same commit yet')).toBeVisible();
});

test('A on the tampered copy names the failing entry and shows no number in any tile', async ({ page }) => {
  await serve(page, { 'o/r': { dir: TAMPERED } }, { listings: [] });
  await page.goto('/results/?repo=o/r');
  await loaded(page);
  await expect(page.locator('.problem')).toContainText('does not verify');
  const failing = page.locator('.problem li');
  await expect(failing).toHaveCount(1);
  await expect(failing.first()).toContainText('.json.gz');
  const tiles = page.locator('.tile');
  expect(await tiles.count()).toBeGreaterThan(0);
  for (const t of await tiles.allInnerTexts()) expect(t, `a tile shows a number: ${t}`).not.toMatch(/\d/);
  await expect(page.locator('figure.viz')).toHaveCount(0);
});

test('A for an unlisted repo says it is not listed and which key it verified against', async ({ page }) => {
  await serve(page, { 'o/r': { dir: GOOD } }, { listings: [listing('pdbethke/corralai')] });
  await page.goto('/results/?repo=o/r');
  await loaded(page);
  await expect(page.locator('.listed-note')).toHaveText('o/r is not listed; verified against the key this ledger publishes.');
});

test('A for an unlisted repo with no root key says the ledger publishes none', async ({ page }) => {
  await serve(page, { 'o/r': { dir: GOOD } }, { listings: [] });
  await page.route('https://raw.githubusercontent.com/o/r/corral/ledger/LEDGER_PUBKEY', (route) =>
    route.fulfill({ status: 404, headers: RAW_HEADERS, body: '404' }),
  );
  await page.goto('/results/?repo=o/r');
  await loaded(page);
  await expect(page.locator('.listed-note')).toContainText('o/r is not listed; this ledger publishes no root key');
});

test('A on a ledger with no entries says so, with no tiles and no 0/0', async ({ page }) => {
  await serve(page, { 'o/r': { dir: EMPTY } }, { listings: [] });
  await page.goto('/results/?repo=o/r');
  await loaded(page);
  await expect(page.getByText('This ledger has no entries yet.')).toBeVisible();
  await expect(page.locator('.tile')).toHaveCount(0);
  await expect(page.locator('#results')).not.toContainText('0/0');
});

test('every load error has its own sentence and renders no number', async ({ page }) => {
  const cases: [string, Record<string, Ledger>, RegExp][] = [
    ['o/none', {}, /has no corral\/ledger branch/],
    ['o/down', { 'o/down': { treesStatus: 500 } }, /answered HTTP 500/],
    ['not a repo', {}, /is not a repository name/],
  ];
  for (const [repo, ledgers, sentence] of cases) {
    await page.unrouteAll();
    await serve(page, ledgers, { listings: [] });
    await page.goto(`/results/?repo=${encodeURIComponent(repo)}`);
    await loaded(page);
    await expect(page.locator('.load-error')).toHaveText(sentence);
    await expect(page.locator('.tile')).toHaveCount(0);
  }
});

test('B with two listings, one answering 500, shows the good ledger and the other its own error', async ({ page }) => {
  const offsite = await serve(page, { 'o/r': { dir: GOOD }, 'o/down': { treesStatus: 500 } }, { listings: [listing('o/r'), listing('o/down')] });
  await page.goto('/results/');
  await loaded(page);
  const rows = page.locator('table.board tbody tr');
  await expect(rows).toHaveCount(2);
  await expect(page.locator('table.board tr[data-repo="o/r"]')).toContainText('2/2');
  const down = page.locator('table.board tr[data-repo="o/down"]');
  await expect(down).toContainText('answered HTTP 500');
  await expect(down).not.toContainText(/\d+\/\d+/);
  await expect(tile(page, 'repos')).toHaveText('1');
  expect(offsite).toEqual([]);
});

test('B sorts by ?sort=proven, and an unknown sort is an error, not a quiet default', async ({ page }) => {
  await serve(page, { 'o/r': { dir: GOOD }, 'pdbethke/corralai': { dir: PREFIX } }, { listings: [listing('o/r'), listing('pdbethke/corralai')] });
  await page.goto('/results/?sort=proven');
  await loaded(page);
  await expect(page.locator('table.board tbody tr')).toHaveCount(2);
  expect(await page.locator('table.board tbody tr').evaluateAll((rs) => rs.map((r) => r.getAttribute('data-repo')))).toEqual(['pdbethke/corralai', 'o/r']);
  await expect(page.locator('table.board th[aria-sort="descending"]')).toContainText('Proven missing tests');
  await expect(tile(page, 'proven')).toHaveText('20');
  await expect(page.locator('[data-tile="proven"] .tile-caption')).toHaveText(
    'added across the listed ledgers below, 4 runs in all; a file audited more than once counts each run',
  );
  await expect(tile(page, 'repos')).toHaveText('2');

  await page.goto('/results/?sort=name');
  await loaded(page);
  expect(await page.locator('table.board tbody tr').evaluateAll((rs) => rs.map((r) => r.getAttribute('data-repo')))).toEqual(['o/r', 'pdbethke/corralai']);

  await page.goto('/results/?sort=bogus');
  await loaded(page);
  await expect(page.locator('.sort-error')).toContainText('"bogus" is not a sort this page knows');
  await expect(page.locator('table.board')).toHaveCount(0);
});

test('B with no ?sort= orders by most recent run', async ({ page }) => {
  await serve(page, { 'o/r': { dir: GOOD }, 'pdbethke/corralai': { dir: PREFIX } }, { listings: [listing('o/r'), listing('pdbethke/corralai')] });
  await page.goto('/results/');
  await loaded(page);
  await expect(page.locator('table.board th[aria-sort="descending"]')).toContainText('Most recent run');
  // o/r recorded no finish time, so it sorts after the ledger that did.
  expect(await page.locator('table.board tbody tr').evaluateAll((rs) => rs.map((r) => r.getAttribute('data-repo')))).toEqual(['pdbethke/corralai', 'o/r']);
});

test('the lookup box goes to A', async ({ page }) => {
  await serve(page, { 'o/r': { dir: GOOD } }, { listings: [] });
  await page.goto('/results/');
  await loaded(page);
  await page.getByLabel('Repository').fill('o/r');
  await page.getByRole('button', { name: 'Look up' }).click();
  await expect(page).toHaveURL(/\/results\/\?repo=o%2Fr$/);
  await loaded(page);
  await expect(tile(page, 'verified')).toHaveText('2/2');
});

test('the shipped listings.json has the shape phase 5 will serve', () => {
  const body = JSON.parse(fs.readFileSync(path.join(SITE, 'public', 'results', 'listings.json'), 'utf8'));
  expect(body).toEqual({ listings: [{ repo: 'pdbethke/corralai', listed_at: '2026-10-22', signing: 'ed25519' }] });
});

for (const url of ['/results/?repo=pdbethke/corralai', '/results/']) {
  test(`${url} does not scroll sideways at 375px and carries no taunting word`, async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 800 });
    await serve(page, { 'pdbethke/corralai': { dir: PREFIX } }, { listings: [listing('pdbethke/corralai')] });
    await page.goto(url);
    await loaded(page);
    const [scrollW, clientW] = await page.evaluate(() => [document.documentElement.scrollWidth, document.documentElement.clientWidth]);
    expect(scrollW, 'the page scrolls sideways').toBeLessThanOrEqual(clientW);
    expect(await page.locator('main').innerText()).not.toMatch(TAUNTS);
  });
}

test('the nav carries Results beside Ledger, and Results is current on /results/', async ({ page }) => {
  await serve(page, {}, { listings: [] });
  await page.goto('/results/');
  const nav = page.locator('header.site-header nav.sh-nav');
  await expect(nav.getByRole('link', { name: 'Results', exact: true })).toHaveAttribute('aria-current', 'page');
  await expect(nav.getByRole('link', { name: 'Ledger', exact: true })).toBeVisible();
});

// Adding "Results" must not grow the header on a phone: at 375px the nav
// stays the height the seven-link nav had. The baseline is measured in the
// same browser (so the same fonts): the Results link removed and the pills
// put back to their wider pre-change spacing (gap 4px, padding 6px 10px).
for (const url of ['/', '/results/']) {
  test(`at 375px the header nav on ${url} is no taller than it was before Results was added`, async ({ page }) => {
    await page.setViewportSize({ width: 375, height: 800 });
    await serve(page, {}, { listings: [] });
    await page.goto(url);
    const nav = page.locator('header.site-header nav.sh-nav');
    const now = (await nav.boundingBox())!.height;
    const before = await nav.evaluate((n) => {
      (n as HTMLElement).style.gap = '4px';
      for (const a of n.querySelectorAll('a')) (a as HTMLElement).style.padding = '6px 10px';
      [...n.querySelectorAll('a')].find((a) => a.textContent === 'Results')!.remove();
      return n.getBoundingClientRect().height;
    });
    expect(now, `nav is ${now}px, was ${before}px`).toBe(before);
  });
}

// sourceURL builds a github.com link from a ledger's own strings (a gap's
// repo, commit and path), so a path that walks out of the blob ("..") or a
// repo name of "." or ".." gets no link at all. src/lib/links.ts has no
// imports, so it is loaded here with its types stripped, in Node.
test('sourceURL refuses paths and repo names that would leave the audited file', async () => {
  const src = stripTypeScriptTypes(fs.readFileSync(path.join(SITE, 'src', 'lib', 'links.ts'), 'utf8'));
  const { sourceURL, shortRepo } = await import(`data:text/javascript,${encodeURIComponent(src)}`);
  const sha = '36e4a824f340dc3d8a1de7d3fd2c3b4c1c4f5e6a';
  expect(sourceURL('https://github.com/pallets/flask', sha, 'src/flask/app.py')).toBe(`https://github.com/pallets/flask/blob/${sha}/src/flask/app.py`);
  expect(sourceURL('pallets/flask', sha, 'a b/c#d.py')).toBe(`https://github.com/pallets/flask/blob/${sha}/a%20b/c%23d.py`);
  for (const p of ['../../../other/repo/blob/x/y', 'src/../../x', './a', 'a//b', '/abs', 'a/', '', 'a/./b']) {
    expect(sourceURL('pallets/flask', sha, p), p).toBeNull();
  }
  for (const r of ['pallets/..', 'pallets/.', 'https://github.com/pallets/..', 'https://gitlab.com/a/b', 'a/b/c']) {
    expect(sourceURL(r, sha, 'x.py'), r).toBeNull();
  }
  expect(sourceURL('pallets/flask', 'main', 'x.py')).toBeNull();
  expect(shortRepo('https://github.com/pallets/flask.git')).toBe('pallets/flask');
});
