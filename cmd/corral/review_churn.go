// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/pdbethke/corralai/internal/review"
)

// runReviewChurn implements `corral review churn`: for every review round on
// the record, how many of its findings were already in the code when the
// previous review of their file looked (drain) and how many were written
// since (churn) — read from the ledger and git blame, so the ratio the review
// loop is judged by is a command rather than an afternoon of blaming by hand
// (review.Churn has the method and its one approximation). No model runs and
// nothing is written.
func runReviewChurn(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("corral review churn", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repoDir := fs.String("repo", ".", "the checkout whose history the findings' lines are blamed in")
	ledgerFlag := fs.String("ledger", "", "the ledger directory whose review entries are the rounds (default: <repo>/.corral/ledger, or $CORRAL_LEDGER)")
	asJSON := fs.Bool("json", false, "print every round and every finding's origin as JSON")
	findings := fs.Bool("findings", false, "list every finding under its round, with the commit that wrote its line")
	if err := fs.Parse(args); err != nil {
		return flagParseExit(err)
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "corral review churn: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	root, err := filepath.Abs(*repoDir)
	if err != nil {
		fmt.Fprintf(stderr, "corral review churn: %v\n", err)
		return 2
	}
	ledgerDir := strings.TrimRight(*ledgerFlag, "/")
	if ledgerDir == "" {
		ledgerDir = defaultLedgerDir(root)
	}
	rec, err := readLedgerRecord(ledgerDir)
	if err != nil {
		fmt.Fprintf(stderr, "corral review churn: reading %s: %v\n", ledgerDir, err)
		return 1
	}
	rounds := review.Churn(reviewsOf(rec), gitBlameLine(root), gitIsAncestor(root))
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if rounds == nil {
			rounds = []review.Round{}
		}
		if err := enc.Encode(rounds); err != nil {
			return 1
		}
		return 0
	}
	printChurn(stdout, rounds, ledgerDir, *findings)
	return 0
}

func printChurn(w io.Writer, rounds []review.Round, ledgerDir string, findings bool) {
	fmt.Fprintf(w, "review churn — %d round(s) in %s\n", len(rounds), ledgerDir)
	fmt.Fprint(w, "  drain: the line was there when the previous review of its file looked; churn: written since;\n  first look: no earlier review covered the file. Findings that fell are not counted.\n\n")
	tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "REVIEWED\tCOMMIT\tSCOPE\tSINCE\tFIRST LOOK\tDRAIN\tCHURN\tUNKNOWN\tFELL\t")
	var drain, churn, first, unknown int
	for _, r := range rounds {
		since := "—"
		if r.Since != "" {
			since = shortSHA(r.Since)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%d\t%d\t%d\t\n", r.When.UTC().Format("2006-01-02"), shortSHA(r.Commit), r.Scope, since, r.FirstLook, r.Drain, r.Churn, r.Unknown, r.Fell)
		if findings {
			for _, f := range r.Findings {
				where := f.File
				if f.Line > 0 {
					where += ":" + strconv.Itoa(f.Line)
				}
				note := f.Origin
				if f.Blamed != "" {
					note += " (line by " + shortSHA(f.Blamed) + ")"
				}
				if f.Reason != "" && f.Origin == review.OriginUnknown {
					note += " — " + f.Reason
				}
				fmt.Fprintf(tw, "\t\t  %s\t%s\t%s\t\t\t\t\t\n", f.ID, where, note)
			}
		}
		drain, churn, first, unknown = drain+r.Drain, churn+r.Churn, first+r.FirstLook, unknown+r.Unknown
	}
	tw.Flush()
	fmt.Fprintf(w, "\ntotal: %d drain, %d churn, %d first look, %d unknown\n", drain, churn, first, unknown)
	fmt.Fprintln(w, "  churn counts every line written since the previous review, not only the commits that fixed its findings: the record does not say which those were")
}

func shortSHA(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}

// gitBlameLine names the commit that last wrote file:line as of commit.
func gitBlameLine(root string) func(commit, file string, line int) (string, error) {
	return func(commit, file string, line int) (string, error) {
		n := strconv.Itoa(line)
		// #nosec G204 -- fixed argv; commit and file come from the ledger, line is an int, and git treats them as a revision and a path after --
		out, err := exec.Command("git", "-C", root, "blame", "--porcelain", "-L", n+","+n, commit, "--", file).Output()
		if err != nil {
			var ee *exec.ExitError
			if errors.As(err, &ee) && len(ee.Stderr) > 0 {
				return "", errors.New(strings.TrimSpace(string(ee.Stderr)))
			}
			return "", err
		}
		sha, _, _ := strings.Cut(string(out), " ")
		if len(sha) < 7 {
			return "", fmt.Errorf("no commit in blame output for %s:%d", file, line)
		}
		return sha, nil
	}
}

// gitIsAncestor reports whether a is an ancestor of b (or a is b).
func gitIsAncestor(root string) func(a, b string) bool {
	return func(a, b string) bool {
		if a == "" || b == "" {
			return false
		}
		// #nosec G204 -- fixed argv; both are commits the ledger or git blame named
		return exec.Command("git", "-C", root, "merge-base", "--is-ancestor", a, b).Run() == nil
	}
}
