// SPDX-License-Identifier: Elastic-2.0

// Command ledgersite renders a corral ledger directory as a static, public,
// read-only site: an index of every entry and a page per review.
//
// It lives in scripts/ and not cmd/ ON PURPOSE. scripts/gen-cli-docs.sh derives
// its binary list from `cmd/*/` and publishes a CLI reference page for each, so
// an internal build tool placed there would ship a user-facing doc page for
// something no user runs.
//
// Every rule about the record comes from internal/auditpush: the chain and
// signatures are checked by VerifyLedgerDir, retractions and verdicts by
// Retracted and Adjudications. This program adds no second opinion about what
// the record means — it only renders what those say, which is the same reason
// corral ui drives the CLI instead of reimplementing it.
package main

import (
	"crypto/ed25519"
	"encoding/hex"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/pdbethke/corralai/internal/auditpush"
)

func main() {
	ledger := flag.String("ledger", "", "the ledger directory (a checkout of the ledger branch)")
	out := flag.String("out", "", "directory to write the static site into (index.html + entry/<hash>.html)")
	pubHex := flag.String("pubkey", "", "hex Ed25519 public key to verify signatures against; omitted means signatures are NOT checked and the page says so")
	flag.Parse()

	if *ledger == "" {
		fmt.Fprintln(os.Stderr, "ledgersite: -ledger is required")
		os.Exit(2)
	}
	if !auditpush.IsLedgerDir(*ledger) {
		fmt.Fprintf(os.Stderr, "ledgersite: %s is not a ledger directory\n", *ledger)
		os.Exit(2)
	}

	// A missing key is NOT a failed check: it means unchecked, and every
	// surface has to keep those apart rather than render unchecked as bad.
	var pub ed25519.PublicKey
	if *pubHex != "" {
		b, err := hex.DecodeString(*pubHex)
		if err != nil || len(b) != ed25519.PublicKeySize {
			fmt.Fprintf(os.Stderr, "ledgersite: -pubkey is not a hex Ed25519 public key\n")
			os.Exit(2)
		}
		pub = ed25519.PublicKey(b)
	}

	entries, err := loadEntries(*ledger)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ledgersite: reading entries: %v\n", err)
		os.Exit(1)
	}
	checks, err := auditpush.VerifyLedgerDir(*ledger, pub)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ledgersite: verifying chain: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("entries read:      %d\n", len(entries))
	fmt.Printf("chain checks:      %d\n", len(checks))
	fmt.Printf("signatures:        %s\n", map[bool]string{true: "checked against -pubkey", false: "NOT CHECKED (no -pubkey given)"}[pub != nil])

	live := auditpush.LiveEntries(entries)
	retracted := auditpush.Retracted(entries)
	verdicts := auditpush.Adjudications(entries)
	fmt.Printf("live entries:      %d\n", len(live))
	fmt.Printf("retracted:         %d\n", len(retracted))
	fmt.Printf("adjudications:     %d\n", len(verdicts))

	kinds := map[string]int{}
	reviews, findings, reproduced := 0, 0, 0
	for _, e := range entries {
		k := e.Kind
		if k == auditpush.KindScan {
			k = "scan(legacy)"
		}
		kinds[k]++
		if e.Review != nil {
			reviews++
			findings += len(e.Review.Findings)
			for _, f := range e.Review.Findings {
				if f.ExitCode != nil && *f.ExitCode == 0 && f.Unrun == "" {
					reproduced++
				}
			}
		}
	}
	var ks []string
	for k := range kinds {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	fmt.Println("kinds:")
	for _, k := range ks {
		fmt.Printf("  %-16s %d\n", k, kinds[k])
	}
	fmt.Printf("reviews:           %d\n", reviews)
	fmt.Printf("findings:          %d\n", findings)
	fmt.Printf("reproduced (exit0):%d\n", reproduced)

	bad := 0
	for _, c := range checks {
		if c.Problem != "" || !c.HashOK || !c.LinkOK {
			bad++
			fmt.Printf("  PROBLEM %s: hashOK=%v linkOK=%v %s\n", c.File, c.HashOK, c.LinkOK, c.Problem)
		}
	}
	fmt.Printf("chain problems:    %d\n", bad)

	if *out == "" {
		return
	}
	v := buildView(entries, checks, pub != nil)
	if pub != nil {
		v.PubKeyHex = hex.EncodeToString(pub)
	}
	if err := writeSite(*out, v); err != nil {
		fmt.Fprintf(os.Stderr, "ledgersite: writing site: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("wrote:             %s (index.html + %d entry pages)\n", *out, len(v.Reviews))
}

// loadEntries is the ledger's own reader: same accepted names (.json and
// .json.gz, never a dot-temp file), same chain order (Pushed). A second
// walker here once disagreed with it on both.
func loadEntries(dir string) ([]auditpush.LedgerEntry, error) {
	return auditpush.ReadLedgerDir(dir)
}
