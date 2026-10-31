// SPDX-License-Identifier: Elastic-2.0

package auditpush

import (
	"os/exec"
	"regexp"
	"strings"

	"github.com/pdbethke/corralai/internal/ledger"
)

// Identity is the audited party; the type and its methods live in
// internal/ledger, which builds for the browser, and are aliased here.
type Identity = ledger.Identity

// CommitIdentity reads the commit's author, committer, co-author trailers
// and "Generated with" footer lines (generatedWith) from the checkout at dir. Empty when dir is not a checkout, the
// commit is not in it, or git is unavailable — a row that could not learn
// the party records none rather than a guess.
func CommitIdentity(dir, commit string) Identity {
	if commit == "" {
		return Identity{}
	}
	// #nosec G204 -- fixed argv; dir is the operator's own checkout and commit a revision it resolved
	out, err := exec.Command("git", "-C", dir, "log", "-1",
		"--format=%an%x00%cn%x00%(trailers:key=Co-authored-by,valueonly,separator=%x00)%x01%B", commit).Output()
	if err != nil {
		return Identity{}
	}
	head, message, _ := strings.Cut(string(out), "\x01")
	parts := strings.Split(strings.TrimRight(head, "\n"), "\x00")
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
	id.GeneratedWith = strings.Join(generatedWith(message), "\n")
	return id
}

// footerLine is a whole line declaring the tool that generated a commit:
// "Generated with" or "by", after an optional emoji, then the tool as a
// Markdown link's text ("[Claude Code](https://claude.com/claude-code)") or
// as one to four plain words, and nothing more but a full stop. Anchored at
// both ends and that narrow on purpose: a sentence of prose that begins
// "Generated with care, …" is not a declaration, and naming a tool the
// commit never declared is the guess the record must not make.
var footerLine = regexp.MustCompile(`^(?:\p{So}\x{FE0F}?\s*)?Generated (?:with|by) (?:\[([^\]]+)\]\([^)\s]+\)|([\p{L}\p{N}][\p{L}\p{N}.+-]*(?: [\p{L}\p{N}][\p{L}\p{N}.+-]*){0,3}))\.?$`)

// generatedWith is the tools message's footer lines name (footerLine), in
// order, each once whatever its case.
func generatedWith(message string) []string {
	seen := map[string]bool{}
	var tools []string
	for _, line := range strings.Split(message, "\n") {
		m := footerLine.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		// The class a plain name is read with takes a full stop (a version,
		// "2.1"), so the sentence's own stop is cut here.
		tool := strings.TrimSuffix(strings.TrimSpace(m[1]+m[2]), ".")
		if tool == "" || seen[strings.ToLower(tool)] {
			continue
		}
		seen[strings.ToLower(tool)] = true
		tools = append(tools, tool)
	}
	return tools
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
