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
# "N entries, chain intact" is the CLI's total; an empty ledger prints
# "no entries" instead and must not pass.
CLI_TOTAL="$(sed -n 's/^\([0-9][0-9]*\) entries, chain intact$/\1/p' "$CLI_OUT")"

WASM_JSON="$(node scripts/check-ledger-wasm.mjs "$TMP/wasm" "$LEDGER" "$PUB")"
echo "wasm: $WASM_JSON"
WASM_VERIFIED="$(node -e 'console.log(JSON.parse(process.argv[1]).verified)' "$WASM_JSON")"
WASM_TOTAL="$(node -e 'console.log(JSON.parse(process.argv[1]).entries)' "$WASM_JSON")"

isint() { [[ "$2" =~ ^[0-9]+$ ]] || { echo "FAIL: could not parse $1 as an integer (got '$2')" >&2; exit 1; }; }
isint "the CLI's 'N entries, chain intact' total (is the ledger empty or not intact?)" "$CLI_TOTAL"
isint "the CLI's verified count" "$CLI_VERIFIED"
isint "the wasm's verified count" "$WASM_VERIFIED"
isint "the wasm's entry count" "$WASM_TOTAL"

echo "cli: $CLI_VERIFIED verified of $CLI_TOTAL; wasm: $WASM_VERIFIED verified of $WASM_TOTAL"
if [ "$WASM_TOTAL" -eq 0 ] || [ "$WASM_VERIFIED" -eq 0 ]; then
  echo "FAIL: nothing was verified (an empty ledger or a wrong key proves nothing)" >&2
  exit 1
fi
if [ "$CLI_TOTAL" != "$WASM_TOTAL" ] || [ "$CLI_VERIFIED" != "$WASM_VERIFIED" ]; then
  echo "FAIL: wasm and corral verify disagree on the entry or verified count" >&2
  exit 1
fi
echo "ok"
