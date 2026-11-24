// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/repoindex"
)

// inlineTestLines are the lines of code that hold the source's OWN tests, for
// a language whose source can declare them inline (Rust's `#[cfg(test)] mod
// tests { ... }`, a test module nested in another, a #[test] fn anywhere).
// They are the audit's evidence, so every grading run of the file refuses a
// mutant that edits them (adequacy.WithEvidenceLines).
//
// repoindex owns the grammar, so this parses nothing: it is
// repoindex.OwnTestLines. A language with no reader answers none, and so
// does a build without cgo (no grammar). That build cannot read a Rust file
// at all and refuses it upstream, so there is no fallback here that would
// guess at test lines. A source whose own tests could not be located (the
// parse set code aside where they could sit) is an error, ownTestsRefusal:
// the audit door (auditLanding) refuses such a file before any spend, and
// this keeps a scorer from ever being built over lines that may miss them.
func inlineTestLines(codePath, langName string, code []byte) ([]lang.LineRange, error) {
	lines, unread := repoindex.OwnTestLines(langName, code)
	if unread != "" {
		return nil, ownTestsRefusal(codePath, unread)
	}
	return lines, nil
}
