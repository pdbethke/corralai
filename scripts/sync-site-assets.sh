#!/usr/bin/env bash
# SPDX-License-Identifier: Elastic-2.0
#
# scripts/sync-site-assets.sh — keeps site/public/replay-player.js in exact
# sync with internal/ui/web/replay-player.js (the single source of truth).
# Default mode: copy + report (for local dev after touching the product
# player). --check mode: compare hashes, fail loudly on drift without
# writing anything (for CI, so a stale committed copy can never ship silently).
set -euo pipefail
cd "$(dirname "$0")/.."

SRC="internal/ui/web/replay-player.js"
DST="site/public/replay-player.js"

if [ ! -f "$SRC" ]; then
  echo "FAIL: $SRC does not exist" >&2
  exit 1
fi

if [ "${1:-}" = "--check" ]; then
  if [ ! -f "$DST" ]; then
    echo "FAIL: $DST does not exist — run scripts/sync-site-assets.sh (no args) and commit it" >&2
    exit 1
  fi
  src_hash=$(sha256sum "$SRC" | cut -d' ' -f1)
  dst_hash=$(sha256sum "$DST" | cut -d' ' -f1)
  if [ "$src_hash" != "$dst_hash" ]; then
    echo "FAIL: $DST has drifted from $SRC" >&2
    echo "  $SRC: $src_hash" >&2
    echo "  $DST: $dst_hash" >&2
    echo "Run: scripts/sync-site-assets.sh   (then commit the updated site/public/replay-player.js)" >&2
    exit 1
  fi
  echo "OK: $DST matches $SRC"
else
  mkdir -p "$(dirname "$DST")"
  cp "$SRC" "$DST"
  echo "synced $SRC -> $DST"
fi
