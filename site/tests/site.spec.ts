// SPDX-License-Identifier: Elastic-2.0
import { test, expect } from '@playwright/test';

// The same deny-list rules as scripts/scrub-golden-run.py's scan_deny(),
// reimplemented in JS since this runs in the site's Node/Playwright
// toolchain, not Python. Belt and suspenders: scripts/export-golden-run.sh
// already gates the file before it's committed — this catches a hand-edited
// commit that skipped it.
const DENY_PATTERNS: [RegExp, string][] = [
  [/[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}/g, 'email address'],
  [/\/home\/[A-Za-z0-9_.-]+/g, 'linux home-directory path'],
  [/\/Users\/[A-Za-z0-9_.-]+/g, 'macOS home-directory path'],
  [/gh[pousr]_[A-Za-z0-9]{20,}/g, 'GitHub token'],
  [/AKIA[0-9A-Z]{16}/g, 'AWS access key id'],
  [/cdt_[A-Za-z0-9]{20,}/g, 'vendor token (cdt_*)'],
  [/sk-[A-Za-z0-9]{20,}/g, 'OpenAI-shaped API key'],
  [/-----BEGIN[A-Z ]*PRIVATE KEY-----/g, 'private key material'],
  // Windows-shaped paths (drive-letter and backslash-home), mirroring the
  // two extra deny rules scrub-golden-run.py carries that scripts/export
  // didn't originally quote in Task 2's excerpt.
  [/\b[A-Za-z]:(?:\\{1,2}[A-Za-z0-9._-]+)+/g, 'Windows drive-letter path'],
  [/\\{1,2}(?:Users|home)\\{1,2}[A-Za-z0-9._-]+/g, 'Windows backslash home path'],
];

// scan_deny in scrub-golden-run.py also checks two more rules that this port
// intentionally DOES carry (parity, Important-1 below), plus two it
// deliberately does NOT: $(whoami) and $(hostname) matches. Those two are
// unportable to CI on purpose — a GitHub Actions runner's username/hostname
// is an ephemeral, meaningless container identity (e.g. "runner" /
// "fv-az123-456"), not an operator's real identity the way it is when a
// human runs the Python export tool interactively on their own machine.
// There is nothing for a CI assertion to meaningfully check there.

const IPV4_RE = /\b(?:\d{1,3}\.){3}\d{1,3}\b/g;
// Absolute paths outside the demo containers' own internal roots — mirrors
// scan_deny's GENERIC absolute-path rule, including its glued-slash
// carve-out (see below).
const PATHLIKE_RE = /(?:\/[A-Za-z0-9._-]+){2,}/g;
const SAFE_PATH_PREFIXES = ['/work', '/tmp', '/root'];

function isPrivateOrLocalIPv4(ip: string): boolean {
  const octets = ip.split('.').map(Number);
  if (octets.length !== 4 || octets.some((o) => Number.isNaN(o) || o < 0 || o > 255)) {
    return true; // not a real IPv4 (e.g. a version string like "1.26.4") — not our problem, mirrors the Python ValueError catch
  }
  const [a, b] = octets;
  if (a === 10) return true; // 10.0.0.0/8
  if (a === 172 && b >= 16 && b <= 31) return true; // 172.16.0.0/12
  if (a === 192 && b === 168) return true; // 192.168.0.0/16
  if (a === 127) return true; // loopback
  if (a === 169 && b === 254) return true; // link-local
  return false;
}

function scanDeny(text: string): string[] {
  const offenses: string[] = [];
  for (const [pattern, label] of DENY_PATTERNS) {
    const matches = text.match(pattern);
    if (matches) offenses.push(`[${label}] ${matches.join(', ')}`);
  }
  for (const m of text.matchAll(IPV4_RE)) {
    if (!isPrivateOrLocalIPv4(m[0])) offenses.push(`[non-private/non-localhost IP] ${m[0]}`);
  }
  for (const m of text.matchAll(PATHLIKE_RE)) {
    const path = m[0];
    const idx = m.index ?? 0;
    // Only a path whose leading '/' actually STARTS a token is an absolute
    // filesystem path. A '/' glued to a preceding word character is the
    // tail of a domain or Go module path (github.com/yourusername/stack)
    // — not a host path, so not a deny (same carve-out scan_deny documents
    // in scripts/scrub-golden-run.py).
    const precedingChar = idx > 0 ? text[idx - 1] : '';
    const precededByWord = idx > 0 && /[A-Za-z0-9._-]/.test(precedingChar);
    if (!precededByWord && !SAFE_PATH_PREFIXES.some((prefix) => path.startsWith(prefix))) {
      offenses.push(`[absolute path outside demo-container roots] ${path}`);
    }
  }
  return offenses;
}

test('the deny-list scan flags the Important-1 parity rules (non-private IPv4, absolute paths outside demo roots)', () => {
  // RED-first fixtures: each of these strings should trip a rule that this
  // JS port previously lacked. Run against inline samples, not the golden
  // file, so this test proves the rule itself fires independent of whether
  // the committed fixture happens to contain a violation today.
  const publicIp = scanDeny('reachable over the vpn at 203.0.113.42 during the incident');
  expect(publicIp.some((o) => o.includes('non-private/non-localhost IP'))).toBe(true);

  const hostPath = scanDeny('the secret lives at /etc/secrets/prod.env on the box');
  expect(hostPath.some((o) => o.includes('absolute path outside demo-container roots'))).toBe(true);

  // Negative cases: private/local IPs and the demo containers' own roots
  // must NOT false-positive.
  const privateIps = scanDeny('internal traffic stays on 10.0.0.5, 172.20.3.1, 192.168.1.1, and 127.0.0.1');
  expect(privateIps).toHaveLength(0);

  const safePaths = scanDeny('demo artifacts land in /work/output, /tmp/scratch, and /root/.cache');
  expect(safePaths).toHaveLength(0);

  // Glued-slash carve-out: a '/' immediately preceded by a word character
  // is a domain/module-path tail, not a host path, and must NOT fire —
  // ported faithfully from scan_deny's own documented edge case.
  const gluedSlash = scanDeny('see github.com/yourusername/stack for the source');
  expect(gluedSlash).toHaveLength(0);
});

test('the committed golden-run.json passes the deny-list scan', async () => {
  // golden-run.json is bundled via the Astro data import, not served as a
  // static asset — this test reads the committed source artifact straight
  // off disk, independent of how Astro packages it into dist/.
  const fs = await import('node:fs');
  const text = fs.readFileSync('src/data/golden-run.json', 'utf-8');
  const offenses = scanDeny(text);
  expect(offenses, `golden-run.json failed the deny-list scan:\n${offenses.join('\n')}`).toHaveLength(0);
});

test('zero non-local network requests across the whole page session, including same-origin /api/*', async ({ page }) => {
  const external: string[] = [];
  const backendApiCalls: string[] = [];
  page.on('request', (req) => {
    const url = new URL(req.url());
    if (url.hostname !== 'localhost' && url.hostname !== '127.0.0.1') {
      external.push(req.url());
    }
    // A relative fetch('/api/...') is same-origin, so it passes the
    // hostname check above as a false "local" pass — but it's the exact bug
    // class the inReplay guard in replay-player.js exists to prevent (a
    // static embed calling back to a backend that doesn't exist here). Track
    // it separately so this test actually catches that regression class.
    if (url.pathname.startsWith('/api/')) {
      backendApiCalls.push(req.url());
    }
  });

  await page.goto('/');
  // Let the replay's fetch-or-no-fetch settle and autoplay begin.
  await expect(async () => {
    const max = await page.locator('#replay-scrub').getAttribute('max');
    expect(Number(max)).toBeGreaterThan(0);
  }).toPass({ timeout: 5000 });

  // Scrub all the way to the END of the stream (not just the midpoint) so
  // the interception above observes the WHOLE 639-event golden run, not
  // half of it — seekReplay() walks every event from index 0 up to the
  // target synchronously, so scrubbing to `max` is what actually exercises
  // every event in the recording under interception.
  const scrub = page.locator('#replay-scrub');
  const max = Number(await scrub.getAttribute('max'));
  await scrub.evaluate((el, target) => {
    (el as HTMLInputElement).value = String(target);
    el.dispatchEvent(new Event('input'));
  }, max);

  // Confirm the full event count actually rendered (renderReplayScrub()
  // writes "idx / max" into #replay-label), not just that we set the input
  // value — this is the proof the whole stream played, not just that we
  // asked it to.
  await expect(page.locator('#replay-label')).toHaveText(`${max} / ${max}`, { timeout: 5000 });

  // A further settle window in case autoplay's timer loop fires one more
  // scheduled step after the seek.
  await page.waitForTimeout(500);

  expect(external, `unexpected external requests: ${external.join(', ')}`).toHaveLength(0);
  expect(
    backendApiCalls,
    `unexpected same-origin /api/* requests during a backend-free static replay: ${backendApiCalls.join(', ')}`
  ).toHaveLength(0);
});

test('replay-player.js does not clobber the site title', async ({ page }) => {
  // Regression guard for Minor-3: setSkin() in replay-player.js sets
  // document.title to the product UI's own title ("CorralAI — the corral")
  // at script-load time. Hero.astro's inline script restores the site's
  // title afterward — assert the restored value wins, not the clobbered one.
  await page.goto('/');
  await expect(page).toHaveTitle('Corralai — the herd performs live');
});

test('the GitHub link resolves and points at the real repo, everywhere it appears', async ({ page, request }) => {
  await page.goto('/');
  const links = page.locator('a[href*="github.com/pdbethke/corralai"]');
  const count = await links.count();
  // Both the hero CTA and the footer carry this link — asserting >=2 here
  // (rather than just "first() is truthy") catches a future edit that
  // silently drops one of them.
  expect(count, 'expected the GitHub link to appear in both the hero CTA and the footer').toBeGreaterThanOrEqual(2);

  const hrefs = await links.evaluateAll((els) => els.map((el) => el.getAttribute('href')));
  for (const href of hrefs) {
    expect(href).toBeTruthy();
  }

  // Only actually fetch the URL once — hitting github.com per matched link
  // is redundant network chatter for an identical href.
  const href = hrefs[0]!;
  const res = await request.get(href);
  expect(res.status(), `GET ${href} returned ${res.status()}`).toBeLessThan(400);
});

test('the page has no obvious accessibility footguns', async ({ page }) => {
  await page.goto('/');
  // Quick, dependency-free a11y pass (no @axe-core/playwright in this
  // toolchain) rather than a full audit: a <title>, a single <h1>, and an
  // accessible name on every link/button/img, which covers the failure
  // modes most likely from hand-authored marketing markup.
  await expect(page).toHaveTitle(/.+/);

  const h1Count = await page.locator('h1').count();
  expect(h1Count, 'expected exactly one <h1> on the page').toBe(1);

  const images = page.locator('img');
  const imgCount = await images.count();
  for (let i = 0; i < imgCount; i++) {
    const alt = await images.nth(i).getAttribute('alt');
    expect(alt, `image ${i} is missing an alt attribute`).not.toBeNull();
  }

  const links = page.locator('a');
  const linkCount = await links.count();
  for (let i = 0; i < linkCount; i++) {
    const link = links.nth(i);
    const text = (await link.innerText()).trim();
    const ariaLabel = await link.getAttribute('aria-label');
    expect(
      text.length > 0 || (ariaLabel && ariaLabel.length > 0),
      `link ${i} has no visible text and no aria-label`
    ).toBeTruthy();
  }
});
