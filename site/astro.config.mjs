// SPDX-License-Identifier: Elastic-2.0
// @ts-check
import { defineConfig } from 'astro/config';

import mdx from '@astrojs/mdx';
import sitemap from '@astrojs/sitemap';
import ledgerChrome from './integrations/ledger-chrome.mjs';
import pagefindIndex from './integrations/pagefind.mjs';

// https://astro.build/config
export default defineConfig({
  site: 'https://corralai.dev',
  integrations: [
    mdx(),
    sitemap(),
    // Copies the site header onto the Go-rendered /ledger/ pages after the build.
    ledgerChrome(),
    // Then indexes every built page for the header's search; after
    // ledgerChrome so the ledger pages are read with their header in place.
    pagefindIndex(),
  ],
});
