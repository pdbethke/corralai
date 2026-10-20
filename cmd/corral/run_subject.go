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
	repo = gitIn(repoDir, "remote", "get-url", "origin")
	commit = gitIn(repoDir, "rev-parse", "HEAD")
	if repo == "" || commit == "" {
		return repo, commit, subjectIdentityNone
	}
	return repo, commit, subjectIdentityGit
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
