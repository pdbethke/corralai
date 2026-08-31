// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
	golang "github.com/pdbethke/corralai/internal/lang"
)

// TestRenderHunkGolden pins the exact block shape for a hunk-native mutant:
// header, numbered context, numbered removed/added lines, numbered trailing
// context.
func TestRenderHunkGolden(t *testing.T) {
	original := "line1\nline2\nline3\nline4\nline5\nline6\nline7\nline8\nline9\nline10\n"
	m := adequacy.Mutant{
		ID:      "m1",
		Search:  "line5\nline6\nline7\n",
		Replace: "LINEFIVE\nLINESIX\nLINESEVEN\n",
	}

	want := strings.Join([]string{
		"--- SURVIVOR m1 (lines 5-7) ---",
		"  2 | line2",
		"  3 | line3",
		"  4 | line4",
		"- 5 | line5",
		"- 6 | line6",
		"- 7 | line7",
		"+ 5 | LINEFIVE",
		"+ 6 | LINESIX",
		"+ 7 | LINESEVEN",
		"  8 | line8",
		"  9 | line9",
		"  10 | line10",
	}, "\n")

	if got := RenderHunk(m, original, 3); got != want {
		t.Fatalf("RenderHunk mismatch:\ngot:\n%s\n\nwant:\n%s", got, want)
	}
}

// TestRenderHunkAtFileEdges covers the three boundary shapes the golden
// case doesn't exercise: the mutant sits at the very start of the file
// (no "before" context to show), at the very end of a file with no
// trailing newline (no "after" context, no synthetic empty final line),
// and mid-file in a file with no trailing newline (isolating "no trailing
// newline" from "change touches the last line").
func TestRenderHunkAtFileEdges(t *testing.T) {
	t.Run("file start", func(t *testing.T) {
		original := "line1\nline2\nline3\nline4\nline5\n"
		m := adequacy.Mutant{ID: "e1", Search: "line1\n", Replace: "LINEONE\n"}
		want := strings.Join([]string{
			"--- SURVIVOR e1 (lines 1-1) ---",
			"- 1 | line1",
			"+ 1 | LINEONE",
			"  2 | line2",
			"  3 | line3",
			"  4 | line4",
		}, "\n")
		if got := RenderHunk(m, original, 3); got != want {
			t.Fatalf("RenderHunk mismatch:\ngot:\n%s\n\nwant:\n%s", got, want)
		}
	})

	t.Run("file end, no trailing newline", func(t *testing.T) {
		original := "line1\nline2\nline3\nline4\nline5"
		m := adequacy.Mutant{ID: "e2", Search: "line5", Replace: "LINEFIVE"}
		want := strings.Join([]string{
			"--- SURVIVOR e2 (lines 5-5) ---",
			"  2 | line2",
			"  3 | line3",
			"  4 | line4",
			"- 5 | line5",
			"+ 5 | LINEFIVE",
		}, "\n")
		if got := RenderHunk(m, original, 3); got != want {
			t.Fatalf("RenderHunk mismatch:\ngot:\n%s\n\nwant:\n%s", got, want)
		}
	})

	t.Run("no trailing newline, mid-file change", func(t *testing.T) {
		original := "a\nb\nc\nd\ne"
		m := adequacy.Mutant{ID: "e3", Search: "c\n", Replace: "C\n"}
		want := strings.Join([]string{
			"--- SURVIVOR e3 (lines 3-3) ---",
			"  2 | b",
			"- 3 | c",
			"+ 3 | C",
			"  4 | d",
		}, "\n")
		if got := RenderHunk(m, original, 1); got != want {
			t.Fatalf("RenderHunk mismatch:\ngot:\n%s\n\nwant:\n%s", got, want)
		}
	})
}

// TestRenderHunkWholeFileMutantDiffs proves the v1 shape (no Search anchor;
// Replace is the whole mutated file) renders as a DIFF against original,
// never the file itself: unrelated unchanged lines far from the edit must
// not appear verbatim, and the actual change must show as -/+ lines.
func TestRenderHunkWholeFileMutantDiffs(t *testing.T) {
	original := "one\ntwo\nthree\nfour\nfive\nsix\nseven\n"
	m := adequacy.Mutant{ID: "w1", Replace: "one\ntwo\nTHREE\nfour\nfive\nsix\nseven\n"}

	got := RenderHunk(m, original, 1)

	if got == original || got == m.Replace {
		t.Fatalf("whole-file mutant rendered a full file, not a diff:\n%s", got)
	}
	if !strings.Contains(got, "- 3 | three") || !strings.Contains(got, "+ 3 | THREE") {
		t.Fatalf("diff missing the actual -/+ change:\n%s", got)
	}
	if strings.Contains(got, "one") || strings.Contains(got, "seven") {
		t.Fatalf("diff shows unchanged lines far from the edit (not just a context window):\n%s", got)
	}

	want := strings.Join([]string{
		"--- SURVIVOR w1 (lines 2-4) ---",
		"  ... (1 unchanged line(s) omitted) ...",
		"  2 | two",
		"- 3 | three",
		"+ 3 | THREE",
		"  4 | four",
		"  ... (3 unchanged line(s) omitted) ...",
	}, "\n")
	if got != want {
		t.Fatalf("RenderHunk mismatch:\ngot:\n%s\n\nwant:\n%s", got, want)
	}
}

// TestRenderHunkDualNumberingGrowingReplace reproduces the reviewer's
// collision: a 2-line SEARCH replaced by a 3-line REPLACE. Removed lines and
// before-context stay in ORIGINAL numbering; added lines and after-context
// must switch to MUTATED numbering (the line count the replacement actually
// produces) — otherwise the 3rd added line and the first after-context line
// both claim the same original line number.
func TestRenderHunkDualNumberingGrowingReplace(t *testing.T) {
	original := "a\nb\nc\nd\ne\nf\ng\nh\n"
	m := adequacy.Mutant{ID: "t1", Search: "c\nd\n", Replace: "C1\nC2\nC3\n"}

	want := strings.Join([]string{
		"--- SURVIVOR t1 (lines 3-5) ---",
		"  1 | a",
		"  2 | b",
		"- 3 | c",
		"- 4 | d",
		"+ 3 | C1",
		"+ 4 | C2",
		"+ 5 | C3",
		"  6 | e",
		"  7 | f",
	}, "\n")
	if got := RenderHunk(m, original, 2); got != want {
		t.Fatalf("RenderHunk mismatch:\ngot:\n%s\n\nwant:\n%s", got, want)
	}
}

// TestRenderHunkDualNumberingShrinkingReplace is the mirror case: a 3-line
// SEARCH replaced by a 1-line REPLACE. After-context numbers must shift
// BACKWARD to the mutated file's numbering, not stay pinned to the
// original's.
func TestRenderHunkDualNumberingShrinkingReplace(t *testing.T) {
	original := "a\nb\nc\nd\ne\nf\ng\nh\n"
	m := adequacy.Mutant{ID: "t2", Search: "c\nd\ne\n", Replace: "X\n"}

	want := strings.Join([]string{
		"--- SURVIVOR t2 (lines 3-3) ---",
		"  1 | a",
		"  2 | b",
		"- 3 | c",
		"- 4 | d",
		"- 5 | e",
		"+ 3 | X",
		"  4 | f",
		"  5 | g",
	}, "\n")
	if got := RenderHunk(m, original, 2); got != want {
		t.Fatalf("RenderHunk mismatch:\ngot:\n%s\n\nwant:\n%s", got, want)
	}
}

// TestRenderHunkAtFileEdgesLengthChange extends the file-edges coverage
// with a length-changing replacement that reaches the last line: after-
// context is naturally empty here regardless, but the ADDED lines and the
// header must use the mutated file's line count, not the original's.
func TestRenderHunkAtFileEdgesLengthChange(t *testing.T) {
	original := "p\nq\nr\ns\nt"
	m := adequacy.Mutant{ID: "t3", Search: "s\nt", Replace: "S1\nS2\nS3"}

	want := strings.Join([]string{
		"--- SURVIVOR t3 (lines 4-6) ---",
		"  2 | q",
		"  3 | r",
		"- 4 | s",
		"- 5 | t",
		"+ 4 | S1",
		"+ 5 | S2",
		"+ 6 | S3",
	}, "\n")
	if got := RenderHunk(m, original, 2); got != want {
		t.Fatalf("RenderHunk mismatch:\ngot:\n%s\n\nwant:\n%s", got, want)
	}
}

// TestRenderHunkWholeFileDiffDualNumbering covers the same dual-numbering
// rule on the v1 whole-file (LCS) path: a length-changing edit (one line
// becomes two) must not leave a post-change context line stamped with its
// ORIGINAL number when that collides with an added line's MUTATED number.
func TestRenderHunkWholeFileDiffDualNumbering(t *testing.T) {
	original := "one\ntwo\nthree\nfour\nfive\n"
	m := adequacy.Mutant{ID: "w2", Replace: "one\ntwo\nTHREE_A\nTHREE_B\nfour\nfive\n"}

	got := RenderHunk(m, original, 1)

	for _, want := range []string{"- 3 | three", "+ 3 | THREE_A", "+ 4 | THREE_B", "  5 | four"} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in:\n%s", want, got)
		}
	}
	// The bug this guards against: "four" kept its ORIGINAL number (4) after
	// the file grew by one line, which collided with "+ 4 | THREE_B" above —
	// two different physical lines both claiming line 4.
	if strings.Contains(got, "  4 | four") {
		t.Fatalf("post-change context line kept its stale ORIGINAL number, colliding with an added line:\n%s", got)
	}
}

// TestRenderHunkSurfacesSpanMismatch pins the "assert they agree" rule for
// m.Span vs. the freshly-computed anchor start: when a producer's recorded
// Span disagrees with what HunkSpan(original, m.Search) computes fresh
// against the ACTUAL source being rendered, that is a caller bug (the
// mutant is being rendered against the wrong bytes) and RenderHunk must say
// so loudly in the output, not silently trust the stale Span or silently
// drop the survivor.
func TestRenderHunkSurfacesSpanMismatch(t *testing.T) {
	original := "a\nb\nc\nd\ne\n"
	m := adequacy.Mutant{
		ID:      "m9",
		Search:  "c\n",
		Replace: "C\n",
		// The real anchor is line 3; a stale/wrong recorded Span claims 1.
		Span: golang.LineRange{Start: 1, End: 1},
	}
	got := RenderHunk(m, original, 1)
	if !strings.Contains(got, "SPAN MISMATCH") {
		t.Fatalf("expected a surfaced span mismatch, got:\n%s", got)
	}
	// Still a working diff computed from the trustworthy, freshly-derived
	// anchor — not a crash, not a dropped survivor.
	if !strings.Contains(got, "- 3 | c") || !strings.Contains(got, "+ 3 | C") {
		t.Fatalf("mismatch report must not come at the cost of a correct diff:\n%s", got)
	}
}
