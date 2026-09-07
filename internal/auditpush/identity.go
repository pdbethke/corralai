// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"os/exec"
	"strings"
)

// Identity is the audited party: who made the commit a record is about.
//
// Nemo iudex in causa sua names two parties, and until this the record
// named only the judge. Author and Committer are git's, by name; CoAuthors
// are the commit's Co-authored-by trailers, by name — which, in code an
// agent helped write, is where the agent is ("Claude Code"). Names only,
// never addresses: the record travels, and a name is what a `GROUP BY`
// needs. Nothing here presumes what a reader does with it: the record
// names the party the same way for a person and for an agent, and the
// first use is the party's own view of what the audit gave back.
//
// CoAuthors is one name per line (the warehouse column's form too:
// `string_split(co_authors, chr(10))` in SQL) so a row stays a comparable
// value.
type Identity struct {
	Author    string `json:"author,omitempty"`
	Committer string `json:"committer,omitempty"`
	CoAuthors string `json:"co_authors,omitempty"`
}

// IsZero reports whether nothing was recorded.
func (id Identity) IsZero() bool { return id == Identity{} }

// CoAuthorList is CoAuthors split, empty when none.
func (id Identity) CoAuthorList() []string {
	var out []string
	for _, n := range strings.Split(id.CoAuthors, "\n") {
		if n = strings.TrimSpace(n); n != "" {
			out = append(out, n)
		}
	}
	return out
}

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
