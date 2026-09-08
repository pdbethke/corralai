// SPDX-License-Identifier: Elastic-2.0
//
// Astro builds with compressHTML on, which collapses the newline between an
// inline tag and its neighbouring text to NOTHING rather than to a space. So
// source that reads perfectly well:
//
//     a portion of <em>freshly generated</em>
//     faults, so it moves between runs
//
// ships to the reader as "freshly generatedfaults". This shipped 32 times
// across the live site before it was caught by eye, in body copy on the home,
// warehouse and recordings pages.
//
// The gate is on the SOURCE property — "no line break sits directly against an
// inline tag boundary" — rather than on rendered strings, because that is the
// actual defect and it has no false positives. Enumerating the known-bad
// phrases would pass the moment someone writes a new paragraph.
import { test, expect } from '@playwright/test';
import { readFileSync, readdirSync, statSync } from 'node:fs';
import { join, relative } from 'node:path';

const SRC = new URL('../src', import.meta.url).pathname;
const INLINE = 'code|strong|em|a|span|abbr';
// a closing inline tag, then a line break, then text that needed the space
const AFTER = new RegExp(`</(?:${INLINE})>\\s*\\n\\s*(?=[A-Za-z0-9&(])`, 'g');
// text, then a line break, then an opening inline tag that needed the space
const BEFORE = new RegExp(`[A-Za-z0-9,.;:)—]\\s*\\n\\s*(?=<(?:${INLINE})\\b)`, 'g');
const PRE = /<pre\b[\s\S]*?<\/pre>/g;

function astroFiles(dir: string): string[] {
  const out: string[] = [];
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) out.push(...astroFiles(p));
    else if (name.endsWith('.astro')) out.push(p);
  }
  return out;
}

test('no line break eats a space against an inline tag (compressHTML)', () => {
  const files = astroFiles(SRC);

  // Floor the walk: a glob that silently finds nothing must fail, not pass.
  expect(files.length).toBeGreaterThan(10);

  const offenders: string[] = [];
  for (const file of files) {
    // <pre> is verbatim; reflowing a code sample would be the real bug.
    const body = readFileSync(file, 'utf8').replace(PRE, '');
    const hits = (body.match(AFTER)?.length ?? 0) + (body.match(BEFORE)?.length ?? 0);
    if (hits > 0) offenders.push(`${relative(SRC, file)}: ${hits}`);
  }

  expect(
    offenders,
    'These files break a line right against an inline tag, so the rendered text ' +
      'loses the space. Join the tag onto the neighbouring word.',
  ).toEqual([]);
});

test('the gate actually catches the defect it is written for', () => {
  // Negative control: prove the pattern fires, so a future refactor that
  // neuters the regex fails here instead of passing everything silently.
  const broken = `<p>\n  a portion of <em>freshly generated</em>\n  faults, so it moves.\n</p>`;
  const fixed = `<p>\n  a portion of <em>freshly generated</em> faults, so it moves.\n</p>`;
  expect(broken.match(AFTER)?.length ?? 0).toBe(1);
  expect(fixed.match(AFTER)?.length ?? 0).toBe(0);
});
