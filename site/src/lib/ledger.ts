// SPDX-License-Identifier: Elastic-2.0
// Load any repository's ledger in the browser and derive it with corral's own
// code: ledger.Derive compiled to WebAssembly (internal/ledger/wasm, built
// into /results/wasm/ at site build from the same commit as this page).
//
// This module fetches and hands bytes across; it decides nothing about the
// record. Which entries verify, which rows count and every figure the page
// shows come back from Derive (Derived.summary). A second implementation here
// would be a second opinion about the record, so there is none.
//
// Every way a load can fail is its own LedgerResult kind, and none of them is
// a derive over part of a ledger: one entry that cannot be read fails the
// load, naming its URL. A could-not-load is never rendered as zeros.

// ---- Derived: the JSON ledger.Derive returns (internal/ledger/derive.go,
// summary.go). Keys are the Go json tags. A figure that was not measured is
// null, never 0.

export const DERIVE_FORMAT = 'corral-derive-1';

export type EntryStatus = {
  file: string;
  hash: string;
  kind: string; // "" is a scan entry
  keyid: string;
  status: 'verified' | 'unsigned' | 'failed';
  problem?: string;
  signer?: string;
  note?: string;
};

export type Telemetry = {
  scans_with_telemetry: number;
  model_calls: number;
  input_tokens: number;
  output_tokens: number;
  unmetered_calls: number;
};

export type Totals = {
  scans: number;
  files_audited: number;
  mutants_planted: number;
  survivors: number;
  proven_missed: number;
  could_not_grade: number;
  caught_share: number | null;
  telemetry: Telemetry | null;
  last_run: string | null; // RFC 3339
};

export type RunPoint = {
  scan_uid: string;
  repo: string;
  commit: string;
  finished_at: string | null; // RFC 3339
  files_audited: number;
  mutants_planted: number;
  survivors: number;
  proven_missed: number;
  caught_share: number | null;
};

export type Gap = {
  scan_uid: string;
  repo: string;
  commit: string;
  path: string;
  mutant_id: string;
  outcome: string;
  authored_test: string;
  source_pushed: boolean;
};

export type ModelLine = {
  role: string; // "mutant-generator"
  model: string;
  files: number;
  mutants_planted: number;
  survivors: number;
  proven_missed: number;
  caught_share: number | null;
};

export type Summary = {
  totals: Totals;
  runs: RunPoint[];
  gaps: Gap[];
  models: ModelLine[];
};

export type Adjudication = {
  adjudicates: string; // "<review entry hash>#<finding id>"
  verdict: 'confirmed' | 'refuted';
  by: string;
  reason: string;
};

// The served rows (ScanRow, Row, MutantRow, ModelCallRow) keep their Go field
// names as JSON keys because they are hashed inside signed entries. They are
// typed opaque on purpose: the page draws `summary` and never sums rows, so
// nothing here offers it fields to sum.
export type OpaqueRow = Readonly<Record<string, unknown>>;

export type Derived = {
  format: typeof DERIVE_FORMAT;
  problem?: string;
  entries: EntryStatus[];
  scans: OpaqueRow[];
  files: OpaqueRow[];
  mutants: OpaqueRow[];
  calls: OpaqueRow[];
  reviews: number;
  adjudications: Record<string, Adjudication>;
  summary: Summary;
};

// ---- The loader.

export type LedgerResult =
  | { kind: 'derived'; repo: string; derived: Derived; rootPubSource: 'LEDGER_PUBKEY' | 'absent' }
  | { kind: 'no-ledger'; repo: string }
  | { kind: 'rate-limited'; repo: string; resetAt: Date }
  | { kind: 'too-large'; repo: string }
  | { kind: 'fetch-failed'; repo: string; url: string; status: number | 'network' }
  // A response GitHub sent that is not the shape it documents (a trees body
  // that is not JSON with a tree array).
  | { kind: 'bad-response'; repo: string; url: string; message: string }
  // The verifier itself could not run: the wasm did not load, Derive threw
  // (two entries with one name), or it returned a format this page does not
  // know. Not a verdict about the ledger.
  | { kind: 'derive-failed'; repo: string; message: string }
  | { kind: 'bad-repo'; input: string };

const BRANCH = 'corral/ledger';
const MAX_IN_FLIGHT = 8;
const WASM_DIR = '/results/wasm';
const REPO_RE = /^[A-Za-z0-9-]+\/[A-Za-z0-9._-]+$/;

// parseRepo returns owner/name when input is exactly that, else null. "." and
// ".." are refused as names: a URL resolves them as path segments.
export function parseRepo(input: string): string | null {
  if (!REPO_RE.test(input)) return null;
  const name = input.split('/')[1];
  return name === '.' || name === '..' ? null : input;
}

const treesURL = (repo: string) =>
  `https://api.github.com/repos/${repo}/git/trees/${encodeURIComponent(BRANCH)}?recursive=1`;
const rawURL = (repo: string, p: string) =>
  `https://raw.githubusercontent.com/${repo}/${BRANCH}/${p.split('/').map(encodeURIComponent).join('/')}`;

// isEntryPath keeps what auditpush.ReadLedgerDir reads: files directly under
// scans/ ending .json or .json.gz, no dotfiles, no subdirectories.
function isEntryPath(p: string): boolean {
  if (!p.startsWith('scans/')) return false;
  const name = p.slice('scans/'.length);
  return name !== '' && !name.includes('/') && !name.startsWith('.') && (name.endsWith('.json') || name.endsWith('.json.gz'));
}

type Failure = Extract<LedgerResult, { kind: 'fetch-failed' }>;
type Got = { ok: true; res: Response } | { ok: false; failure: Failure };

async function get(fetchFn: typeof fetch, repo: string, url: string, init?: RequestInit): Promise<Got> {
  try {
    return { ok: true, res: await fetchFn(url, init) };
  } catch {
    return { ok: false, failure: { kind: 'fetch-failed', repo, url, status: 'network' } };
  }
}

// rateLimitReset is the reset time when res is GitHub's rate limit refusal,
// else null. A refusal without a readable reset time is not reported as a
// rate limit with an invented time; it falls through to fetch-failed.
function rateLimitReset(res: Response): Date | null {
  if (res.status !== 403 && res.status !== 429) return null;
  if (res.headers.get('x-ratelimit-remaining') !== '0') return null;
  const reset = Number(res.headers.get('x-ratelimit-reset'));
  if (!Number.isFinite(reset) || reset <= 0) return null;
  return new Date(reset * 1000);
}

// ---- The verifier, loaded once per page.

type DeriveFn = (entries: { name: string; bytes: Uint8Array }[], trust: { rootPub: string; repo: string }) => string;
type GoRuntime = { importObject: WebAssembly.Imports; run(instance: WebAssembly.Instance): Promise<void> };

let verifier: Promise<DeriveFn> | null = null;

function loadScript(src: string): Promise<void> {
  return new Promise((resolve, reject) => {
    const s = document.createElement('script');
    s.src = src;
    s.onload = () => resolve();
    s.onerror = () => reject(new Error(`could not load ${src}`));
    document.head.appendChild(s);
  });
}

async function startVerifier(fetchFn: typeof fetch): Promise<DeriveFn> {
  const g = globalThis as unknown as { Go?: new () => GoRuntime; corralLedgerDerive?: DeriveFn };
  if (g.Go === undefined) await loadScript(`${WASM_DIR}/wasm_exec.js`);
  if (g.Go === undefined) throw new Error('wasm_exec.js loaded but defined no Go runtime');
  const res = await fetchFn(`${WASM_DIR}/ledger.wasm`);
  if (res.status !== 200) throw new Error(`${WASM_DIR}/ledger.wasm answered ${res.status}`);
  const go = new g.Go();
  const { instance } = await WebAssembly.instantiate(await res.arrayBuffer(), go.importObject);
  // run() settles only when the Go program exits, and it blocks in select{}
  // by design; corralLedgerDerive is registered synchronously before that.
  void go.run(instance);
  if (typeof g.corralLedgerDerive !== 'function') throw new Error('ledger.wasm ran but registered no corralLedgerDerive');
  return g.corralLedgerDerive;
}

function deriver(fetchFn: typeof fetch): Promise<DeriveFn> {
  if (verifier === null) {
    verifier = startVerifier(fetchFn);
    // A failed load is not cached: the next call tries again.
    verifier.catch(() => {
      verifier = null;
    });
  }
  return verifier;
}

const message = (e: unknown) => (e instanceof Error ? e.message : String(e));

// loadLedger reads repo's corral/ledger branch from GitHub and derives it.
// fetchFn is an injection seam for tests, not a default: the page passes
// window.fetch.
export async function loadLedger(repo: string, fetchFn: typeof fetch): Promise<LedgerResult> {
  if (parseRepo(repo) === null) return { kind: 'bad-repo', input: repo };

  // 1. The file list.
  const tURL = treesURL(repo);
  const t = await get(fetchFn, repo, tURL, { headers: { Accept: 'application/vnd.github+json' } });
  if (!t.ok) return t.failure;
  if (t.res.status === 404) return { kind: 'no-ledger', repo };
  const resetAt = rateLimitReset(t.res);
  if (resetAt !== null) return { kind: 'rate-limited', repo, resetAt };
  if (t.res.status !== 200) return { kind: 'fetch-failed', repo, url: tURL, status: t.res.status };
  let listing: { tree?: unknown; truncated?: unknown };
  try {
    listing = await t.res.json();
  } catch (e) {
    return { kind: 'bad-response', repo, url: tURL, message: `not JSON: ${message(e)}` };
  }
  if (listing === null || typeof listing !== 'object' || !Array.isArray(listing.tree)) {
    return { kind: 'bad-response', repo, url: tURL, message: 'no tree array in the listing' };
  }
  if (listing.truncated === true) return { kind: 'too-large', repo };
  const paths = (listing.tree as { path?: unknown; type?: unknown }[])
    .filter((n) => n?.type === 'blob' && typeof n.path === 'string' && isEntryPath(n.path))
    .map((n) => n.path as string)
    .sort();

  // 2. The root key. Absent is a state the result names, not an error: Derive
  // then reports each signed entry as unchecked.
  const kURL = rawURL(repo, 'LEDGER_PUBKEY');
  const k = await get(fetchFn, repo, kURL);
  if (!k.ok) return k.failure;
  let rootPub: string;
  let rootPubSource: 'LEDGER_PUBKEY' | 'absent';
  if (k.res.status === 404) {
    rootPub = '';
    rootPubSource = 'absent';
  } else if (k.res.status === 200) {
    rootPub = (await k.res.text()).trim();
    rootPubSource = 'LEDGER_PUBKEY';
  } else {
    return { kind: 'fetch-failed', repo, url: kURL, status: k.res.status };
  }

  // 3. The entries, at most MAX_IN_FLIGHT at a time. The first failure stops
  // new reads and fails the load: there is no partial derive.
  const entries: { name: string; bytes: Uint8Array }[] = new Array(paths.length);
  let failure: Failure | null = null;
  let next = 0;
  const worker = async () => {
    while (failure === null && next < paths.length) {
      const i = next++;
      const url = rawURL(repo, paths[i]);
      const r = await get(fetchFn, repo, url);
      if (!r.ok) {
        failure ??= r.failure;
        return;
      }
      if (r.res.status !== 200) {
        failure ??= { kind: 'fetch-failed', repo, url, status: r.res.status };
        return;
      }
      try {
        entries[i] = { name: paths[i].slice('scans/'.length), bytes: new Uint8Array(await r.res.arrayBuffer()) };
      } catch {
        failure ??= { kind: 'fetch-failed', repo, url, status: 'network' };
        return;
      }
    }
  };
  await Promise.all(Array.from({ length: Math.min(MAX_IN_FLIGHT, paths.length) }, worker));
  if (failure !== null) return failure;

  // 4. Derive.
  let derived: Derived;
  try {
    const derive = await deriver(fetchFn);
    derived = JSON.parse(derive(entries, { rootPub, repo }));
  } catch (e) {
    return { kind: 'derive-failed', repo, message: message(e) };
  }
  if (derived?.format !== DERIVE_FORMAT) {
    return { kind: 'derive-failed', repo, message: `Derive returned format ${JSON.stringify(derived?.format)}, this page reads ${DERIVE_FORMAT}` };
  }
  return { kind: 'derived', repo, derived, rootPubSource };
}
