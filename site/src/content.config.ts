// SPDX-License-Identifier: Elastic-2.0
import { defineCollection, z } from 'astro:content';
import { glob } from 'astro/loaders';
import { docsLoader } from '@astrojs/starlight/loaders';
import { docsSchema } from '@astrojs/starlight/schema';

export const collections = {
  docs: defineCollection({ loader: docsLoader(), schema: docsSchema() }),
  // Field notes: marketing-side essays (rendered by hand-authored Astro pages,
  // NOT Starlight), so they get the same /, /recordings chrome via Header.astro.
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
