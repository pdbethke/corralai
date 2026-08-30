// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"strings"

	golang "github.com/pdbethke/corralai/internal/lang"
)

// AuthoredRecord is this verdict's authored evidence as ONE string, for a
// ledger column that holds exactly one: the merged file first, then every
// proven part the language's concatenator refused, each behind a separator
// comment naming the survivor it kills and why it is separate.
//
// IT IS A RECORD, NOT A RUNNABLE FILE, whenever AuthoredExtra is non-empty —
// the parts are separate precisely because putting them in one file would not
// build, or would silently override, or would never be collected. The
// separator says so in the file's own comment syntax so a reader who pastes it
// somewhere sees the warning rather than a mystery.
//
// Storing only AuthoredTest was the alternative and it is the one thing this
// must not do: every part here is a test corral WROTE, COMPILED and RAN to
// kill a specific survivor, and ProvenMissed counts it. A record that drops
// half of them reports N proven gaps and retains fewer than N proofs.
func (v Verdict) AuthoredRecord() string {
	if len(v.AuthoredExtra) == 0 {
		return v.AuthoredTest
	}
	c := recordCommentMarker(v.Lang)
	var b strings.Builder
	b.WriteString(strings.TrimRight(v.AuthoredTest, "\n"))
	for _, p := range v.AuthoredExtra {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(c + " --- corral: separate test file (unmergeable) — " + p.MutantID + " ---\n")
		if r := strings.TrimSpace(p.Reason); r != "" {
			b.WriteString(c + " " + r + "\n")
		}
		b.WriteString(strings.TrimRight(p.Source, "\n"))
	}
	b.WriteString("\n")
	return b.String()
}

// recordCommentMarker is the line-comment syntax of the audited language, so
// the separator above is a comment in the file it separates rather than a
// syntax error in the middle of it. `#` is the fallback because it is right
// for python and ruby and harmless as a label anywhere corral does not know.
func recordCommentMarker(langName string) string {
	switch langName {
	case "go", "javascript", "typescript":
		return "//"
	}
	return "#"
}

// AuthoredParts is every proven authored test this verdict holds, merged file
// included, as the parts a caller can render one at a time.
//
// Only the EXTRA parts carry a mutant id: the merged file is by construction
// the tests of several survivors folded together and belongs to no single one.
func (v Verdict) AuthoredParts() []golang.AuthoredPart {
	var out []golang.AuthoredPart
	if strings.TrimSpace(v.AuthoredTest) != "" {
		out = append(out, golang.AuthoredPart{Source: v.AuthoredTest})
	}
	return append(out, v.AuthoredExtra...)
}
