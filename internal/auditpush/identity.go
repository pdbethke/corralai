// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"os/exec"
	"strings"

	"github.com/pdbethke/corralai/internal/ledger"
)

// Identity is the audited party; the type and its methods live in
// internal/ledger, which builds for the browser, and are aliased here.
type Identity = ledger.Identity

// CommitIdentity reads the commit's author, committer and co-author
// trailers from the checkout at dir. Empty when dir is not a checkout, the
// commit is not in it, or git is unavailable — a row that could not learn
// the party records none rather than a guess.
func CommitIdentity(dir, commit string) Identity {
	if commit == "" {
		return Identity{}
	}
	// #nosec G204 -- fixed argv; dir is the operator's own checkout and commit a revision it resolved
	out, err := exec.Command("git", "-C", dir, "log", "-1",
		"--format=%an%x00%cn%x00%(trailers:key=Co-authored-by,valueonly,separator=%x00)", commit).Output()
	if err != nil {
		return Identity{}
	}
	parts := strings.Split(strings.TrimRight(string(out), "\n"), "\x00")
	if len(parts) < 2 {
		return Identity{}
	}
	id := Identity{Author: strings.TrimSpace(parts[0]), Committer: strings.TrimSpace(parts[1])}
	// A trailer that repeats the author, or another trailer, is one party
	// named twice — folded here, or the committer seat would count the
	// author's change twice per audit (seen on the first entry that carried
	// the party: "change by P, with P, Claude Code").
	seen := map[string]bool{strings.ToLower(id.Author): true}
	var names []string
	for _, v := range parts[2:] {
		n := nameOnly(v)
		if n == "" || seen[strings.ToLower(n)] {
			continue
		}
		seen[strings.ToLower(n)] = true
		names = append(names, n)
	}
	id.CoAuthors = strings.Join(names, "\n")
	return id
}

// nameOnly strips the address from "Name <addr>"; a bare address becomes
// its local part, so a trailer that carried no name is still a name.
func nameOnly(v string) string {
	v = strings.TrimSpace(v)
	if i := strings.Index(v, "<"); i >= 0 {
		name := strings.TrimSpace(v[:i])
		if name != "" {
			return name
		}
		addr := strings.Trim(v[i:], "<> ")
		if at := strings.Index(addr, "@"); at > 0 {
			return addr[:at]
		}
		return addr
	}
	return v
}
