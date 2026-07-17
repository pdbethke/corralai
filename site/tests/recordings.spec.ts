// SPDX-License-Identifier: Elastic-2.0
import { test, expect } from '@playwright/test';
import fs from 'node:fs';

const GENERATED_RECORDINGS_DIR = 'src/data/generated/recordings';
const LEGACY_RECORDINGS_DIR = 'src/data/recordings';

const generatedStreamsPresent =
  fs.existsSync(GENERATED_RECORDINGS_DIR) &&
  fs.readdirSync(GENERATED_RECORDINGS_DIR).some((f) => f.endsWith('.json') && !f.endsWith('.meta.json'));
const RECORDINGS_DIR = generatedStreamsPresent ? GENERATED_RECORDINGS_DIR : LEGACY_RECORDINGS_DIR;
const RECORDING_SLUGS = fs.existsSync(RECORDINGS_DIR)
  ? fs.readdirSync(RECORDINGS_DIR)
      .filter((f) => f.endsWith('.json') && !f.endsWith('.meta.json'))
      .map((f) => f.replace(/\.json$/, ''))
  : [];

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
  const slugs = fs
    .readdirSync(RECORDINGS_DIR)
    .filter((f) => f.endsWith('.analysis.md'))
    .map((f) => f.replace(/\.analysis\.md$/, ''));
  test.skip(slugs.length === 0, 'no analysis sidecars found in active recordings source');

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
    // The header now also carries per-agent filter chips (an unfiltered
    // header is "console · replaying the tape · N <chips…>") — pull the
    // leading run-total digits out with a regex rather than assuming the
    // count is the last '·'-delimited segment, which the chips broke.
    const txt = await page.locator('#exec .feedhdr').innerText();
    const m = txt.match(/replaying the tape\s*·\s*(\d+)/);
    return m ? Number(m[1]) : NaN;
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
  const slug = RECORDING_SLUGS.find((s) => s === 'js-lru-cache');
  test.skip(!slug, 'requires js-lru-cache recording');
  await openRecording(page, slug!);
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
  const slug = RECORDING_SLUGS.find((s) => s === 'golden-run') || RECORDING_SLUGS[0];
  test.skip(!slug, 'no recordings available');
  await openRecording(page, slug!);
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
  // The roster is rebuilt via innerHTML on seek/step, so a handle grabbed with
  // rows.first() can be detached before .evaluate() runs — getComputedStyle()
  // then returns '' on the orphaned node, a flake unrelated to the CSS itself.
  // Re-resolve the locator each attempt so we read a live node; the invariant
  // under test is that the :global rule applies (display: flex, not the
  // unstyled block fallback).
  await expect(async () => {
    const display = await page
      .locator('#agents .arow')
      .first()
      .evaluate((el) => getComputedStyle(el).display);
    expect(display, 'cockpit panel classes must be :global — runtime elements get no scope attr').toBe('flex');
  }).toPass({ timeout: 5000 });

  // seek back to 0 rebuilds the roster from scratch
  await seekTo(page, 0);
  expect(await page.locator('#agents .arow').count(), 'roster must rebuild from 0').toBe(0);
});

test('clicking a roster agent opens the faithful floating inspector window (tape-only, no /api/*, survives scrub)', async ({ page }) => {
  const slug = RECORDING_SLUGS.find((s) => s === 'golden-run') || RECORDING_SLUGS[0];
  test.skip(!slug, 'no recordings available');

  // HARD CONSTRAINT: the window must reconstruct detail from the tape alone —
  // never a brain call. Fail the test on ANY /api/* request the click triggers.
  const apiCalls: string[] = [];
  page.on('request', (req) => {
    if (new URL(req.url()).pathname.startsWith('/api/')) apiCalls.push(req.url());
  });

  await openRecording(page, slug!);
  const scrub = page.locator('#replay-scrub');
  const max = Number(await scrub.getAttribute('max'));

  // Park the playhead at the end so playback stops and the roster is static.
  await seekTo(page, max);
  await expect(page.locator('#replay-label')).toContainText(`${max} / ${max}`);

  const firstRow = page.locator('#agents .arow').first();
  await expect(firstRow).toBeVisible();
  const name = (await firstRow.locator('b').innerText()).trim();
  const role = (await firstRow.locator('.ameta').innerText()).trim();

  // Click the row → the ported .aw-win floating window appears (mirrors the
  // product's selectAgent → openAgentWindow).
  await firstRow.click();
  const win = page.locator('.aw-win');
  await expect(win).toBeVisible();
  await expect(win.locator('.aw-title b')).toHaveText(name);
  if (role) await expect(win.locator('.aw-role')).toHaveText(role);

  // The clicked row picks up the .arowsel selected-state, exactly like the app.
  await expect(page.locator('#agents .arow.arowsel')).toHaveCount(1);

  // Body is reconstructed from the tape: the stats section is always present.
  await expect(win.locator('.aw-body')).toContainText('holding');
  await expect(win.locator('.aw-body')).toContainText('completed');
  // The ask box is present (visual parity) but is backend-free by contract.
  await expect(win.locator('.aw-ask-input')).toBeVisible();

  // Survives a scrub: seek elsewhere; the window stays open and repaints.
  await seekTo(page, Math.floor(max * 0.4));
  await expect(win).toBeVisible();
  await expect(win.locator('.aw-body')).toContainText('holding');

  // Close button removes it.
  await win.locator('.aw-close').click();
  await expect(page.locator('.aw-win')).toHaveCount(0);

  expect(apiCalls, `the tape-only inspector must never call the brain: ${apiCalls.join(', ')}`).toHaveLength(0);
});

test('the inspector window populates LIVE as the tape PLAYS forward (not just on scrub)', async ({ page }) => {
  // Prove the window repaints on every playback tick (replayStep →
  // renderReplayScrub → renderReplayPanels → refreshReplayWindows), not only
  // on a manual seek. A small synthetic single-actor tape makes the growth
  // deterministic: Bob claims + finishes two tasks, running a command each.
  const apiCalls: string[] = [];
  page.on('request', (req) => {
    if (new URL(req.url()).pathname.startsWith('/api/')) apiCalls.push(req.url());
  });

  await page.goto('/recordings/');
  await page.evaluate(() => {
    (window as any).startReplay({
      events: [
        { ts: 1, kind: 'task_created', subject: 't1', detail: { role: 'builder', title: 'wire the loop' } },
        { ts: 2, kind: 'task_claimed', actor: 'Bob', subject: 't1', detail: { role: 'builder', title: 'wire the loop' } },
        { ts: 3, kind: 'execution', actor: 'Bob', subject: 'go build ./...', detail: { ok: true, exit_code: 0, role: 'builder' } },
        { ts: 4, kind: 'task_done', actor: 'Bob', subject: 't1', detail: { role: 'builder' } },
        { ts: 5, kind: 'task_created', subject: 't2', detail: { role: 'builder', title: 'add retry backoff' } },
        { ts: 6, kind: 'task_claimed', actor: 'Bob', subject: 't2', detail: { role: 'builder', title: 'add retry backoff' } },
        { ts: 7, kind: 'execution', actor: 'Bob', subject: 'go test ./...', detail: { ok: true, exit_code: 0, role: 'builder' } },
        { ts: 8, kind: 'task_done', actor: 'Bob', subject: 't2', detail: { role: 'builder' } },
      ],
    });
  });
  await expect(async () => {
    expect(Number(await page.locator('#replay-scrub').getAttribute('max'))).toBe(8);
  }).toPass({ timeout: 5000 });

  const scrub = page.locator('#replay-scrub');
  const seek = (t: number) => scrub.evaluate((el, v) => { (el as HTMLInputElement).value = String(v); el.dispatchEvent(new Event('input')); }, t);

  // Park just after Bob claims t1 (paused: startReplay leaves replayPlaying
  // false), open his window, and capture the early state.
  await seek(2);
  const row = page.locator('#agents .arow', { hasText: 'Bob' });
  await expect(row).toBeVisible();
  await row.click();
  const body = page.locator('.aw-win .aw-body');
  await expect(body).toBeVisible();
  await expect(body).toContainText('holding 1');
  await expect(body).toContainText('completed 0');
  await expect(body).toContainText('wire the loop');

  // Now PLAY forward from here (fast) — the window must grow on the timer
  // ticks, with no interaction and no scrub.
  await page.evaluate(() => { (window as any).setReplaySpeed(16); (window as any).toggleReplayPlay(); });

  // As the tape advances, completed climbs to 2 and the working-on flips to
  // the second task — all driven purely by playback repaints.
  await expect(body).toContainText('completed 2', { timeout: 5000 });
  await expect(body).toContainText('add retry backoff');
  await expect(body).toContainText('go test ./...');

  expect(apiCalls, `live-updating window must stay tape-only: ${apiCalls.join(', ')}`).toHaveLength(0);
});

test('the console surfaces BOTH the builder and the tester (command from subject, not detail.command)', async ({ page }) => {
  // python-ratelimit: Bob (builder) ran 5 commands, Tess (tester) ran 3 — the
  // console reads the command from the beat's `subject`, so both actors appear.
  const slug = RECORDING_SLUGS.find((s) => s === 'python-ratelimit');
  test.skip(!slug, 'requires python-ratelimit recording');
  await openRecording(page, slug!);
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
  const slug = RECORDING_SLUGS.find((s) => s === 'python-ratelimit') || RECORDING_SLUGS[0];
  test.skip(!slug, 'no recordings available');
  await openRecording(page, slug!);
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

test('the cockpit skin selector lives in the HUD, applies a real visual palette (matrix → green), and persists', async ({ page }) => {
  const slug = RECORDING_SLUGS.find((s) => s === 'golden-run') || RECORDING_SLUGS[0];
  test.skip(!slug, 'no recordings available');
  await openRecording(page, slug!);

  // The selector is restored to the cockpit HUD (previously suppressed by
  // cockpitHudNoSkin) — it must be VISIBLE and sit inside #hud.
  const skinsel = page.locator('#skinsel');
  await expect(skinsel).toBeVisible();
  await expect(page.locator('#hud #skinsel')).toHaveCount(1);
  await expect(skinsel).toHaveValue('ranch'); // default ranch
  await expect(skinsel.locator('option')).toHaveCount(4); // ranch, flock, matrix, hive

  await skinsel.selectOption('matrix');
  // matrix re-voices the replay title (SKINS.matrix.replay)…
  await expect(page.locator('#replay-title')).toContainText('construct');
  // …AND applies the matrix visual palette: data-skin on <html> + green tokens.
  await expect(page.locator('html')).toHaveAttribute('data-skin', 'matrix');
  const accent = await page.evaluate(() =>
    getComputedStyle(document.documentElement).getPropertyValue('--stage-amber').trim().toLowerCase(),
  );
  expect(accent, 'matrix accent must be phosphor green').toContain('#39ff14');
  // the page keeps its own title, not the product's skin subtitle
  await expect(page).toHaveTitle('Corralai — recordings');
  // the pick persists so the matrix view carries across the demo
  const persisted = await page.evaluate(() => {
    try { return localStorage.getItem('corral-skin'); } catch (_) { return null; }
  });
  expect(persisted, 'the skin choice persists across the demo').toBe('matrix');
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

test('pool reasoning trace renders the why in the console', async ({ page }) => {
  // The advpool run carries pool_subject/pool_dev_adequacy/pool_verdict beats
  // plus finding_reported.detail.evidence (the critic's argument) — this is
  // "show the work", the difference from Fugu. Render them as an ordered,
  // readable trace in the SAME console feed the thoughts/execs use.
  await page.goto('/recordings/');
  await page.evaluate(() => {
    (window as any).startReplay({
      events: [
        { ts: 1, kind: 'pool_subject', actor: 'corral-advpool', detail: { code_path: 'internal/fence/fence.go', dev_test_path: 'internal/fence/fence_test.go', goal: 'neutralize the fence', code: 'package fence', dev_test_code: 'package fence' } },
        { ts: 2, kind: 'pool_dev_adequacy', detail: { dev_kill_rate: 0.8, mutants_total: 5, survivors: 1, survivor_ids: ['m3'] } },
        { ts: 3, kind: 'finding_reported', subject: 'TestX', detail: { type: 'note', severity: 'high', evidence: 'the test asserts nothing' } },
        { ts: 4, kind: 'pool_verdict', detail: { status: 'certified', dev_kill_rate: 0.8, mutants_total: 5, survivors: 1, proven_missed: 0, models_by_role: { 'test-critic': 'gemini-3.5-flash', 'test-writer': 'claude-sonnet-5' }, record_id: 5, record_head: 'abc' } },
      ],
    });
  });
  await expect(async () => {
    expect(Number(await page.locator('#replay-scrub').getAttribute('max'))).toBe(4);
  }).toPass({ timeout: 5000 });

  const scrub = page.locator('#replay-scrub');
  const seek = (t: number) => scrub.evaluate((el, v) => { (el as HTMLInputElement).value = String(v); el.dispatchEvent(new Event('input')); }, t);
  await seek(4);

  const exec = page.locator('#exec');
  await expect(exec).toContainText('grading internal/fence/fence.go');
  await expect(exec).toContainText('killed 4/5');       // 5 total - 1 survivor
  await expect(exec).toContainText('the test asserts nothing'); // critic evidence surfaced
  await expect(exec).toContainText('CERTIFIED');
  await expect(exec).toContainText('record 5');
  await expect(page.locator('#exec .xpool')).toHaveCount(3); // subject + adequacy + verdict beats
});

test('the console per-agent filter isolates one actor\'s thoughts AND commands, and survives scrub/seek', async ({ page }) => {
  // Synthetic two-actor tape: Bob (builder) and Tess (tester) interleave
  // thoughts and executions. The filter chips must let a viewer isolate
  // Bob's whole stream (reasoning + action) and hide Tess's entirely — the
  // launch-relevant "follow one agent's story on a busy tape" addendum.
  await page.goto('/recordings/');
  await page.evaluate(() => {
    (window as any).startReplay({
      events: [
        { ts: 1, kind: 'task_created', subject: 't1', detail: { role: 'builder', title: 'wire the loop' } },
        { ts: 2, kind: 'task_claimed', actor: 'Bob', subject: 't1', detail: { role: 'builder', title: 'wire the loop' } },
        { ts: 3, kind: 'thought', actor: 'Bob', detail: { role: 'builder', text: 'Bob is thinking about the retry loop.' } },
        { ts: 4, kind: 'execution', actor: 'Bob', subject: 'go build ./...', detail: { ok: true, exit_code: 0, role: 'builder' } },
        { ts: 5, kind: 'thought', actor: 'Tess', detail: { role: 'tester', text: 'Tess is thinking about coverage.' } },
        { ts: 6, kind: 'execution', actor: 'Tess', subject: 'go test ./...', detail: { ok: true, exit_code: 0, role: 'tester' } },
        { ts: 7, kind: 'thought', actor: 'Bob', detail: { role: 'builder', text: 'Bob wraps up the loop.' } },
      ],
    });
  });
  await expect(async () => {
    expect(Number(await page.locator('#replay-scrub').getAttribute('max'))).toBe(7);
  }).toPass({ timeout: 5000 });

  const scrub = page.locator('#replay-scrub');
  const seek = (t: number) => scrub.evaluate((el, v) => { (el as HTMLInputElement).value = String(v); el.dispatchEvent(new Event('input')); }, t);
  await seek(7);

  // Both actors' chips render (plus "all"), and by default every beat shows.
  const chips = page.locator('#exec .xchip');
  await expect(chips).toHaveCount(3); // all, Bob, Tess
  await expect(page.locator('#exec .xblk')).toHaveCount(5); // 3 Bob beats + 2 Tess beats

  // Click Bob's chip: only Bob's thoughts + commands remain, Tess vanishes.
  await page.locator('#exec .xchip', { hasText: 'Bob' }).click();
  await expect(page.locator('#exec .xblk')).toHaveCount(3);
  const bobTexts = await page.locator('#exec .xblk').allInnerTexts();
  expect(bobTexts.join(' ')).toContain('Bob is thinking about the retry loop');
  expect(bobTexts.join(' ')).toContain('go build');
  expect(bobTexts.join(' ')).toContain('Bob wraps up the loop');
  for (const t of bobTexts) {
    expect(t, 'Tess must not appear in a Bob-filtered feed').not.toContain('Tess');
  }

  // The filter survives a scrub/seek rebuild-from-0: seek back to mid-tape,
  // Bob's isolation still applies (Tess's beats at ts 5/6 stay hidden).
  await seek(4);
  await expect(page.locator('#exec .xblk')).toHaveCount(2); // Bob's claim-adjacent thought + build, not yet Tess's
  const midTexts = (await page.locator('#exec .xblk').allInnerTexts()).join(' ');
  expect(midTexts).not.toContain('Tess');

  // "all" chip restores the full merged feed.
  await page.locator('#exec .xchip', { hasText: 'all' }).click();
  await seek(7);
  await expect(page.locator('#exec .xblk')).toHaveCount(5);
});

test('the files lens reconstructs the touched directory tree, colored by claiming agent, scrubs, and stays tape-only', async ({ page }) => {
  // Synthetic tape with path-claim beats (claim_made/claim_released) — the v2
  // merge folds these into a real recording's stream, but no committed golden
  // carries them yet, so seed them directly the way the other synthetic tests
  // do. Bob (builder) and Tess (tester) claim files under internal/loop; Bob
  // also grabs cmd/main.go then RELEASES it, so it stays in the tree dimmed.
  const apiCalls: string[] = [];
  page.on('request', (req) => {
    if (new URL(req.url()).pathname.startsWith('/api/')) apiCalls.push(req.url());
  });

  await page.goto('/recordings/');
  await page.evaluate(() => {
    (window as any).startReplay({
      events: [
        { ts: 1, kind: 'task_claimed', actor: 'Bob', subject: 't1', detail: { role: 'builder', title: 'wire the loop' } },
        { ts: 2, kind: 'claim_made', actor: 'Bob', subject: 'internal/loop/run.go', detail: { path: 'internal/loop/run.go', exclusive: true } },
        { ts: 3, kind: 'task_claimed', actor: 'Tess', subject: 't2', detail: { role: 'tester', title: 'cover the loop' } },
        { ts: 4, kind: 'claim_made', actor: 'Tess', subject: 'internal/loop/run_test.go', detail: { path: 'internal/loop/run_test.go' } },
        { ts: 5, kind: 'claim_made', actor: 'Bob', subject: 'cmd/main.go', detail: { path: 'cmd/main.go', exclusive: true } },
        { ts: 6, kind: 'claim_released', actor: 'Bob', subject: 'cmd/main.go', detail: { path: 'cmd/main.go' } },
      ],
    });
  });
  await expect(async () => {
    expect(Number(await page.locator('#replay-scrub').getAttribute('max'))).toBe(6);
  }).toPass({ timeout: 5000 });

  // Switch to the files tab — its panel shows and the scrub transport stays up
  // (the lens is playhead-driven, like the swarm canvas).
  await page.evaluate(() => (window as any).setView('files'));
  await expect(page.locator('#files')).toHaveClass(/show/);
  await expect(page.locator('#replay')).toHaveClass(/show/);

  const scrub = page.locator('#replay-scrub');
  const seek = (t: number) =>
    scrub.evaluate((el, v) => { (el as HTMLInputElement).value = String(v); el.dispatchEvent(new Event('input')); }, t);

  // Before the first claim the tree is empty (rebuild-from-0 contract).
  await seek(1);
  await expect(page.locator('#files .ft-file')).toHaveCount(0);
  await expect(page.locator('#files .ft-empty')).toBeVisible();

  // Scrub forward: the tree fills in as claims accumulate.
  await seek(6);
  await expect(page.locator('#files .ft-file')).toHaveCount(3); // run.go, run_test.go, cmd/main.go
  await expect(page.locator('#files .ft-dir')).not.toHaveCount(0); // internal/, loop/, cmd/ reconstructed
  await expect(page.locator('#files')).toContainText('run.go');
  await expect(page.locator('#files')).toContainText('internal');

  // Colored by the claiming agent: each actively-held file's dot is filled with
  // its owner's role color, and the builder's differs from the tester's.
  const dotColors = await page
    .locator('#files .ft-file:not(.ft-released) .ft-dot')
    .evaluateAll((els) => els.map((el) => getComputedStyle(el).backgroundColor));
  expect(dotColors.length, 'expected at least two actively-held colored files').toBeGreaterThanOrEqual(2);
  for (const c of dotColors) {
    expect(c, 'a held file dot must be filled with the claiming agent color').not.toBe('rgba(0, 0, 0, 0)');
  }
  expect(new Set(dotColors).size, 'builder and tester files must be colored differently').toBeGreaterThan(1);

  // A released path stays in the tree (still "touched") but dimmed, not filled.
  const released = page.locator('#files .ft-file.ft-released');
  await expect(released).toHaveCount(1);
  await expect(released).toContainText('main.go');

  // Seek back rebuilds from zero, exactly like the other panels.
  await seek(1);
  await expect(page.locator('#files .ft-file')).toHaveCount(0);

  expect(apiCalls, `the files lens must reconstruct from the tape alone: ${apiCalls.join(', ')}`).toHaveLength(0);
});

// A small synthetic tape carrying task LINEAGE (depends_on + instruction),
// two workers, and a command each — deterministic ground truth for the task
// storytelling modal + the dim-completed behavior. Seeded straight into the
// shared player exactly like the thought/files tests (startReplay accepts a
// resolved {events} object), so it's tape-only by construction.
const LINEAGE_TAPE = {
  events: [
    { ts: 1, kind: 'task_created', subject: 't1', detail: { role: 'builder', title: 'wire the loop', instruction: 'implement the retry loop', depends_on: [] } },
    { ts: 2, kind: 'task_created', subject: 't2', detail: { role: 'tester', title: 'cover the loop', instruction: 'add table-driven tests', depends_on: ['t1'] } },
    { ts: 3, kind: 'task_claimed', actor: 'Bob', subject: 't1', detail: { role: 'builder', title: 'wire the loop' } },
    { ts: 4, kind: 'execution', actor: 'Bob', subject: 'go build ./...', detail: { ok: true, exit_code: 0, role: 'builder' } },
    { ts: 5, kind: 'task_done', actor: 'Bob', subject: 't1', detail: { role: 'builder' } },
    { ts: 6, kind: 'task_claimed', actor: 'Tess', subject: 't2', detail: { role: 'tester', title: 'cover the loop' } },
    { ts: 7, kind: 'execution', actor: 'Tess', subject: 'go test ./...', detail: { ok: true, exit_code: 0, role: 'tester' } },
    { ts: 8, kind: 'task_done', actor: 'Tess', subject: 't2', detail: { role: 'tester' } },
  ],
};

async function seedLineageTape(page: any) {
  await page.goto('/recordings/');
  await page.evaluate((tape: any) => (window as any).startReplay(tape), LINEAGE_TAPE);
  await expect(async () => {
    expect(Number(await page.locator('#replay-scrub').getAttribute('max'))).toBe(8);
  }).toPass({ timeout: 5000 });
}
const seekIdx = (page: any, t: number) =>
  page.locator('#replay-scrub').evaluate(
    (el: HTMLInputElement, v: number) => { el.value = String(v); el.dispatchEvent(new Event('input')); },
    t,
  );

test('a task ROW opens the storytelling modal — what was done, who did it, and a clickable NEXT link that walks the causal chain', async ({ page }) => {
  const apiCalls: string[] = [];
  page.on('request', (req) => { if (new URL(req.url()).pathname.startsWith('/api/')) apiCalls.push(req.url()); });

  await seedLineageTape(page);
  await seekIdx(page, 8); // whole story reconstructed

  // Click the t1 task ROW → its story modal (the .aw-win chrome, task variant).
  await page.locator('#tasks .trow', { hasText: 'wire the loop' }).click();
  const t1 = page.locator('.aw-win.aw-task').filter({ hasText: 'implement the retry loop' });
  await expect(t1).toBeVisible();
  // WHAT was done: the instruction + the command Bob ran while holding it.
  await expect(t1).toContainText('implement the retry loop');
  await expect(t1).toContainText('go build ./...');
  // WHO did it: the assigned worker, as a clickable chain link.
  await expect(t1.locator('.aw-chain', { hasText: 'Bob' })).toBeVisible();
  // WHAT CAME NEXT: t1 unblocked t2 — a clickable link into the next task.
  await expect(t1).toContainText('what came next');
  const nextLink = t1.locator('.aw-chain', { hasText: 'cover the loop' });
  await expect(nextLink).toBeVisible();

  // Walk the chain forward: click NEXT → the t2 modal opens, and it shows the
  // reverse link ("what triggered it" → back to t1), proving lineage both ways.
  await nextLink.click();
  const t2 = page.locator('.aw-win.aw-task').filter({ hasText: 'add table-driven tests' });
  await expect(t2).toBeVisible();
  await expect(t2).toContainText('what triggered it');
  await expect(t2.locator('.aw-chain', { hasText: 'wire the loop' })).toBeVisible();
  await expect(t2.locator('.aw-chain', { hasText: 'Tess' })).toBeVisible();
  await expect(t2).toContainText('go test ./...');

  expect(apiCalls, `the task modal must reconstruct from the tape alone: ${apiCalls.join(', ')}`).toHaveLength(0);
});

test('a task NODE in the swarm canvas opens the same storytelling modal (nodes stay clickable as they drift)', async ({ page }) => {
  await seedLineageTape(page);
  await seekIdx(page, 8);
  await page.locator('#c').scrollIntoViewIfNeeded(); // canvas sits below the fold

  // Click the canvas TASK node ("p:<key>"). Nodes drift under the force layout,
  // so read the live world position and click, retrying until it lands (the hit
  // radius tolerates the small per-frame drift).
  await expect(async () => {
    const pos = await page.evaluate(() => {
      const rect = document.getElementById('c')!.getBoundingClientRect();
      const nm = (window as any).replayNodes as Map<string, any>;
      for (const n of nm.values()) {
        if (n.kind === 'path') return { x: rect.left + n.x, y: rect.top + n.y };
      }
      return null;
    });
    expect(pos, 'expected at least one task node on the canvas').not.toBeNull();
    await page.mouse.click(pos!.x, pos!.y);
    await expect(page.locator('.aw-win.aw-task')).toBeVisible({ timeout: 600 });
  }).toPass({ timeout: 8000 });
});

test('a canvas AGENT node opens its .aw-win inspector (only roster rows did before)', async ({ page }) => {
  await seedLineageTape(page);
  await seekIdx(page, 8);
  await page.locator('#c').scrollIntoViewIfNeeded(); // canvas sits below the fold

  await expect(async () => {
    const pos = await page.evaluate(() => {
      const rect = document.getElementById('c')!.getBoundingClientRect();
      const nm = (window as any).replayNodes as Map<string, any>;
      for (const n of nm.values()) {
        if (n.kind === 'agent') return { x: rect.left + n.x, y: rect.top + n.y };
      }
      return null;
    });
    expect(pos, 'expected at least one agent node on the canvas').not.toBeNull();
    await page.mouse.click(pos!.x, pos!.y);
    // an AGENT inspector window (not the task variant) must appear
    await expect(page.locator('.aw-win:not(.aw-task)')).toBeVisible({ timeout: 600 });
  }).toPass({ timeout: 8000 });
});

test('completed / superseded tasks DIM in the left list as the tape advances (live on scrub), and are never removed', async ({ page }) => {
  await seedLineageTape(page);

  // Early: both tasks just created, none finished → nothing dimmed.
  await seekIdx(page, 2);
  await expect(page.locator('#tasks .trow')).toHaveCount(2);
  await expect(page.locator('#tasks .trow.tdim')).toHaveCount(0);

  // After t1 finishes: exactly one row dims, but both rows stay in the list.
  await seekIdx(page, 5);
  await expect(page.locator('#tasks .trow.tdim')).toHaveCount(1);
  await expect(page.locator('#tasks .trow')).toHaveCount(2);

  // End of tape: both finished → both dim, still both present.
  await seekIdx(page, 8);
  await expect(page.locator('#tasks .trow.tdim')).toHaveCount(2);
  await expect(page.locator('#tasks .trow')).toHaveCount(2);
  // The dim is a real reduced-opacity, not just a class name.
  const op = await page.locator('#tasks .trow.tdim').first().evaluate((el) => Number(getComputedStyle(el).opacity));
  expect(op).toBeLessThan(1);

  // Scrub back to the start rebuilds from zero — the dim clears with the list.
  await seekIdx(page, 2);
  await expect(page.locator('#tasks .trow.tdim')).toHaveCount(0);
});

// Folded in from an aborted agent's scratch repro (site/tests/_repro.spec.ts):
// the two assertions worth keeping, now as real tests.
test('hero: clicking a roster agent mid-autoplay opens the floating window', async ({ page }) => {
  await page.goto('/');
  await expect(async () => {
    expect(Number(await page.locator('#replay-scrub').getAttribute('max'))).toBeGreaterThan(0);
  }).toPass({ timeout: 8000 });
  // let autoplay run so the roster is mid-rebuild — the click must still land
  await page.waitForTimeout(1200);
  const row = page.locator('#agents .arow').first();
  await expect(row).toBeVisible();
  await row.click();
  await expect(page.locator('.aw-win')).toBeVisible({ timeout: 2000 });
});

test('recordings: a roster row is the SAME DOM node across a playback tick (stable, reconciled in place)', async ({ page }) => {
  await page.goto('/recordings/');
  await page.locator('.card').first().click();
  await expect(async () => {
    expect(Number(await page.locator('#replay-scrub').getAttribute('max'))).toBeGreaterThan(0);
  }).toPass({ timeout: 8000 });
  const scrub = page.locator('#replay-scrub');
  const max = Number(await scrub.getAttribute('max'));
  const seek = (t: number) => scrub.evaluate((el, v) => { (el as HTMLInputElement).value = String(v); el.dispatchEvent(new Event('input')); }, t);
  await seek(Math.floor(max * 0.5));
  await page.locator('#agents .arow').first().evaluate((el) => ((window as any).__n = el));
  await seek(Math.floor(max * 0.6));
  const same = await page.locator('#agents .arow').first().evaluate((el) => el === (window as any).__n);
  expect(same, 'the roster must reconcile rows in place — a stable node, never re-created per tick').toBe(true);
});

test('the cockpit body is bounded and the task list scrolls inside it (never grows the page)', async ({ page }) => {
  await page.goto('/recordings/');
  await page.locator('.card').first().click();
  await expect(async () => {
    expect(Number(await page.locator('#replay-scrub').getAttribute('max'))).toBeGreaterThan(0);
  }).toPass({ timeout: 5000 });
  // The task column is a bounded, independently-scrolling pane (overflow auto),
  // and the cockpit body height is capped (grid-template-rows minmax), so a
  // 60-task run scrolls the list rather than stretching the whole cockpit.
  const overflow = await page.locator('.cockpit-tasks').evaluate((el) => getComputedStyle(el).overflowY);
  expect(overflow).toBe('auto');
  const bounded = await page.locator('#cockpit').evaluate((el) => {
    const rows = getComputedStyle(el).gridTemplateRows;
    return rows && rows !== 'none' && rows !== '';
  });
  expect(bounded, 'the cockpit body must be bounded to a fixed row track').toBe(true);
});

test('task story shows the produced artifact (result)', async ({ page }) => {
  await page.goto('/recordings/');
  await page.evaluate(() => {
    (window as any).startReplay({ events: [
      { ts: 1, kind: 'task_created', subject: 'mutant-generator', detail: { role: 'mutant-generator', title: 'plant faults' } },
      { ts: 2, kind: 'task_claimed', subject: 'mutant-generator', actor: 'claude-writer', detail: {} },
      { ts: 3, kind: 'task_done', subject: 'mutant-generator', actor: 'claude-writer', detail: { result: '===MUTATION_1===\npackage fence\n// the planted fault' } },
    ] });
  });
  await expect(async () => {
    expect(Number(await page.locator('#replay-scrub').getAttribute('max'))).toBe(3);
  }).toPass({ timeout: 5000 });
  const scrub = page.locator('#replay-scrub');
  await scrub.evaluate((el) => { (el as HTMLInputElement).value = '3'; el.dispatchEvent(new Event('input')); });
  const row = page.locator('#tasks .trow', { hasText: 'plant faults' });
  await expect(row).toBeVisible();
  await row.click();
  const body = page.locator('.aw-win .aw-body');
  await expect(body).toBeVisible();
  await expect(body).toContainText('the planted fault');
});

test('fault highlight marks the mutated lines', async ({ page }) => {
  // The founder's key transparency affordance: when the original code under
  // review is on the tape (pool_subject.detail.code), the mutant-generator
  // task story diffs the SURVIVING mutant against it and highlights just the
  // planted-fault line — "here is the exact fault, and your tests pass
  // anyway" — instead of dumping the raw ===MUTATION_n=== blob.
  await page.goto('/recordings/');
  await page.evaluate(() => {
    (window as any).startReplay({
      events: [
        { ts: 1, kind: 'pool_subject', detail: { code_path: 'x.go', code: 'package fence\nfunc F() bool { return true }\n' } },
        { ts: 2, kind: 'task_created', subject: 'mutant-generator', detail: { role: 'mutant-generator', title: 'plant faults' } },
        { ts: 3, kind: 'task_done', subject: 'mutant-generator', detail: { result: '===MUTATION_1===\npackage fence\nfunc F() bool { return false }\n' } },
        { ts: 4, kind: 'pool_dev_adequacy', detail: { survivors: 1, survivor_ids: ['1'] } },
      ],
    });
  });
  await expect(async () => {
    expect(Number(await page.locator('#replay-scrub').getAttribute('max'))).toBe(4);
  }).toPass({ timeout: 5000 });
  const scrub = page.locator('#replay-scrub');
  await scrub.evaluate((el) => { (el as HTMLInputElement).value = '4'; el.dispatchEvent(new Event('input')); });
  const row = page.locator('#tasks .trow', { hasText: 'plant faults' });
  await expect(row).toBeVisible();
  await row.click();
  const body = page.locator('.aw-win .aw-body');
  await expect(body).toBeVisible();
  // the mutated line is wrapped and highlighted...
  await expect(body.locator('.faultline')).toContainText('return false');
  // ...but the unchanged line is NOT.
  await expect(body.locator('.faultline')).not.toContainText('package fence');
});

test('every recording card corresponds to an active stream + meta pair', async () => {
  const files = fs.readdirSync(RECORDINGS_DIR);
  const streamFiles = files.filter((f) => f.endsWith('.json') && !f.endsWith('.meta.json'));
  for (const f of streamFiles) {
    const metaName = f.replace(/\.json$/, '.meta.json');
    expect(files, `${f} is missing its ${metaName} sidecar`).toContain(metaName);
    const meta = JSON.parse(fs.readFileSync(`${RECORDINGS_DIR}/${metaName}`, 'utf-8'));
    expect(Array.isArray(meta.models), `${metaName} must carry a models array (may be empty for pre-model-threading recordings)`).toBe(true);
  }
});
