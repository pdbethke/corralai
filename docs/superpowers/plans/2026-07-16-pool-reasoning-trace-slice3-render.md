<!-- SPDX-License-Identifier: Elastic-2.0 -->
# Pool reasoning trace — Slice 3 (the render: show the work) — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: superpowers:subagent-driven-development. Steps use `- [ ]`.

**Goal:** Render a pool run's replay as an inspectable **reasoning trace** — the console shows the ordered "why" (subject → mutants planted → dev-suite grade + the survivor → the critic's argument → the killing test → signed verdict), clicking a task shows its real artifact (the mutants / the authored test), and the surviving mutant is shown as a **diff against the original with the fault highlighted**. This is transparency made visible — the difference from Fugu.

**Spec:** `docs/superpowers/specs/2026-07-16-pool-reasoning-trace-and-shared-tests-design.md` (Slice 3). Builds on Slice 1 (merged `8f084a8`): the replay now carries `pool_subject`/`pool_dev_adequacy`/`pool_verdict` events, `task_done.detail.result`, and `finding_reported.detail.evidence`.

**Architecture:** All DOM (Playwright-verifiable), all in `internal/ui/web/replay-player.js` (the product file — `site/public/replay-player.js` is a hash-synced copy; NEVER edit it, re-sync via `scripts/sync-site-assets.sh`). Three render additions: (1) new console beats for the three `pool_*` kinds + the critic's `evidence`; (2) a "result" section in the task-story modal surfacing `Task.Result`; (3) a fault-highlight diff for the surviving mutant. Then sync + a Playwright test + deploy.

**Tech Stack:** vanilla JS (no framework, no deps), inline CSS in `internal/ui/web/index.html` `<style>` + the runtime `ensureSiteReplayStyles()`/`ensureReplayTaskStyles()` injectors; Playwright (`site/tests/recordings.spec.ts`).

## Global Constraints
- **Edit ONLY `internal/ui/web/replay-player.js` (+ `internal/ui/web/index.html` for product CSS); never `site/public/replay-player.js`.** After edits, run `bash scripts/sync-site-assets.sh` and confirm `bash scripts/sync-site-assets.sh --check` passes (the CI drift gate).
- **The only HTML primitive is `esc()` (line ~623)** — escape ALL event-derived text (code, critique, model names) before inserting into `innerHTML`. No other sink; never interpolate un-esc'd tape data.
- **Honesty / show-the-real-thing:** the trace renders the verbatim evidence (`esc()`'d), never a paraphrase. If an artifact is absent, say so (mirror the existing `.aw-honest` fallback pattern), never fabricate.
- **Graceful degradation:** a stream WITHOUT pool events must render exactly as today (the new cases only fire on `pool_*`/`result`/`evidence`; unknown kinds still hit the existing `default` no-op).
- **CSS lives in two places** (product `<style>` in index.html AND the site runtime injector) — add any new class to BOTH, mirroring the existing `.xthought`/`.aw-*` split, or the site render will be unstyled.
- Verify gate before each commit: `bash scripts/sync-site-assets.sh --check` (clean), and `cd site && npx playwright test tests/recordings.spec.ts` (the pool-trace test + all existing pass). No Go build touched.

---

### Task 1: Console reasoning-trace beats (the ordered "why")

**Files:**
- Modify: `internal/ui/web/replay-player.js` (the cockpit-accumulation `switch(ev.kind)` ~1927; `renderReplayLine` ~1104; add `finding_reported` evidence to its beat; the site CSS injector `ensureSiteReplayStyles` ~1340; and `internal/ui/web/index.html` `<style>` for the product CSS)
- Test: `site/tests/recordings.spec.ts`

**Interfaces:**
- Consumes: the event stream kinds `pool_subject` (detail `{goal, code, dev_test_code, code_path, dev_test_path}`), `pool_dev_adequacy` (`{dev_kill_rate, mutants_total, survivors, survivor_ids}`), `pool_verdict` (`{status, dev_kill_rate, mutants_total, survivors, proven_missed, models_by_role, record_id, record_head}`), and `finding_reported.detail.evidence`.
- Produces: new `replayConsoleLines` beat kinds `pool` (with a `sub` for subject/adequacy/verdict) rendered by a new `renderReplayLine` branch, styled `.xpool` (+ a status modifier for certified/needs-review).

**Design:** each `pool_*` event pushes a readable beat into the SAME chronological console feed the thoughts/execs use (`reflex_cap_exhausted` at ~1999 is the precedent for a synthetic beat). Text templates (human, terse, `esc()`'d):
- `pool_subject` → "grading {code_path} against its own tests ({dev_test_path})".
- `pool_dev_adequacy` → "the dev's tests killed {mutants_total-survivors}/{mutants_total} planted faults — {survivors} survived (the gap)".
- `pool_verdict` → "{STATUS}: {dev_kill_rate} kill-rate, {survivors} survivors, {proven_missed} proven-missed · models {role=model …} · signed record {record_id}".
- `finding_reported` (already handled at ~1987) also surface `d.evidence` in its console beat (the critic's actual argument), not just type/severity.

- [ ] **Step 1: Write the failing Playwright test**

In `site/tests/recordings.spec.ts`, mirror the existing `window.startReplay({events:[...]})` + `#exec` assertions (the thought/exec split test ~360-405). Inject a synthetic pool stream and assert the console shows the trace:

```ts
test('pool reasoning trace renders the why in the console', async ({ page }) => {
  await page.goto('/recordings/'); // or the page that hosts the player; match the existing tests' target
  await page.evaluate(() => {
    (window as any).startReplay({ events: [
      { ts: 1, kind: 'pool_subject', actor: 'corral-advpool', detail: { code_path: 'internal/fence/fence.go', dev_test_path: 'internal/fence/fence_test.go', goal: 'neutralize the fence', code: 'package fence', dev_test_code: 'package fence' } },
      { ts: 2, kind: 'pool_dev_adequacy', detail: { dev_kill_rate: 0.8, mutants_total: 5, survivors: 1, survivor_ids: ['m3'] } },
      { ts: 3, kind: 'finding_reported', subject: 'TestX', detail: { type: 'note', severity: 'high', evidence: 'the test asserts nothing' } },
      { ts: 4, kind: 'pool_verdict', detail: { status: 'certified', dev_kill_rate: 0.8, mutants_total: 5, survivors: 1, proven_missed: 0, models_by_role: { 'test-critic': 'gemini-3.5-flash', 'test-writer': 'claude-sonnet-5' }, record_id: 5, record_head: 'abc' } },
    ] });
  });
  const exec = page.locator('#exec');
  await expect(exec).toContainText('grading internal/fence/fence.go');
  await expect(exec).toContainText('killed 4/5');       // 5 total - 1 survivor
  await expect(exec).toContainText('the test asserts nothing'); // critic evidence surfaced
  await expect(exec).toContainText('CERTIFIED');
  await expect(exec).toContainText('record 5');
  await expect(page.locator('#exec .xpool')).toHaveCount(3); // subject + adequacy + verdict beats
});
```

Match the test file's page target + `startReplay` global (grep the existing tests for how they navigate + invoke it). Adjust selectors to the real ones you introduce.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd site && npx playwright test tests/recordings.spec.ts -g "pool reasoning trace"`
Expected: FAIL — the pool beats aren't rendered (no `.xpool`, text absent).

- [ ] **Step 3: Implement the beats**

In `replay-player.js`:
1. In the cockpit-accumulation switch (~1927), add cases pushing beats (mirror the `thought` case shape):
   ```js
   case 'pool_subject': {
     const d = ev.detail || {};
     replayConsoleLines.push({kind:'pool', sub:'subject', text:'grading ' + (d.code_path||'the change') + ' against its own tests' + (d.dev_test_path ? ' (' + d.dev_test_path + ')' : '')});
     trimConsole(); break;   // match the existing length-cap pattern (length>200 shift)
   }
   case 'pool_dev_adequacy': {
     const d = ev.detail || {};
     const total = d.mutants_total||0, surv = d.survivors||0;
     replayConsoleLines.push({kind:'pool', sub:'adequacy', text:"the dev's tests killed " + (total-surv) + '/' + total + ' planted faults — ' + surv + ' survived (the gap)'});
     trimConsole(); break;
   }
   case 'pool_verdict': {
     const d = ev.detail || {};
     const models = Object.keys(d.models_by_role||{}).sort().map(r => r + '=' + d.models_by_role[r]).join(' ');
     replayConsoleLines.push({kind:'pool', sub:'verdict', status:(d.status||''), text:(d.status||'').toUpperCase() + ': kill-rate ' + (d.dev_kill_rate) + ', ' + (d.survivors||0) + ' survivors, ' + (d.proven_missed||0) + ' proven-missed · models ' + models + ' · signed record ' + (d.record_id||'?')});
     trimConsole(); break;
   }
   ```
   (Use the file's existing trim pattern inline if there's no `trimConsole` helper — mirror `if(replayConsoleLines.length>200) replayConsoleLines.shift();`.)
2. In the `finding_reported` accumulation case (~1987), also push/annotate the critic's `evidence` into a console beat (or extend the finding beat) so `d.evidence` reaches the feed.
3. In `renderReplayLine` (~1104), add a branch for `e.kind === 'pool'`:
   ```js
   if(e.kind === 'pool'){
     const cls = 'xpool' + (e.sub === 'verdict' ? ' xpool-' + (e.status === 'certified' ? 'ok' : 'review') : '');
     return '<div class="xblk"><div class="xcmdline ' + cls + '">' + esc(e.text) + '</div></div>';
   }
   ```
4. Add `.xpool` (+ `.xpool-ok`/`.xpool-review`) CSS to BOTH `internal/ui/web/index.html`'s `<style>` (product) and `ensureSiteReplayStyles()`'s injected template (site) — mirror how `.xthought` is defined in both. A distinct look (e.g. a left accent + muted for subject/adequacy, a stronger treatment + green/amber for the verdict). Keep it theme-aware if the surrounding styles are.

- [ ] **Step 4: Sync + run tests**

Run: `bash scripts/sync-site-assets.sh` then `bash scripts/sync-site-assets.sh --check` (clean), then `cd site && npx playwright test tests/recordings.spec.ts`
Expected: PASS (the pool-trace test + all existing).

- [ ] **Step 5: Commit**

```bash
bash scripts/sync-site-assets.sh
git add internal/ui/web/replay-player.js internal/ui/web/index.html site/public/replay-player.js site/tests/recordings.spec.ts
git commit -m "replay-player: render the pool reasoning trace + critic evidence in the console"
```

---

### Task 2: Task-story modal surfaces the real artifact (Task.Result)

**Files:**
- Modify: `internal/ui/web/replay-player.js` (`buildReplayTaskStories` ~1542 — the `ensureT` struct + the `task_done` case; `renderReplayTaskWindowBody` ~1721 — add a "result" section; `ensureReplayTaskStyles` ~1643 for a `<pre>` style)
- Test: `site/tests/recordings.spec.ts`

**Interfaces:**
- Consumes: `task_done.detail.result` (the mutant-generator's mutants text; the test-writer's authored test).
- Produces: the story struct gains `result`; the modal body renders a "result" section (a `<pre>` of the `esc()`'d source) after "what was done".

**Design:** clicking a pool task shows its actual output — the mutants Claude planted, or the test the pool wrote. That IS the deliverable/evidence made inspectable.

- [ ] **Step 1: Write the failing test**

Extend `recordings.spec.ts` (mirror the existing task-modal test ~241-296 that drives `.aw-win .aw-body`): inject a stream with a `task_done` carrying `detail.result`, open the task window, assert the result renders.

```ts
test('task story shows the produced artifact (result)', async ({ page }) => {
  await page.goto('/recordings/');
  await page.evaluate(() => {
    (window as any).startReplay({ events: [
      { ts: 1, kind: 'task_created', subject: 'mutant-generator', detail: { role: 'mutant-generator', title: 'plant faults' } },
      { ts: 2, kind: 'task_claimed', subject: 'mutant-generator', actor: 'claude-writer', detail: {} },
      { ts: 3, kind: 'task_done', subject: 'mutant-generator', actor: 'claude-writer', detail: { result: '===MUTATION_1===\npackage fence\n// the planted fault' } },
    ] });
  });
  // open the task window (match how the existing modal test triggers it — a click on the task node/row).
  // ... then:
  const body = page.locator('.aw-win .aw-body');
  await expect(body).toContainText('the planted fault');
});
```

Match the modal-open trigger the existing test uses.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd site && npx playwright test tests/recordings.spec.ts -g "produced artifact"`
Expected: FAIL — no result in the modal.

- [ ] **Step 3: Implement**

1. In `ensureT` (~1542), add `result:''` to the struct default.
2. In the `task_done`/`task_cancelled`/`task_superseded` case (~1576), add `if((ev.detail||{}).result) t.result = ev.detail.result;`.
3. In `renderReplayTaskWindowBody` (~1721), after the "what was done" section, add a "result" section:
   ```js
   if(t.result){ h += '<div class="isec">result</div><pre class="aw-result">' + esc(t.result) + '</pre>'; }
   ```
4. Add `.aw-result` CSS (a scrollable monospace `<pre>`) to `ensureReplayTaskStyles()` (~1643, injected on both site + product).

- [ ] **Step 4: Sync + test**

Run: `bash scripts/sync-site-assets.sh && bash scripts/sync-site-assets.sh --check` then `cd site && npx playwright test tests/recordings.spec.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
bash scripts/sync-site-assets.sh
git add internal/ui/web/replay-player.js site/public/replay-player.js site/tests/recordings.spec.ts
git commit -m "replay-player: task story shows the produced artifact (mutants / authored test)"
```

---

### Task 3: Fault-highlight — the surviving mutant diffed against the original

**Files:**
- Modify: `internal/ui/web/replay-player.js` (a new `renderFaultDiff(original, mutant)` helper; surface it in the task-story "result" section for the mutant-generator task, or a dedicated survivor view; CSS for the diff)
- Test: `site/tests/recordings.spec.ts`

**Interfaces:**
- Consumes: `pool_subject.detail.code` (the original), and a surviving mutant's source. The surviving mutant source is recoverable by parsing the mutant-generator `task_done.result` (the `===MUTATION_n===` blocks) and selecting the ids in `pool_dev_adequacy.survivor_ids`; v1 MAY diff the whole mutant-gen result's first mutant if survivor-id parsing is deferred — but PREFER diffing the actual survivor.
- Produces: `renderFaultDiff` returns HTML — the mutant rendered as a `<pre>` with the lines that DIFFER from the original marked `.faultline` ("here is the exact planted fault, and your suite passes anyway").

**Design:** a mutant is a same-signature drop-in, so a line diff of original vs mutant isolates the fault. No diff lib exists (`esc()` is the only primitive), so implement a minimal LCS-free line diff: split both into lines; a mutant line is a fault line if it's not present at the same position in the original (a simple positional + membership check is enough for the demo — mutants change a small region). Render each mutant line in a `<pre>`; wrap changed lines in `<span class="faultline">…</span>`. Keep it O(n) and dependency-free.

- [ ] **Step 1: Write the failing test**

```ts
test('fault highlight marks the mutated lines', async ({ page }) => {
  await page.goto('/recordings/');
  await page.evaluate(() => {
    (window as any).startReplay({ events: [
      { ts: 1, kind: 'pool_subject', detail: { code_path: 'x.go', code: 'package fence\nfunc F() bool { return true }\n' } },
      { ts: 2, kind: 'task_created', subject: 'mutant-generator', detail: { role: 'mutant-generator', title: 't' } },
      { ts: 3, kind: 'task_done', subject: 'mutant-generator', detail: { result: '===MUTATION_1===\npackage fence\nfunc F() bool { return false }\n' } },
      { ts: 4, kind: 'pool_dev_adequacy', detail: { survivors: 1, survivor_ids: ['1'] } },
    ] });
  });
  // open the mutant-generator task story (or the survivor view), then:
  const body = page.locator('.aw-win .aw-body');
  await expect(body.locator('.faultline')).toContainText('return false'); // the mutated line highlighted
  await expect(body.locator('.faultline')).not.toContainText('package fence'); // unchanged line NOT highlighted
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd site && npx playwright test tests/recordings.spec.ts -g "fault highlight"`
Expected: FAIL — `renderFaultDiff`/`.faultline` don't exist.

- [ ] **Step 3: Implement**

Add `renderFaultDiff(original, mutant)`:
```js
function renderFaultDiff(original, mutant){
  const o = (original||'').split('\n'), m = (mutant||'').split('\n');
  const oset = new Set(o.map(l => l.trim()));
  const rows = m.map(line => {
    const changed = line.trim() !== '' && !oset.has(line.trim());
    const cell = esc(line);
    return changed ? '<span class="faultline">' + cell + '</span>' : cell;
  });
  return '<pre class="aw-result faultdiff">' + rows.join('\n') + '</pre>';
}
```
(Membership-against-the-original-line-set is a robust, order-tolerant "is this line new" test for a small mutation; a line that appears anywhere in the original isn't a fault. Good enough for the demo; a positional LCS diff is a follow-up if false highlights appear.) Then, in the mutant-generator task story (and/or a survivor panel), when `pool_subject.code` is available, render `renderFaultDiff(subjectCode, mutantSource)` instead of the plain result `<pre>`. Thread the subject code into the render (capture `pool_subject.code` into a module-scope `replayPoolSubject` when applying that event, like other accumulated state). Add `.faultline` CSS (a strong highlight — e.g. amber/red background) to the injected styles (both site + product).

- [ ] **Step 4: Sync + test**

Run: `bash scripts/sync-site-assets.sh && bash scripts/sync-site-assets.sh --check` then `cd site && npx playwright test tests/recordings.spec.ts`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
bash scripts/sync-site-assets.sh
git add internal/ui/web/replay-player.js site/public/replay-player.js site/tests/recordings.spec.ts
git commit -m "replay-player: highlight the exact fault (surviving mutant diffed vs the original)"
```

---

### Task 4: Deploy + capture the audit-gate hero recording (controller)

Not a subagent task. After merge + site deploy: re-export the cross-vendor fence run (now complete per Slice 1) as a scrubbed recording, view it on the site, confirm the reasoning trace + result + fault-highlight render, and — the original goal — wire it as (or beside) the hero via `CORRALAI_HERO_SLUG` / a gallery card. This closes the loop: an audit run is now a visible, inspectable, shareable proof.

## Self-Review (plan author)
- Slice-3 coverage: console trace + critic evidence → T1; the real artifact in the story → T2; fault-highlight → T3; capture/wire → T4.
- Constraint consistency: every task edits ONLY the product `replay-player.js` (+ index.html CSS), then syncs + `--check`; every task adds a Playwright assertion; `esc()` used for all tape-derived text.
- Graceful degradation: all new cases key on `pool_*`/`result`/`evidence`; a non-pool stream is unchanged (existing tests must stay green — each task runs the full `recordings.spec.ts`).
