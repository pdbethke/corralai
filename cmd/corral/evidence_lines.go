// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/repoindex"
)

// ownTestEvidence is what a grading scorer is told about the audited
// source's own tests: their lines (advpool.JailScorer.EvidenceLines), and
// why they could not be located when the parse cannot say
// (JailScorer.EvidenceUnread, which invalidates every mutant).
type ownTestEvidence struct {
	lines  []lang.LineRange
	unread string
}

// inlineTestLines are the lines of code that hold the source's OWN tests, for
// a language whose source can declare them inline (Rust's `#[cfg(test)] mod
// tests { ... }`, a test module nested in another, a #[test] fn anywhere).
// They are the audit's evidence, so every grading run of the file refuses a
// mutant that edits them (adequacy.WithEvidenceLines), and every mutant when
// the parse set code aside where tests could hide (adequacy.WithEvidenceUnread).
//
// repoindex owns the grammar, so this parses nothing: it is
// repoindex.OwnTestLines. A language with no reader answers none, and so
// does a build without cgo (no grammar). That build cannot read a Rust file
// at all and refuses it upstream, so there is no fallback here that would
// guess at test lines.
func inlineTestLines(langName string, code []byte) ownTestEvidence {
	lines, unread := repoindex.OwnTestLines(langName, code)
	return ownTestEvidence{lines: lines, unread: unread}
}
