#!/usr/bin/env bash
# SPDX-License-Identifier: Elastic-2.0
#
# scripts/capture-og-image.sh — captures a 1200x630 frame of a seeded scratch
# brain's live canvas for use as the site's OG/Twitter card image. Takes the
# brain URL as $1 (default the local dev brain). MUST only ever be pointed at
# a seeded scratch brain (see Task 2 of the site-docs-expansion plan) — never
# a personal/live one; this script has no way to verify that itself, so the
# human running it is the privacy gate here, same as the golden-run export.
set -euo pipefail
cd "$(dirname "$0")/.."
BRAIN_URL="${1:-http://127.0.0.1:9019}"
OUT="site/public/og-image.png"
node -e "
const { chromium } = require('./site/node_modules/playwright-core');
(async () => {
  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1200, height: 630 } });
  await page.goto('$BRAIN_URL/');
  await page.waitForTimeout(4000); // let agents render and start moving
  await page.screenshot({ path: '$OUT' });
  await browser.close();
})();
"
echo "wrote $OUT — REVIEW IT BY EYE before committing: confirm it shows only synthetic demo agent names/paths, nothing from a personal brain."
