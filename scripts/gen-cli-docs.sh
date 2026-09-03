#!/usr/bin/env bash
# SPDX-License-Identifier: Elastic-2.0
#
# scripts/gen-cli-docs.sh [--check] — the CLI reference generator. Builds
# every cmd/* binary, captures its REAL -h output (stdout+stderr combined —
# some binaries print usage to one, some to the other, and the docs must not
# silently miss whichever a given binary picked), pulls the env-var doc-
# comment block out of its main.go header, and emits markdown for each into
# BOTH docs/cli/ (repo) and site/src/content/docs/docs/cli/ (Starlight tree —
# the collection-root "docs" nesting Task 3 uses for every /docs/... page).
#
# --check: regenerate into a scratch dir and diff against the committed
# files instead of overwriting them; exits 1 with the diff on any drift.
# Docs that lie about a flag fail CI (wired into .github/workflows/deploy-site.yml).
set -euo pipefail
cd "$(dirname "$0")/.."

# DERIVED from cmd/, not hand-listed. The hand-listed version omitted
# corral-desktop, corral-recordings-import (which registers five flags) and
# sign-console-bundle — so docs/cli/corral-desktop.md sat in this directory as
# HAND-WRITTEN prose with no Usage block, while cmd/corral/surfaces_test.go
# cites this directory as its authority precisely because "--check already
# guarantees those files are what the binaries really print". That guarantee
# was false for one of seven.
#
# This is the third hand-maintained enumeration in this area to be replaced by
# a derived one, after the subcommand list below and the dispatch allowlist in
# main.go. A new binary now gets a reference for free.
BINARIES=()
for d in cmd/*/; do BINARIES+=("$(basename "$d")"); done
CHECK=0
[ "${1:-}" = "--check" ] && CHECK=1

WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

echo "building ${BINARIES[*]}..."
for b in "${BINARIES[@]}"; do
  go build -o "$WORKDIR/$b" "./cmd/$b"
done

extract_env_block() {
  # Pulls the doc-comment lines between "// Env:" and the next blank
  # doc-comment line (or the "package main" line, whichever comes first)
  # out of a main.go, stripping the leading "//" and one optional tab/space.
  local mainfile="$1"
  awk '
    /^\/\/ Env:/ { inenv=1; next }
    /^package main/ { inenv=0 }
    inenv && /^\/\/\t?/ {
      line=$0
      sub(/^\/\/\t?/, "", line)
      if (line == "" ) { blank++; if (blank>1) { inenv=0; next } }
      else { blank=0 }
      print line
    }
    inenv && !/^\/\// { inenv=0 }
  ' "$mainfile"
}

capture_help() {
  local bin="$1"
  # Combine stdout+stderr — corral-admin/corral-agent/corral-harness print to
  # stderr, corral-top/corral-observe use Go's default flag.Usage (also
  # stderr); this generator must not assume which.
  #
  # Determinism: corral-top and corral-observe use Go's default flag.Usage,
  # which prints "Usage of <os.Args[0]>:" — if invoked as an absolute path
  # under $WORKDIR (a fresh mktemp dir every run), that path changes on every
  # single invocation and the generated doc would never be byte-stable, so
  # --check would always report drift even with zero code changes. Running
  # from inside WORKDIR and invoking "./$bin" pins os.Args[0] to a constant
  # "./$bin" regardless of where the temp dir landed.
  ( cd "$WORKDIR" && "./$bin" -h 2>&1 ) || true
}

# SUBCOMMANDS whose FLAGS are worth documenting. The top-level `-h` lists the
# subcommands but not their flag sets, so every flag added to `certify --local`
# or `certify --repo` shipped undocumented — that is exactly how --local-endpoint
# and --record-stream reached a release with zero mentions anywhere user-facing.
# DERIVED from the binary's own top-level -h, not hand-listed. The hand-listed
# version named four subcommands, and the other six with flags — demo, doctor,
# eval, scorecard, seal, verify — shipped with 24 flags and no reference section
# between them. Including `doctor`, which getting-started tells a new operator to
# run first. That is the same defect the comment above describes, one level up:
# the fix was applied to the list and the list stayed hand-maintained.
#
# ARGV_FOR holds the few subcommands that cannot reach their flag set from the
# bare name — a positional the parser demands before it will print flags, or a
# sub-subcommand. Anything absent is invoked as `corral <name> -h`. A new
# subcommand therefore gets a section for free; only an argument-hungry one
# needs a line here, and its absence shows up as a missing section rather than
# as silence.
declare -A ARGV_FOR=(
  [certify]="certify --local"
  [scans]="scans push"
)
# `certify` carries three distinct flag sets behind one name, so it is listed
# explicitly rather than derived — deriving would document only the first.
CORRAL_EXTRA_SUBCOMMANDS=("certify --repo ." "certify verify")

derive_corral_subcommands() {
  local names sub
  names="$( ( cd "$WORKDIR" && ./corral -h 2>&1 ) \
    | grep -oE '^  corral [a-z][a-z-]*' | awk '{print $2}' | sort -u )"
  for n in $names; do
    sub="${ARGV_FOR[$n]:-$n}"
    echo "$sub"
  done
  for sub in "${CORRAL_EXTRA_SUBCOMMANDS[@]}"; do echo "$sub"; done
}

capture_sub_help() {
  local bin="$1"
  shift
  # Same stdout+stderr merge and same ./$bin invocation as capture_help, for the
  # same determinism reason: os.Args[0] must not carry the temp dir.
  ( cd "$WORKDIR" && "./$bin" "$@" -h 2>&1 ) || true
}

gen_one() {
  local b="$1" out="$2"
  local help env_block
  help="$(capture_help "$b")"
  env_block="$(extract_env_block "cmd/$b/main.go")"
  {
    echo "---"
    echo "title: $b"
    echo "description: Generated CLI reference for $b — never hand-written; see scripts/gen-cli-docs.sh."
    echo "---"
    echo
    echo "> Generated by \`scripts/gen-cli-docs.sh\` from $b's own \`-h\` output and its main.go doc comment. Do not hand-edit — run \`scripts/gen-cli-docs.sh\` and commit the result."
    echo
    echo "## Usage"
    echo
    echo '```'
    echo "$help"
    echo '```'
    if [ "$b" = "corral" ]; then
      while IFS= read -r sub; do
        # shellcheck disable=SC2086 -- deliberate word splitting: an argv prefix
        subhelp="$(capture_sub_help "$b" $sub)"
        [ -n "$subhelp" ] || continue
        echo
        # The heading names the command a READER types; a positional argument
        # this script supplies only to reach the flag set (e.g. the "." in
        # `certify --repo .`) is not part of that and is trimmed.
        echo "## \`$b ${sub% .}\` flags"
        echo
        echo '```'
        echo "$subhelp"
        echo '```'
      done < <(derive_corral_subcommands)
    fi
    if [ -n "$env_block" ]; then
      echo
      echo "## Environment variables"
      echo
      echo '```'
      echo "$env_block"
      echo '```'
    fi
  } > "$out"
}

SITE_CLI_DIR="site/src/content/docs/docs/cli"

if [ "$CHECK" -eq 1 ]; then
  CHECK_DIR="$WORKDIR/check"
  mkdir -p "$CHECK_DIR/docs" "$CHECK_DIR/site"
  fail=0
  for b in "${BINARIES[@]}"; do
    gen_one "$b" "$CHECK_DIR/docs/$b.md"
    cp "$CHECK_DIR/docs/$b.md" "$CHECK_DIR/site/$b.md"
    if ! diff -u "docs/cli/$b.md" "$CHECK_DIR/docs/$b.md" >/dev/null 2>&1; then
      echo "FAIL: docs/cli/$b.md has drifted from $b's real -h output:" >&2
      diff -u "docs/cli/$b.md" "$CHECK_DIR/docs/$b.md" >&2 || true
      fail=1
    fi
    if ! diff -u "$SITE_CLI_DIR/$b.md" "$CHECK_DIR/site/$b.md" >/dev/null 2>&1; then
      echo "FAIL: $SITE_CLI_DIR/$b.md has drifted from $b's real -h output:" >&2
      diff -u "$SITE_CLI_DIR/$b.md" "$CHECK_DIR/site/$b.md" >&2 || true
      fail=1
    fi
  done
  if [ "$fail" -ne 0 ]; then
    echo "Run: scripts/gen-cli-docs.sh   (then commit the regenerated docs)" >&2
    exit 1
  fi
  echo "OK: generated CLI docs match every binary's real -h output"
else
  mkdir -p docs/cli "$SITE_CLI_DIR"
  for b in "${BINARIES[@]}"; do
    gen_one "$b" "docs/cli/$b.md"
    cp "docs/cli/$b.md" "$SITE_CLI_DIR/$b.md"
    echo "wrote docs/cli/$b.md and $SITE_CLI_DIR/$b.md"
  done
fi
