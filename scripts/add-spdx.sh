#!/usr/bin/env bash
# Idempotently prepend the Elastic-2.0 SPDX header to every .go file lacking it.
set -euo pipefail
cd "$(dirname "$0")/.."
HEADER='// SPDX-License-Identifier: Elastic-2.0'
find . -name '*.go' -not -path './.git/*' -print0 | while IFS= read -r -d '' f; do
  if ! grep -q 'SPDX-License-Identifier' "$f"; then
    { echo "$HEADER"; echo; cat "$f"; } > "$f.spdxtmp" && mv "$f.spdxtmp" "$f"
    echo "headered: $f"
  fi
done
