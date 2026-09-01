#!/usr/bin/env bash
# Idempotently prepend the Elastic-2.0 SPDX header to every .go file lacking it.
set -euo pipefail
cd "$(dirname "$0")/.."
HEADER='// SPDX-License-Identifier: Elastic-2.0'
# Scope to `git ls-files` (the repo), NOT `find .` — the same correction
# check-licensing.sh already carries. `find` also walks untracked and ignored
# paths, including sibling git worktrees under .worktrees/, so the fixer would
# rewrite source in ANOTHER CHECKOUT on another branch. Here that is 1455 files
# the checker never looks at. A fixer and its checker must agree on what the
# repository is, or the fixer edits things nothing is grading.
git ls-files -z '*.go' | while IFS= read -r -d '' f; do
  if ! grep -q 'SPDX-License-Identifier' "$f"; then
    { echo "$HEADER"; echo; cat "$f"; } > "$f.spdxtmp" && mv "$f.spdxtmp" "$f"
    echo "headered: $f"
  fi
done
