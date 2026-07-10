#!/usr/bin/env bash
# SPDX-License-Identifier: Elastic-2.0
#
# scripts/demo-certify-trustless.sh — the certify-trustless-tier demo tape:
# certify a real check, verify it (publicly witnessed), tamper the exported
# record, and watch verify catch it. This is the sequence the LinkedIn
# article's recording is built from — each step below is commented so it
# reads as narration when played back.
#
# PREREQUISITES (this script needs a live brain + network; it is NOT run in
# CI and has no assertions of its own beyond "the commands behave as
# narrated" — read the printed output):
#   - A running corral brain reachable at $CORRAL_BRAIN, built from this
#     branch, with CORRALAI_BUILD_DB and CORRALAI_CERTIFY_KEY_FILE set so
#     report_build is enabled (see cmd/corral/main.go's env var docs).
#   - That brain able to reach the public internet (rekor.sigstore.dev by
#     default, or CORRALAI_REKOR_URL if you've pointed it at a different
#     Rekor instance) — Anchor and the TUF trust-root fetch both need it.
#   - `corral secret set CORRALAI_BRAIN_TOKEN <token>` already done locally,
#     so `corral certify` can authenticate to $CORRAL_BRAIN.
#   - `corral` built and on PATH (`go build -o /tmp/corral ./cmd/corral`,
#     or `go install ./cmd/corral`).
#
# Usage:
#   CORRAL_BRAIN=https://brain.example scripts/demo-certify-trustless.sh
#
# Env:
#   CORRAL_BRAIN   the brain MCP endpoint to certify against (required)
#   CORRAL         path to the corral binary (default: corral, i.e. PATH)

set -euo pipefail
cd "$(dirname "$0")/.."

CORRAL="${CORRAL:-corral}"
: "${CORRAL_BRAIN:?set CORRAL_BRAIN to a running brain's MCP endpoint, e.g. https://brain.example}"

WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT
RECORD="$WORKDIR/record.json"

echo "=== 1. Certify a passing check ==="
echo "corral certify runs the check ITSELF (streaming its output live),"
echo "reports the result to the brain's report_build tool, and the brain:"
echo "  - signs a DSSE envelope over the full build statement,"
echo "  - anchors that envelope to a public transparency log (Rekor),"
echo "  - hands back a record — with the Rekor evidence embedded — to --out."
echo
"$CORRAL" certify --brain "$CORRAL_BRAIN" --out "$RECORD" -- true
echo
echo "--- record.json (trimmed) ---"
python3 -c "
import json
rec = json.load(open('$RECORD'))
print(json.dumps({k: rec[k] for k in ('head', 'anchored') if k in rec}, indent=2))
"
echo

echo "=== 2. Verify the record — independently, against the brain's published key ==="
echo "This checks FOUR things, entirely offline except for --brain's one pubkey"
echo "fetch: the DSSE signature, the ledger hash chain, the statement's subject"
echo "binding to that ledger, and — because the record claims anchored=true —"
echo "the Rekor inclusion proof against the Sigstore TUF trust root. Nothing"
echo "here trusts the brain's in-process state or the record's own claims."
echo
"$CORRAL" certify verify "$RECORD" --brain "$CORRAL_BRAIN"
echo
echo "^ that line names the Rekor log index and the time the log integrated"
echo "  the entry — a third party can confirm this independently at"
echo "  https://search.sigstore.dev, with no corral credentials at all."
echo

echo "=== 3. Tamper the record's predicate, then re-verify ==="
echo "sed-edit the DSSE envelope's embedded statement (a real forger's only"
echo "lever — the record's separate cosmetic 'statement' field is never"
echo "trusted, only the envelope's own embedded copy is) so the check appears"
echo "to have run a different command than it actually did."
echo
python3 -c "
import base64, json

path = '$RECORD'
rec = json.load(open(path))
env = json.loads(rec['signature'])
payload = json.loads(base64.b64decode(env['payload']))
# The tamper: rewrite the certified command after the fact.
payload['predicate']['buildDefinition']['externalParameters']['command'] = 'rm -rf /'
env['payload'] = base64.b64encode(json.dumps(payload).encode()).decode()
rec['signature'] = json.dumps(env)
json.dump(rec, open(path, 'w'), indent=2)
"
echo "--- tampered record written ---"
echo
echo "Re-verify: this MUST fail — the DSSE signature no longer covers the"
echo "rewritten payload, so the very first check (signature) catches it."
echo "The broken link is named explicitly, not just 'verification failed'."
echo
if "$CORRAL" certify verify "$RECORD" --brain "$CORRAL_BRAIN"; then
    echo "!!! expected verify to FAIL on the tampered record — it did not." >&2
    exit 1
fi
echo
echo "^ verify exited non-zero and named 'signature' as the broken link —"
echo "  tampering the predicate after the fact does not survive re-verification,"
echo "  the whole point of a signed, publicly-witnessed accountability record."
