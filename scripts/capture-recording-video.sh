#!/usr/bin/env bash
# SPDX-License-Identifier: Elastic-2.0
#
# scripts/capture-recording-video.sh — renders the landing hero's autoplaying
# replay to an mp4, for an embeddable/downloadable clip of a real run (the
# LinkedIn/social asset). It captures whatever recording the hero is pinned to
# (CORRALAI_HERO_SLUG, default pool-fence-crossvendor) — no brain, no keys: the
# replay plays a static, already-scrubbed tape entirely client-side.
#
# Serves the BUILT site (site/dist) locally, loads the homepage, hides the page
# copy/nav so only the cockpit instrument fills the frame, lets the hero
# autoplay at its header speed, records the viewport with Playwright, then
# ffmpeg-encodes webm -> mp4 (H.264, faststart, yuv420p — the widely-embeddable
# profile). Output: site/public/recordings/<slug>.mp4.
#
# Usage: scripts/capture-recording-video.sh [slug] [ms-per-beat] [hold-seconds]
#   slug          output basename (default pool-fence-crossvendor)
#   ms-per-beat   cadence between tape beats (default 820ms — the built-in
#                 autoplay is a fixed 250/speed ms/event, ~2.4s total, too fast
#                 for a clip, so we DRIVE the scrubber ourselves for a watchable
#                 reveal)
#   hold-seconds  how long to hold on the final signed verdict (default 5)
#
# Prereqs: a fresh `cd site && npm run build` (this reads site/dist), ffmpeg,
# and site/node_modules/playwright-core (already a dev dep).
set -euo pipefail
cd "$(dirname "$0")/.."

SLUG="${1:-pool-fence-crossvendor}"
MS_PER_BEAT="${2:-820}"
HOLD_SECONDS="${3:-5}"
PORT=8799
DIST="site/dist"
OUTDIR="site/public/recordings"
OUT="$OUTDIR/$SLUG.mp4"

[ -d "$DIST" ] || { echo "no $DIST — run: (cd site && npm run build) first" >&2; exit 1; }
command -v ffmpeg >/dev/null || { echo "ffmpeg not installed" >&2; exit 1; }
mkdir -p "$OUTDIR"

TMPV="$(mktemp -d)"
SRV_PID=""
cleanup() { [ -n "$SRV_PID" ] && kill "$SRV_PID" 2>/dev/null || true; rm -rf "$TMPV"; }
trap cleanup EXIT

python3 -m http.server "$PORT" --directory "$DIST" >/dev/null 2>&1 &
SRV_PID=$!
sleep 1

node -e "
const { chromium } = require('./site/node_modules/playwright-core');
(async () => {
  const browser = await chromium.launch();
  const ctx = await browser.newContext({
    viewport: { width: 1280, height: 860 },
    recordVideo: { dir: '$TMPV', size: { width: 1280, height: 860 } },
  });
  const page = await ctx.newPage();
  await page.goto('http://127.0.0.1:$PORT/', { waitUntil: 'domcontentloaded' });
  // Strip the page to just the cockpit instrument so the frame is all replay.
  await page.addStyleTag({ content: \`
    header, footer, .hero-copy, .star-invite, .ctas, .proof-row { display:none !important; }
    #hero { padding-top: 16px !important; }
    #stage-frame { margin: 0 auto !important; }
    body { background:#0e1116 !important; }
  \` });
  // Wait for the tape to be loaded (scrubber max = event count), then TAKE OVER:
  // pause the built-in autoplay and drive the scrubber ourselves at a watchable
  // cadence so each reasoning beat reveals deliberately and lands on the verdict.
  await page.waitForFunction(() => {
    const s = document.getElementById('replay-scrub'); return s && Number(s.max) > 0;
  }, { timeout: 15000 });
  const total = await page.evaluate(() => {
    const btn = document.getElementById('replay-playbtn');
    if (btn && /pause/.test(btn.textContent)) toggleReplayPlay();  // pause autoplay if running
    seekReplay(0);
    return Number(document.getElementById('replay-scrub').max);
  });
  for (let i = 1; i <= total; i++) {
    await page.evaluate((n) => seekReplay(n), i);
    await page.waitForTimeout($MS_PER_BEAT);
  }
  await page.waitForTimeout($HOLD_SECONDS * 1000);   // hold on the signed verdict
  await ctx.close();   // finalizes the webm
  await browser.close();
})();
"

WEBM="$(ls -t "$TMPV"/*.webm 2>/dev/null | head -1)"
[ -n "$WEBM" ] || { echo "Playwright produced no video" >&2; exit 1; }

# Trim ~1.2s of page-load settle off the front; encode a widely-embeddable mp4.
ffmpeg -y -ss 1.2 -i "$WEBM" -movflags +faststart -pix_fmt yuv420p \
  -c:v libx264 -crf 24 -preset medium -an "$OUT" >/dev/null 2>&1

echo "wrote $OUT ($(du -h "$OUT" | cut -f1), $(ffprobe -v error -show_entries format=duration -of csv=p=0 "$OUT" 2>/dev/null | cut -d. -f1)s)"
echo "REVIEW IT BY EYE before committing — confirm it shows the intended run."
