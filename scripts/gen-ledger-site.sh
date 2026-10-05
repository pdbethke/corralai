#!/usr/bin/env bash
# Render the public ledger site from the ledger BRANCH into site/public/ledger.
#
# The record lives on a branch (corral/ledger), not in the worktree, so this
# materializes it into a temp dir with `git archive` — no checkout, nothing
# touched in the working tree — and renders it with scripts/ledgersite.
#
# Signatures are checked against, in order: --pubkey <hex>, CORRALAI_LEDGER_PUBKEY,
# or the repository's published LEDGER_PUBKEY file. With none of them the page
# says signatures were NOT CHECKED, which is neither a pass nor a failure — the
# same standard `corral verify` holds. LEDGER_PUBKEY is the default so the
# deployed page checks corral's own record without the workflow having to be
# told twice where the key lives.
set -euo pipefail

BRANCH="${CORRALAI_LEDGER_BRANCH:-corral/ledger}"
OUT="${1:-site/public/ledger}"
PUBKEY="${CORRALAI_LEDGER_PUBKEY:-}"
if [ "${2:-}" = "--pubkey" ] && [ -n "${3:-}" ]; then PUBKEY="$3"; fi

cd "$(dirname "$0")/.."
if [ -z "$PUBKEY" ] && [ -f LEDGER_PUBKEY ]; then
  PUBKEY="$(tr -d '[:space:]' < LEDGER_PUBKEY)"
fi
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

# Prefer the remote-tracking ref so a stale local copy is never rendered as
# current; fall back to a local branch for an offline build.
REF=""
for candidate in "origin/$BRANCH" "$BRANCH"; do
  if git rev-parse --verify --quiet "$candidate" >/dev/null; then REF="$candidate"; break; fi
done
if [ -z "$REF" ]; then
  echo "gen-ledger-site: no ref for '$BRANCH' (try: git fetch origin $BRANCH)" >&2
  exit 1
fi
echo "gen-ledger-site: rendering $REF ($(git rev-parse --short "$REF"))"
git archive "$REF" | tar -x -C "$TMP"

mkdir -p "$OUT"
if [ -n "$PUBKEY" ]; then
  go run ./scripts/ledgersite -ledger "$TMP" -out "$OUT" -pubkey "$PUBKEY"
else
  go run ./scripts/ledgersite -ledger "$TMP" -out "$OUT"
fi
