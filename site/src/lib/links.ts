// SPDX-License-Identifier: Elastic-2.0
// Links the results page builds from a ledger's own strings. No imports, so
// a test can load it on its own (tests/results.spec.ts).

// A scan's repo is the audited repository (a URL or owner/name), which is not
// the ledger's own repository: corral's ledger records audits of others.
const GH_REPO = /^(?:https:\/\/github\.com\/)?([A-Za-z0-9-]+\/[A-Za-z0-9._-]+?)(?:\.git)?\/?$/;

export function shortRepo(repo: string): string {
  const m = GH_REPO.exec(repo);
  return m ? m[1] : repo;
}

// sourceURL links the audited file at the audited commit on GitHub, or
// returns null. encodeURIComponent leaves "." and ".." as they are and the
// browser resolves them as path segments, so a path segment that is empty,
// "." or ".." (or a repo name that is "." or "..") would point the link at
// some other file or repository: those get no link.
export function sourceURL(repo: string, commit: string, path: string): string | null {
  const m = GH_REPO.exec(repo);
  if (!m || !/^[0-9a-f]{7,40}$/.test(commit)) return null;
  const name = m[1].split('/')[1];
  if (name === '.' || name === '..') return null;
  const segs = path.split('/');
  if (segs.some((s) => s === '' || s === '.' || s === '..')) return null;
  return `https://github.com/${m[1]}/blob/${commit}/${segs.map(encodeURIComponent).join('/')}`;
}
