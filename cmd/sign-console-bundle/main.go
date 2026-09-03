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

	// UPSERT, don't overwrite. The manifest's Version is part of the signed
	// bytes, so one signature covers exactly one build — and a file holding
	// only one meant the repository had to choose between a working
	// development tree ("dev") and a working release ("vX.Y.Z"). It chose
	// development, silently, for its entire life: every released brain served
	// a signature no client could verify.
	//
	// Existing entries for OTHER versions are preserved, so signing a release
	// does not un-sign the development tree and vice versa.
	existing, _ := os.ReadFile(sigPath) // #nosec G304 -- fixed repo-relative path
	fmt.Print(upsert(string(existing), version, hex.EncodeToString(sig)))
}

// sigPath is the committed signature file, relative to the repository root
// (which is where scripts/sign-console-bundle.sh runs this).
const sigPath = "internal/ui/console.manifest.sig"

// upsert returns the signature file with version's entry set to sig, keeping
// every other entry and their order.
//
// A legacy file — a bare signature with no version column — is migrated to the
// "dev" entry it always implicitly was, so no committed signature had to be
// regenerated to adopt the format.
func upsert(existing, version, sig string) string {
	var out []string
	replaced := false
	trimmed := strings.TrimSpace(existing)
	if trimmed != "" && !strings.ContainsAny(trimmed, " \t\n") {
		trimmed = "dev " + trimmed
	}
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if v, _, found := strings.Cut(line, " "); found && strings.TrimSpace(v) == version {
			out = append(out, version+" "+sig)
			replaced = true
			continue
		}
		out = append(out, line)
	}
	if !replaced {
		out = append(out, version+" "+sig)
	}
	return strings.Join(out, "\n") + "\n"
}
