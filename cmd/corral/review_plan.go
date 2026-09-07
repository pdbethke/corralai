// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/review"
)

// runReviewPlan implements `corral review plan`: the scopes of the
// repository, which the ledger says were reviewed and when, which changed
// since, and the planner's proposal for the next round. It runs no model
// and writes nothing; the person names the scope.
func runReviewPlan(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("corral review plan", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repoDir := fs.String("repo", ".", "the checkout")
	ledgerFlag := fs.String("ledger", "", "the ledger directory whose review entries are the record (default: <repo>/.corral/ledger, or $CORRAL_LEDGER)")
	depth := fs.Int("depth", 2, "how many path segments make a scope (2: internal/review, cmd/corral)")
	limit := fs.Int("limit", 25, "how many scopes to list")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	root, err := filepath.Abs(*repoDir)
	if err != nil {
		fmt.Fprintf(stderr, "corral review plan: %v\n", err)
		return 2
	}
	ledgerDir := strings.TrimRight(*ledgerFlag, "/")
	if ledgerDir == "" {
		ledgerDir = defaultLedgerDir(root)
	}
	scopes, err := repoScopes(root, *depth)
	if err != nil {
		fmt.Fprintf(stderr, "corral review plan: %v\n", err)
		return 1
	}
	rec, err := readLedgerRecord(ledgerDir)
	if err != nil {
		fmt.Fprintf(stderr, "corral review plan: reading %s: %v\n", ledgerDir, err)
		return 1
	}
	// A withdrawn review is not coverage: the planner sees what stands.
	entries := rec.Live
	adj := rec.liveAdjudications()
	var reviews []review.Reviewed
	for _, e := range entries {
		if e.Kind != auditpush.KindReview || e.Review == nil {
			continue
		}
		rv := review.Reviewed{Scope: e.Review.Scope, Commit: e.Review.Commit, When: e.Pushed, Findings: e.Review.Findings, Adjudications: map[string]*review.Adjudicated{}}
		for _, f := range e.Review.Findings {
			if a, ok := adj[e.Hash+"#"+f.ID]; ok {
				rv.Adjudications[f.ID] = &review.Adjudicated{Verdict: a.Verdict, By: a.By}
			}
		}
		reviews = append(reviews, rv)
	}
	head := gitHeadCommit(root)
	changed := func(scope, since string) int {
		if head == "" {
			return 0
		}
		// #nosec G204 -- fixed argv; the arguments are a commit the ledger recorded and a path under the checkout
		out, err := exec.Command("git", "-C", root, "diff", "--name-only", since, head, "--", scope).Output()
		if err != nil {
			return 0
		}
		return len(strings.Fields(string(out)))
	}
	plan := review.Plan(scopes, reviews, changed)

	fmt.Fprintf(stdout, "review plan — %s @ %.12s, %d scope(s), %d review(s) in %s\n", resolveRepoName(root, ""), head, len(scopes), len(reviews), ledgerDir)
	fmt.Fprint(stdout, "  never reviewed first (largest scope first), then changed since review (a fix batch nobody re-attacked), then reviewed and unchanged, stalest first\n\n")
	tw := tabwriter.NewWriter(stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "SCOPE\tFILES\tREVIEWED\tHELD\tFELL\tOPEN\tCHANGED SINCE\tWHY\t")
	shown := 0
	for _, s := range plan {
		if shown >= *limit {
			break
		}
		shown++
		last := "—"
		if s.Reviews > 0 {
			last = fmt.Sprintf("%s @ %.7s (%dx)", s.LastReviewed.UTC().Format("2006-01-02"), s.LastCommit, s.Reviews)
		}
		fmt.Fprintf(tw, "%s\t%d\t%s\t%d\t%d\t%d\t%d\t%s\t\n", s.Scope, s.Files, last, s.Held, s.Fell, s.Open, s.ChangedSince, s.Reason)
	}
	tw.Flush()
	if len(plan) > 0 {
		n := plan[0]
		fmt.Fprintf(stdout, "\nproposed next: corral review --scope %s --reviewer-model <m> --verifier-model <m2>  (%s)\n", n.Scope, n.Reason)
		fmt.Fprintln(stdout, "  a proposal, not a choice: a person names the scope — a model choosing its own exam would be grading its own work")
	}
	return 0
}

// repoScopes is the directories at most depth segments deep that hold at
// least one source file a language plugin recognises, with their file
// counts. The usual skip list applies.
func repoScopes(root string, depth int) (map[string]int, error) {
	skip := map[string]bool{".git": true, "node_modules": true, "vendor": true, ".corral": true, "testdata": true, "dist": true, "build": true, ".venv": true, "__pycache__": true, "site": true}
	scopes := map[string]int{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != root && (skip[d.Name()] || strings.HasPrefix(d.Name(), ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if _, ok := lang.Detect(rel); !ok {
			return nil
		}
		parts := strings.Split(rel, "/")
		if len(parts) < 2 {
			return nil // a file at the root has no directory scope
		}
		n := depth
		if len(parts)-1 < n {
			n = len(parts) - 1
		}
		scopes[strings.Join(parts[:n], "/")]++
		return nil
	})
	return scopes, err
}
