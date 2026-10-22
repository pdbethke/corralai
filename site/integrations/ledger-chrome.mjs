// SPDX-License-Identifier: Elastic-2.0
// Puts the site's own header, and the docs nav drawn after it, on every
// /ledger/ page.
//
// The record pages are rendered by Go (internal/recordview, via
// scripts/gen-ledger-site.sh) into public/ledger before Astro runs, so they
// cannot use Header.astro. Drawing a second header in Go was tried: it looked
// like nothing else on the site, and any copy would drift. So after the build
// this takes the header exactly as Astro rendered it on the home page — its
// markup, the scoped CSS rules that style it, the no-flash theme script and
// site-controls.js — and puts it where each record page left its slot
// (`<div data-site-chrome></div>`, written by `ledgersite -site-chrome`).
//
// Only the header's own rules are copied, not the home page's stylesheet:
// global.css restyles body, links and headings, which the record pages style
// themselves. The rules are found by the scope attribute Astro gives each
// component (data-astro-cid-xxxxxxxx), read off the chrome's markup, so a
// rule added to Header.astro or Logo.astro comes along without anyone
// listing it. Astro leaves the scope off a selector inside :has(), so the
// nav's rules that move the page over for it (`body:has(.site-side)`) are
// found by the nav's class instead.
//
// (Until 2026-10-22 the scope was read with /astro-[a-z0-9]+/, which matches
// only the "astro-cid" every scope attribute starts with: every record page
// carried every scoped rule of the home page.)
//
// It fails the build rather than ship a record page without a way back to
// the site: a missing header, a page with no slot or two.
import { readFile, readdir, writeFile } from 'node:fs/promises';
import { existsSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';

export const SLOT = '<div data-site-chrome></div>';

// splitBlocks splits CSS into its top-level `prelude{body}` blocks, honoring
// strings and comments so a brace inside either is not structure.
export function splitBlocks(css) {
  const blocks = [];
  let depth = 0, start = 0, open = -1, quote = '';
  for (let i = 0; i < css.length; i++) {
    const c = css[i];
    if (quote) {
      if (c === '\\') i++;
      else if (c === quote) quote = '';
      continue;
    }
    if (c === '"' || c === "'") { quote = c; continue; }
    if (c === '/' && css[i + 1] === '*') {
      const end = css.indexOf('*/', i + 2);
      i = end < 0 ? css.length : end + 1;
      continue;
    }
    if (c === '{') { if (depth++ === 0) open = i; }
    else if (c === '}' && --depth === 0) {
      blocks.push({ prelude: css.slice(start, open).trim(), body: css.slice(open + 1, i) });
      start = i + 1;
    }
  }
  return blocks;
}

// headerRules keeps the rules whose selector carries one of the header's
// scoped classes, inside @media blocks too, plus the --stage-* palette the
// header is drawn in (declared on :root in tokens.css), and the site's page
// background, light and dark. The record pages keep their own palette for
// everything else (their cards stay white); only the ground under them is
// the site's, so a record page is not a lighter sheet among cream ones.
// Both palettes call the background --bg, and the chrome comes after the
// record page's own <style>, so the site's value wins.
export function headerRules(css, scopes) {
  // scopes: strings any one of which in a selector makes the rule the chrome's
  const out = [];
  const stage = [];
  const bg = {};
  const decls = (body) => body.split(';').map((d) => d.trim());
  for (const { prelude, body } of splitBlocks(css)) {
    const sel = prelude.replace(/["']/g, '');
    if (prelude.startsWith('@media') || prelude.startsWith('@supports')) {
      const inner = headerRules(body, scopes).rules;
      if (inner) out.push(`${prelude}{${inner}}`);
    } else if (sel === ':root' || sel === ':root[data-theme=dark]') {
      for (const d of decls(body)) {
        if (sel === ':root' && d.startsWith('--stage-')) stage.push(d);
        if (d.startsWith('--bg:')) bg[sel] = d;
      }
    } else if (scopes.some((s) => prelude.includes(s))) {
      out.push(`${prelude}{${body}}`);
    }
  }
  for (const sel of [':root[data-theme=dark]', ':root']) if (bg[sel]) out.unshift(`${sel}{${bg[sel]}}`);
  if (stage.length) out.unshift(`:root{${stage.join(';')}}`);
  return { rules: out.join(''), stage: stage.length, bg: Object.keys(bg).length };
}

// chromeFrom pulls the header, its CSS and its scripts out of a built page.
export function chromeFrom(html, readCss) {
  // The header and the docs nav Header.astro draws straight after it.
  const header = html.match(/<header class="site-header[\s\S]*?<\/header>\s*<aside class="site-side[\s\S]*?<\/aside>/)?.[0];
  if (!header) throw new Error('ledger-chrome: no <header class="site-header"> followed by <aside class="site-side"> on the home page');
  const scopes = [...new Set(header.match(/data-astro-cid-[a-z0-9]+/g) ?? [])];
  if (!scopes.length) throw new Error('ledger-chrome: the header carries no scope attribute, so its CSS cannot be found');
  const theme = html.match(/<script>[^<]*corralai-site-theme[^<]*<\/script>/)?.[0];
  if (!theme) throw new Error('ledger-chrome: no theme script on the home page');
  const controls = html.match(/<script src="\/site-controls\.js"[^>]*><\/script>/)?.[0];
  if (!controls) throw new Error('ledger-chrome: no site-controls.js on the home page');
  const hrefs = [...html.matchAll(/<link rel="stylesheet" href="([^"]+)"/g)].map((m) => m[1]);
  const inline = [...html.matchAll(/<style>([\s\S]*?)<\/style>/g)].map((m) => m[1]);
  let css = '', stage = 0, bg = 0;
  for (const sheet of [...hrefs.map(readCss), ...inline]) {
    const got = headerRules(sheet, [...scopes, '.site-side']);
    css += got.rules;
    stage += got.stage;
    bg += got.bg;
  }
  if (!scopes.every((s) => css.includes(s))) throw new Error(`ledger-chrome: no CSS found for ${scopes.filter((s) => !css.includes(s)).join(', ')}`);
  if (!css.includes('body:has(.site-side)')) throw new Error('ledger-chrome: no rule moves the page over for the docs nav');
  if (!stage) throw new Error('ledger-chrome: no --stage-* palette found for the header');
  if (bg < 2) throw new Error('ledger-chrome: the site\'s --bg, light and dark, was not found for the record pages');
  // Copied from the home page, the header marks Home as current; on a record
  // page the Ledger link is the current one.
  const marked = header
    .replace(/ aria-current="page"/g, '')
    .replace(/<a href="\/ledger\/"/, '<a href="/ledger/" aria-current="page"');
  if (!marked.includes('aria-current="page"')) throw new Error('ledger-chrome: the header has no /ledger/ link to mark current');
  return `${theme}<style>${css}</style>${marked}${controls}`;
}

async function htmlFiles(dir) {
  const out = [];
  for (const e of await readdir(dir, { withFileTypes: true })) {
    const p = join(dir, e.name);
    if (e.isDirectory()) out.push(...(await htmlFiles(p)));
    else if (e.name.endsWith('.html')) out.push(p);
  }
  return out;
}

export default function ledgerChrome() {
  return {
    name: 'corral-ledger-chrome',
    hooks: {
      'astro:build:done': async ({ dir, logger }) => {
        const dist = fileURLToPath(dir);
        const ledger = join(dist, 'ledger');
        if (!existsSync(ledger)) {
          logger.info('no /ledger/ pages in this build (scripts/gen-ledger-site.sh not run); nothing to do');
          return;
        }
        const home = await readFile(join(dist, 'index.html'), 'utf8');
        const sheets = new Map();
        for (const m of home.matchAll(/<link rel="stylesheet" href="(\/[^"]+)"/g)) {
          sheets.set(m[1], await readFile(join(dist, m[1]), 'utf8'));
        }
        const chrome = chromeFrom(home, (href) => sheets.get(href) ?? '');
        const pages = await htmlFiles(ledger);
        for (const p of pages) {
          const page = await readFile(p, 'utf8');
          const n = page.split(SLOT).length - 1;
          if (n !== 1) throw new Error(`ledger-chrome: ${p} has ${n} site chrome slots, want 1 (render with ledgersite -site-chrome)`);
          await writeFile(p, page.replace(SLOT, chrome));
        }
        logger.info(`site header put on ${pages.length} /ledger/ page(s)`);
      },
    },
  };
}
