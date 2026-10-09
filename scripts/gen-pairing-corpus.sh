#!/usr/bin/env bash
# SPDX-License-Identifier: Elastic-2.0
#
# gen-pairing-corpus.sh — writes testdata/pairing-corpus.tsv: every tracked path
# of the foreign sweep's pinned repositories (same SHAs as foreign-sweep.sh) and
# of this repository, as "repo<TAB>path", sorted. It is the input to the pairing
# characterization goldens (Round B1): real trees, because that is where pairing's
# edge cases live. Regenerating it is deliberate — it changes what the goldens
# cover — and needs the network; nothing in CI runs it.
set -euo pipefail
here="$(cd "$(dirname "$0")/.." && pwd)"
out="$here/testdata/pairing-corpus.tsv"
work="$(mktemp -d)"; trap 'rm -rf "$work"' EXIT
# The pins are read from foreign-sweep.sh, never restated: one list of pins.
mapfile -t pins < <(sed -n 's/^  "\([^ ]*\) *\([0-9a-f]\{40\}\)"$/\1 \2/p' "$here/scripts/foreign-sweep.sh")
[ "${#pins[@]}" -ge 8 ] || { echo "found ${#pins[@]} pins in foreign-sweep.sh, want >= 8" >&2; exit 1; }
{
  for pin in "${pins[@]}"; do
    repo="${pin%% *}"; sha="${pin##* }"; dir="$work/${repo//\//_}"
    git init -q "$dir"
    git -C "$dir" fetch -q --depth 1 "https://github.com/$repo.git" "$sha"
    git -C "$dir" ls-tree -r --name-only "$sha" | sed "s|^|$repo\t|"
  done
  git -C "$here" ls-files | sed 's|^|pdbethke/corralai\t|'
} | LC_ALL=C sort > "$out"
echo "wrote $(wc -l < "$out") rows to $out"
