// SPDX-License-Identifier: Elastic-2.0

// Command ledgersite renders a corral ledger directory as a static, public,
// read-only site: an index of every entry and a page per review and per audit.
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
//
// Rendering lives in internal/recordview, shared with `corral ui`. This
// program reads the ledger, prints what it found, and writes the static pages.
package main

import (
	"flag"
	"fmt"
	"os"
	"regexp"
	"sort"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/ledger"
	"github.com/pdbethke/corralai/internal/recordview"
)

// repoRE is the shape of a GitHub owner/name; the same as corral verify's
// --ledger-repo.
var repoRE = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)

func main() {
	ledgerDir := flag.String("ledger", "", "the ledger directory (a checkout of the ledger branch)")
	out := flag.String("out", "", "directory to write the static site into (index.html + entry/<hash>.html)")
	pubHex := flag.String("pubkey", "", "hex Ed25519 public key to verify signatures against; else $CORRALAI_LEDGER_PUBKEY; with neither, signatures are NOT checked and the page says so")
	repo := flag.String("repo", "", "REQUIRED: the GitHub repository (owner/name) whose Actions identity signed the ledger's keyless entries; they are checked against exactly this one")
	siteChrome := flag.Bool("site-chrome", false, "leave the slot at the top of every page that the corralai.dev build fills with the site's own header")
	flag.Parse()

	if *ledgerDir == "" {
		fmt.Fprintln(os.Stderr, "ledgersite: -ledger is required")
		os.Exit(2)
	}
	if !repoRE.MatchString(*repo) {
		fmt.Fprintf(os.Stderr, "ledgersite: -repo is required and must be owner/name, got %q\n", *repo)
		os.Exit(2)
	}
	if !auditpush.IsLedgerDir(*ledgerDir) {
		fmt.Fprintf(os.Stderr, "ledgersite: %s is not a ledger directory\n", *ledgerDir)
		os.Exit(2)
	}

	// A missing key is NOT a failed check: it means unchecked, and every
	// surface has to keep those apart rather than render unchecked as bad.
	// A key that is SET but malformed is a refusal, so a typo in the deploy
	// cannot quietly publish a page that checked nothing.
	pub, _, err := auditpush.ResolveLedgerPubKey(*pubHex)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ledgersite: %v\n", err)
		os.Exit(2)
	}

	entries, err := loadEntries(*ledgerDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ledgersite: reading entries: %v\n", err)
		os.Exit(1)
	}
	root, err := ledger.PublicGoodTrustedRoot()
	if err != nil {
		fmt.Fprintf(os.Stderr, "ledgersite: the embedded Sigstore root: %v\n", err)
		os.Exit(1)
	}
	trust := ledger.Trust{RootPub: pub, Repo: *repo, SigstoreRoot: root}
	checks, err := auditpush.VerifyLedgerDirTrust(*ledgerDir, trust)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ledgersite: verifying chain: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("entries read:      %d\n", len(entries))
	fmt.Printf("chain checks:      %d\n", len(checks))
	// Counted per entry from the check itself, as the page does: a keyless
	// entry is checked against -repo with no key at all.
	signed, checked, ok := 0, 0, 0
	for _, c := range checks {
		if c.Signed {
			signed++
		}
		if c.SigChecked {
			checked++
		}
		if c.SigOK {
			ok++
		}
	}
	fmt.Printf("signatures:        %d signed, %d checked, %d verified; %d NOT CHECKED (an ed25519 one needs -pubkey or CORRALAI_LEDGER_PUBKEY, a keyless one -repo)\n", signed, checked, ok, signed-checked)

	live := auditpush.LiveEntries(entries)
	retracted := auditpush.Retracted(entries)
	verdicts := auditpush.Adjudications(live) // only verdicts that stand, as the page counts them
	fmt.Printf("live entries:      %d\n", len(live))
	fmt.Printf("retracted:         %d\n", len(retracted))
	fmt.Printf("adjudications:     %d\n", len(verdicts))

	kinds := map[string]int{}
	reviews, findings, reproduced := 0, 0, 0
	for _, e := range entries {
		k := e.Kind
		if e.IsScan() {
			k = "scan"
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
	v, err := recordview.LoadTrust(*ledgerDir, trust)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ledgersite: loading record: %v\n", err)
		os.Exit(1)
	}
	v.SiteChrome = *siteChrome
	if err := recordview.WriteStatic(*out, v); err != nil {
		fmt.Fprintf(os.Stderr, "ledgersite: writing site: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("wrote:             %s (index.html + %d review and %d audit pages)\n", *out, len(v.Reviews), len(v.Scans))
}

// loadEntries is the ledger's own reader: same accepted names (.json and
// .json.gz, never a dot-temp file), same chain order (Pushed). A second
// walker here once disagreed with it on both.
func loadEntries(dir string) ([]auditpush.LedgerEntry, error) {
	return auditpush.ReadLedgerDir(dir)
}
