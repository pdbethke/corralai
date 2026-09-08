#!/bin/bash
# The review video, part 1: a cold reviewer and an adversarial verifier on flask, as it happens.
export PATH="$HOME/go/bin:$PATH"
cd ${FLASK:-/tmp/corral-rc/flask}
run() { printf '\n\033[1;32m$\033[0m %s\n' "$*"; "$@"; }
sleep 1
run corral review --repo . --scope src/flask/sessions.py \
  --reviewer-model claude-code --verifier-model codex \
  --ledger .corral/ledger
sleep 2
