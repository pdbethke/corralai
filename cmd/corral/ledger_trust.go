// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"crypto/ed25519"
	"fmt"
	"regexp"
	"strings"

	"github.com/pdbethke/corralai/internal/ledger"
)

// ledgerRepoUsageDefault is empty on purpose: the repository a keyless entry
// must name is never guessed, not from Scan.Repo, the directory or git config.
const ledgerRepoUsageDefault = ""

const ledgerRepoUsage = "the GitHub repository (owner/name) whose Actions identity signed this ledger's keyless entries. Required only when the chain holds a keyless (corral-ledger-6) entry, which is checked against exactly this repository; never guessed from the scan's repo, the directory or git config, because the repository the entries claim is the thing being checked"

// ledgerRepoRE is the shape of a GitHub owner/name, and nothing wider: a URL,
// a path or a wildcard here would let the check pass for a repository nobody
// named.
var ledgerRepoRE = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)

// ledgerSigstoreRoot is the Sigstore trusted root keyless entries are checked
// against: the public-good root embedded in this release. A variable only so a
// test can stand an in-process Sigstore in for it.
var ledgerSigstoreRoot = ledger.PublicGoodTrustedRoot

// ledgerTrust is the ONE place a command's flags become the ledger package's
// Trust. ledger has no defaults, so every reader names its root key (pub, may
// be nil: unchecked), its repository (repoFlag, "" when keyless entries are
// not expected: the ledger then says so for the first one it meets) and its
// Sigstore root.
func ledgerTrust(pub ed25519.PublicKey, repoFlag string) (ledger.Trust, error) {
	repo := strings.TrimSpace(repoFlag)
	if repo != "" && !ledgerRepoRE.MatchString(repo) {
		return ledger.Trust{}, fmt.Errorf("--ledger-repo %q is not owner/name (letters, digits, - . _ only, one slash)", repo)
	}
	root, err := ledgerSigstoreRoot()
	if err != nil {
		return ledger.Trust{}, fmt.Errorf("the embedded Sigstore root: %w", err)
	}
	return ledger.Trust{RootPub: pub, Repo: repo, SigstoreRoot: root}, nil
}
