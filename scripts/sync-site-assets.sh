#!/usr/bin/env bash
# SPDX-License-Identifier: Elastic-2.0
#
# scripts/sync-site-assets.sh — keeps the site's copies of shared product UI
# assets in exact sync with their internal/ui/web source of truth. Two pairs:
#
#   1. site/public/replay-player.js  <-  internal/ui/web/replay-player.js
#      (whole-file byte-identical copy)
#   2. the four cockpit-panel regions inside internal/ui/web/index.html
#      (marked by <!-- COCKPIT-SHELL:<NAME>:BEGIN/END --> comments)
#      <-  internal/ui/web/cockpit-shell.html
#      (the panel markup fragment Hero.astro/recordings.astro import via a
#      Vite `?raw` import — see those files)
#
# Default mode: copy/splice + report (for local dev after touching either
# product file). --check mode: compare, fail loudly on drift without writing
# anything (for CI, so a stale committed copy can never ship silently).
set -euo pipefail
cd "$(dirname "$0")/.."

SRC="internal/ui/web/replay-player.js"
DST="site/public/replay-player.js"

SHELL_SRC="internal/ui/web/cockpit-shell.html"
INDEX="internal/ui/web/index.html"
SECTIONS=(TASKS AGENTS FINDINGS EXEC)

if [ ! -f "$SRC" ]; then
  echo "FAIL: $SRC does not exist" >&2
  exit 1
fi
if [ ! -f "$SHELL_SRC" ]; then
  echo "FAIL: $SHELL_SRC does not exist" >&2
  exit 1
fi
if [ ! -f "$INDEX" ]; then
  echo "FAIL: $INDEX does not exist" >&2
  exit 1
fi

CHECK=0
if [ "${1:-}" = "--check" ]; then
  CHECK=1
fi

if [ "$CHECK" = "1" ]; then
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

# ---- cockpit-shell.html <-> index.html marker regions ----
python3 - "$CHECK" "$SHELL_SRC" "$INDEX" "${SECTIONS[@]}" <<'PYEOF'
import re, sys

check = sys.argv[1] == "1"
shell_path = sys.argv[2]
index_path = sys.argv[3]
sections = sys.argv[4:]

with open(shell_path, "r") as f:
    shell = f.read()
with open(index_path, "r") as f:
    index = f.read()

def region(text, name):
    begin = f"<!-- COCKPIT-SHELL:{name}:BEGIN -->"
    end = f"<!-- COCKPIT-SHELL:{name}:END -->"
    bi = text.find(begin)
    ei = text.find(end)
    if bi < 0 or ei < 0 or bi >= ei:
        return None
    # content_start/content_end bound the region STRICTLY BETWEEN the two
    # marker comments (markers themselves are never touched by a splice).
    content_start = bi + len(begin)
    content_end = ei
    return text[content_start:content_end], content_start, content_end

def norm(s):
    # whitespace-normalized comparison: collapse all runs of whitespace to a
    # single space and strip the ends, so indentation differences between the
    # standalone fragment and its embedded home in index.html never count as
    # drift — only real markup changes do.
    return re.sub(r"\s+", " ", s).strip()

failed = False
new_index = index
for name in sections:
    shell_region = region(shell, name)
    if shell_region is None:
        print(f"FAIL: {shell_path} missing COCKPIT-SHELL:{name} markers", file=sys.stderr)
        failed = True
        continue
    index_region = region(new_index, name)
    if index_region is None:
        print(f"FAIL: {index_path} missing COCKPIT-SHELL:{name} markers", file=sys.stderr)
        failed = True
        continue
    shell_content, _, _ = shell_region
    index_content, bi, ei = index_region
    if check:
        if norm(shell_content) != norm(index_content):
            print(f"FAIL: {index_path}'s COCKPIT-SHELL:{name} region has drifted from {shell_path}", file=sys.stderr)
            print(f"  {shell_path} : {norm(shell_content)!r}", file=sys.stderr)
            print(f"  {index_path} : {norm(index_content)!r}", file=sys.stderr)
            print(f"Run: scripts/sync-site-assets.sh   (then commit the updated {index_path})", file=sys.stderr)
            failed = True
    else:
        # Splice shell_content into index.html, but re-indent it to match
        # index.html's own indentation at the BEGIN marker (the fragment file
        # itself is unindented, since it's also injected raw into Astro
        # templates) — purely cosmetic, so index.html's diff stays clean
        # instead of every splice flattening its surrounding indentation.
        line_start = new_index.rfind("\n", 0, bi) + 1
        indent = re.match(r"[ \t]*", new_index[line_start:bi]).group(0)
        inner_lines = [ln for ln in shell_content.split("\n") if ln.strip() != ""]
        reindented = "\n" + "".join(indent + ln.strip() + "\n" for ln in inner_lines) + indent
        new_index = new_index[:bi] + reindented + new_index[ei:]

if check:
    if failed:
        sys.exit(1)
    print(f"OK: {index_path}'s cockpit-shell regions match {shell_path}")
else:
    if failed:
        sys.exit(1)
    if new_index != index:
        with open(index_path, "w") as f:
            f.write(new_index)
        print(f"synced {shell_path} -> {index_path} (cockpit-shell regions)")
    else:
        print(f"{index_path} cockpit-shell regions already match {shell_path}")
PYEOF
