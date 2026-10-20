// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/pdbethke/corralai/internal/auditpush"
)

// Runner is the one door every external command goes through. Tests script it
// with a fake, so no test touches git, gh or the network, and so the
// "nothing here acts on an upstream project" rule can be asserted on the argv
// that was actually issued rather than remembered.
type Runner interface {
	Run(ctx context.Context, dir string, argv ...string) (stdout string, err error)
}

// ledgerBranch is the branch both the fork's ledger worktree and corral's own
// ledger worktree track.
const ledgerBranch = "corral/ledger"

// CertifyArgv is the exact `corral certify --repo` command line for one
// target. Every seat and cap comes from the Target; nothing is defaulted here
// either. With no goals file the derive seat is named instead, because corral
// refuses a --repo run with neither.
func CertifyArgv(t Target, repoDir, ledgerDir, goalsPath string) []string {
	argv := []string{
		"corral", "certify",
		"--repo", repoDir,
		"--path", t.Path,
		"--substrate", t.Substrate,
		"--mutant-model", t.Seats.Mutant,
		"--writer-model", t.Seats.Writer,
		"--shadow-writer-model", t.Seats.ShadowWriter,
		"--critic-model", t.Seats.Critic,
		"--max-tokens", strconv.Itoa(t.MaxTokens),
		"--timeout", t.Timeout,
		"--ledger", ledgerDir,
	}
	if goalsPath != "" {
		argv = append(argv, "--goals", goalsPath)
	} else {
		argv = append(argv, "--derive-model", t.Seats.Derive)
	}
	argv = append(argv, "--")
	return append(argv, t.TestCommand...)
}

// checkAllowed is the campaign's whole authority, as a list. The campaign
// audits clones and writes to ledger branches of the founder's own fork and
// of corral; it never pushes to, files on or comments in an upstream project
// (AGENTS.md), and it never force-pushes anything. The real runner refuses
// any argv this rejects, and the test asserts it over every recorded command.
// The words after "--" in a certify line are the target's own test command and
// are not inspected.
func checkAllowed(argv []string) error {
	if len(argv) < 2 {
		return fmt.Errorf("campaign: refusing a command with no subcommand: %q", argv)
	}
	own := argv
	for i, a := range argv {
		if a == "--" {
			own = argv[:i]
			break
		}
	}
	for _, a := range own {
		if a == "--force" || a == "-f" || a == "--force-with-lease" || strings.HasPrefix(a, "--force-with-lease=") {
			return fmt.Errorf("campaign: refusing %q: forced operations are never allowed", argv)
		}
	}
	switch argv[0] + " " + argv[1] {
	case "corral certify", "git fetch", "git worktree", "git add", "git commit", "git push", "git reset":
		return nil
	case "corral ledger":
		if len(argv) > 2 && argv[2] == "append" {
			return nil
		}
	case "git -C":
		if len(argv) > 3 && argv[3] == "rev-parse" {
			return nil
		}
	case "gh repo":
		// The only thing gh may ever do: bring the founder's own fork level
		// with its upstream. Shape is checked by the caller's test against
		// the target's Fork; here it must be a sync.
		if len(argv) == 4 && argv[2] == "sync" {
			return nil
		}
	}
	return fmt.Errorf("campaign: %q is not an allowed command", argv)
}

// execRunner runs real commands. Stdout is captured for the caller and echoed
// to echo, as is stderr, so a long audit is watched as it happens.
type execRunner struct{ echo io.Writer }

func (e execRunner) Run(ctx context.Context, dir string, argv ...string) (string, error) {
	if err := checkAllowed(argv); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- argv passed checkAllowed
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = io.MultiWriter(&out, e.echo)
	cmd.Stderr = e.echo
	err := cmd.Run()
	return out.String(), err
}

// RunTarget audits the newest upstream commit of one target and records the
// result. Order, and why:
//
//  1. gh repo sync, git fetch: bring the fork and the clone level with upstream.
//  2. resolve upstream/<branch>: the commit to audit.
//  3. skip when the newest ledger entry already names this commit and path, so
//     a scheduled run costs nothing on a quiet repository.
//  4. audit in a detached worktree of the clone, so the founder's curation
//     checkout is never touched.
//  5. publish whatever the ledger gained, EVEN IF the audit failed: a failed
//     run is still a fact, and it is not retried (a retry would spend twice).
//  6. remove the audit worktree.
//
// campaignDir/<name>/ must already hold clone/, ledger/ (a worktree of the
// fork's corral/ledger) and campaignDir/_corral-ledger/; a missing one is
// refused before any spend.
func RunTarget(ctx context.Context, r Runner, t Target, campaignDir string, stdout, stderr io.Writer) error {
	root := filepath.Join(campaignDir, t.Name)
	clone := filepath.Join(root, "clone")
	audit := filepath.Join(root, "audit")
	ledger := filepath.Join(root, "ledger")
	corralLedger := filepath.Join(campaignDir, "_corral-ledger")
	for _, need := range []string{clone, ledger, corralLedger} {
		if st, err := os.Stat(need); err != nil || !st.IsDir() {
			return fmt.Errorf("campaign: %s is missing; set it up before running %q", need, t.Name)
		}
	}

	if _, err := r.Run(ctx, root, "gh", "repo", "sync", t.Fork); err != nil {
		return fmt.Errorf("campaign: syncing fork %s: %w", t.Fork, err)
	}
	if _, err := r.Run(ctx, clone, "git", "fetch", "upstream"); err != nil {
		return fmt.Errorf("campaign: fetching upstream: %w", err)
	}
	out, err := r.Run(ctx, root, "git", "-C", clone, "rev-parse", "upstream/"+t.Branch)
	if err != nil {
		return fmt.Errorf("campaign: resolving upstream/%s: %w", t.Branch, err)
	}
	sha := strings.TrimSpace(out)
	if sha == "" {
		return fmt.Errorf("campaign: upstream/%s resolved to nothing", t.Branch)
	}

	before, err := auditpush.ReadLedgerDir(ledger)
	if err != nil {
		return fmt.Errorf("campaign: reading %s: %w", ledger, err)
	}
	if audited(before, sha, t.Path) {
		fmt.Fprintf(stdout, "already audited %s\n", sha)
		return nil
	}
	seen := map[string]bool{}
	for _, e := range before {
		seen[e.File] = true
	}

	// A worktree left by a run that died is not an audit worth keeping.
	if _, err := os.Stat(audit); err == nil {
		removeAudit(ctx, r, clone, audit)
	}
	if _, err := r.Run(ctx, clone, "git", "worktree", "add", "--detach", audit, sha); err != nil {
		return fmt.Errorf("campaign: adding audit worktree at %s: %w", sha, err)
	}
	defer removeAudit(ctx, r, clone, audit)

	fmt.Fprintf(stderr, "campaign: auditing %s at %s\n", t.Upstream, sha)
	_, certErr := r.Run(ctx, root, CertifyArgv(t, audit, ledger, t.Goals)...)

	after, err := auditpush.ReadLedgerDir(ledger)
	if err != nil {
		return errors.Join(certErr, fmt.Errorf("campaign: reading %s after the audit: %w", ledger, err))
	}
	var fresh []string // absolute paths of the entries this run added
	for _, e := range after {
		if !seen[e.File] {
			fresh = append(fresh, filepath.Join(ledger, auditpush.ScansSubdir, e.File))
		}
	}

	var pubErr error
	if len(fresh) > 0 {
		msg := fmt.Sprintf("Record the corral audit of %s at %s.", t.Upstream, sha)
		// The fork's entries are already in ledger/ (corral wrote them there).
		pubErr = publish(ctx, r, ledger, fresh, msg, stderr)
		// Corral's own ledger receives a copy of each, through the same door.
		if pubErr == nil {
			for _, f := range fresh {
				if _, err := r.Run(ctx, corralLedger, "corral", "ledger", "append", f, corralLedger); err != nil {
					pubErr = fmt.Errorf("campaign: appending %s to %s: %w", filepath.Base(f), corralLedger, err)
					break
				}
			}
		}
		if pubErr == nil {
			pubErr = publish(ctx, r, corralLedger, fresh, msg, stderr)
		}
	}
	if certErr != nil {
		certErr = fmt.Errorf("campaign: audit of %s failed (not retried): %w", t.Name, certErr)
	}
	return errors.Join(certErr, pubErr)
}

// audited reports whether the newest scan entry already names this commit and
// path. The ledger is read with the one reader the rest of corral uses.
func audited(entries []auditpush.LedgerEntry, sha, path string) bool {
	scans := auditpush.ScanEntries(entries)
	if len(scans) == 0 {
		return false
	}
	s := scans[len(scans)-1].Bundle.Scan
	return s.Commit == sha && s.Scope == path
}

// removeAudit takes the audit worktree away. `git worktree remove` refuses a
// tree with untracked build output, and the forced form is never allowed, so a
// refusal falls back to deleting the directory (it is a disposable copy) and
// pruning git's record of it.
func removeAudit(ctx context.Context, r Runner, clone, audit string) {
	if _, err := r.Run(ctx, clone, "git", "worktree", "remove", audit); err != nil {
		_ = os.RemoveAll(audit)
		_, _ = r.Run(ctx, clone, "git", "worktree", "prune")
	}
}

// publish commits the ledger worktree's new entries and pushes them. A push
// the remote rejected because the branch moved is re-linked, never forced: the
// entries are saved aside, the worktree is reset to the remote branch (which
// would delete them), and each is re-appended with `corral ledger append`,
// which re-hashes and re-links it onto the new head. A second rejection is an
// error.
func publish(ctx context.Context, r Runner, dir string, entries []string, msg string, stderr io.Writer) error {
	commitAndPush := func() error {
		if _, err := r.Run(ctx, dir, "git", "add", auditpush.ScansSubdir); err != nil {
			return err
		}
		if _, err := r.Run(ctx, dir, "git", "commit", "-m", msg); err != nil {
			return err
		}
		_, err := r.Run(ctx, dir, "git", "push", "origin", "HEAD:"+ledgerBranch)
		return err
	}
	err := commitAndPush()
	if err == nil || !moved(err) {
		return err
	}
	fmt.Fprintf(stderr, "campaign: %s moved on the remote; re-linking %d entries\n", ledgerBranch, len(entries))
	tmp, terr := os.MkdirTemp("", "campaign-entries-")
	if terr != nil {
		return terr
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	var saved []string
	for _, e := range entries {
		raw, rerr := os.ReadFile(e) // #nosec G304 -- an entry this run just listed
		if rerr != nil {
			return rerr
		}
		p := filepath.Join(tmp, filepath.Base(e))
		if werr := os.WriteFile(p, raw, 0o600); werr != nil {
			return werr
		}
		saved = append(saved, p)
	}
	if _, err := r.Run(ctx, dir, "git", "fetch", "origin", ledgerBranch); err != nil {
		return err
	}
	if _, err := r.Run(ctx, dir, "git", "reset", "--hard", "origin/"+ledgerBranch); err != nil {
		return err
	}
	for _, p := range saved {
		if _, err := r.Run(ctx, dir, "corral", "ledger", "append", p, dir); err != nil {
			return fmt.Errorf("re-linking %s: %w", filepath.Base(p), err)
		}
	}
	return commitAndPush()
}

// moved reports whether a push failed because the remote branch is ahead.
func moved(err error) bool {
	m := err.Error()
	return strings.Contains(m, "non-fast-forward") || strings.Contains(m, "fetch first") || strings.Contains(m, "[rejected]")
}
