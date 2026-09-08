#!/bin/bash
# The certify video: one real run on flask, recorded as it happens.
export PATH="${FLASK:-/tmp/corral-rc/flask}/.venv/bin:$PATH"
export GEMINI_API_KEY="$(pass show corralai/gemini-api-key | head -1)" ANTHROPIC_API_KEY="$(pass show corralai/anthropic-api-key | head -1)"
cd ${FLASK:-/tmp/corral-rc/flask}
run() { printf '\n\033[1;32m$\033[0m %s\n' "$*"; "$@"; }
sleep 1
run git log -1 --format='%h %s'
run corral certify --repo . --top 1 --substrate workspace \
  --mutant-model gemini-3.6-flash --writer-model gemini-3.6-flash --derive-model gemini-3.6-flash \
  --critic-model claude-haiku-4-5 --shadow-model off \
  --ledger .corral/ledger -- python -m pytest -q tests/ -p no:cacheprovider
sleep 1
run corral scans list --ledger .corral/ledger
sleep 1
run corral ledger verify .corral/ledger
sleep 2
