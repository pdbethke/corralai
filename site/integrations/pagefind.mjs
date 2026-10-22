// SPDX-License-Identifier: Elastic-2.0
// Builds the site's search index once the site is built: every page under
// dist, the Go-rendered /ledger/ pages included, written to dist/pagefind and
// served from there, so a search never leaves corralai.dev. List it AFTER
// ledgerChrome in astro.config: Astro runs build:done hooks in integration
// order, and the ledger pages must carry the header (which is
// data-pagefind-ignore) before they are read.
//
// It fails the build on an indexing error or an empty index rather than ship a
// search box with nothing behind it. Starlight ran Pagefind for the docs alone
// until 2026-10-22; this replaced it and widened it to the whole site.
import { fileURLToPath } from 'node:url';
import { join } from 'node:path';
import * as pagefind from 'pagefind';

export default function pagefindIndex() {
  return {
    name: 'corral-pagefind',
    hooks: {
      'astro:build:done': async ({ dir, logger }) => {
        const site = fileURLToPath(dir);
        const { index, errors } = await pagefind.createIndex({});
        if (!index) throw new Error(`pagefind: ${errors.join('; ')}`);
        try {
          const added = await index.addDirectory({ path: site });
          if (added.errors.length) throw new Error(`pagefind: ${added.errors.join('; ')}`);
          if (!added.page_count) throw new Error('pagefind: no pages indexed');
          const written = await index.writeFiles({ outputPath: join(site, 'pagefind') });
          if (written.errors.length) throw new Error(`pagefind: ${written.errors.join('; ')}`);
          logger.info(`search index: ${added.page_count} page(s)`);
        } finally {
          await pagefind.close();
        }
      },
    },
  };
}
