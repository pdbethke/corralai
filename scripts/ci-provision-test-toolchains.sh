#!/usr/bin/env bash
# Provision the toolchains corral's own `go test ./...` needs on a CI runner.
#
# corral's suite is NOT self-contained: the multi-language audit gate exercises
# each internal/lang plugin against the REAL tool (pytest, ruby, tsc, bwrap), so
# a bare runner fails or skips tests that a provisioned one actually runs. This
# was factored out when a second workflow needed it — the self-audit, which runs
# `go test ./...` as the suite UNDER AUDIT and reported `baseline-failed` on a
# runner missing pytest, i.e. corral could not grade itself because its own
# suite could not pass in a clean environment.
#
# Every install is best-effort (`|| true`) and followed by a version probe: a
# missing tool must degrade to a SKIPPED language test, never a hard failure of
# the whole run for an unrelated language.
#
# JAIL-VISIBILITY RULE (load-bearing, applies to every language here): the bwrap
# grading jail binds /usr read-only and does NOT bind the user's home. A tool
# installed with --user / --user-install / a project-local npm install is
# INVISIBLE inside the jail, and grading then fails closed even though the tool
# reports as present on the host. Everything below installs system-wide on
# purpose. Never add --user here.
set -uo pipefail

# Some runner images ship with empty or stale apt lists, so a bare
# `apt-get install` fails to find the package. Done once, up front.
sudo apt-get update || true

echo "== Python (internal/lang python plugin) =="
sudo apt-get install -y python3-pytest ||
	python3 -m pip install --break-system-packages --quiet pytest || true
python3 -m pytest --version || echo "pytest not available — python-in-jail test will SKIP"

echo "== Ruby (internal/lang ruby plugin) =="
sudo apt-get install -y ruby ruby-rspec || true
ruby --version || echo "ruby not available — ruby-in-jail test will SKIP"

echo "== JS/TS (internal/lang javascript + typescript plugins) =="
node --version || echo "node missing — js/ts-in-jail tests will SKIP"
# Global npm lands under /usr/lib/node_modules, which IS jail-visible; a
# project-local install is not.
sudo npm install -g typescript @types/node || npm install -g typescript @types/node || true
tsc --version || echo "tsc not available — ts-in-jail test will SKIP"

echo "== bubblewrap (jail tests) =="
sudo apt-get install -y bubblewrap || true
# Ubuntu 24.04 blocks unprivileged user namespaces, which is what bwrap needs.
# Relaxing it here is what lets the jail tests run instead of skipping; if it
# fails they skip as before.
#
# DELIBERATE SANDBOX WEAKENING, accepted for an EPHEMERAL runner only: a job
# that does this may also run `go install …@<ref>`, so third-party code executes
# on a host whose unprivileged-userns restriction is off. The trade is knowingly
# made — a jail test that SKIPS silently proves nothing about the jail, which is
# the thing corral's whole audit rests on. Never apply this to a durable host.
sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0 || true
bwrap --version || echo "bwrap unavailable — jail tests will SKIP"

# Always succeed: every probe above is advisory. A provisioning failure must not
# fail the caller's job — the tests themselves decide to run or skip.
exit 0
