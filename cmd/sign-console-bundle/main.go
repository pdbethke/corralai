// SPDX-License-Identifier: Elastic-2.0

// Command sign-console-bundle is scripts/sign-console-bundle.sh's Go half:
// it computes the canonical console bundle manifest
// (ui.CanonicalManifestBytes — the EXACT bytes GET /console/manifest.json
// serves for a given version) and signs it with an Ed25519 key, printing
// the hex-encoded detached signature to stdout (no trailing newline).
//
// Kept as its own tiny `go run` command — rather than reimplemented in
// shell — so the manifest bytes are produced by the SAME
// buildManifest/json.Marshal code path the daemon itself uses when it
// serves /console/manifest.json. Two implementations of "walk web/ and
// hash it" would drift; one shared path (internal/ui.CanonicalManifestBytes)
// can't.
//
// The private key seed is read from argv (scripts/sign-console-bundle.sh's
// job to source it, from $CORRALAI_RELEASE_KEY or the committed dev key)
// and never logged — only the resulting signature is written to stdout.
package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/pdbethke/corralai/internal/ui"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: sign-console-bundle <version> <hex-ed25519-seed>")
		os.Exit(1)
	}
	version := os.Args[1]
	seedHex := strings.TrimSpace(os.Args[2])
	seed, err := hex.DecodeString(seedHex)
	if err != nil || len(seed) != ed25519.SeedSize {
		fmt.Fprintln(os.Stderr, "sign-console-bundle: signing key must be a hex-encoded 32-byte Ed25519 seed")
		os.Exit(1)
	}
	priv := ed25519.NewKeyFromSeed(seed)

	manifestBytes, err := ui.CanonicalManifestBytes(version)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sign-console-bundle: build manifest:", err)
		os.Exit(1)
	}

	sig := ed25519.Sign(priv, manifestBytes)
	fmt.Print(hex.EncodeToString(sig))
}
