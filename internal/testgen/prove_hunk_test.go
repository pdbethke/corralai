// SPDX-License-Identifier: Elastic-2.0

package testgen

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/pdbethke/corralai/internal/lang"
)

// ProveHunk is the single-region proof parseMutantsDiag applies to a model's
// blocks, exported so a mutant from any other producer (a mutation tool's
// report) is held to the SAME check rather than a second copy of it.
func TestProveHunkProvesOrNamesTheRefusal(t *testing.T) {
	const original = "a := 1\nb := 2\nb := 2\nc := 3\n"
	sum := sha256.Sum256([]byte(original))
	parent := hex.EncodeToString(sum[:])

	m, why := ProveHunk(original, "x1", "c := 3\n", "c := 4\n")
	if why != HunkProven {
		t.Fatalf("a unique, changing anchor was refused: %q", why)
	}
	if m.ID != "x1" || m.ParentSHA256 != parent || m.Search != "c := 3\n" || m.Replace != "c := 4\n" {
		t.Fatalf("proven mutant carries the wrong fields: %+v", m)
	}
	if m.Span != (lang.LineRange{Start: 4, End: 4}) {
		t.Fatalf("Span = %+v, want line 4", m.Span)
	}

	// A line-bounded anchor borrows the newline ending line 1; the span is
	// the line the edit changes, line 2 (adequacy.EditSpan).
	if lb, why := ProveHunk("a\nb\nc\n", "x", "\nb\n", "\nB\n"); why != HunkProven || lb.Span != (lang.LineRange{Start: 2, End: 2}) {
		t.Fatalf("line-bounded anchor: %q, Span %+v, want line 2", why, lb.Span)
	}

	for _, tc := range []struct {
		search, replace string
		want            HunkRejection
	}{
		{"", "x", HunkNoOp},
		{"a := 1\n", "a := 1\n", HunkNoOp},
		{"z := 9\n", "z := 8\n", HunkAnchorNotFound},
		{"b := 2\n", "b := 5\n", HunkAnchorNotUnique},
	} {
		if _, why := ProveHunk(original, "x", tc.search, tc.replace); why != tc.want {
			t.Errorf("ProveHunk(%q -> %q) = %q, want %q", tc.search, tc.replace, why, tc.want)
		}
	}

	// Overlapping occurrences are two occurrences: "aa" in "aaa" starts at 0
	// and at 1. Apply refuses it as non-unique, and so does ProveHunk. (The
	// parser this was lifted from searched from i+len(search), which cannot
	// see an overlap, and so filed this as an integrity failure; the kept set
	// was the same either way.)
	for _, tc := range []struct {
		original, search, replace string
		want                      HunkRejection
	}{
		{"aaa", "aa", "ab", HunkAnchorNotUnique},
	} {
		if _, why := ProveHunk(tc.original, "x", tc.search, tc.replace); why != tc.want {
			t.Errorf("ProveHunk(%q in %q) = %q, want %q", tc.search, tc.original, why, tc.want)
		}
	}
}
