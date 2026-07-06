// SPDX-License-Identifier: Elastic-2.0
//
// cockpitShell.ts — pulls the four shared cockpit panels (tasks/agents/
// findings/exec) out of internal/ui/web/cockpit-shell.html, the single
// source of truth also spliced into the product's internal/ui/web/index.html
// by scripts/sync-site-assets.sh. Both Hero.astro (landing page) and
// recordings.astro import from here rather than hand-duplicating the panel
// markup — the DRY mechanism decided for the "wow cockpit" work: a Vite
// `?raw` import of a path outside the site/ root (verified working against
// `npm run build`; no fallback copy-into-site/public was needed).
import raw from '../../../internal/ui/web/cockpit-shell.html?raw';

function section(name: string): string {
  const begin = `<!-- COCKPIT-SHELL:${name}:BEGIN -->`;
  const end = `<!-- COCKPIT-SHELL:${name}:END -->`;
  const bi = raw.indexOf(begin);
  const ei = raw.indexOf(end);
  if (bi < 0 || ei < 0) {
    throw new Error(`internal/ui/web/cockpit-shell.html is missing COCKPIT-SHELL:${name} markers`);
  }
  return raw.slice(bi + begin.length, ei).trim();
}

const hudSection = section('HUD');
export const cockpitHud = hudSection;
/** Landing hero: full HUD minus #skinsel (selector lives on /recordings only). */
export const cockpitHudNoSkin = hudSection.replace(
  /<select id="skinsel"[\s\S]*?<\/select>\s*/,
  '',
);
export const cockpitViews = section('VIEWS');
export const cockpitTasks = section('TASKS');
export const cockpitAgents = section('AGENTS');
export const cockpitFindings = section('FINDINGS');
export const cockpitExec = section('EXEC');
