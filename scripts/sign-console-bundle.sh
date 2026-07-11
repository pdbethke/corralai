#!/usr/bin/env bash
# SPDX-License-Identifier: Elastic-2.0
#
# scripts/sign-console-bundle.sh — signs the console SPA bundle manifest
# (internal/ui.CanonicalManifestBytes) with the corralai release key and
# writes the detached hex signature to internal/ui/console.manifest.sig,
# which the daemon embeds (//go:embed) and serves at GET
# /console/manifest.sig.
#
# The signature is over the EXACT canonical JSON bytes buildManifest
# produces for VERSION — the same bytes the daemon serves at
# /console/manifest.json once built with -ldflags "-X main.version=$VERSION".
# Signing with a version that doesn't match what the build actually stamps
# produces a signature that will never verify against that daemon's served
# manifest — always pass the version this build will actually carry.
#
# Usage:
#   CORRALAI_RELEASE_KEY=<hex ed25519 seed> scripts/sign-console-bundle.sh <version>
#
# For DEV/TEST use (no real release key available), omit
# CORRALAI_RELEASE_KEY — this falls back to the committed dev signing key
# (scripts/dev-console-signing-key.hex). That file is NOT a secret: it
# signs nothing that matters beyond local dev/CI, and is published so
# anyone can independently verify a dev build's bundle. It must NEVER be
# used to sign a real release. The dev key's public half is pinned in
# internal/ui/console_bundle.go as ConsoleReleasePubKeyHex.
#
# Output: internal/ui/console.manifest.sig — hex-encoded Ed25519 signature,
# no trailing newline (matches the exact bytes GET /console/manifest.sig
# serves; see console_bundle.go's consoleManifestSig).
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${1:-dev}"
KEY_HEX="${CORRALAI_RELEASE_KEY:-}"
DEV_KEY_FILE="scripts/dev-console-signing-key.hex"

if [ -z "$KEY_HEX" ]; then
  if [ ! -f "$DEV_KEY_FILE" ]; then
    echo "sign-console-bundle: no \$CORRALAI_RELEASE_KEY set and no dev key at $DEV_KEY_FILE" >&2
    exit 1
  fi
  KEY_HEX="$(cat "$DEV_KEY_FILE")"
  echo "sign-console-bundle: no \$CORRALAI_RELEASE_KEY — using the committed DEV signing key (NOT for a real release)" >&2
fi

go run ./cmd/sign-console-bundle "$VERSION" "$KEY_HEX" > internal/ui/console.manifest.sig
echo "sign-console-bundle: wrote internal/ui/console.manifest.sig (version=$VERSION)" >&2
