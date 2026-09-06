// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
	"github.com/pdbethke/corralai/internal/agentbackend"
	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/review"
)

// runReview implements `corral review`: a cold reviewer seat over a scope,
// its reproductions run, the record written beside the audits — and the
// two verbs that read and adjudicate it. See internal/review and
// docs/design/adversarial-review.md; this is the one-week slice: no
// verifier seat, the human adjudicating in the ledger.
func runReview(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || wantsHelp(args[:1]) {
		fmt.Fprint(stdout, reviewUsage)
		// The run verb's own flags, so the CLI reference (generated from
		// -h) and the surface manifest see every one of them.
		fs, _ := reviewFlagSet(stdout)
		fmt.Fprintln(stdout, "\nflags of `corral review`:")
		fs.PrintDefaults()
		return 0
	}
	switch args[0] {
	case "adjudicate":
		return runReviewAdjudicate(args[1:], stdout, stderr)
	case "show":
		return runReviewShow(args[1:], stdout, stderr)
	}
	return runReviewRun(args, stdout, stderr)
}

const reviewUsage = `corral review — a cold model reviews a scope of the repository; corral runs its reproductions and records the review beside the audits.

  corral review --scope <dir|file> --reviewer-model <m> [--repo <dir>] [flags]
      The reviewer is told to assume the code is wrong. Every finding carries a tier it
      declared — REPRODUCED (with a sh script that exits 0 iff the defect is demonstrated),
      CODE-READ (file:line, argued), HYPOTHESIS — and the run executes every REPRODUCED
      script against a detached worktree at HEAD. A script that does not hold demotes its
      finding to CODE-READ on the record, out loud. The reviewer must also list what it
      checked and found sound. The opinion is printed and carried; only the reproductions
      are what the entry's signature vouches for. Exit 0 either way: a review is not a gate.
      flags: --ledger <dir> (default <repo>/.corral/ledger)  --no-ledger  --timeout 60s
             --max-bytes 200000 (how much of the scope the reviewer is shown)
  corral review show <ledger dir> <review hash>       print a review with its adjudications applied
  corral review adjudicate <ledger dir> <hash>#<Rn> --confirm|--refute --reason "…" [--by <who>]
      A person's verdict on one finding, as its own entry: the newest verdict per finding
      stands; automatic passes never write one. --by defaults to the OS user.
`

// reviewFlags is the run verb's flag set, bound in one place so -h and the
// run agree about every flag.
type reviewFlags struct {
	repoDir, scope, model, ledger string
	noLedger                      bool
	timeout                       time.Duration
	maxBytes                      int
}

func reviewFlagSet(out io.Writer) (*flag.FlagSet, *reviewFlags) {
	fs := flag.NewFlagSet("corral review", flag.ContinueOnError)
	fs.SetOutput(out)
	f := &reviewFlags{}
	fs.StringVar(&f.repoDir, "repo", ".", "the checkout to review (a git repository at a commit)")
	fs.StringVar(&f.scope, "scope", "", "the directory or file under --repo to review (required)")
	fs.StringVar(&f.model, "reviewer-model", "", "the reviewer seat's model — an alias from the registry or a provider model name (required; corral has no default models)")
	fs.StringVar(&f.ledger, "ledger", "", "the ledger directory the review entry is written to (default: <repo>/.corral/ledger, or $CORRAL_LEDGER)")
	fs.BoolVar(&f.noLedger, "no-ledger", false, "print the review and write no entry")
	fs.DurationVar(&f.timeout, "timeout", time.Minute, "wall-clock bound on each reproduction script")
	fs.IntVar(&f.maxBytes, "max-bytes", 200000, "how many bytes of the scope the reviewer is shown; files past the cap are listed by name and the review records them as unshown")
	return fs, f
}

func runReviewRun(args []string, stdout, stderr io.Writer) int {
	fs, f := reviewFlagSet(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	repoDir, scope, model, ledgerFlag := &f.repoDir, &f.scope, &f.model, &f.ledger
	noLedger, timeout, maxBytes := &f.noLedger, &f.timeout, &f.maxBytes
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "corral review: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if strings.TrimSpace(*scope) == "" || strings.TrimSpace(*model) == "" {
		fmt.Fprintln(stderr, "corral review: --scope and --reviewer-model are required")
		return 2
	}
	root, err := filepath.Abs(*repoDir)
	if err != nil {
		fmt.Fprintf(stderr, "corral review: %v\n", err)
		return 2
	}
	commit := gitHeadCommit(root)
	if commit == "" {
		fmt.Fprintln(stderr, "corral review: --repo is not a git checkout at a commit — a review names the revision it reviewed")
		return 2
	}
	if _, err := resolveSeatRegistry("corral review", root, []seatFlag{{flag: "reviewer-model", val: model}}, stderr); err != nil {
		fmt.Fprintf(stderr, "corral review: %v\n", err)
		return 2
	}
	backend, err := newReviewerBackend(*model, "")
	if err != nil {
		fmt.Fprintf(stderr, "corral review: reviewer seat: %v\n", err)
		return 2
	}

	sc, err := review.LoadScope(root, *scope, *maxBytes)
	if err != nil {
		fmt.Fprintf(stderr, "corral review: %v\n", err)
		return 2
	}
	repoName := resolveRepoName(root, "")
	fmt.Fprintf(stdout, "review — %s @ %.12s, scope %s: %d file(s), %d bytes shown", repoName, commit, *scope, len(sc.Files), sc.Bytes)
	if sc.Truncated {
		fmt.Fprintf(stdout, ", %d NOT shown (--max-bytes)", len(sc.Unshown))
	}
	fmt.Fprintf(stdout, "\n  reviewer: %s (cold — it has never seen this repository)\n", *model)

	r := review.Review{Repo: repoName, Commit: commit, Scope: *scope, ReviewerModel: *model,
		Substrate: "workspace (a detached worktree at the commit; not a jail)", StartedAt: time.Now().UTC(),
		FilesShown: sc.Files, BytesShown: sc.Bytes, Truncated: sc.Truncated}
	reply, err := backend.Chat([]agentbackend.Message{
		{Role: "system", Content: review.BriefSystem},
		{Role: "user", Content: review.Brief(repoName, commit, *scope, sc)},
	}, nil)
	if err != nil {
		fmt.Fprintf(stderr, "corral review: the reviewer seat failed: %v\n", err)
		return 1
	}
	r.InputTokens, r.OutputTokens = int64(reply.Usage.InputTokens), int64(reply.Usage.OutputTokens)
	opinion, findings, sound, perr := review.Parse(reply.Content)
	if perr != nil {
		fmt.Fprintf(stderr, "corral review: %v\n--- the reply, verbatim ---\n%s\n", perr, reply.Content)
		return 1
	}
	r.Opinion, r.Findings, r.Sound = opinion, findings, sound

	// The reproductions: every REPRODUCED script, run against a detached
	// worktree at the commit so the operator's checkout is never touched.
	rep, cleanup, rerr := newWorktreeReproducer(root, commit, *timeout)
	if rerr != nil {
		fmt.Fprintf(stderr, "corral review: %v\n", rerr)
		return 1
	}
	defer cleanup()
	review.Reproduce(context.Background(), rep, &r)

	printReview(stdout, r, nil)

	if *noLedger {
		return 0
	}
	ledgerDir := strings.TrimRight(*ledgerFlag, "/")
	if ledgerDir == "" {
		ledgerDir = defaultLedgerDir(root)
	}
	signer, signed := ledgerSignerFromLocalKey()
	name, werr := auditpush.WriteReview(ledgerDir, r, signer)
	if werr != nil {
		fmt.Fprintf(stderr, "corral review: writing the entry to %s: %v\n", ledgerDir, werr)
		return 0
	}
	entries, _ := auditpush.ReadLedgerDir(ledgerDir)
	hash := ""
	if n := len(entries); n > 0 {
		hash = entries[n-1].Hash
	}
	fmt.Fprintf(stdout, "\nledger: review entry %s written to %s (%s)\n  adjudicate a finding: corral review adjudicate %s %.12s#R1 --confirm|--refute --reason \"…\"\n", name, ledgerDir, signed, ledgerDir, hash)
	return 0
}

// newReviewerBackend is the seat's constructor; a test substitutes a fake.
var newReviewerBackend = seatBackend

// seatBackend resolves one seat's model to a backend the way the goal
// deriver does: a local daemon when the registry placed it on one, the
// pinned MODEL_BACKEND when it serves this vendor, else the model's own
// provider.
func seatBackend(model, endpoint string) (agentbackend.Backend, error) {
	if endpoint != "" {
		return agentbackend.NewOllamaBackend(endpoint, model), nil
	}
	if v := agentbackend.VendorOf(model); v != "" && backendPinned() && (baseVendor() == "" || baseVendor() == v) {
		base := agentbackend.FromEnv()
		if sw, ok := base.(agentbackend.ModelSwitcher); ok {
			return sw.WithModel(model), nil
		}
		return base, nil
	}
	return agentbackend.ForModelOrLocal(model)
}

// worktreeReproducer runs a script in a detached git worktree at the
// commit. The operator's checkout is never the subject: a model-written
// script runs in a copy that is removed afterwards.
type worktreeReproducer struct {
	runner *adequacy.WorkspaceRunner
}

func newWorktreeReproducer(root, commit string, timeout time.Duration) (review.Reproducer, func(), error) {
	dir, err := os.MkdirTemp("", "corral-review-")
	if err != nil {
		return nil, nil, err
	}
	tree := filepath.Join(dir, "tree")
	// #nosec G204 -- fixed argv; root and commit are the operator's own checkout and its HEAD
	if out, err := exec.Command("git", "-C", root, "worktree", "add", "--detach", tree, commit).CombinedOutput(); err != nil {
		os.RemoveAll(dir)
		return nil, nil, fmt.Errorf("git worktree at %.12s: %v: %s", commit, err, strings.TrimSpace(string(out)))
	}
	cleanup := func() {
		_ = exec.Command("git", "-C", root, "worktree", "remove", "--force", tree).Run() // #nosec G204 -- fixed argv
		os.RemoveAll(dir)
	}
	return worktreeReproducer{runner: adequacy.NewWorkspaceRunner(tree, timeout, adequacy.WithWorkspaceMaxOutput(64<<10))}, cleanup, nil
}

func (w worktreeReproducer) Run(ctx context.Context, script string) (string, int, error) {
	res, err := w.runner.EnumerateDetailed(ctx, nil, []string{"sh", "-c", script})
	out := res.Output
	if res.Stderr != "" {
		out += "\n[stderr]\n" + res.Stderr
	}
	return out, res.ExitCode, err
}

// printReview renders a review, with adjudications (by finding ref) when
// the caller has them.
func printReview(w io.Writer, r review.Review, adj map[string]auditpush.Adjudication) {
	fmt.Fprintf(w, "\n%s\n", r.Opinion)
	rep, cr, hy := r.Counts()
	fmt.Fprintf(w, "\nfindings: %d reproduced, %d code-read, %d hypothesis\n", rep, cr, hy)
	if len(r.Findings) > 0 {
		tw := tabwriter.NewWriter(w, 0, 2, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tTIER\tSEVERITY\tWHERE\tCLAIM\tADJUDICATED\t")
		for _, f := range r.Findings {
			tier := f.Tier
			if f.Tier != f.Declared {
				tier = fmt.Sprintf("%s (declared %s)", f.Tier, f.Declared)
			}
			where := f.File
			if f.Line > 0 {
				where = fmt.Sprintf("%s:%d", f.File, f.Line)
			}
			verdict := ""
			if a, ok := adj[f.ID]; ok {
				verdict = a.Verdict + " by " + a.By
			}
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t\n", f.ID, tier, f.Severity, where, f.Claim, verdict)
		}
		tw.Flush()
		for _, f := range r.Findings {
			if f.Script == "" {
				continue
			}
			fmt.Fprintf(w, "\n%s — script:\n%s\n", f.ID, indent(f.Script))
			if f.ExitCode != nil {
				fmt.Fprintf(w, "%s — exit %d, output:\n%s\n", f.ID, *f.ExitCode, indent(strings.TrimSpace(f.Stdout)))
			}
			if f.Demoted != "" {
				fmt.Fprintf(w, "%s — DEMOTED to %s: %s\n", f.ID, f.Tier, f.Demoted)
			}
			if a, ok := adj[f.ID]; ok {
				fmt.Fprintf(w, "%s — %s by %s: %s\n", f.ID, a.Verdict, a.By, a.Reason)
			}
		}
	}
	fmt.Fprintln(w, "\nchecked and found sound:")
	if len(r.Sound) == 0 {
		fmt.Fprintln(w, "  (the reviewer listed nothing — the review's coverage is unknown)")
	}
	for _, s := range r.Sound {
		fmt.Fprintf(w, "  · %s\n", s)
	}
	if r.Truncated {
		fmt.Fprintln(w, "\nsome of the scope was NOT shown to the reviewer (--max-bytes) — nothing above is a claim about those files")
	}
}

func indent(s string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = "    " + lines[i]
	}
	return strings.Join(lines, "\n")
}

func runReviewShow(args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 {
		fmt.Fprintln(stderr, "corral review show: usage: corral review show <ledger dir> <review hash>")
		return 2
	}
	dir := strings.TrimRight(args[0], "/")
	entries, err := auditpush.ReadLedgerDir(dir)
	if err != nil {
		fmt.Fprintf(stderr, "corral review show: %v\n", err)
		return 1
	}
	e, err := auditpush.FindReview(entries, args[1])
	if err != nil {
		fmt.Fprintf(stderr, "corral review show: %v\n", err)
		return 1
	}
	adj := map[string]auditpush.Adjudication{}
	for ref, a := range auditpush.Adjudications(entries) {
		if h, id, ok := strings.Cut(ref, "#"); ok && h == e.Hash {
			adj[id] = a
		}
	}
	r := *e.Review
	fmt.Fprintf(stdout, "review %.12s — %s @ %.12s, scope %s, by %s, %s\n", e.Hash, r.Repo, r.Commit, r.Scope, r.ReviewerModel, e.Pushed.UTC().Format("2006-01-02 15:04"))
	printReview(stdout, r, adj)
	return 0
}

func runReviewAdjudicate(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("corral review adjudicate", flag.ContinueOnError)
	fs.SetOutput(stderr)
	confirm := fs.Bool("confirm", false, "the finding is real as stated")
	refute := fs.Bool("refute", false, "the finding is not real, or not as stated")
	reason := fs.String("reason", "", "why, in your words (required)")
	by := fs.String("by", "", "who is deciding (default: the OS user)")
	if err := fs.Parse(flagsFirst(fs, args)); err != nil {
		return 2
	}
	if fs.NArg() != 2 || *confirm == *refute || strings.TrimSpace(*reason) == "" {
		fmt.Fprintln(stderr, "corral review adjudicate: usage: corral review adjudicate <ledger dir> <review hash>#<Rn> --confirm|--refute --reason \"…\" [--by <who>]")
		return 2
	}
	verdict := auditpush.VerdictConfirmed
	if *refute {
		verdict = auditpush.VerdictRefuted
	}
	who := strings.TrimSpace(*by)
	if who == "" {
		if u, err := user.Current(); err == nil {
			who = u.Username
		}
	}
	dir := strings.TrimRight(fs.Arg(0), "/")
	signer, signed := ledgerSignerFromLocalKey()
	name, err := auditpush.WriteAdjudication(dir, fs.Arg(1), verdict, who, *reason, signer)
	if err != nil {
		fmt.Fprintf(stderr, "corral review adjudicate: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "%s: %s by %s — %s (%s, entry %s)\n", fs.Arg(1), verdict, who, *reason, signed, name)
	return 0
}
