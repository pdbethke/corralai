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

# This sweep's diagnostics (below) key on `grep -P` (PCRE), a GNU extension:
# BSD/macOS grep and busybox grep silently treat -P as unsupported and every
# capture comes back empty, so the parse-failure branch fires against
# perfectly good `certify --repo` output and reports corral itself as
# broken. Fail with an honest, specific message instead of that red herring.
if ! printf 'x' | grep -oP 'x' >/dev/null 2>&1; then
  echo "foreign-sweep: this script requires GNU grep (grep -P support) to parse dry-run output; the grep on PATH does not support -P. On macOS: brew install grep and put gnubin first on PATH." >&2
  exit 1
fi

# Fail fast on a missing golden file (see the full explanation further down,
# by the write) BEFORE paying for a build and 7 clones — a missing file that
# is going to be a hard failure either way shouldn't cost network+CPU to
# discover.
if [ ! -f "$GOLDEN" ] && [ "${FOREIGN_SWEEP_BOOTSTRAP:-}" != "1" ]; then
  echo "foreign-sweep: no golden file at $GOLDEN — refusing to pass silently." >&2
  echo "foreign-sweep: if this is a deliberate regeneration, run: FOREIGN_SWEEP_BOOTSTRAP=1 bash scripts/foreign-sweep.sh" >&2
  echo "foreign-sweep: if the file was deleted/lost by accident, restore it from git history instead." >&2
  exit 1
fi

# repo | pinned commit SHA | why it is in the set
#
#   pallets/flask     src/ layout + a parallel tests/ tree
#   psf/requests      produces a genuine ambiguous-test demotion
#   pydantic/pydantic the 51->8 collision case (a v1/ compat tree mirroring
#                      filenames)
#   andrewyng/aisuite  multi-language: Python + TypeScript
#   gin-gonic/gin      Go -- the regression canary, must never change
#   rubocop/rubocop    Ruby, went 0 -> 735 candidates
#   expressjs/express  JS -- expected to pair ZERO by CONVENTION. This is not a
#                      bug: express names tests after behaviour (lib/response.js
#                      is covered by test/res.send.js, res.json.js, ...), and no
#                      filename rule derives `response -> res`. Pinned at 0 so
#                      nobody "fixes" it into a false positive without noticing
#                      the regression here first.
#   webmozart/assert   PHP -- the multilang-gate acceptance repo (2026-08-31):
#                      a small, pure PHPUnit library, proven CERTIFIED (40/40
#                      planted faults killed, 0 survivors) via `certify --local
#                      --repo-dir`. Its own sibling tests/ tree pairs
#                      src/Assert.php -> tests/AssertTest.php by the same
#                      Test-suffix convention the php plugin's TestPaths uses.
#
# A repo with a map at testdata/foreign-sweep-tests/<name>.json is scanned a
# SECOND time with --tests, recorded as a separate `<name>+tests` row against
# the SAME clone. Today that is express alone, and it closes a real hole: the
# express row is ONE-DIRECTIONAL. Pinning it at 0 catches JS pairing going
# nonzero, but 0 is also what a vanished JS plugin, a broken signature
# extractor or a dropped `.js` extension would produce — so the gate's only JS
# assertion was one that a total JS failure would satisfy. gin covers Go the
# same way rubocop covers Ruby; nothing covered JS positively. The mapped row
# does: it asserts 6 auditable files, which requires the JS plugin to exist,
# the walk to see .js, and --tests to be consulted before convention.
REPOS=(
  "pallets/flask     36e4a824f340fdee7ed50937ba8e7f6bc7d17f81"
  "psf/requests      414f0513c33883adf6f2b46901d4f0b38a455851"
  "pydantic/pydantic e8b6ff8dbaca8d41bc009864db24f7576237e3a2"
  "andrewyng/aisuite cb29165b00f719cceae6a82ed4621cbcb79aaaf7"
  "gin-gonic/gin     34dac209ffb6ef85cc78c5d217bbb7ad001d68fd"
  "rubocop/rubocop   c4607810f6291eeed9a5155feecd03501ac1feb2"
  "expressjs/express a3714473feb3d2908add734d340e7755fd85e0a3"
  "webmozart/assert  2ccb7c2e821038c03a3e6e1700c570c158c55f70"
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

# scan_and_record <row-name> <repo-dir> [extra corral flags...]
#
# One dry run, parsed into one golden row. Factored out because a repo with a
# tenant test map is scanned twice against the same clone and the parse is
# finicky enough (three PCRE captures, one of which is legitimately absent)
# that a second copy would drift from this one.
scan_and_record() {
  local row="$1" dir="$2"
  shift 2

  local report walked candidates ambiguous
  report="$("$BIN" certify --repo "$dir" --dry-run --top 5 "$@")"

  # "  <walked> file(s) walked; <candidates> candidate(s); <jobs> job(s); ..."
  walked=$(printf '%s\n' "$report" | grep -oP '^\s+\K[0-9]+(?= file\(s\) walked)')
  candidates=$(printf '%s\n' "$report" | grep -oP 'walked; \K[0-9]+(?= candidate\(s\))')
  # The by-reason tally only prints a line for reasons that occurred at all,
  # so a repo with zero ambiguous-test demotions has no such line — absence
  # means 0, not a parse failure.
  ambiguous=$(printf '%s\n' "$report" | grep -oP '^\s+\K[0-9]+(?= ambiguous-test$)' || true)
  ambiguous="${ambiguous:-0}"

  if [ -z "$walked" ] || [ -z "$candidates" ]; then
    echo "foreign-sweep: could not parse dry-run output for $row:" >&2
    printf '%s\n' "$report" >&2
    exit 1
  fi

  printf '%s\t%s\t%s\t%s\n' "$row" "$walked" "$candidates" "$ambiguous" >> "$OUT"
}

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

  # Seven network round-trips run on every PR; a single transient GitHub
  # blip must not red a PR that has nothing wrong with it (the same
  # flaky-gate-gets-disabled argument this script's own header makes about
  # pinning SHAs applies here too). Three attempts, short backoff.
  fetched=0
  for attempt in 1 2 3; do
    if git -C "$dir" fetch -q --depth 1 origin "$sha"; then
      fetched=1
      break
    fi
    echo "foreign-sweep: fetch attempt $attempt/3 failed for $repo @ $sha" >&2
    [ "$attempt" -lt 3 ] && sleep $((attempt * 2))
  done
  if [ "$fetched" -ne 1 ]; then
    echo "foreign-sweep: failed to fetch $repo @ $sha after 3 attempts" >&2
    exit 1
  fi
  git -C "$dir" checkout -q FETCH_HEAD

  echo "scanning $name..." >&2
  scan_and_record "$name" "$dir"

  # Second pass with the tenant test map, if this repo has one. A MISSING map
  # is normal (most repos pair by convention and have none); a map that exists
  # but produces no extra row would be a silent hole, so the golden file — not
  # this script — is what makes the mapped row mandatory: drop the JSON and the
  # `<name>+tests` row disappears from actual.tsv and the diff goes red.
  testmap="$REPO_ROOT/testdata/foreign-sweep-tests/$name.json"
  if [ -f "$testmap" ]; then
    echo "scanning $name with tenant test map..." >&2
    scan_and_record "$name+tests" "$dir" --tests "$testmap"
  fi
done

sort -o "$OUT" "$OUT"

if [ ! -f "$GOLDEN" ]; then
  # Reachable only via the explicit FOREIGN_SWEEP_BOOTSTRAP=1 opt-in checked
  # near the top of this script — a missing golden file otherwise fails
  # fast, before the build/clone cost, rather than silently writing a fresh
  # one and passing. A missing-by-default bootstrap would make an accidental
  # deletion (a bad rebase, a careless PR) look like a healthy green gate
  # forever; this path exists only for a deliberate regeneration.
  echo "foreign-sweep: FOREIGN_SWEEP_BOOTSTRAP=1 — writing a fresh golden file at $GOLDEN from this run" >&2
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

echo "foreign-sweep: all $(wc -l < "$OUT") row(s) across ${#REPOS[@]} repo(s) match the golden file" >&2
exit 0
