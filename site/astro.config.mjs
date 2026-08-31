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
            { label: 'DuckDB integration', link: '/warehouse/' },
            { label: 'Field notes', link: '/field-notes/' },
            { label: 'GitHub', link: 'https://github.com/pdbethke/corralai', attrs: { target: '_blank', rel: 'noopener noreferrer' } },
          ],
        },
        { label: 'Getting started', slug: 'docs/getting-started' },
        { label: 'Your first audit, in detail', slug: 'docs/first-audit' },
        // Third, and above Concepts: the Action is how corral is actually
        // adopted. It sat unpublished while the console UI tour had seven
        // pages — builder-era shelf space the audit product never claimed.
        { label: 'The GitHub Action', slug: 'docs/github-action' },
        // Near the top on purpose: the fastest way to stop taking our word
        // for anything is to check a real verdict yourself, offline.
        { label: 'Verify a record yourself', slug: 'docs/verify-a-record' },
        {
          label: 'Concepts',
          items: [
            // First: it is the vocabulary every other page assumes, and the
            // answer to "isn't this just mutation testing?".
            { label: 'Mutants, survivors, kill rates', slug: 'docs/concepts/mutation-testing' },
            { label: 'The task queue + verify gate', slug: 'docs/concepts/queue-and-verify' },
            { label: 'Claims & leases', slug: 'docs/concepts/claims-and-leases' },
            { label: 'Memory tiers + the learning loop', slug: 'docs/concepts/memory-and-learning-loop' },
            { label: 'Mission history + replay', slug: 'docs/concepts/history-and-replay' },
            { label: 'Multi-model herds', slug: 'docs/concepts/multi-model-herds' },
            { label: 'The knowledge corpus', slug: 'docs/concepts/knowledge-corpus' },
            { label: 'Trust & security', slug: 'docs/concepts/trust-and-security' },
          ],
        },
        { label: 'The DuckDB warehouse', slug: 'docs/warehouse' },
        { label: 'Running it', slug: 'docs/running-it' },
        { label: 'Configuration', slug: 'docs/configuration' },
        { label: 'MCP tools reference', slug: 'docs/mcp-tools' },
        { label: 'Publishing recordings', slug: 'docs/publishing-recordings' },
        {
          // The cockpit is a post-mortem instrument, not the front door: you
          // never need it to run the gate. Kept, documented, and placed after
          // the material someone adopting corral actually needs.
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
