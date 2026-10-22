// SPDX-License-Identifier: Elastic-2.0
// The docs sidebar, in reading order. src/pages/docs/[...slug].astro draws it
// and takes each page's previous/next links from the same order, so the two
// cannot disagree. A slug is the page's path under src/content/docs without
// its extension; a group with `collapsed` starts closed unless it holds the
// current page. The site's own pages are in the header (SiteNav), not here.
export const DOCS_SIDEBAR = [
  // First: what it is and how each flow works, in plain language — the
  // page a reader who has thirty seconds should land on.
  { label: 'How it works', slug: 'docs/how-it-works' },
  { label: 'Getting started', slug: 'docs/getting-started' },
  { label: 'Your first audit, in detail', slug: 'docs/first-audit' },
  // Third, and above Concepts: the Action is how corral is actually
  // adopted. It sat unpublished while the console UI tour had seven
  // pages — builder-era shelf space the audit product never claimed.
  { label: 'The GitHub Action', slug: 'docs/github-action' },
  { label: 'On an internal forge', slug: 'docs/internal-forge' },
  // Near the top on purpose: the fastest way to stop taking our word
  // for anything is to check a real verdict yourself, offline.
  { label: 'Verify a record yourself', slug: 'docs/verify-a-record' },
  {
    label: 'Concepts',
    items: [
      // First: it is the vocabulary every other page assumes, and the
      // answer to "isn't this just mutation testing?".
      { label: 'Mutants, survivors, kill rates', slug: 'docs/concepts/mutation-testing' },
      { label: 'Multi-model herds', slug: 'docs/concepts/multi-model-herds' },
      { label: 'Trust & security', slug: 'docs/concepts/trust-and-security' },
    ],
  },
  { label: 'The record — DuckDB, Rekor, MotherDuck', slug: 'docs/warehouse' },
  { label: 'Configuration', slug: 'docs/configuration' },
  // Half audit (what a run prints, keeps and reuses; dependency binds),
  // half daemon; the page's own note says which section is which.
  { label: 'Running it', slug: 'docs/running-it' },
  // Generated from internal/lang's runner table (scripts/gen-language-support.sh).
  { label: 'Language support', slug: 'docs/language-support' },
  {
    // The daemon is optional infrastructure: nothing in it is required
    // for a verdict, and nothing in it is read by one. Grouped so a
    // reader adopting the audit never has to walk through it — the
    // "too many moving parts" a first read used to meet was mostly
    // this group, unlabelled, interleaved with the audit's pages.
    // Frozen (2026-10-16): shipped and supported, no longer developed.
    // Every page in it opens with a note saying so.
    label: 'The optional daemon (frozen — not read by any audit)',
    collapsed: true,
    items: [
      { label: 'The task queue + verify gate', slug: 'docs/concepts/queue-and-verify' },
      { label: 'Claims & leases', slug: 'docs/concepts/claims-and-leases' },
      { label: 'Memory tiers + the learning loop', slug: 'docs/concepts/memory-and-learning-loop' },
      { label: 'The knowledge corpus', slug: 'docs/concepts/knowledge-corpus' },
      { label: 'Mission history + replay', slug: 'docs/concepts/history-and-replay' },
      { label: 'MCP tools reference', slug: 'docs/mcp-tools' },
      { label: 'Publishing recordings', slug: 'docs/publishing-recordings' },
      {
        // The cockpit is a post-mortem instrument, not the front door.
        label: 'The UI, tab by tab (post-mortem)',
        items: [
          { label: 'Records (default landing view)', slug: 'docs/ui-tour/records' },
          { label: 'The corral (canvas view)', slug: 'docs/ui-tour/corral' },
          { label: 'Progress', slug: 'docs/ui-tour/progress' },
          { label: 'Topology', slug: 'docs/ui-tour/topology' },
          { label: 'Memory', slug: 'docs/ui-tour/memory' },
          { label: 'Skills', slug: 'docs/ui-tour/skills' },
          { label: 'Proposals', slug: 'docs/ui-tour/proposals' },
          { label: 'Completed + replay + agent windows', slug: 'docs/ui-tour/completed-and-replay' },
        ],
      },
    ],
  },
  {
    // Generated mechanically by scripts/gen-cli-docs.sh from each
    // binary's own -h output — never hand-written.
    label: 'CLI reference',
    items: [
      { label: 'corral', slug: 'docs/cli/corral' },
      { label: 'corral-admin', slug: 'docs/cli/corral-admin' },
      { label: 'corral-agent', slug: 'docs/cli/corral-agent' },
      { label: 'corral-desktop', slug: 'docs/cli/corral-desktop' },
      { label: 'corral-harness', slug: 'docs/cli/corral-harness' },
      { label: 'corral-observe', slug: 'docs/cli/corral-observe' },
      { label: 'corral-top', slug: 'docs/cli/corral-top' },
      { label: 'corral-wrangler', slug: 'docs/cli/corral-wrangler' },
    ],
  },
  { label: 'Limitations & roadmap', slug: 'docs/limitations' },
];

/** The URL a docs slug is served at: docs/index → /docs/, docs/x → /docs/x/. */
export function docsHref(slug) {
  return `/${slug.replace(/(^|\/)index$/, '')}/`.replace(/\/+/g, '/');
}

/** Every page link in sidebar order, groups flattened: the previous/next sequence. */
export function sidebarPages(items = DOCS_SIDEBAR) {
  return items.flatMap((i) => (i.items ? sidebarPages(i.items) : [i]));
}
