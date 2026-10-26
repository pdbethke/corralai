// SPDX-License-Identifier: Elastic-2.0

// Command ledgerderive prints ledger.Derive's JSON for a ledger directory:
// the native twin of the browser's corralLedgerDerive. scripts/check-ledger-wasm.sh
// runs both on the same files and requires the two documents to be byte
// identical, which is what lets the results page claim the CLI's rules.
//
//	go run ./scripts/ledgerderive -pub <hex> -repo <owner/name> <ledger-dir>
//
// It lives in scripts/ and not cmd/ ON PURPOSE: scripts/gen-cli-docs.sh
// derives its binary list from `cmd/*/` and publishes a CLI reference page
// for each, so a check tool placed there would ship a doc page for something
// no user runs.
//
// Both flags are required. A ledger belongs to one repository, and its
// keyless entries are checked against exactly that one, so a guessed -repo
// would check the wrong identity; and with no -pub nothing signed with the
// root key passes, which is a different document, not a lenient one.
//
// The trust is built as the WASM builds it (internal/ledger/wasm): the key,
// the repository, and the Sigstore root embedded in this build. Output is
// json.Marshal(Derived) with no trailing newline, the exact string the WASM
// returns.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/ledger"
)

// repoRE is the shape of a GitHub owner/name; the same as ledgersite's -repo.
var repoRE = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// run returns 2 for a usage error, 1 when the ledger cannot be read or
// derived, 0 otherwise. A ledger with a Derived.Problem still prints and
// exits 0: the document says what is wrong, and the check compares documents.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("ledgerderive", flag.ContinueOnError)
	fs.SetOutput(stderr)
	pubHex := fs.String("pub", "", "REQUIRED: hex Ed25519 root public key to verify signed entries against (the ledger's LEDGER_PUBKEY)")
	repo := fs.String("repo", "", "REQUIRED: the GitHub repository (owner/name) whose Actions identity signed the ledger's keyless entries")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	usage := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "ledgerderive: "+format+"\n", a...)
		fmt.Fprintln(stderr, "usage: ledgerderive -pub <hex> -repo <owner/name> <ledger-dir>")
		return 2
	}
	if *pubHex == "" {
		return usage("-pub is required: the ledger's root public key in hex (its LEDGER_PUBKEY)")
	}
	if !repoRE.MatchString(*repo) {
		return usage("-repo is required and must be owner/name, got %q", *repo)
	}
	if fs.NArg() != 1 {
		return usage("want exactly one ledger directory, got %d arguments", fs.NArg())
	}
	dir := fs.Arg(0)
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return usage("%s is not a ledger directory", dir)
	}
	// -pub is non-empty here, so the resolver reads the flag and never the
	// environment.
	pub, _, err := auditpush.ResolveLedgerPubKey(*pubHex)
	if err != nil {
		return usage("-pub: %v", err)
	}

	root, err := ledger.PublicGoodTrustedRoot()
	if err != nil {
		fmt.Fprintf(stderr, "ledgerderive: the embedded Sigstore trust root does not parse: %v\n", err)
		return 1
	}
	raw, err := auditpush.ReadRawLedgerDir(dir)
	if err != nil {
		fmt.Fprintf(stderr, "ledgerderive: reading %s: %v\n", dir, err)
		return 1
	}
	d, err := ledger.Derive(raw, ledger.Trust{RootPub: pub, Repo: *repo, SigstoreRoot: root})
	if err != nil {
		fmt.Fprintf(stderr, "ledgerderive: %v\n", err)
		return 1
	}
	out, err := json.Marshal(d)
	if err != nil {
		fmt.Fprintf(stderr, "ledgerderive: %v\n", err)
		return 1
	}
	if _, err := stdout.Write(out); err != nil {
		fmt.Fprintf(stderr, "ledgerderive: %v\n", err)
		return 1
	}
	return 0
}
