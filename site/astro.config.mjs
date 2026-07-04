// SPDX-License-Identifier: Elastic-2.0
// @ts-check
import { defineConfig } from 'astro/config';

import starlight from '@astrojs/starlight';

// https://astro.build/config
export default defineConfig({
  site: 'https://corralai.dev',
  integrations: [
    starlight({
      title: 'Corralai docs',
      description:
        'Getting started, concepts, running it, and the CLI reference for corralai.',
      social: [
        { icon: 'github', label: 'GitHub', href: 'https://github.com/pdbethke/corralai' },
      ],
      // Starlight ships Pagefind (fully local, no network calls) and system
      // fonts by default — no <link> to a Google/Adobe font host anywhere in
      // its default theme. Verified in Task 3 Step 5 and re-verified in
      // Task 8's e2e docs-session network interception.
      customCss: ['./src/styles/starlight-tokens.css'],
      sidebar: [
        { label: 'Getting started', slug: 'docs/getting-started' },
        {
          label: 'Concepts',
          items: [
            { label: 'Mission lifecycle', slug: 'docs/concepts/mission-lifecycle' },
            { label: 'The task queue + verify gate', slug: 'docs/concepts/queue-and-verify' },
            { label: 'Claims & leases', slug: 'docs/concepts/claims-and-leases' },
            { label: 'Memory tiers + the learning loop', slug: 'docs/concepts/memory-and-learning-loop' },
            { label: 'Mission history + replay', slug: 'docs/concepts/history-and-replay' },
            { label: 'Multi-model herds', slug: 'docs/concepts/multi-model-herds' },
            { label: 'The knowledge corpus', slug: 'docs/concepts/knowledge-corpus' },
            { label: 'Trust & security', slug: 'docs/concepts/trust-and-security' },
          ],
        },
        { label: 'Running it', slug: 'docs/running-it' },
        {
          label: 'The UI, tab by tab',
          items: [
            { label: 'The corral (canvas view)', slug: 'docs/ui-tour/corral' },
            { label: 'Progress', slug: 'docs/ui-tour/progress' },
            { label: 'Topology', slug: 'docs/ui-tour/topology' },
            { label: 'Memory', slug: 'docs/ui-tour/memory' },
            { label: 'Proposals', slug: 'docs/ui-tour/proposals' },
            { label: 'Completed + replay + agent windows', slug: 'docs/ui-tour/completed-and-replay' },
          ],
        },
        {
          // CLI reference pages don't exist yet — Task 4 generates them
          // mechanically from each binary's --help output. Starlight
          // validates `slug` entries against the content collection at
          // build time, so these use raw `link` entries instead of `slug`
          // until Task 4 adds the actual pages (at which point Task 4
          // should switch these back to `slug` entries to get Starlight's
          // prev/next + active-link handling).
          label: 'CLI reference',
          items: [
            { label: 'corral', link: '/docs/cli/corral/' },
            { label: 'corral-admin', link: '/docs/cli/corral-admin/' },
            { label: 'corral-agent', link: '/docs/cli/corral-agent/' },
            { label: 'corral-harness', link: '/docs/cli/corral-harness/' },
            { label: 'corral-observe', link: '/docs/cli/corral-observe/' },
            { label: 'corral-top', link: '/docs/cli/corral-top/' },
          ],
        },
      ],
    }),
  ],
});
