#!/usr/bin/env bash
# SPDX-License-Identifier: Elastic-2.0
#
# scripts/capture-og-image.sh — captures a 1200x630 frame of a SCRATCH brain's
# live canvas for use as the site's OG/Twitter card image.
#
# SAFETY: this script constructs the safe world itself, rather than trusting
# whatever brain happens to be listening. The brain is launched under `env -i`
# with a throwaway $HOME and every CORRALAI_* store path pointed into one
# mktemp -d scratch dir. That is not paranoia — internal/memory/store.go
# derives its corpus glob (~/.claude/projects/*/memory) from the process HOME
# and memStore.Build(nil) walks it UNCONDITIONALLY at brain startup, so a
# brain started with the operator's real HOME indexes their PERSONAL memory
# corpus even when every *_DB var points at scratch. Scratch HOME defuses the
# glob; CORRALAI_MEMORY_DIR redirects new writes; the *_DB vars cover every
# store cmd/corral would otherwise default into ~/.claude/.
#
# Default mode (no args): build cmd/corral, launch it against the scratch
# world, optionally seed a scene, capture, tear everything down.
#   OG_SEED_CMD  — optional command run BEFORE the brain starts, with the
#                  scratch CORRALAI_* env exported (e.g. a throwaway seeder
#                  writing directly to the scratch stores).
#   OG_DRIVE_CMD — optional command run AFTER the brain is healthy (e.g. an
#                  MCP driver feeding report_execution over HTTP).
#
# Escape hatch: `--brain-url URL --i-vetted-this-brain` captures an already-
# running brain instead. The flag is mandatory with a URL: the script cannot
# verify a remote brain is scratch, so the human passing the flag is the
# privacy gate — same discipline as the golden-run export.
set -euo pipefail
cd "$(dirname "$0")/.."
OUT="site/public/og-image.png"
ADDR="127.0.0.1:9019"

BRAIN_URL=""
VETTED=0
while [ $# -gt 0 ]; do
  case "$1" in
    --brain-url) BRAIN_URL="$2"; shift 2 ;;
    --i-vetted-this-brain) VETTED=1; shift ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done

capture() {
  local url="$1"
  node -e "
const { chromium } = require('./site/node_modules/playwright-core');
(async () => {
  const browser = await chromium.launch();
  const page = await browser.newPage({ viewport: { width: 1200, height: 630 } });
  await page.goto('$url/');
  await page.waitForTimeout(4000); // let agents render and start moving
  await page.screenshot({ path: '$OUT' });
  await browser.close();
})();
"
  echo "wrote $OUT — REVIEW IT BY EYE before committing: confirm it shows only synthetic demo agent names/paths, nothing from a personal brain."
}

if [ -n "$BRAIN_URL" ]; then
  if [ "$VETTED" != 1 ]; then
    echo "REFUSING: --brain-url requires --i-vetted-this-brain. This script cannot" >&2
    echo "verify a running brain is a scratch one — you must vouch for it explicitly." >&2
    exit 2
  fi
  capture "$BRAIN_URL"
  exit 0
fi

# ---- default mode: safe by construction ------------------------------------
SCRATCH="$(mktemp -d)"
FAKEHOME="$SCRATCH/home"
mkdir -p "$FAKEHOME" "$SCRATCH/memory"
BRAIN_PID=""
cleanup() {
  if [ -n "$BRAIN_PID" ]; then kill "$BRAIN_PID" 2>/dev/null || true; fi
  rm -rf "$SCRATCH"
}
trap cleanup EXIT

# One flat list so the seed hook and the brain see the IDENTICAL scratch
# world. Every store cmd/corral opens is here — if a new store is added with
# a ~/.claude default, add its var here too (the scratch HOME is the backstop
# either way).
SAFE_ENV=(
  HOME="$FAKEHOME"
  PATH="$PATH"
  CORRALAI_ADDR="$ADDR"
  CORRALAI_MEMORY_DIR="$SCRATCH/memory"
  CORRALAI_DB="$SCRATCH/coord.sqlite3"
  CORRALAI_MEMORY_DB="$SCRATCH/memory.duckdb"
  CORRALAI_PRINCIPALS_DB="$SCRATCH/principals.sqlite3"
  CORRALAI_GATEWAY_DB="$SCRATCH/gateway.sqlite3"
  CORRALAI_ARTIFACTS_DB="$SCRATCH/artifacts.sqlite3"
  CORRALAI_MISSION_DB="$SCRATCH/missions.sqlite3"
  CORRALAI_QUEUE_DB="$SCRATCH/queue.sqlite3"
  CORRALAI_REFERENCE_DB="$SCRATCH/reference.duckdb"
  CORRALAI_TELEMETRY_DB="$SCRATCH/telemetry.duckdb"
  CORRALAI_LEARN_DB="$SCRATCH/learn.sqlite3"
  CORRALAI_OIDC_ISSUER=""
)

# Build with the operator's normal env (module/build caches live in the real
# HOME); only the brain PROCESS gets the scrubbed world.
go build -o "$SCRATCH/corral-bin" ./cmd/corral

if [ -n "${OG_SEED_CMD:-}" ]; then
  # Seeders only touch the paths the env hands them, so they keep the
  # operator's toolchain env (go caches etc.) and just gain the scratch vars.
  #
  # !! SAFETY: restoring the real HOME below is safe ONLY for seeders that
  # never derive paths from $HOME. Any seeder that computes ~/.claude/* paths
  # — memory seeding especially — MUST keep the scratch HOME (HOME="$FAKEHOME")
  # instead, because memStore.Build(nil) walks the fixed
  # ~/.claude/projects/*/memory glob at startup regardless of
  # CORRALAI_MEMORY_DIR. This exact mistake leaked demo memory files into the
  # operator's real HOME during the Task 7 UI-tour capture (see ticket #26).
  env "${SAFE_ENV[@]}" HOME="$HOME" bash -c "$OG_SEED_CMD"
fi

# If something is ALREADY answering on $ADDR (e.g. the operator's real brain),
# our scratch child would die on bind failure while the health poll below
# happily blessed the incumbent — and the capture would shoot the personal
# brain. Refuse up front rather than trust whatever is listening.
if curl -sf "http://$ADDR/healthz" > /dev/null 2>&1; then
  echo "REFUSING: something is already listening on $ADDR — likely a real brain." >&2
  echo "Stop it (or use --brain-url ... --i-vetted-this-brain) and re-run." >&2
  exit 2
fi

env -i "${SAFE_ENV[@]}" "$SCRATCH/corral-bin" > "$SCRATCH/corral.log" 2>&1 &
BRAIN_PID=$!
for _ in $(seq 1 50); do
  curl -sf "http://$ADDR/healthz" > /dev/null 2>&1 && break
  sleep 0.2
done
if ! curl -sf "http://$ADDR/healthz" > /dev/null 2>&1; then
  echo "brain never became healthy; log follows:" >&2
  cat "$SCRATCH/corral.log" >&2
  exit 1
fi
# Belt to the pre-launch check's suspenders: the healthy responder must be OUR
# child, not an incumbent that raced onto the port after the check above.
if ! kill -0 "$BRAIN_PID" 2>/dev/null; then
  echo "REFUSING: port $ADDR answered healthz but our scratch brain (pid $BRAIN_PID) is dead —" >&2
  echo "a brain we didn't launch owns the port. Not capturing it." >&2
  exit 2
fi

if [ -n "${OG_DRIVE_CMD:-}" ]; then
  bash -c "$OG_DRIVE_CMD" # talks to the live brain over HTTP; needs no scratch env
fi

capture "http://$ADDR"
