// SPDX-License-Identifier: Elastic-2.0

package ledger

import "strings"

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
