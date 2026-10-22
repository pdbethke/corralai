// SPDX-License-Identifier: Elastic-2.0
// The site's primary navigation, in one place. The marketing header
// (Header.astro), the docs header (StarlightHeader.astro) and the docs
// sidebar's "Site" group (astro.config.mjs) all read it, and the /ledger/
// pages get it by copying the marketing header (integrations/ledger-chrome.mjs).
// Until 2026-10-22 each header kept its own list, and the docs one had lost
// "DuckDB integration" and "GitHub".
export const REPO = 'https://github.com/pdbethke/corralai';

/** key matches Header.astro's `active` prop; external links open in a new tab. */
export const SITE_NAV = [
  { key: 'home', label: 'Home', href: '/' },
  { key: 'docs', label: 'Docs', href: '/docs/getting-started/' },
  { key: 'recordings', label: 'Recordings', href: '/recordings/' },
  { key: 'ledger', label: 'Ledger', href: '/ledger/' },
  { key: 'warehouse', label: 'DuckDB integration', href: '/warehouse/' },
  { key: 'notes', label: 'Field notes', href: '/field-notes/' },
  { key: 'github', label: 'GitHub', href: REPO, external: true },
];
