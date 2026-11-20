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
# the whole run for an unrelated language. Swift is the one exception, and the
# exception is not here: this script stays advisory for Swift too, and
# deploy.yml's step after it fails the job unless the Swift this script pins
# for the runner's release is first on PATH (see the Swift block for why).
#
# A multi-step install runs in a `( set -e; ... )` subshell whose status is
# read on the NEXT line (`rc=$?`), never as `( ... ) || echo`. Bash ignores
# `set -e` inside any command whose status is tested by `||`, `&&`, `if` or
# `!`, so the `||` form runs every remaining step after one fails: a checksum
# that does not match is followed by executing the file it rejected. That is
# how the Rust block below was written until 2026-11-19. A block that tests a
# step's status itself (`step || { ...; exit 1; }`) still stops: the explicit
# exit does not depend on `set -e`.
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

# Hashed requirements (pip-compile --generate-hashes from ci-python-requirements.in).
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PYREQ="$REPO/scripts/ci-python-requirements.txt"

echo "== Python (internal/lang python plugin) =="
sudo apt-get install -y python3-pytest ||
	python3 -m pip install --break-system-packages --quiet --require-hashes -r "$PYREQ" || true
python3 -m pytest --version || echo "pytest not available — python-in-jail test will SKIP"
sudo apt-get install -y python3-pytest-cov ||
	true # pytest-cov is in the same hashed requirements file
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
# A project-local install is not jail-visible; anything under /usr is. So the
# compiler goes into /usr/local/lib and tsc onto /usr/local/bin. It is installed
# with `npm ci` from scripts/npm-pins/ci-toolchains/package-lock.json, so every
# package is pinned by its integrity hash (`npm install -g <name>@<version>`
# pins a version but takes whatever bytes the registry serves for it).
# Change a version in that package.json, then regenerate the lockfile with
#   (cd scripts/npm-pins/ci-toolchains && npm install --package-lock-only)
TSDIR=/usr/local/lib/corral-ci-toolchains
{ sudo install -d "$TSDIR" &&
  sudo cp "$REPO/scripts/npm-pins/ci-toolchains/package.json" "$REPO/scripts/npm-pins/ci-toolchains/package-lock.json" "$TSDIR/" &&
  sudo npm ci --prefix "$TSDIR" --no-audit --no-fund &&
  sudo ln -sf "$TSDIR/node_modules/.bin/tsc" /usr/local/bin/tsc &&
  # The same node_modules is what the real-compiler halves of the project-tsc
  # tests bind into the jail as a project's own; without it they skip.
  if [ -n "${GITHUB_ENV:-}" ]; then
    echo "CORRALAI_TYPESCRIPT_NODE_MODULES=$TSDIR/node_modules" >> "$GITHUB_ENV"
  fi; } || true
tsc --version || echo "tsc not available — ts-in-jail test will SKIP"

echo "== Rust (internal/lang rust plugin; the jail's pinned RUSTUP_HOME) =="
# The jail pins RUSTUP_HOME=/usr/local/rustup (internal/sandbox/pinnedenv.go),
# so the toolchain must live there — CARGO_HOME=/usr/local/cargo holds the
# proxies and cargo-nextest — and be world-readable. Both paths are literals
# here because this is shell; the Go source of truth is
# sandbox.SystemRustupHome and sandbox.SystemCargoHome, and the two must move
# together. GitHub's ubuntu-latest
# already has a rustup under the runner's HOME; that one is invisible to the
# jail (see the rule above), so this installs a second, system-wide one rather
# than trusting the image's.
#
# Both pinned installs are checked, but not equally. rustup-init is verified
# against the sha256 rust-lang publishes beside it. cargo-nextest is checked
# against NEXTEST_SHA256 below, which was COMPUTED from the tarball when it
# was first downloaded (trust on first use) — it was NOT compared with any
# checksum the nextest project publishes. It proves every later run gets the
# same bytes as that first download, not that the first download was genuine.
# The
# nextest release tarball is chosen over `cargo install --locked cargo-nextest`
# on purpose: that compiles ~400 crates on every run, minutes on a CI runner,
# for the identical binary. Bumping NEXTEST_VERSION means recomputing the sha
# (again on first use, unless upstream sums are checked at that point).
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
)
rc=$?
[ "$rc" -eq 0 ] || echo "rust toolchain install failed (exit $rc) — rust-in-jail test will SKIP"
RUSTUP_HOME=/usr/local/rustup /usr/local/cargo/bin/cargo --version || echo "system-wide cargo not available — rust-in-jail test will SKIP"
/usr/local/cargo/bin/cargo-nextest nextest --version || echo "cargo-nextest not available — rust-in-jail test will SKIP"

echo "== Swift (internal/lang swift plugin; real-toolchain tests) =="
# corral's jail does not run Swift (a Swift file is audited on the workspace
# substrate only), so this install is for the tests that call the real
# `swift` / `swiftc`: the coverage capture checked against a live run, the
# suite-exit check and the canary syntax check. (The stock command's
# preflight check skips for Swift whether or not it is installed: Swift's
# preflight needs a repository.) It still goes under
# /usr/local like every toolchain here, so the JAIL-VISIBILITY RULE holds if
# that ever changes. The release tarball is self-contained and relocatable; it
# is unpacked whole into its own directory (not over /usr, where its clang,
# lld and lldb would replace the runner's) and only `swift` and `swiftc` are
# linked onto /usr/local/bin. The driver finds swift-build, swift-test and
# llvm-cov beside the file the link resolves to.
#
# ONE PINNED VERSION PER UBUNTU RELEASE, because swift.org builds a tarball per
# release and does not build every version for every release: its release
# index (https://www.swift.org/api/v1/install/releases.json, read 2026-11-20)
# lists Ubuntu 26.04 from 6.4.0 on and no 6.3.x for it, and
# download.swift.org answered the 6.3.2 Ubuntu 26.04 tarball's URL with a
# redirect to its 404 page. Ubuntu 24.04 keeps 6.3.2, the version this
# branch's Swift work was measured on; Ubuntu 26.04 (GitHub's ubuntu-latest
# from November 2026) gets 6.4.2. On any other release or architecture this
# says so and installs nothing rather than guess a neighbouring build.
#
# THE RUNNER'S OWN SWIFT MUST NOT STAND IN. GitHub's ubuntu-26.04 image ships a
# Swift of its own (6.4.2 on image 20261116.202.1), so a failed install here
# used to leave the tests running on a toolchain nobody pinned. So: the pinned
# toolchain's bin directory is put FIRST on PATH for the job's later steps
# (GITHUB_PATH), and the version this release pins is exported as
# CORRALAI_CI_SWIFT_VERSION (GITHUB_ENV) whether or not the install succeeds.
# deploy.yml's next step fails the job unless `swift` on PATH resolves into
# the pinned directory and reports that version; this script stays advisory,
# like every probe here.
#
# WHAT IS VERIFIED: swift.org publishes no checksum for its tarballs, only a
# detached PGP signature beside each one (the install page,
# https://www.swift.org/install/linux/tarball/, checked 2026-11-19). The
# tarball is checked against that signature, in a throwaway GNUPGHOME, and
# accepted only if gpg reports GOODSIG (so not an expired or revoked key) and
# a VALIDSIG whose primary-key fingerprint is SWIFT_SIGNING_FPR below. Nothing
# is unpacked before that check passes.
#
# A failed step stops the block (see the subshell rule at the top) and links
# nothing. The tarball is unpacked into "$dest.partial", which the block's
# exit trap removes, and is moved to "$dest" only after the unpack and the
# permission fix succeed and both binaries are present; the links are made
# last, and if either fails both are removed. So a run that fails leaves no
# swift or swiftc of its own on PATH. A toolchain an earlier successful run
# installed is replaced only at the move.
#
# WHAT IS TRUSTED: the keyring is fetched from
# https://www.swift.org/keys/all-keys.asc over HTTPS at run time, so it is
# trusted only as far as the fingerprint pin: a key swift.org's server swapped
# in would not carry that fingerprint. The pin itself is the "Swift 6.x
# Release Signing Key" fingerprint as swift.org's install page lists it,
# copied by hand on 2026-11-19 and matched against the key that signed both
# 6.3.2 Ubuntu 24.04 tarballs (x86_64 and aarch64) and, on 2026-11-20, the
# 6.4.2 Ubuntu 26.04 x86_64 tarball. It is trusted on swift.org's word at
# that date, not cross-checked against a second source. Revocation is seen
# only if swift.org's key file carries the revocation; no keyserver is
# asked, because a keyserver outage would otherwise skip the language.
#
# Each tarball is about 1.1 GB (download.swift.org's Content-Length: 6.3.2
# Ubuntu 24.04 x86_64 1.07 GB on 2026-11-19, 6.4.2 Ubuntu 26.04 x86_64
# 1,121,753,798 bytes on 2026-11-20) and nothing caches it between runs.
# Bumping a pinned version means checking which key signed the new release;
# a new major version is signed by a new key, so the pin moves with it.
SWIFT_SIGNING_FPR=52BB7E3DE28A71BE22EC05FFEF80A866B47A981F
# shellcheck source=/dev/null
swift_release=$(. /etc/os-release && echo "${ID:-unknown} ${VERSION_ID:-unknown}")
# The runtime dependencies swift.org's tarball install page lists for each
# release (checked 2026-11-20).
case "$swift_release" in
"ubuntu 24.04")
	SWIFT_VERSION=6.3.2
	swift_deps="binutils git gnupg2 libc6-dev libcurl4-openssl-dev libedit2 libgcc-13-dev libncurses-dev libpython3-dev libsqlite3-0 libstdc++-13-dev libxml2-dev libz3-dev pkg-config tzdata zip unzip zlib1g-dev"
	;;
"ubuntu 26.04")
	SWIFT_VERSION=6.4.2
	swift_deps="binutils binutils-gold git gnupg2 libc6-dev libcurl4-openssl-dev libedit2 libgcc-15-dev libpython3-dev libsqlite3-0 libstdc++-15-dev libxml2-dev libncurses-dev libz3-dev pkg-config tzdata unzip zip zlib1g-dev"
	;;
*)
	SWIFT_VERSION=
	swift_deps=
	;;
esac
swift_dest="/usr/local/swift-$SWIFT_VERSION"
if [ -n "$SWIFT_VERSION" ]; then
	# Written before the install, so a failed install still leaves the gate
	# in deploy.yml knowing what it must find.
	[ -z "${GITHUB_ENV:-}" ] || echo "CORRALAI_CI_SWIFT_VERSION=$SWIFT_VERSION" >>"$GITHUB_ENV"
	[ -z "${GITHUB_PATH:-}" ] || echo "$swift_dest/usr/bin" >>"$GITHUB_PATH"
fi
(
	set -e
	if [ -z "$SWIFT_VERSION" ]; then
		echo "swift: this script pins Swift for Ubuntu 24.04 and 26.04 only; this host is $swift_release" >&2
		exit 1
	fi
	rel=${swift_release#ubuntu }
	case "$(uname -m)" in
	x86_64) platdir="ubuntu${rel//./}" suffix= ;;
	aarch64) platdir="ubuntu${rel//./}-aarch64" suffix=-aarch64 ;;
	*)
		echo "swift: no Swift $SWIFT_VERSION tarball is provisioned for $(uname -m)" >&2
		exit 1
		;;
	esac
	# shellcheck disable=SC2086 # the list is words on purpose
	sudo apt-get install -y $swift_deps
	dest="$swift_dest"
	stage="$dest.partial"
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"; sudo rm -rf "$stage"' EXIT
	name="swift-$SWIFT_VERSION-RELEASE-ubuntu$rel$suffix"
	url="https://download.swift.org/swift-$SWIFT_VERSION-release/$platdir/swift-$SWIFT_VERSION-RELEASE/$name.tar.gz"
	curl -fsSL "$url" -o "$tmp/swift.tar.gz"
	curl -fsSL "$url.sig" -o "$tmp/swift.tar.gz.sig"
	# --compressed: the server sends this file gzip-encoded whether asked or not.
	curl -fsSL --compressed https://www.swift.org/keys/all-keys.asc -o "$tmp/keys.asc"
	export GNUPGHOME="$tmp/gnupg"
	install -d -m 700 "$GNUPGHOME"
	gpg --batch --quiet --import "$tmp/keys.asc"
	gpg --batch --status-fd 1 --verify "$tmp/swift.tar.gz.sig" "$tmp/swift.tar.gz" >"$tmp/status"
	grep -q '^\[GNUPG:\] GOODSIG ' "$tmp/status" || {
		echo "swift: gpg reported no good signature (an expired or revoked key gives none)" >&2
		exit 1
	}
	grep -Eq "^\[GNUPG:\] VALIDSIG .* $SWIFT_SIGNING_FPR\$" "$tmp/status" || {
		echo "swift: the signature is not by the pinned Swift release key $SWIFT_SIGNING_FPR" >&2
		exit 1
	}
	sudo rm -rf "$stage"
	sudo install -d "$stage"
	sudo tar -xzf "$tmp/swift.tar.gz" -C "$stage" --strip-components=1
	sudo chmod -R a+rX "$stage"
	test -x "$stage/usr/bin/swift" && test -x "$stage/usr/bin/swiftc" || {
		echo "swift: the unpacked tarball has no usr/bin/swift and usr/bin/swiftc" >&2
		exit 1
	}
	sudo rm -rf "$dest"
	sudo mv "$stage" "$dest"
	{ sudo ln -sf "$dest/usr/bin/swift" /usr/local/bin/swift &&
		sudo ln -sf "$dest/usr/bin/swiftc" /usr/local/bin/swiftc; } || {
		sudo rm -f /usr/local/bin/swift /usr/local/bin/swiftc
		echo "swift: linking swift and swiftc onto /usr/local/bin failed" >&2
		exit 1
	}
)
rc=$?
[ "$rc" -eq 0 ] || echo "swift toolchain install failed (exit $rc) — the pinned-Swift check in deploy.yml fails the job unless the Swift pinned for this release${SWIFT_VERSION:+ ($SWIFT_VERSION)} is first on PATH"
# What the job's later steps will run: the pinned directory first, as
# GITHUB_PATH puts it. A different version here is the runner's own.
PATH="$swift_dest/usr/bin:$PATH" command -v swift || echo "swift not available"
PATH="$swift_dest/usr/bin:$PATH" swift --version || echo "swift --version failed"

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
