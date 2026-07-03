#!/usr/bin/env bash
# Asserts every CorralAI security invariant. Exit 0 = all hold.
set -uo pipefail
cd "$(dirname "$0")/.."
fail=0
note() { echo "FAIL: $1"; fail=1; }

# Locate gosec — prefer PATH, fall back to GOPATH/bin.
GOSEC="$(command -v gosec 2>/dev/null || echo "$(go env GOPATH)/bin/gosec")"

# 1. gofmt — every tracked .go file must be properly formatted.
bad=$(git ls-files '*.go' | xargs gofmt -l 2>/dev/null)
[ -n "$bad" ] && note "unformatted files:"$'\n'"$bad"

# 2. gosec — zero MEDIUM+ severity issues (HIGH + MEDIUM; LOW is not gated).
if [ -x "$GOSEC" ] || command -v "$GOSEC" &>/dev/null; then
    if ! "$GOSEC" -quiet -severity=medium -confidence=medium -fmt=text ./... 2>&1; then
        note "gosec found MEDIUM+ issues"
    fi
else
    note "gosec not found; install: go install github.com/securego/gosec/v2/cmd/gosec@latest"
fi

# 3. govulncheck — optional; non-fatal if not installed.
GOVULN="$(command -v govulncheck 2>/dev/null || echo "$(go env GOPATH)/bin/govulncheck")"
if [ -x "$GOVULN" ] || command -v "$GOVULN" &>/dev/null; then
    if ! "$GOVULN" ./... 2>&1; then
        note "govulncheck found vulnerabilities"
    fi
fi

if [ "$fail" -eq 0 ]; then echo "OK: all security invariants hold"; fi
exit "$fail"
