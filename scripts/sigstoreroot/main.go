// SPDX-License-Identifier: Elastic-2.0

// Command sigstoreroot fetches Sigstore's public-good trusted_root.json
// through its TUF repository and writes it to the path given, after checking
// that both sigstore-go and internal/ledger can read it.
//
// It is run by scripts/update-sigstore-root.sh, by a person, and the result
// is committed and reviewed like any other change to what corral trusts.
// Nothing fetches the root at run time: internal/ledger embeds the reviewed
// file. It lives in scripts/ and not cmd/ for the reason
// scripts/ledgersite/main.go gives: cmd/ ships a CLI reference page per binary.
package main

import (
	"fmt"
	"os"

	"github.com/sigstore/sigstore-go/pkg/root"
	"github.com/sigstore/sigstore-go/pkg/tuf"

	"github.com/pdbethke/corralai/internal/ledger"
)

func main() {
	if len(os.Args) != 2 || os.Args[1] == "" {
		fmt.Fprintln(os.Stderr, "usage: sigstoreroot <output path>")
		os.Exit(2)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "sigstoreroot:", err)
		os.Exit(1)
	}
}

func run(out string) error {
	// DefaultOptions is the public-good TUF repository, anchored on the TUF
	// root embedded in sigstore-go: the fetch is verified, not trusted.
	c, err := tuf.New(tuf.DefaultOptions())
	if err != nil {
		return fmt.Errorf("TUF client: %w", err)
	}
	js, err := c.GetTarget("trusted_root.json")
	if err != nil {
		return fmt.Errorf("fetching trusted_root.json: %w", err)
	}
	if _, err := root.NewTrustedRootFromJSON(js); err != nil {
		return fmt.Errorf("sigstore-go cannot read it: %w", err)
	}
	if _, err := ledger.ParseSigstoreRoot(js); err != nil {
		return fmt.Errorf("internal/ledger cannot read it: %w", err)
	}
	if err := os.WriteFile(out, js, 0o600); err != nil { // #nosec G703 -- out is the operator's own argument to a maintainer script (update-sigstore-root.sh passes a mktemp path), not remote input
		return err
	}
	fmt.Printf("wrote %s (%d bytes)\n", out, len(js))
	return nil
}
