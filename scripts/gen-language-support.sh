#!/usr/bin/env bash
# SPDX-License-Identifier: Elastic-2.0
#
# scripts/gen-language-support.sh [--check] — renders the language-support
# page from the runner table and the parity list the code dispatches on
# (internal/lang/runner.go, internal/lang/parity.go), into BOTH
# docs/corral/language-support.md (repo) and
# site/src/content/docs/docs/language-support.md (Starlight).
#
# Why generated: a support matrix written by hand claims what someone
# believed when they wrote it. This one is the table corral reads when it
# runs, so a capability cannot appear here before the code has it, and a gap
# cannot disappear without its exemption being deleted (which the parity test
# allows only once the capability exists).
#
# --check: regenerate into a scratch dir and diff against the committed
# files instead of overwriting them; exits 1 with the diff on any drift.
# Wired into .github/workflows/deploy.yml unconditionally: a Markdown-only
# change is the one change class that can hand-edit a generated page.
set -euo pipefail
cd "$(dirname "$0")/.."

CHECK=0
[ "${1:-}" = "--check" ] && CHECK=1

WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

go run ./scripts/langsupport > "$WORKDIR/repo.md"
go run ./scripts/langsupport -site > "$WORKDIR/site.md"

REPO=docs/corral/language-support.md
SITE=site/src/content/docs/docs/language-support.md

if [ "$CHECK" = 1 ]; then
  rc=0
  diff -u "$REPO" "$WORKDIR/repo.md" || rc=1
  diff -u "$SITE" "$WORKDIR/site.md" || rc=1
  if [ "$rc" != 0 ]; then
    echo "language-support page drifted from internal/lang: run scripts/gen-language-support.sh and commit" >&2
  fi
  exit "$rc"
fi
cp "$WORKDIR/repo.md" "$REPO"
cp "$WORKDIR/site.md" "$SITE"
echo "wrote $REPO and $SITE"
