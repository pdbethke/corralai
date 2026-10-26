#!/usr/bin/env bash
# Build the in-browser ledger deriver into <outdir>:
#   ledger.wasm         internal/ledger/wasm, exposing corralLedgerDerive()
#   wasm_exec.js        the Go runtime shim, copied from THIS toolchain's GOROOT
#                       (it must match the compiler that built the wasm)
#   ledger.wasm.sha256  digest of ledger.wasm
# Prints the wasm's size in bytes. The output directory is explicit: no default.
set -euo pipefail

if [ $# -ne 1 ] || [ -z "$1" ]; then
  echo "usage: scripts/build-ledger-wasm.sh <outdir>" >&2
  exit 2
fi
OUT="$1"

cd "$(dirname "$0")/.."
mkdir -p "$OUT"
GOOS=js GOARCH=wasm go build -trimpath -ldflags "-s -w" -o "$OUT/ledger.wasm" ./internal/ledger/wasm
# GOROOT files are read-only on many installs, so a plain cp onto an
# earlier build's copy fails; install writes a fresh 0644 file each time.
install -m 0644 "$(go env GOROOT)/lib/wasm/wasm_exec.js" "$OUT/wasm_exec.js"
(cd "$OUT" && sha256sum ledger.wasm > ledger.wasm.sha256)
echo "ledger.wasm: $(wc -c < "$OUT/ledger.wasm") bytes"
