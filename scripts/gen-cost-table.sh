#!/usr/bin/env bash
# SPDX-License-Identifier: Elastic-2.0
#
# scripts/gen-cost-table.sh [--check] — regenerates the cost-model page's
# per-phase table from the two reference-scan fixtures
# (docs/design/fixtures/cost-model-flask.json, cost-model-requests.json),
# the same way scripts/gen-cli-docs.sh regenerates the CLI reference from
# real -h output: a table a human could hand-edit is a table that will
# drift the first time nobody remembers to update it by hand.
#
# The real logic lives in scripts/gen-cost-table.py (see its header for the
# fixtures' field mapping — both are generated straight from corral's own
# `corral scans show <id> --timing --json`, then slimmed to the fields the
# table needs). This wrapper only finds the markers and does the --check
# diff, matching gen-cli-docs.sh's shape so the two generators read the
# same to a reader.
#
# --check: regenerate into a scratch file and diff the marked section of
# docs/design/cost-model.md against it, instead of overwriting. Exits 1 with
# the diff on any drift. Wired into .github/workflows/deploy.yml alongside
# `gen-cli-docs.sh --check`.
set -euo pipefail
cd "$(dirname "$0")/.."

PAGE="docs/design/cost-model.md"
START='<!-- cost-table:start -->'
END='<!-- cost-table:end -->'
CHECK=0
[ "${1:-}" = "--check" ] && CHECK=1

if [ ! -f "$PAGE" ]; then
  echo "gen-cost-table.sh: $PAGE does not exist yet" >&2
  exit 1
fi

# Fail loudly rather than silently no-op splicing nothing: a page that lost
# its markers (a careless edit, a bad merge) must not quietly stop being
# checked.
if ! grep -qF "$START" "$PAGE" || ! grep -qF "$END" "$PAGE"; then
  echo "gen-cost-table.sh: $PAGE is missing the $START / $END markers" >&2
  exit 1
fi

NEW_TABLE="$(python3 scripts/gen-cost-table.py)"

# Splice NEW_TABLE between the markers, leaving everything else in the file
# untouched — the surrounding prose is hand-written and this generator must
# never overwrite it.
splice() {
  awk -v start="$START" -v end="$END" -v table="$NEW_TABLE" '
    $0 == start { print; print table; skipping=1; next }
    $0 == end { skipping=0 }
    skipping { next }
    { print }
  ' "$PAGE"
}

if [ "$CHECK" -eq 1 ]; then
  WORKDIR="$(mktemp -d)"
  trap 'rm -rf "$WORKDIR"' EXIT
  splice > "$WORKDIR/spliced.md"
  if ! diff -u "$PAGE" "$WORKDIR/spliced.md" >/dev/null 2>&1; then
    echo "FAIL: $PAGE's generated table has drifted from the recorded fixtures in docs/design/fixtures/:" >&2
    diff -u "$PAGE" "$WORKDIR/spliced.md" >&2 || true
    echo "Run: scripts/gen-cost-table.sh   (then commit the regenerated page)" >&2
    exit 1
  fi
  echo "OK: $PAGE's cost table matches scripts/gen-cost-table.py's output from the recorded fixtures"
else
  WORKDIR="$(mktemp -d)"
  trap 'rm -rf "$WORKDIR"' EXIT
  splice > "$WORKDIR/spliced.md"
  mv "$WORKDIR/spliced.md" "$PAGE"
  echo "wrote $PAGE"
fi
