// SPDX-License-Identifier: Elastic-2.0
// Renders /results/: A (?repo=owner/name, one repository's report card) and B
// (no repo, every listed ledger on one page).
//
// Every figure drawn here is a field of Derived.summary, as ledger.Derive
// computed it in this browser (ledger.ts), entry counts included
// (Totals.entries_*). This module formats, sorts and lays out; it never sums
// rows, counts entries or decides which entries count. It does pick entries
// to LIST (the failed ones under a ledger's problem), and groups gaps by run
// and file for display. The one arithmetic it does is B's totals across
// ledgers, which adds counts each ledger's Summary already gave (a
// presentation of several records side by side); shares are never averaged
// across ledgers.
//
// A could-not-load or could-not-verify state never renders as a number: each
// LedgerResult kind has its own sentence, and a ledger with a problem shows
// its tiles with no value in them.
//
// Copy states what was measured. It never comments on a project.

import { loadLedger, parseRepo, type Derived, type Gap, type LedgerResult, type ModelLine, type Summary } from './ledger';
import { barChart, lineChart } from './charts';
import { shortRepo, sourceURL } from './links';

export type Listing = { repo: string; listed_at: string; signing: string };
type Listings = { ok: true; listings: Listing[] } | { ok: false; message: string };

export const SORTS = ['proven', 'caught', 'planted', 'recent', 'name'] as const;
export type SortKey = (typeof SORTS)[number];
const SORT_LABEL: Record<SortKey, string> = {
  proven: 'Proven missing tests',
  caught: 'Share caught',
  planted: 'Mutants planted',
  recent: 'Most recent run',
  name: 'Repository',
};

// ---- DOM helpers. Every string from a ledger goes in through textContent.

function el<K extends keyof HTMLElementTagNameMap>(tag: K, cls?: string, text?: string): HTMLElementTagNameMap[K] {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

function link(href: string, text: string, cls?: string): HTMLAnchorElement {
  const a = el('a', cls, text);
  a.href = href;
  return a;
}

function para(cls: string, ...parts: (string | Node)[]): HTMLParagraphElement {
  const p = el('p', cls);
  p.append(...parts);
  return p;
}

// ---- Formatting (presentation only).

const count = (n: number) => n.toLocaleString('en-US');
// Totals.scans is the run count: one ledger scan entry is one run.
const runs = (n: number) => `${count(n)} run${n === 1 ? '' : 's'}`;
export const pct = (v: number) => `${(Math.round(v * 1000) / 10).toFixed(1)}%`;

function compact(n: number): string {
  if (n >= 1e9) return `${(n / 1e9).toFixed(1)}B`;
  if (n >= 1e6) return `${(n / 1e6).toFixed(1)}M`;
  if (n >= 1e4) return `${(n / 1e3).toFixed(0)}K`;
  return count(n);
}

const day = (iso: string) =>
  new Date(iso).toLocaleDateString('en-GB', { timeZone: 'UTC', day: 'numeric', month: 'short', year: 'numeric' });
const stamp = (iso: string) =>
  `${day(iso)}, ${new Date(iso).toLocaleTimeString('en-GB', { timeZone: 'UTC', hour: '2-digit', minute: '2-digit' })} UTC`;

const shortSha = (c: string) => (/^[0-9a-f]{12,}$/.test(c) ? c.slice(0, 12) : c);

// ---- What each failure says. None carries a number but a status code or a
// time, and none is phrased as a verdict about a project.

export function errorSentence(r: Exclude<LedgerResult, { kind: 'derived' }>): string {
  switch (r.kind) {
    case 'bad-repo':
      return `"${r.input}" is not a repository name. Give it as owner/name.`;
    case 'no-ledger':
      return `${r.repo} has no corral/ledger branch, so there is no record to show.`;
    case 'rate-limited':
      return `GitHub's limit on unauthenticated requests from this address was reached. It resets at ${r.resetAt.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })} your time; reload after that.`;
    case 'too-large':
      return `${r.repo}'s ledger lists more files than GitHub returns in one response, so it was not loaded: part of a ledger is not the ledger.`;
    case 'fetch-failed':
      return r.status === 'network'
        ? `Reading ${r.url} did not complete over the network, so the ledger was not derived.`
        : `${r.url} answered HTTP ${r.status}, so the ledger was not derived.`;
    case 'bad-response':
      return `GitHub's answer for ${r.url} was not a file listing (${r.message}), so the ledger was not derived.`;
    case 'derive-failed':
      return `The verifier could not run in this browser (${r.message}). This says nothing about the ledger itself.`;
    case 'bad-root-key':
      return `The root key this ledger publishes, ${r.url}, could not be used: ${r.message}. No entry was checked.`;
  }
}

// ---- The tiles.

type Tile = { key: string; label: string; value: string; caption?: string; hero?: boolean };

function tiles(list: Tile[]): HTMLElement {
  const row = el('div', 'tiles');
  for (const t of list) {
    const d = el('div', t.hero ? 'tile tile-hero' : 'tile');
    d.dataset.tile = t.key;
    // A value that is a phrase (nothing graded, not shown) is set smaller than a number.
    d.append(el('div', 'tile-label', t.label), el('div', /^[\d.,%/]+$/.test(t.value) ? 'tile-value' : 'tile-value tile-phrase', t.value));
    if (t.caption) d.appendChild(el('div', 'tile-caption', t.caption));
    row.appendChild(d);
  }
  return row;
}

function telemetryTile(s: Summary): Tile {
  const t = s.totals.telemetry;
  if (t === null) return { key: 'telemetry', label: 'Model calls', value: 'no telemetry recorded', caption: 'these scans predate token telemetry' };
  const floor = t.unmetered_calls > 0;
  const parts = [
    `${floor ? 'at least ' : ''}${compact(t.input_tokens)} input and ${compact(t.output_tokens)} output tokens`,
    `from ${count(t.scans_with_telemetry)} of ${count(s.totals.scans)} scans`,
  ];
  if (floor) parts.push(`${count(t.unmetered_calls)} calls reported no tokens, so the token totals are a floor`);
  return { key: 'telemetry', label: 'Model calls', value: count(t.model_calls), caption: parts.join('; ') };
}

// The entry counts are Derive's (Totals.entries_*); n is the list's length.
function verifiedCounts(d: Derived) {
  const t = d.summary.totals;
  return { v: t.entries_verified, u: t.entries_unsigned, n: d.entries.length };
}

function aTiles(d: Derived): HTMLElement {
  const s = d.summary;
  const t = s.totals;
  const { v, u, n } = verifiedCounts(d);
  return tiles([
    { key: 'proven', label: 'Proven missing tests', value: count(t.proven_missed), caption: `planted bugs the suite let through across ${runs(t.scans)}, each then caught by a test corral wrote and ran; a file audited more than once counts each run`, hero: true },
    {
      key: 'caught',
      label: 'Share of graded planted bugs the suite caught',
      value: t.caught_share === null ? 'nothing graded' : pct(t.caught_share),
      caption:
        t.caught_share === null
          ? 'no audited file was graded'
          : `of ${count(t.mutants_graded)} graded planted bugs; ${count(t.mutants_planted)} were planted, and any that did not compile are in no share`,
    },
    { key: 'verified', label: 'Entries verified', value: `${v}/${n}`, caption: u > 0 ? `${count(u)} unsigned, counted in no figure` : 'intact and signed by the root key named above, checked in this browser' },
    { key: 'planted', label: 'Mutants planted', value: count(t.mutants_planted), caption: `${count(t.survivors)} survived the suite` },
    {
      key: 'files',
      label: 'Files audited',
      value: count(t.files_audited),
      caption: `in ${count(t.scans)} scan${t.scans === 1 ? '' : 's'}${t.could_not_grade > 0 ? `; ${count(t.could_not_grade)} could not be graded and are in no share` : ''}`,
    },
    telemetryTile(s),
    { key: 'reviews', label: 'Review entries', value: count(d.reviews), caption: 'adversarial reviews recorded on this ledger' },
    { key: 'last', label: 'Most recent run', value: t.last_run === null ? 'no finish time recorded' : day(t.last_run), caption: t.last_run === null ? undefined : `${new Date(t.last_run).toLocaleTimeString('en-GB', { timeZone: 'UTC', hour: '2-digit', minute: '2-digit' })} UTC` },
  ]);
}

const plural = (n: number, one: string, many: string) => `${count(n)} ${n === 1 ? one : many}`;

// The tiles of a ledger with no figure to show: the same labels, a phrase for
// a value. The entries tile keeps Derive's counts (Totals.entries_*), which
// stay true when nothing else can be shown.
function phraseTiles(d: Derived, value: string, caption: string, verifiedCaption: string): HTMLElement {
  const { v, n } = verifiedCounts(d);
  return tiles([
    { key: 'proven', label: 'Proven missing tests', value, caption, hero: true },
    { key: 'caught', label: 'Share of graded planted bugs the suite caught', value, caption },
    { key: 'verified', label: 'Entries verified', value: `${v}/${n}`, caption: verifiedCaption },
    { key: 'planted', label: 'Mutants planted', value, caption },
  ]);
}

// A ledger that did not verify: a figure from a chain that is not intact
// would be a number nobody verified.
const problemTiles = (d: Derived) =>
  phraseTiles(
    d,
    'not shown',
    'the ledger did not verify',
    `${plural(d.summary.totals.entries_failed, 'entry', 'entries')} did not verify, so the chain is not intact; no other figure is shown`,
  );

// A ledger that verified but holds no verified scan (every entry unsigned, or
// only reviews): its counts would be zeros nobody measured.
const noRunTiles = (d: Derived) =>
  phraseTiles(
    d,
    'no verified run',
    'no scan entry on this ledger verified',
    d.summary.totals.entries_unsigned > 0
      ? `${plural(d.summary.totals.entries_unsigned, 'unsigned entry', 'unsigned entries')}; an unsigned entry counts in no figure`
      : 'no entry here is a scan',
  );

// ---- Sections.

function section(title: string, ...body: Node[]): HTMLElement {
  const s = el('section', 'block');
  s.appendChild(el('h2', undefined, title));
  s.append(...body);
  return s;
}

function runCharts(s: Summary): HTMLElement {
  // A run that graded nothing has a null caught share and counts that are 0
  // by definition, not by measurement: it is plotted nowhere and counted here.
  const graded = s.runs.filter((r) => r.caught_share !== null);
  const skipped = s.runs.length - graded.length;
  const out = el('div', 'charts');
  if (graded.length === 0) {
    out.appendChild(para('muted', 'The per-run charts need a run that graded a file, and no run on this ledger has graded one yet.'));
    return out;
  }
  const xLabels = graded.map((r) => (r.finished_at === null ? 'no finish time' : day(r.finished_at)));
  // "Run N of M" numbers a run among every run on the ledger, graded or not,
  // so the numbers match the ledger and do not shift when a run is left out.
  const xDetail = graded.map(
    (r) => `Run ${s.runs.indexOf(r) + 1} of ${s.runs.length} · ${shortRepo(r.repo)} · ${r.finished_at === null ? 'no finish time recorded' : stamp(r.finished_at)}`,
  );
  const skippedNote = skipped > 0 ? ` ${count(skipped)} run${skipped === 1 ? '' : 's'} that graded no file ${skipped === 1 ? 'is' : 'are'} left out.` : '';
  out.appendChild(
    lineChart({
      title: 'Survivors and proven missing tests, per run',
      note: `Each point is one run, in the order the runs finished. A survivor is a planted bug the suite did not catch; a proven missing test is a survivor corral then caught with a test it wrote and ran.${skippedNote}`,
      xLabels,
      xDetail,
      xHeading: 'Run',
      series: [
        { name: 'survived the suite', color: 'var(--viz-2)', values: graded.map((r) => r.survivors) },
        { name: 'proven missing tests', color: 'var(--viz-1)', values: graded.map((r) => r.proven_missed) },
      ],
      format: count,
      integer: true,
      nullText: 'not measured',
    }),
  );
  out.appendChild(
    lineChart({
      title: 'Share of graded planted bugs the suite caught, per run',
      note: 'Mutants that did not compile are excluded. One run is one sample: the planted bugs differ from run to run.',
      xLabels,
      xDetail,
      xHeading: 'Run',
      series: [{ name: 'share caught', color: 'var(--viz-2)', values: graded.map((r) => r.caught_share) }],
      format: (v) => `${Math.round(v * 1000) / 10}%`,
      nullText: 'nothing graded',
      yTicks: [0, 0.25, 0.5, 0.75, 1],
    }),
  );
  return out;
}

// One row per audited file in one run: a file's proven gaps share the run's
// commit and the one test corral wrote for that file, so 257 gaps read as the
// files they are in. Every gap is still listed, by its mutant id.
function gapsTable(s: Summary): HTMLElement {
  if (s.gaps.length === 0) return para('muted', 'No proven missing tests on this ledger.');
  const finished = new Map(s.runs.map((r) => [r.scan_uid, r.finished_at]));
  const groups = new Map<string, Gap[]>();
  for (const g of s.gaps) {
    const k = `${g.scan_uid}\u0000${g.path}`;
    const list = groups.get(k);
    if (list) list.push(g);
    else groups.set(k, [g]);
  }
  const wrap = el('div', 'tablewrap');
  const t = el('table', 'gaps');
  const hr = el('tr');
  for (const [h, cls] of [['File', ''], ['Proven', 'num'], ['Mutants', ''], ['The proving test', '']]) {
    const th = el('th', cls || undefined, h);
    th.scope = 'col';
    hr.appendChild(th);
  }
  t.appendChild(el('thead')).appendChild(hr);
  const body = el('tbody');
  for (const gs of groups.values()) {
    const g = gs[0];
    const tr = el('tr');
    const file = el('td', 'gap-file');
    const when = finished.get(g.scan_uid);
    file.append(
      el('code', undefined, g.path),
      el('span', 'gap-where', `${shortRepo(g.repo)} @ ${shortSha(g.commit)}${when ? ` · ${day(when)}` : ''}`),
    );
    const ids = el('td', 'gap-ids');
    for (const x of gs) ids.appendChild(el('span', 'mutant-id', x.mutant_id));
    const test = el('td', 'gap-test');
    // The test is per file; a gap whose run did not publish source carries "".
    const authored = gs.find((x) => x.authored_test !== '')?.authored_test ?? '';
    if (authored !== '') {
      const det = el('details');
      det.appendChild(el('summary', undefined, 'Show the test'));
      // Filled on first open: a ledger can carry hundreds of these.
      det.addEventListener('toggle', () => {
        if (det.open && det.querySelector('pre') === null) det.appendChild(el('pre', undefined, authored));
      });
      test.appendChild(det);
    } else {
      test.appendChild(document.createTextNode("proven; the test's text was not published"));
      const href = sourceURL(g.repo, g.commit, g.path);
      if (href) {
        test.appendChild(document.createTextNode(' · '));
        test.appendChild(link(href, 'the audited source'));
      }
    }
    tr.append(file, el('td', 'num', count(gs.length)), ids, test);
    body.appendChild(tr);
  }
  t.appendChild(body);
  wrap.appendChild(t);
  return wrap;
}

function modelsBlock(lines: { ledger: string; m: ModelLine }[], showLedger: boolean): HTMLElement {
  if (lines.length === 0) {
    return para(
      'muted',
      'No file has been graded by two different mutant models at the same commit yet, so there is no like-for-like model comparison to draw. A comparison appears here once one exists.',
    );
  }
  return barChart({
    title: 'Proven missing tests, by mutant model',
    note: 'Only files graded by more than one mutant model at the same commit are counted, so each model faced the same suite on the same code.',
    color: 'var(--viz-1)',
    rows: lines.map(({ ledger, m }) => ({
      label: m.model,
      sub: showLedger ? ledger : undefined,
      value: m.proven_missed,
      detail: [
        `${count(m.proven_missed)} proven missing tests`,
        `${count(m.mutants_planted)} planted, ${count(m.mutants_graded)} graded, ${count(m.survivors)} survived`,
        `share caught: ${m.caught_share === null ? 'nothing graded' : pct(m.caught_share)}`,
        `${count(m.files)} files, ${count(m.rows)} graded rows`,
      ],
    })),
    format: count,
    labelHeading: 'Model',
    table: [
      { heading: 'Proven missing tests', cells: lines.map(({ m }) => count(m.proven_missed)) },
      { heading: 'Planted', cells: lines.map(({ m }) => count(m.mutants_planted)) },
      { heading: 'Graded', cells: lines.map(({ m }) => count(m.mutants_graded)) },
      { heading: 'Share caught', cells: lines.map(({ m }) => (m.caught_share === null ? 'nothing graded' : pct(m.caught_share))) },
      { heading: 'Files', cells: lines.map(({ m }) => count(m.files)) },
      { heading: 'Graded rows', cells: lines.map(({ m }) => count(m.rows)) },
    ],
  });
}

function entriesTable(d: Derived): HTMLElement {
  const det = el('details', 'entries');
  const { v, n } = verifiedCounts(d);
  det.appendChild(el('summary', undefined, `Every entry and the key that signed it (${v} of ${n} verified)`));
  const wrap = el('div', 'tablewrap');
  const t = el('table');
  const hr = el('tr');
  for (const h of ['File', 'Kind', 'Key', 'Status']) hr.appendChild(el('th', undefined, h));
  t.appendChild(el('thead')).appendChild(hr);
  const body = el('tbody');
  for (const e of d.entries) {
    const tr = el('tr');
    const status = el('td');
    status.appendChild(el('span', `status status-${e.status}`, e.status));
    if (e.problem) status.appendChild(el('div', 'entry-problem', e.problem));
    if (e.note) status.appendChild(el('div', 'entry-note', e.note));
    tr.append(el('td', 'mono', e.file), el('td', undefined, e.kind === '' ? 'scan' : e.kind), el('td', 'mono', e.keyid || '(none)'), status);
    body.appendChild(tr);
  }
  t.appendChild(body);
  wrap.appendChild(t);
  det.appendChild(wrap);
  return det;
}

// listedNote says whether the repo is listed, which root key the entries
// were checked against (Derive's fingerprint), and what that check shows: the
// key is read from the same branch as the entries, so "verified" means intact
// and signed by that key, not that the key is the project's. A listing does
// not pin a key yet (phase 5), so the listed note claims no more than that.
function listedNote(repo: string, listings: Listings, r: Extract<LedgerResult, { kind: 'derived' }>): HTMLElement {
  let listed: string;
  let l: Listing | undefined;
  if (!listings.ok) listed = `Whether ${repo} is listed could not be read (${listings.message}).`;
  else if ((l = listings.listings.find((x) => x.repo === repo))) listed = `Listed by corral on ${l.listed_at}.`;
  else listed = `${repo} is not listed.`;
  const at = r.commit.slice(0, 12);
  const key =
    r.rootPubSource === 'LEDGER_PUBKEY' && r.derived.root_keyid !== ''
      ? `Entries were checked against root key ${r.derived.root_keyid}, read from LEDGER_PUBKEY on the same corral/ledger branch as the entries, at commit ${at}. So “verified” means the entries are intact and signed by that key; it does not show that the key is the project's.${l ? ' A listing does not pin the key yet.' : ''}`
      : `This ledger publishes no root key (no LEDGER_PUBKEY on its corral/ledger branch), so no signed entry could be checked. Entries were read at commit ${at}.`;
  return para('listed-note', `${listed} ${key}`);
}

function problemPanel(d: Derived): HTMLElement {
  const box = el('div', 'problem');
  box.setAttribute('role', 'alert');
  box.appendChild(el('p', 'problem-head', d.problem ?? ''));
  box.appendChild(el('p', undefined, 'No figure is shown: a figure from a chain that is not intact would be a number this page did not verify. The entries that did not verify:'));
  const ul = el('ul');
  for (const e of d.entries.filter((x) => x.status === 'failed')) {
    const li = el('li');
    li.append(el('code', undefined, e.file), document.createTextNode(` ${e.problem ?? ''}`));
    ul.appendChild(li);
  }
  box.appendChild(ul);
  return box;
}

// ---- Page A.

export function renderA(root: HTMLElement, result: LedgerResult, listings: Listings, ms: number) {
  root.replaceChildren();
  if (result.kind !== 'derived') {
    root.appendChild(para('load-error', errorSentence(result)));
    return;
  }
  const d = result.derived;
  const head = el('div', 'a-head');
  const h = el('h2', 'a-repo');
  h.appendChild(link(`https://github.com/${result.repo}/tree/corral/ledger`, result.repo));
  head.appendChild(h);
  head.appendChild(listedNote(result.repo, listings, result));
  head.appendChild(para('muted timing', `Read from GitHub and checked in this browser by corral's own verifier in ${(ms / 1000).toFixed(1)} s.`));
  root.appendChild(head);

  if (d.entries.length === 0) {
    root.appendChild(para('empty-ledger', 'This ledger has no entries yet.'));
    return;
  }
  if (d.problem) {
    root.append(problemTiles(d), problemPanel(d), section('Entries', entriesTable(d)));
    return;
  }
  const s = d.summary;
  if (s.totals.scans === 0) {
    root.append(noRunTiles(d), para('muted', 'No verified run on this ledger, so there is nothing to chart or list.'), section('Entries', entriesTable(d)));
    return;
  }
  root.append(
    aTiles(d),
    section('Each run', runCharts(s)),
    section(`Proven missing tests (${count(s.gaps.length)}, across ${runs(s.totals.scans)})`, gapsTable(s)),
    section('Models compared', modelsBlock(s.models.map((m) => ({ ledger: result.repo, m })), false)),
    section('Entries', entriesTable(d)),
  );
}

// ---- Page B.

type Loaded = { listing: Listing; result: LedgerResult };

// A ledger counts on B when it derived with no problem and served at least
// one verified run: a ledger of unsigned entries has entries but no figure.
const intact = (r: LedgerResult): r is Extract<LedgerResult, { kind: 'derived' }> =>
  r.kind === 'derived' && !r.derived.problem && r.derived.entries.length > 0 && r.derived.summary.totals.scans > 0;

function sortRows(rows: Loaded[], key: SortKey): Loaded[] {
  const good = rows.filter((r) => intact(r.result)) as (Loaded & { result: Extract<LedgerResult, { kind: 'derived' }> })[];
  const rest = rows.filter((r) => !intact(r.result));
  const t = (r: (typeof good)[number]) => r.result.derived.summary.totals;
  // Unmeasured values (null share, no finish time) go last, never as 0.
  const desc = (a: number | null, b: number | null) => (a === null ? (b === null ? 0 : 1) : b === null ? -1 : b - a);
  const byName = (a: Loaded, b: Loaded) => a.listing.repo.localeCompare(b.listing.repo);
  const cmp: Record<SortKey, (a: (typeof good)[number], b: (typeof good)[number]) => number> = {
    proven: (a, b) => desc(t(a).proven_missed, t(b).proven_missed),
    caught: (a, b) => desc(t(a).caught_share, t(b).caught_share),
    planted: (a, b) => desc(t(a).mutants_planted, t(b).mutants_planted),
    recent: (a, b) => desc(t(a).last_run === null ? null : Date.parse(t(a).last_run!), t(b).last_run === null ? null : Date.parse(t(b).last_run!)),
    name: byName,
  };
  return [...good.sort((a, b) => cmp[key](a, b) || byName(a, b)), ...rest.sort(byName)];
}

function board(rows: Loaded[], key: SortKey): HTMLElement {
  const wrap = el('div', 'tablewrap');
  const t = el('table', 'board');
  const hr = el('tr');
  const cols: [SortKey | null, string][] = [
    ['name', 'Repository'],
    ['proven', 'Proven missing tests'],
    ['caught', 'Share caught'],
    ['planted', 'Mutants planted'],
    ['recent', 'Most recent run'],
    [null, 'Entries verified'],
  ];
  for (const [k, label] of cols) {
    const th = el('th', k !== 'name' ? 'num' : undefined);
    th.scope = 'col';
    if (k) {
      th.setAttribute('aria-sort', k === key ? (k === 'name' ? 'ascending' : 'descending') : 'none');
      th.appendChild(link(`?sort=${k}`, label, k === key ? 'sort-on' : undefined));
    } else th.textContent = label;
    hr.appendChild(th);
  }
  t.appendChild(el('thead')).appendChild(hr);
  const body = el('tbody');
  for (const { listing, result } of sortRows(rows, key)) {
    const tr = el('tr');
    tr.dataset.repo = listing.repo;
    const name = el('td', 'board-repo');
    name.appendChild(link(`/results/?repo=${encodeURIComponent(listing.repo)}`, listing.repo));
    tr.appendChild(name);
    if (intact(result)) {
      const s = result.derived.summary.totals;
      const { v, n } = verifiedCounts(result.derived);
      tr.append(
        el('td', 'num', count(s.proven_missed)),
        el('td', 'num', s.caught_share === null ? 'nothing graded' : pct(s.caught_share)),
        el('td', 'num', count(s.mutants_planted)),
        el('td', 'num', s.last_run === null ? 'no finish time' : day(s.last_run)),
        el('td', 'num', `${v}/${n}`),
      );
    } else {
      const msg =
        result.kind !== 'derived'
          ? errorSentence(result)
          : result.derived.entries.length === 0
            ? 'This ledger has no entries yet.'
            : result.derived.problem
              ? `${result.derived.problem} Its figures are not shown.`
              : result.derived.summary.totals.entries_unsigned > 0
                ? `No verified run on this ledger (${plural(result.derived.summary.totals.entries_unsigned, 'unsigned entry counts', 'unsigned entries count')} in no figure).`
                : 'No verified run on this ledger.';
      const td = el('td', 'board-error', msg);
      td.colSpan = cols.length - 1;
      tr.appendChild(td);
    }
    body.appendChild(tr);
  }
  t.appendChild(body);
  wrap.appendChild(t);
  return wrap;
}

function bTotals(rows: Loaded[]): HTMLElement {
  const good = rows.map((r) => r.result).filter(intact);
  const sum = (f: (s: Summary) => number) => good.reduce((a, r) => a + f(r.derived.summary), 0);
  const tel = good.map((r) => r.derived.summary.totals.telemetry).filter((t) => t !== null);
  const list: Tile[] = [
    { key: 'proven', label: 'Proven missing tests', value: count(sum((s) => s.totals.proven_missed)), caption: `added across the listed ledgers below, ${runs(sum((s) => s.totals.scans))} in all; a file audited more than once counts each run`, hero: true },
    { key: 'repos', label: 'Ledgers verified', value: count(good.length), caption: `of ${count(rows.length)} listed: intact, signed by the root key each publishes, with a verified run` },
    {
      key: 'planted',
      label: 'Mutants planted',
      value: count(sum((s) => s.totals.mutants_planted)),
      caption: `${count(sum((s) => s.totals.mutants_graded))} compiled and ran against the suite; ${count(sum((s) => s.totals.survivors))} survived it`,
    },
  ];
  if (tel.length === 0) list.push({ key: 'telemetry', label: 'Model calls', value: 'no telemetry recorded' });
  else {
    const floor = tel.some((t) => t.unmetered_calls > 0);
    list.push({
      key: 'telemetry',
      label: 'Model calls',
      value: count(tel.reduce((a, t) => a + t.model_calls, 0)),
      caption: `${floor ? 'at least ' : ''}${compact(tel.reduce((a, t) => a + t.input_tokens + t.output_tokens, 0))} tokens, from ${count(tel.length)} of ${count(good.length)} ledgers${floor ? '; some calls reported no tokens, so this is a floor' : ''}`,
    });
  }
  return tiles(list);
}

export function renderB(root: HTMLElement, rows: Loaded[], sort: string) {
  root.replaceChildren();
  root.appendChild(bTotals(rows));
  const sortOK = (SORTS as readonly string[]).includes(sort);
  const boardBody: Node[] = [];
  if (!sortOK) {
    const p = para('sort-error', `"${sort}" is not a sort this page knows. Sort by: `);
    SORTS.forEach((k, i) => {
      if (i > 0) p.appendChild(document.createTextNode(', '));
      p.appendChild(link(`?sort=${k}`, SORT_LABEL[k].toLowerCase()));
    });
    p.appendChild(document.createTextNode('.'));
    boardBody.push(p);
  } else {
    boardBody.push(
      para('muted', 'Each figure is that ledger\'s own, checked in this browser against the root key the ledger publishes on its own branch (a listing does not pin the key yet). Shares are per ledger and never averaged across them. Select a column heading to sort.'),
      board(rows, sort as SortKey),
    );
  }
  root.appendChild(section('Listed ledgers', ...boardBody));
  const models = rows
    .map((r) => r.result)
    .filter(intact)
    .flatMap((r) => r.derived.summary.models.map((m) => ({ ledger: r.repo, m })));
  root.appendChild(section('Models compared', modelsBlock(models, true)));
}

// ---- Wiring.

async function readListings(fetchFn: typeof fetch): Promise<Listings> {
  let res: Response;
  try {
    res = await fetchFn('/results/listings.json');
  } catch {
    return { ok: false, message: 'the request did not complete' };
  }
  if (res.status !== 200) return { ok: false, message: `HTTP ${res.status}` };
  let body: unknown;
  try {
    body = await res.json();
  } catch {
    return { ok: false, message: 'not JSON' };
  }
  const ls = (body as { listings?: unknown })?.listings;
  const valid = (x: unknown): x is Listing =>
    typeof x === 'object' && x !== null && ['repo', 'listed_at', 'signing'].every((k) => typeof (x as Record<string, unknown>)[k] === 'string');
  if (!Array.isArray(ls) || !ls.every(valid)) return { ok: false, message: 'not a list of listings' };
  return { ok: true, listings: ls };
}

function status(root: HTMLElement, text: string) {
  root.replaceChildren(para('loading', text));
}

export async function main(root: HTMLElement, lookup: HTMLFormElement) {
  const fetchFn = window.fetch.bind(window);
  const params = new URLSearchParams(location.search);
  const repo = params.get('repo');
  root.dataset.state = 'loading';
  document.body.dataset.view = repo === null ? 'b' : 'a';
  const input = lookup.querySelector('input');
  if (input && repo !== null) input.value = repo;

  if (repo !== null) {
    document.title = `${repo} — Results — Corralai`;
    status(root, parseRepo(repo) === null ? '' : `Reading ${repo}'s ledger from GitHub and verifying every entry in this browser…`);
    const t0 = performance.now();
    const [listings, result] = await Promise.all([readListings(fetchFn), loadLedger(repo, fetchFn)]);
    renderA(root, result, listings, performance.now() - t0);
  } else {
    status(root, 'Reading the listed ledgers from GitHub and verifying every entry in this browser…');
    const listings = await readListings(fetchFn);
    if (!listings.ok) {
      root.replaceChildren(para('load-error', `The list of listed ledgers could not be read (${listings.message}).`));
    } else {
      // Each ledger loads on its own: one that cannot be read shows its own
      // sentence and never blanks the others.
      const rows = await Promise.all(
        listings.listings.map(async (listing): Promise<Loaded> => {
          try {
            return { listing, result: await loadLedger(listing.repo, fetchFn) };
          } catch (e) {
            return { listing, result: { kind: 'derive-failed', repo: listing.repo, message: e instanceof Error ? e.message : String(e) } };
          }
        }),
      );
      renderB(root, rows, params.get('sort') ?? 'recent');
    }
  }
  root.dataset.state = 'done';
}
