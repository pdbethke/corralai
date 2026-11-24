// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/repoindex"
)

// inlineTestLines are the lines of code that hold the source's OWN tests, for
// a language whose source can declare them inline (Rust's `#[cfg(test)] mod
// tests { ... }`). They are the audit's evidence, so every grading run of the
// file refuses a mutant that edits them (adequacy.WithEvidenceLines).
//
// repoindex owns the grammar, so this parses nothing: it keeps the inline
// modules' line ranges from repoindex.TestModules. A language with no modules
// reader answers none, and so does a build without cgo (no grammar). That
// build cannot read a Rust file at all and refuses it upstream, so there is no
// fallback here that would guess at test lines.
func inlineTestLines(langName string, code []byte) []lang.LineRange {
	var out []lang.LineRange
	for _, m := range repoindex.TestModules(langName, code) {
		if m.Inline && m.StartLine > 0 {
			out = append(out, lang.LineRange{Start: m.StartLine, End: m.EndLine})
		}
	}
	return out
}
