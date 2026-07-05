// SPDX-License-Identifier: Elastic-2.0
import { test, expect } from '@playwright/test';

test('the gallery renders a card per recording, plays one, shows analytics, and stays on-domain', async ({ page }) => {
  const external: string[] = [];
  const backendApiCalls: string[] = [];
  page.on('request', (req) => {
    const url = new URL(req.url());
    if (url.hostname !== 'localhost' && url.hostname !== '127.0.0.1') external.push(req.url());
    if (url.pathname.startsWith('/api/')) backendApiCalls.push(req.url());
  });

  await page.goto('/recordings/');
  const cards = page.locator('.card');
  expect(await cards.count(), 'expected at least one recording card').toBeGreaterThanOrEqual(1);

  await cards.first().click();
  await expect(async () => {
    const max = await page.locator('#replay-scrub').getAttribute('max');
    expect(Number(max)).toBeGreaterThan(0);
  }).toPass({ timeout: 5000 });

  await expect(page.locator('#analytics table').first()).toBeVisible();
  await expect(page.locator('#analytics .bar').first()).toBeVisible();

  expect(external, `unexpected external requests: ${external.join(', ')}`).toHaveLength(0);
  expect(backendApiCalls, `unexpected /api/* calls from a backend-free page: ${backendApiCalls.join(', ')}`).toHaveLength(0);
});

test('a recording with an .analysis.md shows the affordance and reveals the analysis on selection', async ({ page }) => {
  const fs = await import('node:fs');
  const slugs = fs
    .readdirSync('src/data/recordings')
    .filter((f) => f.endsWith('.analysis.md'))
    .map((f) => f.replace(/\.analysis\.md$/, ''));
  test.skip(slugs.length === 0, 'no analysis sidecars committed');

  await page.goto('/recordings/');
  const card = page.locator(`.card[data-slug="${slugs[0]}"]`);
  await expect(card.locator('.has-analysis')).toBeVisible();

  const panel = page.locator(`.analysis-panel[data-analysis-slug="${slugs[0]}"]`);
  await expect(panel).toBeHidden();
  await card.click();
  await expect(panel).toBeVisible();
  await expect(panel.locator('h2').first()).toContainText('Analysis');
});

test('the cockpit panels replay the tape: console lines appear and track the scrub position', async ({ page }) => {
  await page.goto('/recordings/');
  await page.locator('.card').first().click();
  await expect(async () => {
    const max = await page.locator('#replay-scrub').getAttribute('max');
    expect(Number(max)).toBeGreaterThan(0);
  }).toPass({ timeout: 5000 });

  const scrub = page.locator('#replay-scrub');
  const max = Number(await scrub.getAttribute('max'));
  const seek = (target: number) =>
    scrub.evaluate((el, t) => { (el as HTMLInputElement).value = String(t); el.dispatchEvent(new Event('input')); }, target);

  // Mid-tape: the console has lines, tasks and findings headers render.
  await seek(Math.floor(max / 2));
  const midConsole = await page.locator('#exec .xblk').count();
  expect(midConsole, 'expected console lines mid-tape').toBeGreaterThan(0);
  await expect(page.locator('#tasks .feedhdr')).toContainText('tasks ·');

  // The console header's total count grows with the scrub position — the
  // rendered tail is capped at 24 rows, so assert on the header's running
  // total, which reflects every execution beat accumulated so far.
  const headerCount = async () => {
    const txt = await page.locator('#exec .feedhdr').innerText();
    return Number(txt.split('·').pop()!.trim());
  };
  const midTotal = await headerCount();
  await seek(max);
  const endTotal = await headerCount();
  expect(endTotal, 'console total must grow from mid-tape to end-of-tape').toBeGreaterThan(midTotal);

  // Seek BACK rebuilds from zero: an early position must show fewer than the end.
  await seek(Math.floor(max / 10));
  const earlyTotal = await headerCount();
  expect(earlyTotal, 'seeking back must rebuild the panels from zero, not keep accumulating').toBeLessThan(endTotal);

  // Findings accumulate by end-of-tape (the golden run has real findings).
  await seek(max);
  await expect(page.locator('#findings .feedhdr')).toContainText('findings ·');
});

// Helper: open a recording by slug and wait for its tape to load.
async function openRecording(page: any, slug: string) {
  await page.goto('/recordings/');
  await page.locator(`.card[data-slug="${slug}"]`).click();
  await expect(async () => {
    expect(Number(await page.locator('#replay-scrub').getAttribute('max'))).toBeGreaterThan(0);
  }).toPass({ timeout: 5000 });
}
const seekTo = (page: any, target: number) =>
  page.locator('#replay-scrub').evaluate(
    (el: HTMLInputElement, t: number) => { el.value = String(t); el.dispatchEvent(new Event('input')); },
    target,
  );

test('findings PERSIST as the tape plays: open findings stay visible and severity-ranked, not zeroed', async ({ page }) => {
  // js-lru-cache leaves findings open across the run (the ollama herd doesn't
  // resolve everything) — the golden run resolves fast, so it's the wrong
  // fixture for a persistence assertion. Reconstructed open-count must be > 0
  // for a meaningful stretch, mirroring the product's live renderFindings.
  await openRecording(page, 'js-lru-cache');
  const max = Number(await page.locator('#replay-scrub').getAttribute('max'));
  const openCount = async () => {
    const txt = await page.locator('#findings .feedhdr').innerText();
    const m = txt.match(/findings · (\d+) open/);
    return m ? Number(m[1]) : 0;
  };
  let sawOpen = 0;
  for (const frac of [0.25, 0.5, 0.75]) {
    await seekTo(page, Math.floor(max * frac));
    const n = await openCount();
    if (n > 0) sawOpen++;
    // whenever the header says N open, exactly that many severity rows render
    if (n > 0) expect(await page.locator('#findings .frow').count()).toBe(n);
  }
  expect(sawOpen, 'findings must stay OPEN across the tape (persist), not collapse to 0').toBeGreaterThan(0);

  // Rebuild-from-0 still applies to findings: seeking back to the very start
  // must clear the accumulated open set.
  await seekTo(page, 0);
  expect(await page.locator('#findings .frow').count()).toBe(0);
});

test('the agents roster reconstructs the herd from claims + executions', async ({ page }) => {
  await openRecording(page, 'golden-run');
  const max = Number(await page.locator('#replay-scrub').getAttribute('max'));
  await seekTo(page, Math.floor(max * 0.8));
  await expect(page.locator('#agents .feedhdr')).toContainText('agents ·');
  // the golden run's herd has many named workers (Bob, Tess, Sage, …) each
  // with a role — assert several rows, each carrying a role label.
  const rows = page.locator('#agents .arow');
  expect(await rows.count(), 'expected multiple agents in the roster').toBeGreaterThan(2);
  await expect(page.locator('#agents .ameta').first()).not.toBeEmpty();

  // Regression guard: the panel classes (.arow/.feedhdr/…) are injected at
  // RUNTIME by the shared renderer, so they carry no Astro scope attribute —
  // the CSS must be :global or it silently doesn't apply. Assert the layout
  // rule actually took (flex row), not the unstyled block fallback.
  const display = await rows.first().evaluate((el) => getComputedStyle(el).display);
  expect(display, 'cockpit panel classes must be :global — runtime elements get no scope attr').toBe('flex');

  // seek back to 0 rebuilds the roster from scratch
  await seekTo(page, 0);
  expect(await page.locator('#agents .arow').count(), 'roster must rebuild from 0').toBe(0);
});

test('the console surfaces BOTH the builder and the tester (command from subject, not detail.command)', async ({ page }) => {
  // python-ratelimit: Bob (builder) ran 5 commands, Tess (tester) ran 3 — the
  // console reads the command from the beat's `subject`, so both actors appear.
  await openRecording(page, 'python-ratelimit');
  const max = Number(await page.locator('#replay-scrub').getAttribute('max'));
  await seekTo(page, max);
  const actors = await page.locator('#exec .xcmdline b').allInnerTexts();
  const set = new Set(actors.map((a) => a.trim()));
  expect(set.has('Bob'), 'the builder Bob must appear in the console').toBeTruthy();
  expect(set.has('Tess'), 'the tester Tess must appear in the console too').toBeTruthy();
  // commands are real text (from subject), not empty
  await expect(page.locator('#exec .xcmd').first()).not.toBeEmpty();
});

test('pressing play at the end restarts from the top instead of sitting dead', async ({ page }) => {
  await openRecording(page, 'python-ratelimit');
  const scrub = page.locator('#replay-scrub');
  const max = Number(await scrub.getAttribute('max'));
  await seekTo(page, max);
  await expect(page.locator('#replay-label')).toContainText(`${max} / ${max}`);
  await page.locator('#replay-playbtn').click(); // play at end → restart from 0
  await expect(async () => {
    const v = Number(await scrub.inputValue());
    expect(v, 'play-at-end must rewind to the start and play forward').toBeLessThan(max);
  }).toPass({ timeout: 3000 });
});

test('the cockpit skin selector re-voices the replay client-side and never leaks into the hero', async ({ page }) => {
  await openRecording(page, 'golden-run');
  const skinsel = page.locator('#skinsel');
  await expect(skinsel).toBeVisible();
  await expect(skinsel).toHaveValue('ranch'); // default ranch
  await expect(skinsel.locator('option')).toHaveCount(4); // ranch, flock, matrix, hive

  await skinsel.selectOption('matrix');
  // matrix re-voices the replay title (SKINS.matrix.replay) — pure client render
  await expect(page.locator('#replay-title')).toContainText('construct');
  // the page keeps its own title, not the product's skin subtitle
  await expect(page).toHaveTitle('Corralai — recordings');
  // persistence is reset to ranch so the choice is session-only and never
  // leaks into the landing hero (shared origin)
  const persisted = await page.evaluate(() => {
    try { return localStorage.getItem('corral-skin'); } catch (_) { return null; }
  });
  expect(persisted, 'the site keeps the persisted skin at ranch so the hero stays clean').toBe('ranch');
});

test('thought beats render distinctly from actions in the console, interleaved by ts, and rebuild on scrub-back', async ({ page }) => {
  // No committed recording carries kind="thought" beats yet (the story
  // engine's narration is new — see internal/brain/thought.go), so this
  // seeds a small synthetic tape directly into the shared player, exactly
  // the way Hero.astro / recordings.astro already feed it a resolved
  // {events:[...]} object (startReplay accepts either a URL or that shape).
  await page.goto('/recordings/');
  await page.evaluate(() => {
    (window as any).startReplay({
      events: [
        { ts: 1, kind: 'task_created', subject: 't1', detail: { role: 'builder', title: 'wire the loop' } },
        { ts: 2, kind: 'task_claimed', actor: 'Bob', subject: 't1', detail: { role: 'builder', title: 'wire the loop' } },
        { ts: 3, kind: 'thought', actor: 'Bob', detail: { role: 'builder', text: 'Checking the interface before I write the loop — want to match its retry contract exactly.' } },
        { ts: 4, kind: 'execution', actor: 'Bob', subject: 'go test ./internal/loop/...', detail: { ok: true, exit_code: 0, role: 'builder' } },
        { ts: 5, kind: 'thought', actor: 'Bob', detail: { role: 'builder', text: 'Green. Moving on to the retry backoff case next.' } },
      ],
    });
  });
  await expect(async () => {
    expect(Number(await page.locator('#replay-scrub').getAttribute('max'))).toBe(5);
  }).toPass({ timeout: 5000 });

  // Full tape: two thought rows + one exec row, in tape order (thought,
  // exec, thought) — the console feed interleaves by ts, not by kind.
  const scrub = page.locator('#replay-scrub');
  const seek = (t: number) => scrub.evaluate((el, v) => { (el as HTMLInputElement).value = String(v); el.dispatchEvent(new Event('input')); }, t);
  await seek(5);
  const blocks = page.locator('#exec .xblk');
  await expect(blocks).toHaveCount(3);
  const kinds = await blocks.evaluateAll((els) => els.map((el) => (el.classList.contains('xthought') ? 'thought' : 'exec')));
  expect(kinds, 'thought and exec beats must interleave in tape order').toEqual(['thought', 'exec', 'thought']);

  // Visually distinct: a thought row carries the 💭 affix + italic muted
  // text and NONE of the action chrome (❯ prompt, <code> command, ✓/✗ badge).
  const thoughtRow = blocks.nth(0);
  await expect(thoughtRow.locator('.xthoughtico')).toContainText('💭');
  await expect(thoughtRow.locator('.xthoughttext')).toBeVisible();
  expect(await thoughtRow.locator('.xprompt').count(), 'a thought row must not carry the action ❯ prompt').toBe(0);
  expect(await thoughtRow.locator('.xcmd').count(), 'a thought row must not carry an action command').toBe(0);
  expect(await thoughtRow.locator('.xbadge').count(), 'a thought row must not carry an exit badge').toBe(0);
  const style = await thoughtRow.locator('.xthoughttext').evaluate((el) => getComputedStyle(el).fontStyle);
  expect(style, 'thought text must render italic, distinct from action lines').toBe('italic');

  // HONESTY: the rendered text is the agent's words verbatim — not
  // truncated, summarized, or rewritten by the UI.
  await expect(thoughtRow.locator('.xthoughttext')).toContainText(
    'Checking the interface before I write the loop — want to match its retry contract exactly.',
  );

  // Scrub-back rebuilds from zero: seeking before the first thought must
  // show fewer console lines, same rebuild-from-0 contract the other panels
  // already guarantee.
  await seek(2);
  await expect(page.locator('#exec .xblk')).toHaveCount(0);
  await seek(3);
  await expect(page.locator('#exec .xblk')).toHaveCount(1);
  await expect(page.locator('#exec .xblk').first()).toHaveClass(/xthought/);
});

test('every recording card corresponds to a committed stream + meta pair', async () => {
  const fs = await import('node:fs');
  const files = fs.readdirSync('src/data/recordings');
  const streamFiles = files.filter((f) => f.endsWith('.json') && !f.endsWith('.meta.json'));
  for (const f of streamFiles) {
    const metaName = f.replace(/\.json$/, '.meta.json');
    expect(files, `${f} is missing its ${metaName} sidecar`).toContain(metaName);
    const meta = JSON.parse(fs.readFileSync(`src/data/recordings/${metaName}`, 'utf-8'));
    expect(Array.isArray(meta.models), `${metaName} must carry a models array (may be empty for pre-model-threading recordings)`).toBe(true);
  }
});
