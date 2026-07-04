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
OUT_JSON="site/src/data/recordings/golden-run.json"
SLUG=""
REDERIVE=""
I_KNOW=0
YES=0
BEARER=""
PLATFORM_INFERENCE="local ollama"

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
  --out PATH      Default: site/src/data/recordings/golden-run.json. The
                   metadata sidecar always travels with it: PATH with .json
                   swapped for .meta.json (so the pair never drifts apart).
  --slug NAME     Shorthand for --out site/src/data/recordings/NAME.json —
                   the recordings-gallery layout. The meta sidecar travels
                   with it as always.
  --platform-inference "STR"
                   What ran the models for this recording (default
                   "local ollama"; e.g. "vendor cloud (subscription CLIs)").
                   The meta gains a platform object {inference, gpu, cpu,
                   ram, host} — gpu/cpu/ram probed from THIS host at export
                   time (nvidia-smi, /proc), host as GOOS/GOARCH. The gpu is
                   omitted for pure vendor-cloud runs (the local GPU did no
                   inference). Probed values are echoed with the manifest:
                   the same deny discipline applies — model/hardware names
                   only, never hostnames or usernames.
  --rederive-meta PATH.json
                   Offline mode: no brain contact, no export. Recomputes the
                   models field of PATH's existing .meta.json sidecar from
                   the committed stream (for recordings exported before the
                   models field existed). All other meta fields are kept.
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
    --slug) SLUG="$2"; shift 2 ;;
    --platform-inference) PLATFORM_INFERENCE="$2"; shift 2 ;;
    --rederive-meta) REDERIVE="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) echo "unknown arg: $1" >&2; usage; exit 1 ;;
  esac
done

[ -n "$SLUG" ] && OUT_JSON="site/src/data/recordings/$SLUG.json"

if [ -n "$REDERIVE" ]; then
  META="${REDERIVE%.json}.meta.json"
  [ -f "$REDERIVE" ] && [ -f "$META" ] || { echo "FAIL: need both $REDERIVE and $META" >&2; exit 1; }
  MODELS_JSON="$(python3 scripts/scrub-golden-run.py models "$REDERIVE")"
  python3 - "$META" "$MODELS_JSON" <<'PYEOF'
import json, sys
meta_path, models = sys.argv[1], json.loads(sys.argv[2])
meta = json.load(open(meta_path, encoding='utf-8'))
meta['models'] = models
with open(meta_path, 'w', encoding='utf-8') as f:
    json.dump(meta, f, indent=2)
    f.write('\n')
print('rederived models for', meta_path, '->', models)
PYEOF
  exit 0
fi

# The sidecar is derived from --out, never independently settable — a
# mismatched json/meta pair would quietly desync the site's hero stats.
OUT_META="${OUT_JSON%.json}.meta.json"

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

# ---- PLATFORM (probed on THIS host; part of the human review) ----
# {inference, gpu, cpu, ram, host} — hardware/model names only, echoed below
# so the reviewer can eyeball it under the same deny discipline as the
# manifest (no hostnames, no usernames). The gpu probe is skipped for pure
# vendor-cloud runs: the local GPU ran no inference for those.
PLATFORM_JSON="$(python3 - "$PLATFORM_INFERENCE" <<'PYEOF'
import json, platform as plat, re, subprocess, sys

inference = sys.argv[1]
out = {"inference": inference}

if not inference.startswith("vendor cloud"):
    try:
        gpu = subprocess.run(
            ["nvidia-smi", "--query-gpu=name,memory.total", "--format=csv,noheader"],
            capture_output=True, text=True, timeout=10).stdout.strip().splitlines()
        if gpu:
            name, mem = (p.strip() for p in gpu[0].split(",", 1))
            mib = float(re.sub(r"[^0-9.]", "", mem))
            out["gpu"] = f"{name} {round(mib / 1024)}GB"
    except Exception:
        pass  # no NVIDIA GPU / no nvidia-smi -> omit, never guess

try:
    with open("/proc/cpuinfo", encoding="utf-8") as f:
        for line in f:
            if line.lower().startswith("model name"):
                out["cpu"] = re.sub(r"\s+Processor$", "", line.split(":", 1)[1].strip())
                break
except OSError:
    pass

try:
    with open("/proc/meminfo", encoding="utf-8") as f:
        kb = int(next(l for l in f if l.startswith("MemTotal")).split()[1])
    gib = kb / 1048576
    # marketing size: the nearest stick-count capacity, not the OS-visible total
    sizes = [2, 4, 6, 8, 12, 16, 24, 32, 48, 64, 96, 128, 192, 256, 384, 512, 768, 1024]
    out["ram"] = f"{min(sizes, key=lambda s: abs(s - gib))}GB"
except (OSError, StopIteration):
    pass

goos = plat.system().lower()
goarch = {"x86_64": "amd64", "aarch64": "arm64", "arm64": "arm64"}.get(plat.machine(), plat.machine())
out["host"] = f"{goos}/{goarch}"
print(json.dumps(out))
PYEOF
)"
echo "--- platform (probed; review with the manifest) ---"
echo "$PLATFORM_JSON"
echo "--- end platform ---"

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

MODELS_JSON="$(python3 scripts/scrub-golden-run.py models "$TMP_JSON")"

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
    'models': json.loads('''$MODELS_JSON'''),
    'platform': json.loads('''$PLATFORM_JSON'''),
}
with open('$OUT_META', 'w') as f:
    json.dump(meta, f, indent=2)
    f.write('\n')
"
echo "wrote $OUT_JSON and $OUT_META"
