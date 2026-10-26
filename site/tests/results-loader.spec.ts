// SPDX-License-Identifier: Elastic-2.0
// The results page's ledger loader (src/lib/ledger.ts), driven in a real
// Chromium against the built site's /results/wasm/, with GitHub replaced by
// fixtures through page.route. Every way a load can fail is its own kind, and
// none of them is a derive over a partial ledger.
//
// The loader is put into the page by this test, not by a page the site ships:
// its source is stripped of types (node:module stripTypeScriptTypes, so no
// bundler dependency) and added as a module script to a blank page served on
// the site's own origin, so /results/wasm/ resolves exactly as it will for the
// real results page. Nothing here is linked or deployed.
import { test, expect, type Page, type Route } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
import zlib from 'node:zlib';
import { stripTypeScriptTypes } from 'node:module';

const SITE = path.join(import.meta.dirname, '..');
const SOURCE_FIXTURE = path.join(SITE, '..', 'internal', 'ledger', 'testdata', 'two-entries');
const FIXTURES = path.join(SITE, 'tests', 'fixtures', 'ledgers');
const GOOD = path.join(FIXTURES, 'two-entries');
const TAMPERED = path.join(FIXTURES, 'tampered');
const REPO = 'o/r';
const HARNESS = 'http://localhost:4321/__results-loader-harness';
const ALLOWED_HOSTS = new Set(['localhost', 'api.github.com', 'raw.githubusercontent.com']);
// What api.github.com exposes to a cross-origin reader, so the fixture's
// rate-limit headers are readable exactly as the real ones are.
const API_HEADERS = {
  'access-control-allow-origin': '*',
  'access-control-expose-headers': 'X-RateLimit-Limit, X-RateLimit-Remaining, X-RateLimit-Reset, X-RateLimit-Used',
  'content-type': 'application/json',
};
const RAW_HEADERS = { 'access-control-allow-origin': '*', 'content-type': 'application/octet-stream' };

// The test setup copies the Go fixture and makes a copy with one byte of one
// entry's JSON changed ("Cores": 0 -> 1), re-gzipped so the entry still
// parses and the change is caught by the hash, not by the gzip reader.
test.beforeAll(() => {
  fs.rmSync(FIXTURES, { recursive: true, force: true });
  fs.cpSync(SOURCE_FIXTURE, GOOD, { recursive: true });
  fs.cpSync(SOURCE_FIXTURE, TAMPERED, { recursive: true });
  const victim = fs.readdirSync(path.join(TAMPERED, 'scans')).sort()[0];
  const file = path.join(TAMPERED, 'scans', victim);
  const json = zlib.gunzipSync(fs.readFileSync(file)).toString('utf8');
  const tampered = json.replace('"Cores": 0', '"Cores": 1');
  if (tampered === json || tampered.length !== json.length) throw new Error('tamper did not change exactly one byte');
  fs.writeFileSync(file, zlib.gzipSync(Buffer.from(tampered, 'utf8')));
});

const scanFiles = (dir: string) => fs.readdirSync(path.join(dir, 'scans')).sort();
const rawURL = (p: string) => `https://raw.githubusercontent.com/${REPO}/corral/ledger/${p}`;

type Serve = {
  dir: string;
  trees?: (route: Route) => Promise<void> | void;
  raw?: (route: Route, p: string) => Promise<void> | void;
  extraScans?: string[];
};

// serveLedger answers the trees API and raw reads from a fixture directory,
// unless a test overrides one of them. The listing carries a non-scan blob,
// a nested blob and a tree entry, which the loader must ignore.
async function serveLedger(page: Page, s: Serve) {
  const scans = [...scanFiles(s.dir), ...(s.extraScans ?? [])];
  await page.route('https://api.github.com/**', async (route) => {
    if (s.trees) return s.trees(route);
    const url = new URL(route.request().url());
    expect(url.pathname).toBe(`/repos/${REPO}/git/trees/corral%2Fledger`);
    expect(url.searchParams.get('recursive')).toBe('1');
    const tree = [
      { path: 'README.md', type: 'blob' },
      { path: 'LEDGER_PUBKEY', type: 'blob' },
      { path: 'scans', type: 'tree' },
      { path: 'scans/nested/x.json', type: 'blob' },
      { path: 'scans/.hidden.json', type: 'blob' },
      { path: 'scans/notes.txt', type: 'blob' },
      ...scans.map((f) => ({ path: `scans/${f}`, type: 'blob' })),
    ];
    await route.fulfill({ status: 200, headers: API_HEADERS, body: JSON.stringify({ sha: 'x', tree, truncated: false }) });
  });
  await page.route('https://raw.githubusercontent.com/**', async (route) => {
    const prefix = `/${REPO}/corral/ledger/`;
    const p = new URL(route.request().url()).pathname;
    expect(p.startsWith(prefix), p).toBe(true);
    const rel = decodeURIComponent(p.slice(prefix.length));
    if (s.raw) return s.raw(route, rel);
    if (rel === 'LEDGER_PUBKEY') {
      return route.fulfill({ status: 200, headers: RAW_HEADERS, body: fs.readFileSync(path.join(s.dir, 'rootpub.hex')) });
    }
    const f = path.join(s.dir, rel);
    if (!rel.startsWith('scans/') || !fs.existsSync(f)) return route.fulfill({ status: 404, headers: RAW_HEADERS, body: '404' });
    return route.fulfill({ status: 200, headers: RAW_HEADERS, body: fs.readFileSync(f) });
  });
}

// harness opens a blank page on the site's origin with the loader on window.
async function harness(page: Page) {
  const offsite: string[] = [];
  page.on('request', (req) => {
    const host = new URL(req.url()).hostname;
    if (!ALLOWED_HOSTS.has(host)) offsite.push(req.url());
  });
  await page.route(HARNESS, (route) =>
    route.fulfill({ status: 200, contentType: 'text/html', body: '<!doctype html><meta charset="utf-8"><title>harness</title>' }),
  );
  await page.goto(HARNESS);
  const src = stripTypeScriptTypes(fs.readFileSync(path.join(SITE, 'src', 'lib', 'ledger.ts'), 'utf8'));
  await page.addScriptTag({ type: 'module', content: `${src}\nwindow.__ledger = { loadLedger, parseRepo };` });
  await page.waitForFunction(() => (window as any).__ledger !== undefined);
  return offsite;
}

const load = (page: Page, repo = REPO) =>
  page.evaluate((r) => (window as any).__ledger.loadLedger(r, window.fetch.bind(window)), repo);

test('the two-entries fixture derives, both entries verified, two scans in the summary', async ({ page }) => {
  await serveLedger(page, { dir: GOOD });
  const offsite = await harness(page);
  const r = await load(page);
  expect(r.kind).toBe('derived');
  expect(r.repo).toBe(REPO);
  expect(r.rootPubSource).toBe('LEDGER_PUBKEY');
  expect(r.derived.format).toBe('corral-derive-1');
  expect(r.derived.problem).toBeUndefined();
  expect(r.derived.summary.totals.scans).toBe(2);
  expect(r.derived.entries).toHaveLength(2);
  for (const e of r.derived.entries) expect(e.status).toBe('verified');
  expect(offsite).toEqual([]);
});

test('a one-byte-tampered entry derives with a problem and every total empty', async ({ page }) => {
  await serveLedger(page, { dir: TAMPERED });
  await harness(page);
  const r = await load(page);
  expect(r.kind).toBe('derived');
  expect(r.derived.problem).toBeTruthy();
  expect(r.derived.entries.some((e: any) => e.status === 'failed')).toBe(true);
  const { totals, runs, gaps, models } = r.derived.summary;
  expect(totals).toEqual({
    scans: 0, files_audited: 0, mutants_planted: 0, survivors: 0, proven_missed: 0, could_not_grade: 0,
    caught_share: null, telemetry: null, last_run: null,
  });
  expect([runs, gaps, models, r.derived.scans, r.derived.files]).toEqual([[], [], [], [], []]);
});

test('no LEDGER_PUBKEY on the branch: rootPub is absent and signed entries fail, naming the missing key', async ({ page }) => {
  await serveLedger(page, {
    dir: GOOD,
    raw: (route, p) =>
      p === 'LEDGER_PUBKEY'
        ? route.fulfill({ status: 404, headers: RAW_HEADERS, body: '404: Not Found' })
        : route.fulfill({ status: 200, headers: RAW_HEADERS, body: fs.readFileSync(path.join(GOOD, p)) }),
  });
  await harness(page);
  const r = await load(page);
  expect(r.kind).toBe('derived');
  expect(r.rootPubSource).toBe('absent');
  expect(r.derived.problem).toBeTruthy();
  for (const e of r.derived.entries) {
    expect(e.status).toBe('failed');
    expect(e.problem).toContain('no root key');
  }
});

// Review Focus 1.
test('a repository with no corral/ledger branch is no-ledger, not zeros', async ({ page }) => {
  await serveLedger(page, {
    dir: GOOD,
    trees: (route) => route.fulfill({ status: 404, headers: API_HEADERS, body: '{"message":"Not Found"}' }),
  });
  await harness(page);
  expect(await load(page)).toEqual({ kind: 'no-ledger', repo: REPO });
});

// Review Focus 2.
for (const status of [403, 429]) {
  test(`GitHub's rate limit (${status}, remaining 0) is rate-limited with the reset time`, async ({ page }) => {
    const reset = 1793000000;
    await serveLedger(page, {
      dir: GOOD,
      trees: (route) =>
        route.fulfill({
          status,
          headers: { ...API_HEADERS, 'x-ratelimit-remaining': '0', 'x-ratelimit-reset': String(reset) },
          body: '{"message":"API rate limit exceeded"}',
        }),
    });
    await harness(page);
    const r = await load(page);
    expect(r.kind).toBe('rate-limited');
    expect(r.repo).toBe(REPO);
    expect(r.resetAt).toBeInstanceOf(Date);
    expect(r.resetAt.getTime()).toBe(reset * 1000);
  });
}

test('a 403 that is not the rate limit is fetch-failed, naming the URL', async ({ page }) => {
  await serveLedger(page, {
    dir: GOOD,
    trees: (route) =>
      route.fulfill({ status: 403, headers: { ...API_HEADERS, 'x-ratelimit-remaining': '42' }, body: '{"message":"Forbidden"}' }),
  });
  await harness(page);
  expect(await load(page)).toEqual({
    kind: 'fetch-failed', repo: REPO, status: 403,
    url: `https://api.github.com/repos/${REPO}/git/trees/corral%2Fledger?recursive=1`,
  });
});

// Review Focus 3.
test('a truncated trees listing is too-large, never a derive over part of it', async ({ page }) => {
  let rawReads = 0;
  await serveLedger(page, {
    dir: GOOD,
    trees: (route) =>
      route.fulfill({ status: 200, headers: API_HEADERS, body: JSON.stringify({ sha: 'x', tree: [], truncated: true }) }),
    raw: (route) => {
      rawReads++;
      return route.fulfill({ status: 500, headers: RAW_HEADERS, body: '' });
    },
  });
  await harness(page);
  expect(await load(page)).toEqual({ kind: 'too-large', repo: REPO });
  expect(rawReads).toBe(0);
});

test('one entry returning 500 is fetch-failed naming that URL, with no partial derive', async ({ page }) => {
  const broken = scanFiles(GOOD)[1];
  await serveLedger(page, {
    dir: GOOD,
    raw: (route, p) => {
      if (p === `scans/${broken}`) return route.fulfill({ status: 500, headers: RAW_HEADERS, body: 'boom' });
      const body = p === 'LEDGER_PUBKEY' ? fs.readFileSync(path.join(GOOD, 'rootpub.hex')) : fs.readFileSync(path.join(GOOD, p));
      return route.fulfill({ status: 200, headers: RAW_HEADERS, body });
    },
  });
  await harness(page);
  expect(await load(page)).toEqual({ kind: 'fetch-failed', repo: REPO, url: rawURL(`scans/${broken}`), status: 500 });
});

test('an entry the network drops is fetch-failed with status network', async ({ page }) => {
  const broken = scanFiles(GOOD)[0];
  await serveLedger(page, {
    dir: GOOD,
    raw: (route, p) => {
      if (p === `scans/${broken}`) return route.abort('failed');
      const body = p === 'LEDGER_PUBKEY' ? fs.readFileSync(path.join(GOOD, 'rootpub.hex')) : fs.readFileSync(path.join(GOOD, p));
      return route.fulfill({ status: 200, headers: RAW_HEADERS, body });
    },
  });
  await harness(page);
  expect(await load(page)).toEqual({ kind: 'fetch-failed', repo: REPO, url: rawURL(`scans/${broken}`), status: 'network' });
});

test('a LEDGER_PUBKEY read that fails other than 404 is fetch-failed', async ({ page }) => {
  await serveLedger(page, {
    dir: GOOD,
    raw: (route, p) =>
      p === 'LEDGER_PUBKEY'
        ? route.fulfill({ status: 503, headers: RAW_HEADERS, body: '' })
        : route.fulfill({ status: 200, headers: RAW_HEADERS, body: fs.readFileSync(path.join(GOOD, p)) }),
  });
  await harness(page);
  expect(await load(page)).toEqual({ kind: 'fetch-failed', repo: REPO, url: rawURL('LEDGER_PUBKEY'), status: 503 });
});

test('entries are read at most 8 at a time', async ({ page }) => {
  const extra = Array.from({ length: 20 }, (_, i) => `2026${String(i).padStart(4, '0')}-extra.json`);
  let inFlight = 0;
  let peak = 0;
  await serveLedger(page, {
    dir: GOOD,
    extraScans: extra,
    raw: async (route, p) => {
      if (p === 'LEDGER_PUBKEY') {
        return route.fulfill({ status: 200, headers: RAW_HEADERS, body: fs.readFileSync(path.join(GOOD, 'rootpub.hex')) });
      }
      inFlight++;
      peak = Math.max(peak, inFlight);
      await new Promise((r) => setTimeout(r, 50));
      inFlight--;
      const f = path.join(GOOD, p);
      return route.fulfill({ status: 200, headers: RAW_HEADERS, body: fs.existsSync(f) ? fs.readFileSync(f) : '{}' });
    },
  });
  await harness(page);
  const r = await load(page);
  // The extra entries are not ledger entries, so the ledger has a problem;
  // what this test measures is how they were read.
  expect(r.kind).toBe('derived');
  expect(r.derived.problem).toBeTruthy();
  expect(peak).toBeGreaterThan(1);
  expect(peak).toBeLessThanOrEqual(8);
});

test('parseRepo accepts owner/name and refuses anything else', async ({ page }) => {
  await harness(page);
  const got = await page.evaluate(() => {
    const { parseRepo } = (window as any).__ledger;
    return ['pdbethke/corralai', 'a-b/c.d_e', '../etc', 'a/b/c', 'a', '', 'a/', '/b', 'a b/c', 'a/b?x=1', 'a/..', 'a/.'].map(parseRepo);
  });
  expect(got).toEqual(['pdbethke/corralai', 'a-b/c.d_e', null, null, null, null, null, null, null, null, null, null]);
});

test('a malformed repo is bad-repo and fetches nothing', async ({ page }) => {
  let fetched = 0;
  await page.route(/https:\/\/(api\.github\.com|raw\.githubusercontent\.com)\//, (route) => {
    fetched++;
    return route.abort();
  });
  await harness(page);
  expect(await load(page, '../etc')).toEqual({ kind: 'bad-repo', input: '../etc' });
  expect(fetched).toBe(0);
});
