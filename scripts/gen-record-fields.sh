#!/usr/bin/env bash
# SPDX-License-Identifier: Elastic-2.0
#
# scripts/gen-record-fields.sh [--check] — renders docs/corral/record-fields.md
# from the tier table (internal/ledger/tiers.go): for every field the signed
# record carries, whether it is measured, declared, meta or a key.
#
# Why generated: a trust label written by hand says what someone believed when
# they wrote it. This table is the one the views print their labels from, and
# TestEveryRecordedFieldHasATier fails when the record gains a field with no
# row, so the page cannot lag the record or promise a tier the code does not
# hold.
#
# --check: regenerate into a scratch dir and diff against the committed file
# instead of overwriting it; prints OK, or exits 1 with the diff on any drift.
# Wired into .github/workflows/deploy.yml unconditionally: a Markdown-only
# change is the one change class that can hand-edit a generated page.
set -euo pipefail
cd "$(dirname "$0")/.."

CHECK=0
[ "${1:-}" = "--check" ] && CHECK=1

WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

go run ./scripts/recordfields > "$WORKDIR/page.md"

PAGE=docs/corral/record-fields.md

if [ "$CHECK" = 1 ]; then
  if ! diff -u "$PAGE" "$WORKDIR/page.md"; then
    echo "record-fields page drifted from internal/ledger/tiers.go: run scripts/gen-record-fields.sh and commit" >&2
    exit 1
  fi
  echo OK
  exit 0
fi
cp "$WORKDIR/page.md" "$PAGE"
echo "wrote $PAGE"
