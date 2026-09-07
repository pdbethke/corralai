// SPDX-License-Identifier: Elastic-2.0

package prior

import (
	"fmt"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

// TestDigestHashesTheHunkItRenders is a Claude Code reviewer's
// reproduction (`corral review --scope internal/prior`, 2026-09-07),
// inverted: the same edit as a ledger-only prior (span and shape, no
// hunk) and as a merged document+ledger prior (the hunk restored) render
// DIFFERENT paragraphs, so they must carry different digests — the digest
// is the cache key's prior component. Fails on the unfixed Digest.
func TestDigestHashesTheHunkItRenders(t *testing.T) {
	ledgerOnly := Tried{
		Path: "f.py", ParentSHA256: "e3b0c44298fc1c14", ID: "s0/m1",
		Span: lang.LineRange{Start: 12, End: 12}, Shape: "constant-changed",
		Outcome: "survived",
	}
	withHunk := ledgerOnly
	withHunk.Search, withHunk.Replace = "    limit = 10", "    limit = 11"

	a, b := []Tried{ledgerOnly}, []Tried{withHunk}
	da, db := Digest(a), Digest(b)
	ra, rb := Render(a), Render(b)
	if ra == rb {
		t.Fatalf("no defect to guard: the two priors render the same paragraph")
	}
	if da == db {
		t.Fatalf("two priors that hand the generator DIFFERENT text carry ONE digest %s:\n%s---\n%s", da, ra, rb)
	}
	_ = fmt.Sprintf
}
