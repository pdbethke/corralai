#!/usr/bin/env bash
# Asserts every CorralAI licensing invariant. Exit 0 = all hold.
set -uo pipefail
cd "$(dirname "$0")/.."
fail=0
note() { echo "FAIL: $1"; fail=1; }

# 1. LICENSE is Elastic License 2.0, not MIT.
grep -q 'Elastic License 2.0' LICENSE 2>/dev/null || note "LICENSE missing 'Elastic License 2.0'"
grep -qi 'MIT License' LICENSE 2>/dev/null && note "LICENSE still contains 'MIT License'"

# 2. Every tracked .go file carries the SPDX header.
#    Scope to `git ls-files` (the repo), NOT `grep -r .` — the latter also
#    traverses untracked/ignored paths such as sibling git worktrees under
#    .claude/, producing false positives outside the repo proper.
missing=$(git ls-files '*.go' | while IFS= read -r f; do
  grep -q 'SPDX-License-Identifier: Elastic-2.0' "$f" || echo "$f"
done)
[ -n "$missing" ] && note "Go files missing SPDX header:"$'\n'"$missing"

# 3. NOTICE exists and points to commercial licensing.
[ -f NOTICE ] || note "NOTICE missing"
grep -qi 'commercial' NOTICE 2>/dev/null || note "NOTICE missing commercial-licensing pointer"

# 4. CONTRIBUTING + CLA in place.
[ -f CONTRIBUTING.md ] || note "CONTRIBUTING.md missing"
[ -f CLA.md ] || note "CLA.md missing"
[ -f .github/workflows/cla.yml ] || note ".github/workflows/cla.yml missing"

# 5. README no longer advertises MIT.
grep -q 'Plain MIT' README.md 2>/dev/null && note "README still says 'Plain MIT'"
grep -qiE 'MIT license|Plain MIT' docs/DESIGN.md 2>/dev/null && note "docs/DESIGN.md still claims MIT"

if [ "$fail" -eq 0 ]; then echo "OK: all licensing invariants hold"; fi
exit "$fail"
