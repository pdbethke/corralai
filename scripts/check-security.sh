#!/usr/bin/env bash
# Asserts every CorralAI security invariant. Exit 0 = all hold.
set -uo pipefail
cd "$(dirname "$0")/.."
fail=0
note() { echo "FAIL: $1"; fail=1; }

# Locate gosec — prefer PATH, fall back to GOPATH/bin.
GOSEC="$(command -v gosec 2>/dev/null || echo "$(go env GOPATH)/bin/gosec")"

# 1. gofmt — every tracked .go file must be properly formatted.
#    The tool's ABSENCE used to read as "clean": stderr went to /dev/null and
#    empty stdout was success, so on a machine without gofmt this gate passed
#    over any amount of unformatted code. A gate that cannot fail is not a
#    gate. Found by a cold review, 2026-09-08 (R7).
command -v gofmt >/dev/null 2>&1 || note "gofmt is not installed — this gate cannot run, so it must not pass"
if command -v gofmt >/dev/null 2>&1; then
  bad=$(git ls-files '*.go' | xargs gofmt -l)
  [ -n "$bad" ] && note "unformatted files:"$'\n'"$bad"
fi

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
