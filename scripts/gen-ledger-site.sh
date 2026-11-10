#!/usr/bin/env bash
# Render the public ledger site from the ledger BRANCH into site/public/ledger.
#
# The record lives on a branch (corral/ledger), not in the worktree, so this
# materializes it into a temp dir with `git archive` — no checkout, nothing
# touched in the working tree — and renders it with scripts/ledgersite.
#
# Signatures are checked against --pubkey <hex> or CORRALAI_LEDGER_PUBKEY —
# explicitly, never inferred from a file. With neither the page says signatures
# were NOT CHECKED. The environment variable is left for ledgersite to read
# itself, so a malformed one is refused naming the true source.
#
# CORRALAI_LEDGER_DIR=<dir> renders a ledger DIRECTORY instead of the branch
# (the site's e2e build renders the committed fixture this way, so the tests
# never depend on what the live record happens to hold). Unset, the branch is
# rendered; there is no fallback between the two.
set -euo pipefail

# The repository whose Actions identity signs the ledger's keyless entries. It
# is stated here, not inferred, because the check is "signed by THIS repository".
LEDGER_REPO="pdbethke/corralai"
BRANCH="${CORRALAI_LEDGER_BRANCH:-corral/ledger}"
OUT="${1:-site/public/ledger}"
PUBKEY=""
if [ "${2:-}" = "--pubkey" ] && [ -n "${3:-}" ]; then PUBKEY="$3"; fi

cd "$(dirname "$0")/.."
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

if [ -n "${CORRALAI_LEDGER_DIR:-}" ]; then
  [ -d "$CORRALAI_LEDGER_DIR" ] || { echo "gen-ledger-site: CORRALAI_LEDGER_DIR '$CORRALAI_LEDGER_DIR' is not a directory" >&2; exit 1; }
  SRC="$(cd "$CORRALAI_LEDGER_DIR" && pwd)"
  echo "gen-ledger-site: rendering directory $SRC"
else
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
SRC="$TMP"
fi

mkdir -p "$OUT"
# -site-chrome leaves a slot at the top of every record page; the site build
# fills it with the site's own header (site/integrations/ledger-chrome.mjs),
# so a reader who arrives at an entry from a PR or issue link can find out
# what corral is.
if [ -n "$PUBKEY" ]; then
  go run ./scripts/ledgersite -ledger "$SRC" -out "$OUT" -repo "$LEDGER_REPO" -pubkey "$PUBKEY" -site-chrome
else
  go run ./scripts/ledgersite -ledger "$SRC" -out "$OUT" -repo "$LEDGER_REPO" -site-chrome
fi
