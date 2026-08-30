// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"strings"
	"testing"

	"github.com/pdbethke/corralai/internal/adequacy"
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
