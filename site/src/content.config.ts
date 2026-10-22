// SPDX-License-Identifier: Elastic-2.0
import { defineCollection, z } from 'astro:content';
import { glob } from 'astro/loaders';

export const collections = {
  // The docs, rendered by src/pages/docs/[...slug].astro with the site's own
  // Header (Starlight drew them until 2026-10-22). An entry's id is its path
  // under src/content/docs without the extension (docs/getting-started), which
  // is the slug src/data/docs-sidebar.mjs lists. scripts/gen-cli-docs.sh and
  // scripts/gen-language-support.sh write generated pages into this tree.
  docs: defineCollection({
    loader: glob({ pattern: '**/*.{md,mdx}', base: './src/content/docs' }),
    schema: z.object({
      title: z.string(),
      description: z.string(),
    }),
  }),
  // Field notes: marketing-side essays, rendered by hand-authored Astro pages
  // with the same /, /recordings chrome via Header.astro.
  // The glob loader gives each file an `id` from its basename (fugu.mdx -> fugu),
  // which becomes /field-notes/<id>/.
  fieldNotes: defineCollection({
    loader: glob({ pattern: '**/*.{md,mdx}', base: './src/content/field-notes' }),
    schema: z.object({
      title: z.string(),
      description: z.string(),
      pubDate: z.coerce.date(),
      draft: z.boolean().default(false),
    }),
  }),
};
