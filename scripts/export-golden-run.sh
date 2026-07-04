#!/usr/bin/env bash
# SPDX-License-Identifier: Elastic-2.0
#
# scripts/export-golden-run.sh — exports one real mission's /api/replay
# stream from a running corral demo brain into site/src/data/golden-run.json
# (the corralai.dev hero's baked replay), plus a metadata sidecar.
#
# PRIVACY GATE (not optional, not an assumption): see scripts/scrub-golden-run.py.
# This script always runs the automated deny-list scan (fails loudly, prints
# offenders) AND prints a human-review manifest that the operator must
# confirm before anything is written — belt and suspenders, because a static
# site is public forever the moment it deploys.
set -euo pipefail
cd "$(dirname "$0")/.."

BRAIN_URL="${BRAIN_URL:-http://127.0.0.1:9019}"
MISSION_ID=""
OUT_JSON="site/src/data/golden-run.json"
OUT_META="site/src/data/golden-run.meta.json"
I_KNOW=0
YES=0
BEARER=""

usage(){ cat <<'EOF'
Usage: scripts/export-golden-run.sh [--mission N] [--brain-url URL] [--bearer TOKEN]
                                     [--i-know] [--yes] [--out PATH]

  --mission N     Export this mission id. Default: the most recently
                   completed mission from /api/history.
  --brain-url URL Default: http://127.0.0.1:9019 (a dev-mode demo brain).
  --bearer TOKEN  Bearer token for an AUTHED brain. Requires --i-know: an
                   authed brain's recorded actors can be real principal
                   emails, not synthetic demo names — extra scrutiny needed.
  --i-know        Required alongside --bearer. Without it, this script only
                   talks to a dev-mode (unauthenticated) brain.
  --yes           Skip the interactive manifest confirmation. Only use this
                   after you've reviewed the manifest once for this exact
                   mission — e.g. a scripted re-export of an already-vetted
                   run. Never use --yes on a mission you haven't reviewed.
  --out PATH      Default: site/src/data/golden-run.json
EOF
}

while [ $# -gt 0 ]; do
  case "$1" in
    --mission) MISSION_ID="$2"; shift 2 ;;
    --brain-url) BRAIN_URL="$2"; shift 2 ;;
    --bearer) BEARER="$2"; shift 2 ;;
    --i-know) I_KNOW=1; shift ;;
    --yes) YES=1; shift ;;
    --out) OUT_JSON="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage; exit 1 ;;
  esac
done

if [ -n "$BEARER" ] && [ "$I_KNOW" -ne 1 ]; then
  echo "FAIL: --bearer (authed brain) requires --i-know — an authed brain's recorded actors" >&2
  echo "can be real principal emails, not synthetic demo names." >&2
  exit 1
fi

# ---- stash deploy/demo/.env (the demo-dev .env gotcha: its presence masks
#      the key-free, first-time-reviewer behavior this export must reflect) ----
STASHED=""
TMP_JSON=""
if [ -f deploy/demo/.env ]; then
  STASHED="deploy/demo/.env.stashed-by-export-golden-run"
  mv deploy/demo/.env "$STASHED"
  echo "stashed deploy/demo/.env -> $STASHED (restored on exit)"
fi
# One combined EXIT trap: a second `trap ... EXIT` would silently REPLACE the
# first, and the stashed .env would never make it back to the corral.
cleanup(){
  [ -n "$TMP_JSON" ] && rm -f "$TMP_JSON"
  if [ -n "$STASHED" ] && [ -f "$STASHED" ]; then mv "$STASHED" deploy/demo/.env; fi
}
trap cleanup EXIT

curl_auth=(-fsS)
[ -n "$BEARER" ] && curl_auth+=(-H "Authorization: Bearer $BEARER")

# ---- resolve the mission id ----
if [ -z "$MISSION_ID" ]; then
  echo "no --mission given; looking up the most recent completed mission at $BRAIN_URL/api/history"
  MISSION_ID=$(curl "${curl_auth[@]}" "$BRAIN_URL/api/history" | python3 -c '
import json, sys
missions = json.load(sys.stdin).get("missions") or []
print(max(missions, key=lambda m: m.get("id", 0))["id"] if missions else "", end="")
')
  if [ -z "$MISSION_ID" ]; then
    echo "FAIL: no completed missions found at $BRAIN_URL/api/history — run 'make demo-mission' in deploy/demo/ first" >&2
    exit 1
  fi
fi
echo "exporting mission $MISSION_ID from $BRAIN_URL"

# ---- fetch the replay stream ----
TMP_JSON="$(mktemp)"
curl "${curl_auth[@]}" "$BRAIN_URL/api/replay?mission=$MISSION_ID" -o "$TMP_JSON"

# ---- AUTOMATED DENY-LIST SCAN (the floor — always runs, never skippable) ----
python3 scripts/scrub-golden-run.py deny "$TMP_JSON" "$(whoami)" "$(hostname)"

# ---- HUMAN-REVIEW MANIFEST (the ceiling) ----
python3 scripts/scrub-golden-run.py manifest "$TMP_JSON"

if [ "$YES" -ne 1 ]; then
  echo
  read -r -p "Reviewed the manifest above — write $OUT_JSON? [y/N] " ans
  case "$ans" in
    y|Y|yes|YES) ;;
    *) echo "aborted — nothing written"; exit 1 ;;
  esac
else
  echo "--yes given: skipping interactive confirmation (manifest was still printed above)"
fi

# ---- write the final JSON + metadata sidecar ----
mkdir -p "$(dirname "$OUT_JSON")"
cp "$TMP_JSON" "$OUT_JSON"

curl "${curl_auth[@]}" "$BRAIN_URL/api/history" | python3 -c "
import json, sys
missions = json.load(sys.stdin).get('missions') or []
m = next((x for x in missions if x.get('id') == $MISSION_ID), {})
meta = {
    'directive': m.get('directive', ''),
    'task_count': m.get('task_count', 0),
    'done_task_count': m.get('done_task_count', 0),
    'finding_count': m.get('finding_count', 0),
    'duration_seconds': m.get('duration_seconds', 0),
}
with open('$OUT_META', 'w') as f:
    json.dump(meta, f, indent=2)
    f.write('\n')
"
echo "wrote $OUT_JSON and $OUT_META"
