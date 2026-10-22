#!/usr/bin/env bash
# Update the Sigstore public-good trusted root that internal/ledger embeds.
#
#   scripts/update-sigstore-root.sh
#
# A DELIBERATE, REVIEWED update, never a runtime fetch: the root says which
# Fulcio authorities and Rekor logs a keyless ledger entry may be signed and
# witnessed by, so a change to it is a change to what corral trusts. Run it,
# read the diff of internal/ledger/sigstore_trusted_root.json, and commit it
# through a pull request like any other change. The fetch goes through
# Sigstore's TUF repository (scripts/sigstoreroot), so what it writes is
# verified against the TUF root shipped in sigstore-go.
set -euo pipefail

if [ $# -ne 0 ]; then
  echo "usage: scripts/update-sigstore-root.sh" >&2
  exit 2
fi
cd "$(dirname "$0")/.."
# The embedded file must exist for internal/ledger to build; the program
# writes a temporary file first and the real one only once both readers
# accepted it.
TMP="$(mktemp)"
trap 'rm -f "$TMP"' EXIT
go run ./scripts/sigstoreroot "$TMP"
cp "$TMP" internal/ledger/sigstore_trusted_root.json
git --no-pager diff --stat -- internal/ledger/sigstore_trusted_root.json
