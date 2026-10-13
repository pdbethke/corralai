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
sudo apt-get install -y python3-pytest-cov ||
	python3 -m pip install --break-system-packages --quiet pytest-cov || true
python3 -c "import pytest_cov" || echo "pytest-cov not available — python selection integration test will SKIP"

echo "== PHP (internal/lang php plugin + its coverage reporter) =="
# php-pcov is REQUIRED for the coverage pre-flight proof, not optional polish:
# PHP is the one language of the six that cannot report coverage without a
# runtime extension, so TestCoverageReporterAgainstRealSuites/php SKIPS without
# it — and a proof that silently skips is the failure mode this repository has
# been bitten by (the container jail's integration tests skipped in CI for
# weeks while the backend was completely broken).
sudo apt-get install -y php-cli php-pcov || true
php --version || echo "php not available — php coverage proof will SKIP"

echo "== Ruby (internal/lang ruby plugin) =="
sudo apt-get install -y ruby ruby-rspec || true
ruby --version || echo "ruby not available — ruby-in-jail test will SKIP"

echo "== JS/TS (internal/lang javascript + typescript plugins) =="
node --version || echo "node missing — js/ts-in-jail tests will SKIP"
# Global npm lands under /usr/lib/node_modules, which IS jail-visible; a
# project-local install is not.
sudo npm install -g typescript @types/node || npm install -g typescript @types/node || true
tsc --version || echo "tsc not available — ts-in-jail test will SKIP"

echo "== Rust (internal/lang rust plugin; the jail's pinned RUSTUP_HOME) =="
# The jail pins RUSTUP_HOME=/usr/local/rustup (internal/sandbox/pinnedenv.go),
# so the toolchain must live there — CARGO_HOME=/usr/local/cargo holds the
# proxies and cargo-nextest — and be world-readable. GitHub's ubuntu-latest
# already has a rustup under the runner's HOME; that one is invisible to the
# jail (see the rule above), so this installs a second, system-wide one rather
# than trusting the image's.
#
# Both pinned installs are verified: rustup-init against the sha256 rust-lang
# publishes beside it, cargo-nextest against the digest recorded here. The
# nextest release tarball is chosen over `cargo install --locked cargo-nextest`
# on purpose: that compiles ~400 crates on every run, minutes on a CI runner,
# for the identical binary. Bumping NEXTEST_VERSION means recomputing the sha.
RUST_VERSION=1.92.0
NEXTEST_VERSION=0.9.148
NEXTEST_SHA256=223ba936714cc861a9cdedad5a8eb664dfdd69d6fbdf6cea6aa8184f523290dc
(
	set -e
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT
	url=https://static.rust-lang.org/rustup/dist/x86_64-unknown-linux-gnu
	curl -fsSL "$url/rustup-init" -o "$tmp/rustup-init"
	echo "$(curl -fsSL "$url/rustup-init.sha256" | cut -d' ' -f1)  $tmp/rustup-init" | sha256sum -c -
	chmod +x "$tmp/rustup-init"
	sudo env RUSTUP_HOME=/usr/local/rustup CARGO_HOME=/usr/local/cargo \
		"$tmp/rustup-init" -y --no-modify-path --profile minimal --default-toolchain "$RUST_VERSION"
	curl -fsSL "https://get.nexte.st/$NEXTEST_VERSION/linux" -o "$tmp/nextest.tgz"
	echo "$NEXTEST_SHA256  $tmp/nextest.tgz" | sha256sum -c -
	sudo tar -xzf "$tmp/nextest.tgz" -C /usr/local/cargo/bin
	sudo chmod -R a+rX /usr/local/rustup /usr/local/cargo
) || echo "rust toolchain install failed — rust-in-jail test will SKIP"
RUSTUP_HOME=/usr/local/rustup /usr/local/cargo/bin/cargo --version || echo "system-wide cargo not available — rust-in-jail test will SKIP"
/usr/local/cargo/bin/cargo-nextest nextest --version || echo "cargo-nextest not available — rust-in-jail test will SKIP"

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
