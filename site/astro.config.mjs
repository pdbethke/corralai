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
      // Show the CorralAI lockup in the docs header (links back to the site
      // home), so /docs and the marketing pages read as one continuous site.
      components: {
        SiteTitle: './src/components/StarlightSiteTitle.astro',
        // Adds the marketing site-nav (Home/Recordings/Field notes) inline in the
        // docs header so /docs isn't a nav dead end. Reuses Starlight's own
        // search/theme/social so nothing is lost.
        Header: './src/components/StarlightHeader.astro',
      },
      sidebar: [
        {
          label: 'Site',
          items: [
            { label: 'Home', link: '/' },
            { label: 'Recordings', link: '/recordings/' },
            { label: 'Field notes', link: '/field-notes/' },
          ],
        },
        { label: 'Getting started', slug: 'docs/getting-started' },
        { label: 'Your first audit, in detail', slug: 'docs/first-audit' },
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
        { label: 'Configuration', slug: 'docs/configuration' },
        { label: 'MCP tools reference', slug: 'docs/mcp-tools' },
        { label: 'Publishing recordings', slug: 'docs/publishing-recordings' },
        {
          label: 'The UI, tab by tab',
          items: [
            { label: 'Records (default landing view)', slug: 'docs/ui-tour/records' },
            { label: 'The corral (canvas view)', slug: 'docs/ui-tour/corral' },
            { label: 'Progress', slug: 'docs/ui-tour/progress' },
            { label: 'Topology', slug: 'docs/ui-tour/topology' },
            { label: 'Memory', slug: 'docs/ui-tour/memory' },
            { label: 'Proposals', slug: 'docs/ui-tour/proposals' },
            { label: 'Lookbook', slug: 'docs/ui-tour/lookbook' },
            { label: 'Completed + replay + agent windows', slug: 'docs/ui-tour/completed-and-replay' },
          ],
        },
        {
          // Generated mechanically by scripts/gen-cli-docs.sh from each
          // binary's own -h output — never hand-written. Now that Task 4 has
          // written the pages, these are real `slug` entries (not `link`),
          // which gets Starlight's prev/next + active-link handling.
          label: 'CLI reference',
          items: [
            { label: 'corral', slug: 'docs/cli/corral' },
            { label: 'corral-admin', slug: 'docs/cli/corral-admin' },
            { label: 'corral-agent', slug: 'docs/cli/corral-agent' },
            { label: 'corral-desktop', slug: 'docs/cli/corral-desktop' },
            { label: 'corral-harness', slug: 'docs/cli/corral-harness' },
            { label: 'corral-observe', slug: 'docs/cli/corral-observe' },
            { label: 'corral-top', slug: 'docs/cli/corral-top' },
          ],
        },
        { label: 'Limitations & roadmap', slug: 'docs/limitations' },
      ],
    }),
  ],
});
