#!/usr/bin/env bash
# The foreign-repo enumeration sweep.
#
# `certify --repo <dir> --dry-run` runs enumeration, language detection, test
# pairing, ambiguity demotion, ranking, and selection, then returns BEFORE any
# audit — no jail, no bwrap, no model call, no money, no language toolchain
# beyond Go. That is what keeps this gate cheap enough to run on every push:
# a gate that costs nothing stays enabled; a gate that needs a provider key or
# a jail gets disabled the first time it is inconvenient.
#
# It exists because on 2026-07-30, pointing corral at repositories nobody here
# wrote surfaced ~15 real defects the in-repo suite never caught: test pairing
# found 2 of 36 real candidates on one repo and 0 of 735 on another, a pairing
# fix was 84% unsafe on pydantic, and a GitHub Action would have failed 100%
# of runs. This script pins that check so it runs on every push instead of
# once, heroically, by hand.
#
# Each repo is shallow-cloned (--depth 1) at a PINNED commit SHA. Pinning is
# not optional: an unpinned sweep breaks CI the moment an upstream repo
# changes shape, and a flaky gate is a gate someone deletes the first time it
# is inconvenient. A shallow clone also means Rank() has no commit history to
# compute churn from, so ranking degrades to size-only — that is fine and
# deterministic, because this sweep measures ENUMERATION (how many files were
# walked, how many became candidates, how many were demoted as ambiguous),
# not which 5 of them a real scan would have picked first.
#
# No `-- <check-cmd>` is passed to any of these dry runs: --dry-run never
# executes the check command (or any suite) at all, so it cannot affect the
# counts this script measures — and omitting it sidesteps
# checkArgvSpansOneLanguage's refusal on aisuite, which is deliberately
# multi-language (Python + TypeScript) and would otherwise error out before
# it ever got counted.
set -uo pipefail
cd "$(dirname "$0")/.."
REPO_ROOT="$(pwd)"

GOLDEN="$REPO_ROOT/testdata/foreign-sweep-expected.tsv"

# repo | pinned commit SHA | why it is in the set
#
#   pallets/flask     src/ layout + a parallel tests/ tree
#   psf/requests      produces a genuine ambiguous-test demotion
#   pydantic/pydantic the 51->8 collision case (a v1/ compat tree mirroring
#                      filenames)
#   andrewyng/aisuite  multi-language: Python + TypeScript
#   gin-gonic/gin      Go -- the regression canary, must never change
#   rubocop/rubocop    Ruby, went 0 -> 735 candidates
#   expressjs/express  JS -- expected to pair ZERO. This is not a bug: JS/TS
#                      test pairing is a known model limitation, deliberately
#                      pinned at 0 so nobody "fixes" it into a false positive
#                      without noticing the regression here first.
REPOS=(
  "pallets/flask     36e4a824f340fdee7ed50937ba8e7f6bc7d17f81"
  "psf/requests      414f0513c33883adf6f2b46901d4f0b38a455851"
  "pydantic/pydantic e8b6ff8dbaca8d41bc009864db24f7576237e3a2"
  "andrewyng/aisuite cb29165b00f719cceae6a82ed4621cbcb79aaaf7"
  "gin-gonic/gin     34dac209ffb6ef85cc78c5d217bbb7ad001d68fd"
  "rubocop/rubocop   c4607810f6291eeed9a5155feecd03501ac1feb2"
  "expressjs/express a3714473feb3d2908add734d340e7755fd85e0a3"
)

WORK="$(mktemp -d)"
BIN="$WORK/corral"
OUT="$WORK/actual.tsv"
trap 'rm -rf "$WORK"' EXIT

echo "building corral..." >&2
if ! go build -o "$BIN" ./cmd/corral; then
  echo "foreign-sweep: build failed" >&2
  exit 1
fi

: > "$OUT"

for entry in "${REPOS[@]}"; do
  # shellcheck disable=SC2086
  set -- $entry
  repo="$1"
  sha="$2"
  name="$(basename "$repo")"
  dir="$WORK/$name"

  echo "cloning $repo @ $sha..." >&2
  git init -q "$dir"
  git -C "$dir" remote add origin "https://github.com/$repo.git"
  if ! git -C "$dir" fetch -q --depth 1 origin "$sha"; then
    echo "foreign-sweep: failed to fetch $repo @ $sha" >&2
    exit 1
  fi
  git -C "$dir" checkout -q FETCH_HEAD

  echo "scanning $name..." >&2
  report="$("$BIN" certify --repo "$dir" --dry-run --top 5)"

  # "  <walked> file(s) walked; <candidates> candidate(s); <jobs> job(s); ..."
  walked=$(printf '%s\n' "$report" | grep -oP '^\s+\K[0-9]+(?= file\(s\) walked)')
  candidates=$(printf '%s\n' "$report" | grep -oP 'walked; \K[0-9]+(?= candidate\(s\))')
  # The by-reason tally only prints a line for reasons that occurred at all,
  # so a repo with zero ambiguous-test demotions has no such line — absence
  # means 0, not a parse failure.
  ambiguous=$(printf '%s\n' "$report" | grep -oP '^\s+\K[0-9]+(?= ambiguous-test$)' || true)
  ambiguous="${ambiguous:-0}"

  if [ -z "$walked" ] || [ -z "$candidates" ]; then
    echo "foreign-sweep: could not parse dry-run output for $repo:" >&2
    printf '%s\n' "$report" >&2
    exit 1
  fi

  printf '%s\t%s\t%s\t%s\n' "$name" "$walked" "$candidates" "$ambiguous" >> "$OUT"
done

sort -o "$OUT" "$OUT"

if [ ! -f "$GOLDEN" ]; then
  echo "foreign-sweep: no golden file at $GOLDEN yet — writing the first one from this run" >&2
  sort -o "$GOLDEN" "$OUT"
  cat "$GOLDEN"
  exit 0
fi

sorted_golden="$WORK/golden-sorted.tsv"
sort -o "$sorted_golden" "$GOLDEN"

if ! diff -u "$sorted_golden" "$OUT" > "$WORK/diff.txt"; then
  echo "foreign-sweep: enumeration counts drifted from testdata/foreign-sweep-expected.tsv" >&2
  cat "$WORK/diff.txt" >&2
  exit 1
fi

echo "foreign-sweep: all $(wc -l < "$OUT") repo(s) match the golden file" >&2
exit 0
