// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/pdbethke/corralai/internal/statedir"
)

// Identity words for localSubject. They are not defaults: they are the
// recorded fact of how (or whether) the subject was derived.
const (
	subjectIdentityGit  = "git"
	subjectIdentityNone = "none"
	// subjectIdentityOperator is a subject the operator TYPED (--repo and
	// --commit on certify --local): neither derived from git nor unknown.
	// git was not asked to back it, so it is not "git"; it was given, so it
	// is not "none".
	subjectIdentityOperator = "operator"
)

// machineLedgerDir is where this machine keeps its own ledger, created 0o750
// on first use. The path comes from the statedir table (override
// CORRALAI_MACHINE_LEDGER) and from nowhere else: there is no fallback
// directory, because a ledger written to a place nobody declared is a record
// nobody will find. The mode is 0o750 rather than the table's usual 0o700
// because a ledger is meant to be read (and published from) by tooling run by
// the same group; the signing key beside it stays private.
func machineLedgerDir() (string, error) {
	dir, err := statedir.Path(statedir.MachineLedger)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("create the machine ledger %s: %w", dir, err)
	}
	return dir, nil
}

// localSubject names the repository and revision a `certify --local` run
// audited, asking git ABOUT repoDir (git -C), never about the process's
// working directory: the operator runs corral from anywhere, and the old
// cwd-based lookup stamped a run with whatever repository the shell happened
// to be sitting in (or, outside one, the invented word "local").
//
// identity is "git" only when BOTH values resolve. Anything less is "none"
// with the unresolved value left "" — a repo that cannot be derived is
// recorded as unknown, never as a directory basename (resolveRepoName's
// fallback, deliberately NOT used: a basename is a different project's
// identity waiting to collide) and never as a placeholder. An empty repoDir
// (single-file mode, which seeds no repository) is "", "", "none".
func localSubject(repoDir string) (repo, commit, identity string) {
	repoDir = strings.TrimSpace(repoDir)
	if repoDir == "" {
		return "", "", subjectIdentityNone
	}
	repo = originRepoName(repoDir)
	commit = gitIn(repoDir, "rev-parse", "HEAD")
	if repo == "" || commit == "" {
		return repo, commit, subjectIdentityNone
	}
	return repo, commit, subjectIdentityGit
}

// originRepoName is the name a checkout's origin gives it, and the ONE place
// that name is derived: localSubject (--local), resolveRepoName (--repo,
// review) and repoIdentity all read it here, so the name a run is filed
// under and whether that name counts as a git identity are decided together.
// Before, --local kept origin's ".git" suffix and --repo stripped it, so one
// checkout was filed as two repositories depending on the verb (final
// whole-branch review, 2026-10-20).
//
// get-url rather than `config --get remote.origin.url`: it applies
// url.<base>.insteadOf rewrites, so the name recorded is the URL git would
// actually contact, which is what a reader can resolve. The ".git" suffix is
// removed because it is a transport spelling, not part of the project's
// name (https://github.com/o/r and https://github.com/o/r.git are one
// repository). An origin of "." is the checkout pointing at itself, which
// names nothing. "" means no name: no git, not a checkout, or no origin.
func originRepoName(repoDir string) string {
	repo := strings.TrimSuffix(gitIn(repoDir, "remote", "get-url", "origin"), ".git")
	if repo == "." {
		return ""
	}
	return repo
}

// repoIdentity says whether a --repo run's subject came from git: "git" when
// origin names the checkout (the name resolveRepoName files the run under is
// then originRepoName's), "none" when it does not and resolveRepoName fell
// back to the checkout directory's BASENAME. That basename still keys the
// row today — changing the recorded name is a separate decision — but it is
// not an identity, and the ledger must say so rather than let a reader take
// a directory name for a project's.
func repoIdentity(repoDir string) string {
	if originRepoName(repoDir) == "" {
		return subjectIdentityNone
	}
	return subjectIdentityGit
}

// treeDirtyAt is the TreeDirty a run whose recorded commit is commit
// records. TreeDirty is defined against HEAD — whether the audited tree
// differed from HEAD — so it is only a statement about the RECORDED revision
// when that revision is HEAD. `--commit <other sha>` on a feature-branch
// checkout audits HEAD's tree and records another commit; treeDirty would
// have said false (clean against HEAD), and a reader would have taken that
// as "this commit's code, unmodified" (final whole-branch review,
// 2026-10-20). So when the recorded commit is not HEAD — or cannot be
// resolved to compare — the answer is nil, unknown, never a false.
//
// Compared as full SHAs: an abbreviated typed commit counts as HEAD only if
// git resolves it to HEAD's own SHA. An empty commit (no revision recorded)
// has no HEAD to be measured against and is nil too.
func treeDirtyAt(repoDir, commit string) *bool {
	commit = strings.TrimSpace(commit)
	repoDir = strings.TrimSpace(repoDir)
	// A leading "-" would be read by rev-parse as an option, not a revision.
	if commit == "" || repoDir == "" || strings.HasPrefix(commit, "-") {
		return nil
	}
	head := gitIn(repoDir, "rev-parse", "--verify", "--quiet", "HEAD^{commit}")
	if head == "" {
		return nil
	}
	if commit != head && gitIn(repoDir, "rev-parse", "--verify", "--quiet", commit+"^{commit}") != head {
		return nil
	}
	return treeDirty(repoDir)
}

// treeDirty reports whether the working tree at repoDir has changes the
// commit does not contain: modified or deleted tracked files, staged changes,
// or untracked files (--untracked-files=all, so one new file inside a new
// directory is not hidden behind the directory's name). A verdict signed
// against a commit describes that commit only when the tree is clean; this is
// the bit that lets a reader tell.
//
// Corral's own state directory (.corral/, relative to repoDir) is ignored: a
// run writes its ledger there, so counting it would make the answer depend on
// whether the run had already started. Callers therefore ask ONCE, before the
// audit begins.
//
// nil means git could not say (absent, not a work tree, status failed) — an
// unknown tree is never rendered as a clean one.
func treeDirty(repoDir string) *bool {
	repoDir = strings.TrimSpace(repoDir)
	if repoDir == "" || gitIn(repoDir, "rev-parse", "--is-inside-work-tree") != "true" {
		return nil
	}
	// Not gitIn: its TrimSpace would eat the leading space of the first
	// porcelain line (" M file"), and an empty result must be told apart from
	// a failure.
	// #nosec G204 -- fixed argv; repoDir is the operator's own --repo-dir, never remote input
	out, err := exec.Command("git", "-C", repoDir, "status", "--porcelain=v1", "-z", "--untracked-files=all").Output()
	if err != nil {
		return nil
	}
	// status paths are relative to the repository root, so corral's directory
	// is the subdirectory repoDir sits in (if any) plus ".corral".
	corralDir := gitIn(repoDir, "rev-parse", "--show-prefix") + ".corral"
	dirty := false
	entries := strings.Split(string(out), "\x00")
	for i := 0; i < len(entries); i++ {
		e := entries[i]
		if len(e) < 4 {
			continue
		}
		// A rename or copy is followed by a second entry naming its source.
		if e[0] == 'R' || e[0] == 'C' || e[1] == 'R' || e[1] == 'C' {
			i++
		}
		path := e[3:]
		if path == corralDir || strings.HasPrefix(path, corralDir+"/") {
			continue
		}
		dirty = true
		break
	}
	return &dirty
}

// gitIn runs git against dir and returns its trimmed stdout, or "" when git
// is absent or the question has no answer (not a repository, no origin).
func gitIn(dir string, args ...string) string {
	// #nosec G204 -- fixed argv; dir is the operator's own --repo-dir, never remote input
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// resolveSubject is the one rule for naming a local run's subject, and for
// how that name was established. --repo and --commit are ONE unit: both
// given are used as typed (identity "operator": a typed name is the
// operator's claim, and git was not asked to back it — "git" would vouch for
// a name nothing checked, "none" would deny a name that was given), neither
// given are both derived from repoDir's git (localSubject, which says "git"
// or "none"), and exactly one is an error — pairing a typed repo with another
// repository's HEAD signs a revision that never existed. The error is a usage
// error, raised before any spend.
func resolveSubject(repoFlag, commitFlag, repoDir string) (repo, commit, identity string, err error) {
	repo, commit = strings.TrimSpace(repoFlag), strings.TrimSpace(commitFlag)
	switch {
	case repo != "" && commit != "":
		return repo, commit, subjectIdentityOperator, nil
	case repo == "" && commit == "":
		repo, commit, identity = localSubject(repoDir)
		return repo, commit, identity, nil
	}
	missing := "--commit"
	if repo == "" {
		missing = "--repo"
	}
	return "", "", "", fmt.Errorf("--repo and --commit name one subject together; pass both or neither (with neither, both come from --repo-dir's git); %s is missing", missing)
}
