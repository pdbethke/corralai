// SPDX-License-Identifier: Elastic-2.0
// The site's primary navigation, in one place. Header.astro draws it on every
// page, /docs included, and the /ledger/ pages get it by copying that header
// (integrations/ledger-chrome.mjs). Until 2026-10-22 the docs had a header of
// their own with its own list, which had lost "DuckDB integration" and "GitHub".
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
