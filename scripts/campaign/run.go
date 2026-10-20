// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

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

// corralRepo is the repository _corral-ledger/ must push to. It is a constant
// and not a Target field because it is corral's own record, the same for every
// target.
const corralRepo = "pdbethke/corralai"

// allowedCommands is the first two words of every command the campaign may
// issue; checkAllowed adds a shape rule for the ones that need it. The tests
// iterate this same table, so the list and its enforcement cannot disagree.
var allowedCommands = map[string]bool{
	"corral certify": true, "corral ledger": true,
	"git fetch": true, "git worktree": true, "git -C": true,
	"git add": true, "git commit": true, "git push": true, "git reset": true,
	"git status": true, "git diff": true, "git remote": true,
	"gh repo": true,
}

// diffAddedArgv lists the entry files the worktree has that the remote branch
// lacks. Three dots and --diff-filter=A: only files ADDED on this side since
// the merge base, so a diverged remote's own files are never mistaken for ours.
func diffAddedArgv() []string {
	return []string{"git", "diff", "--name-only", "--diff-filter=A", "origin/" + ledgerBranch + "...HEAD", "--", auditpush.ScansSubdir}
}

var combinedShort = regexp.MustCompile(`^-[A-Za-z]+$`)

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
	for _, a := range own[1:] {
		forced := a == "--force" || a == "-f" || a == "--force-with-lease" ||
			strings.HasPrefix(a, "--force-with-lease=") || a == "--force-if-includes" ||
			(combinedShort.MatchString(a) && strings.Contains(a, "f")) ||
			(argv[1] == "push" && strings.HasPrefix(a, "+"))
		if forced {
			return fmt.Errorf("campaign: refusing %q: forced operations are never allowed", argv)
		}
	}
	key := argv[0] + " " + argv[1]
	if !allowedCommands[key] {
		return fmt.Errorf("campaign: %q is not an allowed command", argv)
	}
	bad := func() error { return fmt.Errorf("campaign: %q is not an allowed form of %s", argv, key) }
	switch key {
	case "corral ledger":
		if len(argv) < 3 || argv[2] != "append" {
			return bad()
		}
	case "git -C":
		if len(argv) < 4 || argv[3] != "rev-parse" {
			return bad()
		}
	case "gh repo":
		// The only thing gh may ever do: bring the founder's own fork level
		// with its upstream.
		if len(argv) != 4 || argv[2] != "sync" {
			return bad()
		}
	case "git remote":
		if !reflect.DeepEqual(argv, []string{"git", "remote", "get-url", "--push", "origin"}) {
			return bad()
		}
	case "git push":
		// One refspec, one remote: deletes, mirrors, --all, --prune and any
		// ":ref" refspec are all unreachable because nothing else is allowed.
		if !reflect.DeepEqual(argv, []string{"git", "push", "origin", "HEAD:" + ledgerBranch}) {
			return bad()
		}
	case "git status":
		if !reflect.DeepEqual(argv, []string{"git", "status", "--porcelain", "--branch", "--untracked-files=all"}) {
			return bad()
		}
	case "git diff":
		if !reflect.DeepEqual(argv, diffAddedArgv()) {
			return bad()
		}
	}
	return nil
}

// execRunner runs real commands. Stdout is captured for the caller and echoed
// to echo, as is stderr; stderr's tail is also returned inside the error,
// because callers decide what to do from git's own words ("non-fast-forward")
// and a bare "exit status 1" says nothing.
type execRunner struct{ echo io.Writer }

func (e execRunner) Run(ctx context.Context, dir string, argv ...string) (string, error) {
	if err := checkAllowed(argv); err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) // #nosec G204 -- argv passed checkAllowed
	cmd.Dir = dir
	var out, errb bytes.Buffer
	cmd.Stdout = io.MultiWriter(&out, e.echo)
	cmd.Stderr = io.MultiWriter(&errb, e.echo)
	err := cmd.Run()
	if err != nil {
		tail := strings.TrimSpace(errb.String())
		if len(tail) > 2048 {
			tail = tail[len(tail)-2048:]
		}
		if tail != "" {
			err = fmt.Errorf("%w: %s", err, tail)
		}
	}
	return out.String(), err
}

// remoteNames reports whether a git remote URL names github.com/<repo>, in
// the https, scp-like ssh or ssh:// form, with or without .git.
func remoteNames(url, repo string) bool {
	u := strings.TrimSuffix(strings.TrimSpace(url), "/")
	u = strings.TrimSuffix(u, ".git")
	for _, p := range []string{"https://github.com/", "git@github.com:", "ssh://git@github.com/"} {
		if strings.HasPrefix(u, p) {
			return strings.EqualFold(strings.TrimPrefix(u, p), repo)
		}
	}
	return false
}

// checkRemote refuses a ledger worktree whose origin is not the repository it
// must publish to: the push target is `origin`, and an unchecked origin would
// let a misconfigured worktree send signed entries somewhere unintended.
func checkRemote(ctx context.Context, r Runner, dir, repo string) error {
	out, err := r.Run(ctx, dir, "git", "remote", "get-url", "--push", "origin")
	if err != nil {
		return fmt.Errorf("campaign: reading origin of %s: %w", dir, err)
	}
	if !remoteNames(out, repo) {
		return fmt.Errorf("campaign: origin of %s is %q, want github.com/%s", dir, strings.TrimSpace(out), repo)
	}
	return nil
}

// checkTracking refuses a ledger worktree that is not on corral/ledger
// tracking origin/corral/ledger: "ahead" and the diff are measured against
// that upstream, so on any other branch the recovery logic would be blind.
func checkTracking(ctx context.Context, r Runner, dir string) error {
	out, err := r.Run(ctx, dir, "git", "status", "--porcelain", "--branch", "--untracked-files=all")
	if err != nil {
		return fmt.Errorf("campaign: reading the branch of %s: %w", dir, err)
	}
	want := "## " + ledgerBranch + "...origin/" + ledgerBranch
	header, _, _ := strings.Cut(out, "\n")
	if header != want && !strings.HasPrefix(header, want+" ") {
		return fmt.Errorf("campaign: %s is on %q, want %q; fix with: git -C %s checkout %s && git -C %s branch --set-upstream-to=origin/%s",
			dir, strings.TrimPrefix(header, "## "), strings.TrimPrefix(want, "## "), dir, ledgerBranch, dir, ledgerBranch)
	}
	return nil
}

// manifestFile is the durable list of scans this campaign itself produced.
// Only those are ever copied to corral's public ledger: a fork may hold older
// entries (an earlier Action's, say) that nobody chose to re-sign there.
func manifestFile(root string) string { return filepath.Join(root, "campaign-entries.json") }

func loadManifest(path string) (map[string]bool, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- the campaign's own file
	if os.IsNotExist(err) {
		return map[string]bool{}, nil
	}
	if err != nil {
		return nil, err
	}
	var keys []string
	if err := json.Unmarshal(raw, &keys); err != nil {
		return nil, fmt.Errorf("campaign: %s: %w", path, err)
	}
	m := map[string]bool{}
	for _, k := range keys {
		m[k] = true
	}
	return m, nil
}

// recordManifest adds keys and writes the file atomically.
func recordManifest(path string, add []string) error {
	m, err := loadManifest(path)
	if err != nil {
		return err
	}
	for _, k := range add {
		m[k] = true
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	raw, err := json.MarshalIndent(keys, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// normalizeScope is the form corral records a --path scope in
// (cmd/corral resolveScanScope): cleaned, slash-separated, "." meaning "".
func normalizeScope(p string) string {
	if strings.TrimSpace(p) == "" {
		return ""
	}
	c := filepath.Clean(p)
	if c == "." {
		return ""
	}
	return filepath.ToSlash(c)
}

// scanKey is a scan's identity independent of where it is placed. ScanUID is
// NOT used: placing an entry in a ledger re-derives it from the placement
// time, so the fork's copy and corral's copy of one scan never share one.
func scanKey(s auditpush.ScanRow) string {
	started := ""
	if s.StartedAt != nil {
		started = s.StartedAt.UTC().Format(time.RFC3339Nano)
	}
	return fmt.Sprintf("%q|%q|%q|%q|%q|%q|%d|%s", s.Repo, s.RunURL, s.Host, s.CorralVersion, s.Commit, s.Substrate, s.ScanID, started)
}

// RunTarget audits the newest upstream commit of one target and records the
// result. Order, and why:
//
//  1. check both ledger worktrees push where they should, before any spend.
//  2. gh repo sync, git fetch: bring the fork and the clone level with upstream.
//  3. resolve upstream/<branch>: the commit to audit.
//  4. settle: publish anything an earlier run left unpublished, so a failed
//     push is retried here and not stranded behind "already audited".
//  5. skip when the newest ledger entry already names this commit and path, so
//     a scheduled run costs nothing on a quiet repository.
//  6. audit in a detached worktree of the clone, so the founder's curation
//     checkout is never touched.
//  7. settle again, EVEN IF the audit failed: a failed run is still a fact,
//     and it is not retried (a retry would spend twice).
//  8. remove the audit worktree.
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
	if err := checkRemote(ctx, r, ledger, t.Fork); err != nil {
		return err
	}
	if err := checkRemote(ctx, r, corralLedger, corralRepo); err != nil {
		return err
	}
	for _, d := range []string{ledger, corralLedger} {
		if err := checkTracking(ctx, r, d); err != nil {
			return err
		}
	}
	manifest, unplaced := manifestFile(root), filepath.Join(root, "unplaced")

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
	msg := fmt.Sprintf("Record the corral audit of %s.", t.Upstream)

	if err := settle(ctx, r, ledger, corralLedger, manifest, unplaced, msg, stderr); err != nil {
		return err
	}
	entries, err := auditpush.ReadLedgerDir(ledger)
	if err != nil {
		return fmt.Errorf("campaign: reading %s: %w", ledger, err)
	}
	if audited(entries, sha, t.Path) {
		fmt.Fprintf(stdout, "already audited %s\n", sha)
		return nil
	}

	// A worktree left by a run that died is not an audit worth keeping.
	if _, err := os.Stat(audit); err == nil {
		removeAudit(ctx, r, clone, audit)
	}
	if _, err := r.Run(ctx, clone, "git", "worktree", "add", "--detach", audit, sha); err != nil {
		return fmt.Errorf("campaign: adding audit worktree at %s: %w", sha, err)
	}
	defer removeAudit(ctx, r, clone, audit)

	preexisting := map[string]bool{}
	for _, e := range entries {
		preexisting[e.File] = true
	}
	fmt.Fprintf(stderr, "campaign: auditing %s at %s\n", t.Upstream, sha)
	_, certErr := r.Run(ctx, root, CertifyArgv(t, audit, ledger, t.Goals)...)
	if certErr != nil {
		certErr = fmt.Errorf("campaign: audit of %s failed (not retried): %w", t.Name, certErr)
	}
	// Record what this run produced before anything can fail, success or not:
	// the manifest is what later runs use to know an entry is the campaign's.
	var mfErr error
	if after, err := auditpush.ReadLedgerDir(ledger); err != nil {
		mfErr = fmt.Errorf("campaign: reading %s after the audit: %w", ledger, err)
	} else {
		var fresh []string
		for _, e := range after {
			if e.IsScan() && !preexisting[e.File] {
				fresh = append(fresh, scanKey(e.Bundle.Scan))
			}
		}
		if len(fresh) > 0 {
			mfErr = recordManifest(manifest, fresh)
		}
	}
	return errors.Join(certErr, mfErr, settle(ctx, r, ledger, corralLedger, manifest, unplaced, msg, stderr))
}

// settle brings both ledgers level with what has been measured: unpublished
// fork entries are pushed, fork entries corral's ledger lacks are appended
// there, and corral's ledger is pushed. Each step is idempotent, so a run
// that died between any two of them is finished by the next one.
func settle(ctx context.Context, r Runner, ledger, corralLedger, manifest, unplaced, msg string, stderr io.Writer) error {
	mine, err := loadManifest(manifest)
	if err != nil {
		return err
	}
	if err := publish(ctx, r, ledger, unplaced, msg, stderr); err != nil {
		return err
	}
	fork, err := auditpush.ReadLedgerDir(ledger)
	if err != nil {
		return fmt.Errorf("campaign: reading %s: %w", ledger, err)
	}
	have, err := auditpush.ReadLedgerDir(corralLedger)
	if err != nil {
		return fmt.Errorf("campaign: reading %s: %w", corralLedger, err)
	}
	known := map[string]bool{}
	for _, e := range have {
		if e.IsScan() {
			known[scanKey(e.Bundle.Scan)] = true
		}
	}
	for _, e := range fork {
		k := scanKey(e.Bundle.Scan)
		if !e.IsScan() || known[k] || !mine[k] {
			continue
		}
		src := filepath.Join(ledger, auditpush.ScansSubdir, e.File)
		if _, err := r.Run(ctx, corralLedger, "corral", "ledger", "append", src, corralLedger); err != nil {
			return fmt.Errorf("campaign: appending %s to %s: %w", e.File, corralLedger, err)
		}
	}
	return publish(ctx, r, corralLedger, unplaced, msg, stderr)
}

// audited reports whether the newest scan entry already names this commit and
// path. The ledger is read with the one reader the rest of corral uses.
func audited(entries []auditpush.LedgerEntry, sha, path string) bool {
	scans := auditpush.ScanEntries(entries)
	if len(scans) == 0 {
		return false
	}
	s := scans[len(scans)-1].Bundle.Scan
	return s.Commit == sha && s.Scope == normalizeScope(path)
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

// unpublished lists the entry files (as paths relative to dir) a ledger
// worktree holds that the remote branch does not: new files in the tree and
// files in commits it is ahead by.
func unpublished(ctx context.Context, r Runner, dir string) (files []string, dirty, ahead bool, err error) {
	prefix := auditpush.ScansSubdir + "/"
	seen := map[string]bool{}
	add := func(p string) {
		if strings.HasPrefix(p, prefix) && !seen[p] {
			seen[p] = true
			files = append(files, p)
		}
	}
	out, err := r.Run(ctx, dir, "git", "status", "--porcelain", "--branch", "--untracked-files=all")
	if err != nil {
		return nil, false, false, err
	}
	for _, line := range strings.Split(out, "\n") {
		switch {
		case strings.HasPrefix(line, "## "):
			ahead = strings.Contains(line, "[ahead")
		case len(line) > 3:
			dirty = true
			add(strings.TrimSpace(line[3:]))
		}
	}
	if ahead {
		out, err := r.Run(ctx, dir, diffAddedArgv()...)
		if err != nil {
			return nil, false, false, err
		}
		for _, line := range strings.Split(out, "\n") {
			add(strings.TrimSpace(line))
		}
	}
	return files, dirty, ahead, nil
}

// publish commits and pushes whatever a ledger worktree holds that its remote
// does not. A push the remote rejected because the branch moved is re-linked,
// never forced: the entries are saved aside, the worktree is reset to the
// remote branch (which would delete them), and each is re-appended with
// `corral ledger append`, which re-hashes and re-links it onto the new head
// under a new file name. A second rejection is an error.
func publish(ctx context.Context, r Runner, dir, unplaced, msg string, stderr io.Writer) error {
	files, dirty, ahead, err := unpublished(ctx, r, dir)
	if err != nil {
		return err
	}
	if !dirty && !ahead {
		return nil
	}
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
	push := func() error {
		_, err := r.Run(ctx, dir, "git", "push", "origin", "HEAD:"+ledgerBranch)
		return err
	}
	if dirty {
		err = commitAndPush()
	} else {
		err = push()
	}
	if err == nil || !moved(err) {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("campaign: %s is ahead of a moved remote and no entry files could be named to re-link: %w", dir, err)
	}
	fmt.Fprintf(stderr, "campaign: %s moved on the remote; re-linking %d entries in %s\n", ledgerBranch, len(files), dir)
	// The saved copies are the only record of these entries once the reset
	// runs, so they live under the campaign directory and are deleted only
	// when every one has been re-placed.
	if terr := os.MkdirAll(unplaced, 0o750); terr != nil {
		return terr
	}
	tmp, terr := os.MkdirTemp(unplaced, "relink-")
	if terr != nil {
		return terr
	}
	keep := true
	defer func() {
		if !keep {
			_ = os.RemoveAll(tmp)
		}
	}()
	var saved []string
	for _, f := range files {
		raw, rerr := os.ReadFile(filepath.Join(dir, f)) // #nosec G304 -- listed by git in the ledger worktree
		if rerr != nil {
			return rerr
		}
		p := filepath.Join(tmp, filepath.Base(f))
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
			return fmt.Errorf("re-linking %s: %w (the saved entries are kept in %s)", filepath.Base(p), err, tmp)
		}
	}
	if err := commitAndPush(); err != nil {
		return fmt.Errorf("%w (the saved entries are kept in %s)", err, tmp)
	}
	keep = false
	return nil
}

// moved reports whether a push failed because the remote branch is ahead.
func moved(err error) bool {
	m := err.Error()
	return strings.Contains(m, "non-fast-forward") || strings.Contains(m, "fetch first") || strings.Contains(m, "[rejected]")
}
