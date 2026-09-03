// SPDX-License-Identifier: Elastic-2.0

// Command verify-console-signature checks that the COMMITTED console bundle
// signature verifies, for a given version, against the configured trust anchor.
//
// It exists because a release shipped a console no client could verify, and
// nothing noticed. The manifest's Version is part of the signed bytes and the
// only committed signature covered "dev" — the version an unstamped build
// reports — while `go install ...@vX` resolves a real module version. Every
// released brain therefore served a manifest whose signature every thin client
// refused, and the only place that was visible was an operator's terminal.
//
// Run it with the version a release will report and the release public key in
// CORRALAI_CONSOLE_PUBKEY:
//
//	CORRALAI_CONSOLE_PUBKEY=<hex> go run ./cmd/verify-console-signature v0.8.4
//
// It is the release workflow's gate, and it is deliberately a separate binary
// rather than a test: a test proves the repository is self-consistent, and this
// has to answer a different question — whether the artefact a STRANGER will
// install can be verified with the key they were given.
package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/pdbethke/corralai/internal/consolebundle"
	"github.com/pdbethke/corralai/internal/ui"
)

const usage = `verify-console-signature — check that the COMMITTED console bundle
signature verifies for a given version against the configured trust anchor.

Usage:
  verify-console-signature <version>        e.g. v0.8.4

Environment:
  CORRALAI_CONSOLE_PUBKEY   the release public key (hex) to verify against
  CORRALAI_CONSOLE_DEV=1    instead accept the PUBLISHED dev key, whose private
                            half is committed to this repository — development
                            and CI only

Exits non-zero when the committed signature does not verify for that version,
which is what every thin client would report as "manifest signature INVALID".
`

func main() {
	// -h is answered BEFORE the argument count check, and that ordering is
	// load-bearing: scripts/gen-cli-docs.sh derives this repository's CLI
	// reference from every binary's real `-h` output, so a binary that treats
	// "-h" as data records its own error message as documentation. That has
	// already happened once here, to a different command.
	if len(os.Args) > 1 && (os.Args[1] == "-h" || os.Args[1] == "--help" || os.Args[1] == "help") {
		fmt.Print(usage)
		return
	}
	if len(os.Args) != 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	version := os.Args[1]

	anchor, source, err := consolebundle.TrustAnchor()
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify-console-signature: %v\n", err)
		os.Exit(1)
	}
	pub, err := hex.DecodeString(anchor)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		fmt.Fprintf(os.Stderr, "verify-console-signature: trust anchor from %s is not a valid Ed25519 public key\n", source)
		os.Exit(1)
	}

	body, err := ui.CanonicalManifestBytes(version)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify-console-signature: building the manifest for %s: %v\n", version, err)
		os.Exit(1)
	}
	raw, err := os.ReadFile("internal/ui/console.manifest.sig")
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify-console-signature: reading the committed signature: %v\n", err)
		os.Exit(1)
	}
	sig, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(sig) != ed25519.SignatureSize {
		fmt.Fprintln(os.Stderr, "verify-console-signature: the committed signature is not hex of the expected size")
		os.Exit(1)
	}

	if !ed25519.Verify(pub, body, sig) {
		fmt.Fprintf(os.Stderr,
			"verify-console-signature: the committed signature does NOT verify for version %q against the key from %s.\n"+
				"Every thin client would refuse this console with \"manifest signature INVALID\".\n"+
				"Re-sign before tagging:\n"+
				"    CORRALAI_RELEASE_KEY=<the release seed> scripts/sign-console-bundle.sh %s\n"+
				"and commit internal/ui/console.manifest.sig.\n", version, source, version)
		os.Exit(1)
	}
	fmt.Printf("verify-console-signature: OK — the committed signature verifies for %s against the key from %s\n", version, source)
}
