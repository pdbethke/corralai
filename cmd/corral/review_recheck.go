// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/review"
)

const (
	recheckStill = "still-reproduces"
	recheckGone  = "no-longer-reproduces"
	recheckUnrun = "could-not-run"
)

// recheckResult is what a recheck measured. It is printed, never written to
// the ledger: a person who wants it on the record quotes it in a reason.
type recheckResult struct {
	Ref      string `json:"ref"`
	Commit   string `json:"commit"`
	Outcome  string `json:"outcome"`
	ExitCode *int   `json:"exit_code,omitempty"`
	Output   string `json:"output"`
	Reason   string `json:"reason,omitempty"`
}

// runReviewRecheck re-runs one finding's recorded reproduction script
// against the CURRENT HEAD of --repo, in a disposable worktree, the way
// `corral review` ran it at review time.
//
// "Is it fixed?" is answered by execution, never by a commit message that
// says so. Three outcomes and only three: the script still demonstrates the
// defect (exit 0), it no longer does (any other exit), or it could not run
// (a harness error, a timeout, or exit 126/127 — the shell's "not
// executable" and "not found", a missing toolchain rather than a result).
// could-not-run is never reported as no-longer-reproduces.
func runReviewRecheck(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("corral review recheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	repo := fs.String("repo", ".", "the checkout whose HEAD the script runs against, in a disposable worktree")
	timeout := fs.Duration("timeout", time.Minute, "wall-clock bound on the script")
	asJSON := fs.Bool("json", false, "print the result as one JSON object")
	if err := fs.Parse(flagsFirst(fs, args)); err != nil {
		return 2
	}
	usage := "corral review recheck: usage: corral review recheck <ledger dir> <review hash>#<Rn> [--repo <dir>] [--timeout 1m] [--json]"
	if fs.NArg() != 2 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	hash, id, ok := strings.Cut(fs.Arg(1), "#")
	if !ok || hash == "" || id == "" {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	dir := strings.TrimRight(fs.Arg(0), "/")
	rec, err := readLedgerRecord(dir)
	if err != nil {
		fmt.Fprintf(stderr, "corral review recheck: %v\n", err)
		return 1
	}
	e, err := auditpush.FindReview(rec.Live, hash)
	if err != nil {
		fmt.Fprintf(stderr, "corral review recheck: %v\n", err)
		return 1
	}
	var f *review.Finding
	for i := range e.Review.Findings {
		if e.Review.Findings[i].ID == id {
			f = &e.Review.Findings[i]
		}
	}
	if f == nil {
		fmt.Fprintf(stderr, "corral review recheck: review %.12s has no finding %s\n", e.Hash, id)
		return 1
	}
	if strings.TrimSpace(f.Script) == "" {
		fmt.Fprintf(stderr, "corral review recheck: %s has no script — judge it from the code at %s:%d\n", fs.Arg(1), f.File, f.Line)
		return 1
	}
	root, err := filepath.Abs(*repo)
	if err != nil {
		fmt.Fprintf(stderr, "corral review recheck: %v\n", err)
		return 2
	}
	res := recheckResult{Ref: fmt.Sprintf("%.12s#%s", e.Hash, id), Commit: gitHeadCommit(root)}
	if res.Commit == "" {
		fmt.Fprintf(stderr, "corral review recheck: %s is not a git checkout\n", root)
		return 2
	}
	if reason := recheckAncestry(root, e.Review.Commit); reason != "" {
		res.Outcome, res.Reason = recheckUnrun, reason
		return printRecheck(stdout, res, *asJSON)
	}
	// auditContext, not context.Background: `corral ui` runs this as a
	// child, and a Ctrl-C there reaches the whole process group. With the
	// default SIGINT handling this process died before the deferred cleanup
	// ran, leaving a stale worktree registered in the operator's repo. The
	// signal-aware context turns the first interrupt into a cancellation, so
	// the script is stopped, the result is could-not-run (a canceled run is
	// never a measurement), and the worktree is removed on the way out. It is
	// taken before the worktree exists and released after it is removed
	// (defers run in reverse), so cleanup itself runs under the handler.
	ctx, stop := auditContext(stderr)
	defer stop()
	rep, cleanup, werr := newWorktreeReproducer(root, res.Commit, *timeout)
	if werr != nil {
		res.Outcome, res.Reason = recheckUnrun, werr.Error()
		return printRecheck(stdout, res, *asJSON)
	}
	defer cleanup()
	out, code, rerr := rep.Run(ctx, f.Script)
	res.Output = tail(out, 4000)
	switch {
	case rerr != nil:
		res.Outcome, res.Reason = recheckUnrun, rerr.Error()
	case code < 0:
		// A negative code (WorkspaceRunner's convention, see
		// internal/adequacy/workspace.go around st.Exited()) means the
		// process never exited normally — killed by a signal, or never
		// finished — not that the defect is gone. Reporting it as
		// no-longer-reproduces would let a crash pass as a fix.
		res.Outcome, res.Reason = recheckUnrun, "the script did not exit normally: killed by a signal, or never finished — not a result"
	case code == 126 || code == 127:
		res.ExitCode = &code
		res.Outcome, res.Reason = recheckUnrun, fmt.Sprintf("the script exited %d: a command was not found or not executable — a missing toolchain, not a result", code)
	case code == 0:
		res.ExitCode, res.Outcome = &code, recheckStill
	default:
		res.ExitCode, res.Outcome = &code, recheckGone
	}
	return printRecheck(stdout, res, *asJSON)
}

// recheckAncestry returns why HEAD of root is not a place the finding can be
// rechecked, or "" when it is. A script run against a checkout that does not
// contain the reviewed commit ran against unrelated code: its non-zero exit
// would read as no-longer-reproduces while measuring nothing about the
// finding. So HEAD must descend from the reviewed commit, and a review that
// names no commit cannot be rechecked at all.
func recheckAncestry(root, reviewed string) string {
	reviewed = strings.TrimSpace(reviewed)
	if reviewed == "" {
		return "the review records no reviewed commit, so there is no code this HEAD can be compared against"
	}
	if strings.HasPrefix(reviewed, "-") {
		return fmt.Sprintf("the review's recorded commit %q is not a commit", reviewed)
	}
	// #nosec G204 -- fixed argv; root is the operator's own --repo path and reviewed is a commit hash read from the operator's own signed ledger, passed after the subcommand as a revision, never as an option
	if err := exec.Command("git", "-C", root, "merge-base", "--is-ancestor", reviewed, "HEAD").Run(); err != nil {
		return fmt.Sprintf("HEAD of %s does not descend from the reviewed commit %.12s (or that commit is absent from this checkout) — the script would run against unrelated code, not a result", root, reviewed)
	}
	return ""
}

func printRecheck(w io.Writer, res recheckResult, asJSON bool) int {
	if asJSON {
		_ = json.NewEncoder(w).Encode(res)
	} else {
		fmt.Fprintf(w, "%s at %.12s: %s", res.Ref, res.Commit, res.Outcome)
		if res.ExitCode != nil {
			fmt.Fprintf(w, " (exit %d)", *res.ExitCode)
		}
		if res.Reason != "" {
			fmt.Fprintf(w, " — %s", res.Reason)
		}
		fmt.Fprintln(w)
		if res.Output != "" {
			fmt.Fprintln(w, indent(res.Output))
		}
	}
	if res.Outcome == recheckUnrun {
		return 3
	}
	return 0
}
