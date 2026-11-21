#!/usr/bin/env bash
# SPDX-License-Identifier: Elastic-2.0
#
# scripts/ci-check-pinned-swift.sh — fails unless the `swift` on PATH is the
# toolchain scripts/ci-provision-test-toolchains.sh pinned for this runner's
# Ubuntu release. Run after that script, in a later step of the same job, by
# every workflow that runs it (deploy.yml and self-audit.yml), so the check is
# written once.
#
# Why it fails rather than warns: GitHub's ubuntu-26.04 image ships a Swift of
# its own, and the first CI run on it (2026-11-20) ran the real-toolchain Swift
# tests on a version nothing here pinned, because the provisioning had refused
# the release. The provisioning script stays advisory, like every probe in it;
# this is the step that holds it to its pin.
#
# What it reads: CORRALAI_CI_SWIFT_VERSION, which the provisioning script
# writes to GITHUB_ENV before it installs anything (so a failed install still
# leaves this check knowing what it must find), and PATH, whose first entry the
# provisioning script makes the pinned toolchain's corral-links directory
# (GITHUB_PATH). It passes only when `swift` resolves, links followed, into
# /usr/local/swift-<version>/ and `swift --version` reports that version.
set -uo pipefail

want="${CORRALAI_CI_SWIFT_VERSION:-}"
if [ -z "$want" ]; then
	echo "::error::scripts/ci-provision-test-toolchains.sh pins no Swift for this runner's release; the real-toolchain Swift tests would skip or run on an unpinned swift." >&2
	exit 1
fi
# Resolved only when there is one: on ubuntu:26.04 `readlink -f ""` prints
# "/", so an absent swift was reported as "/" (2026-11-20).
at=
if p=$(command -v swift); then at=$(readlink -f "$p" || true); fi
got=$(swift --version 2>/dev/null | head -n 1 || true)
echo "swift on PATH: ${at:-none}; $got"
case "$at" in
"/usr/local/swift-$want/"*) ;;
*)
	echo "::error::swift on PATH is ${at:-absent}, not the pinned /usr/local/swift-$want toolchain." >&2
	exit 1
	;;
esac
if [ "$got" != "Swift version $want (swift-$want-RELEASE)" ]; then
	echo "::error::swift --version says '$got', want Swift $want." >&2
	exit 1
fi
