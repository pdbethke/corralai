#!/usr/bin/env bash
# Check that the in-browser deriver (ledger.wasm) agrees with `corral verify`.
#
#   scripts/check-ledger-wasm.sh <git-ref | directory> --pub <hex>
#
# The ledger is a git ref (materialized with `git archive`, nothing touched in
# the worktree) or an existing directory holding scans/. There is no default
# ref: a ledger belongs to a repo, and guessing one would check the wrong
# record. The key is explicit too.
#
# Exits non-zero unless Derived.problem is empty, no entry is `failed`, and the
# wasm's verified count equals the number of entries the CLI reports as signed
# and verified against --pub.
set -euo pipefail

usage() { echo "usage: scripts/check-ledger-wasm.sh <git-ref | directory> --pub <hex>" >&2; exit 2; }
[ $# -eq 3 ] && [ "$2" = "--pub" ] && [ -n "$1" ] && [ -n "$3" ] || usage
SRC="$1"
PUB="$3"

cd "$(dirname "$0")/.."
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

if [ -d "$SRC" ]; then
  LEDGER="$SRC"
else
  LEDGER="$TMP/ledger"
  mkdir -p "$LEDGER"
  git archive "$SRC" | tar -x -C "$LEDGER"
fi

bash scripts/build-ledger-wasm.sh "$TMP/wasm"

# One verified entry is one CLI line "✓ <file> ... signed by <who> and verified
# against <source>" (cmd/corral/verify_attest.go, runVerifyLedger).
CLI_OUT="$TMP/cli.txt"
go run ./cmd/corral verify --ledger "$LEDGER" --pub "$PUB" | tee "$CLI_OUT" | tail -2
CLI_VERIFIED="$(grep -c '^✓ .* and verified against ' "$CLI_OUT" || true)"

WASM_JSON="$(node scripts/check-ledger-wasm.mjs "$TMP/wasm" "$LEDGER" "$PUB")"
echo "wasm: $WASM_JSON"
WASM_VERIFIED="$(node -e 'console.log(JSON.parse(process.argv[1]).verified)' "$WASM_JSON")"

echo "cli verified: $CLI_VERIFIED, wasm verified: $WASM_VERIFIED"
if [ "$CLI_VERIFIED" != "$WASM_VERIFIED" ]; then
  echo "FAIL: wasm and corral verify disagree on the verified count" >&2
  exit 1
fi
echo "ok"
