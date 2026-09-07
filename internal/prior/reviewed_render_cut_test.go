// SPDX-License-Identifier: Elastic-2.0

package prior

import (
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

// TestRenderCutNamesTheTailItDropped is a Gemini reviewer's reproduction
// (review dcd0874fecb8#R1, Claude Code verifying, 2026-09-07), inverted.
// Render lists the edits in line order and cuts at MaxRendered, so the
// undisclosed ones are the file's TAIL — but the cut line re-sliced the
// caller's UNSORTED slice, and named whichever edits happened to sit past
// index MaxRendered in the caller's order. The line that was added that
// morning to say where the cut was, said the wrong place. Fails on the
// unfixed Render.
func TestRenderCutNamesTheTailItDropped(t *testing.T) {
	var tried []Tried
	for i := MaxRendered + 7; i >= 1; i-- { // reverse order in: the first 7 in the caller's slice are the tail
		tried = append(tried, Tried{Span: lang.LineRange{Start: i, End: i}, Shape: "other", Outcome: "killed"})
	}
	para := Render(tried)
	want := "between lines 41 and 47"
	if !strings.Contains(para, want) {
		t.Fatalf("the cut must name the tail the ordered list dropped (%s):\n%s", want, para)
	}
}
