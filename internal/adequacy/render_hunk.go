// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"fmt"
	"strings"
)

// RenderHunk renders one mutant as a unified-diff block sized for a prompt:
// a header naming the survivor and the ORIGINAL lines it spans, `context`
// lines of unchanged code before and after, the removed lines prefixed "-"
// and the added lines prefixed "+", every line numbered
// ("  12 | ...", "- 13 | ...", "+ 13 | ..."). It exists so a prompt that
// must show a model what a mutant DID never needs the whole mutated file to
// do it — see Mutant's doc comment for the cost that materialising the
// whole file was measured to have (0.5M tokens on one writer seat).
//
// A whole-file (v1) mutant (m.IsWholeFile()) has no anchor to hunk against —
// Replace IS the whole mutated file — so it renders as a line diff of
// Replace against original instead: a simple LCS (correctness over
// prettiness), never the file verbatim.
//
// RenderHunk never errors and never dumps a file: an anchor that does not
// occur in original (which Apply would reject) renders a degraded,
// plainly-labelled block instead of panicking or silently vanishing — see
// renderUnanchoredHunk.
func RenderHunk(m Mutant, original string, context int) string {
	if context < 0 {
		context = 0
	}
	if m.IsWholeFile() {
		return renderWholeFileDiff(m, original, context)
	}
	return renderAnchoredHunk(m, original, context)
}

// splitLines splits s into its content lines, dropping the single trailing
// empty element strings.Split produces when s ends with "\n" — so line N in
// the result is exactly line N by the counting HunkSpan uses.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// renderAnchoredHunk renders a hunk-native mutant: Search anchors a known
// span of original, so the removed/added lines and their surrounding
// context are computed directly, with no diffing needed.
func renderAnchoredHunk(m Mutant, original string, context int) string {
	span := HunkSpan(original, m.Search)
	if span.IsZero() {
		// The anchor is not IN original (a mutant graded against different
		// bytes than it was cut from, or a hand-built fixture). Apply would
		// refuse this; RenderHunk instead says so plainly rather than
		// indexing into lines that do not exist.
		return renderUnanchoredHunk(m)
	}
	start, end := span.Start, span.End
	origLines := splitLines(original)
	replaceLines := splitLines(m.Replace)

	var b strings.Builder
	fmt.Fprintf(&b, "--- SURVIVOR %s (lines %d-%d) ---", m.ID, start, end)

	beforeFrom := start - context
	if beforeFrom < 1 {
		beforeFrom = 1
	}
	for n := beforeFrom; n < start; n++ {
		fmt.Fprintf(&b, "\n  %d | %s", n, origLines[n-1])
	}
	for n := start; n <= end; n++ {
		fmt.Fprintf(&b, "\n- %d | %s", n, origLines[n-1])
	}
	for i, line := range replaceLines {
		fmt.Fprintf(&b, "\n+ %d | %s", start+i, line)
	}
	afterTo := end + context
	if afterTo > len(origLines) {
		afterTo = len(origLines)
	}
	for n := end + 1; n <= afterTo; n++ {
		fmt.Fprintf(&b, "\n  %d | %s", n, origLines[n-1])
	}
	return b.String()
}

// renderUnanchoredHunk is the graceful degradation for a mutant whose
// SEARCH cannot be located in original: it names the survivor and shows the
// raw removed/added lines with no original-line numbers to attach them to,
// rather than crashing or vanishing the survivor from the prompt.
func renderUnanchoredHunk(m Mutant) string {
	var b strings.Builder
	fmt.Fprintf(&b, "--- SURVIVOR %s (anchor not found in this source) ---", m.ID)
	for _, line := range splitLines(m.Search) {
		fmt.Fprintf(&b, "\n- %s", line)
	}
	for _, line := range splitLines(m.Replace) {
		fmt.Fprintf(&b, "\n+ %s", line)
	}
	return b.String()
}

// diffOp is one line of an LCS-computed line diff: 'e' (equal, present in
// both), 'd' (present only in the original), or 'i' (present only in the
// new text).
type diffOp struct {
	kind byte
	line string
}

// lcsDiff computes a minimal line-level edit script turning a into b via
// the classic O(len(a)*len(b)) dynamic-programming LCS. Whole-file (v1)
// mutants are the only caller, and those are read back from recorded
// documents rather than freshly generated at prompt-render scale, so the
// quadratic cost is acceptable — see RenderHunk's doc comment.
func lcsDiff(a, b []string) []diffOp {
	n, m := len(a), len(b)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			switch {
			case a[i] == b[j]:
				dp[i][j] = dp[i+1][j+1] + 1
			case dp[i+1][j] >= dp[i][j+1]:
				dp[i][j] = dp[i+1][j]
			default:
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	var ops []diffOp
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			ops = append(ops, diffOp{'e', a[i]})
			i++
			j++
		case dp[i+1][j] >= dp[i][j+1]:
			ops = append(ops, diffOp{'d', a[i]})
			i++
		default:
			ops = append(ops, diffOp{'i', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		ops = append(ops, diffOp{'d', a[i]})
	}
	for ; j < m; j++ {
		ops = append(ops, diffOp{'i', b[j]})
	}
	return ops
}

// numberedOp attaches the 1-based original/new line number an lcsDiff op
// corresponds to (0 when the op has no line on that side: an insert has no
// origN, a delete has no newN).
type numberedOp struct {
	diffOp
	origN, newN int
}

// renderWholeFileDiff renders a v1 whole-file mutant as an LCS line diff of
// Replace against original, showing only the changed lines plus `context`
// lines of surrounding equal lines — an omission marker stands in for any
// run of equal lines beyond that window, so this is still a diff and never
// the file, however small the actual edit turns out to be.
func renderWholeFileDiff(m Mutant, original string, context int) string {
	origLines := splitLines(original)
	newLines := splitLines(m.Replace)
	ops := lcsDiff(origLines, newLines)

	numbered := make([]numberedOp, len(ops))
	origN, newN := 0, 0
	for i, op := range ops {
		switch op.kind {
		case 'e':
			origN++
			newN++
			numbered[i] = numberedOp{op, origN, newN}
		case 'd':
			origN++
			numbered[i] = numberedOp{op, origN, 0}
		case 'i':
			newN++
			numbered[i] = numberedOp{op, 0, newN}
		}
	}

	show := make([]bool, len(numbered))
	for i, op := range numbered {
		if op.kind != 'e' {
			show[i] = true
		}
	}
	for i, op := range numbered {
		if op.kind == 'e' {
			continue
		}
		for d := 1; d <= context; d++ {
			if i-d >= 0 {
				show[i-d] = true
			}
			if i+d < len(numbered) {
				show[i+d] = true
			}
		}
	}

	firstOrig, lastOrig := 0, 0
	for i, op := range numbered {
		if !show[i] || op.kind == 'i' {
			continue
		}
		if firstOrig == 0 {
			firstOrig = op.origN
		}
		lastOrig = op.origN
	}

	var b strings.Builder
	fmt.Fprintf(&b, "--- SURVIVOR %s (lines %d-%d) ---", m.ID, firstOrig, lastOrig)
	skipped := 0
	for i, op := range numbered {
		if !show[i] {
			skipped++
			continue
		}
		if skipped > 0 {
			fmt.Fprintf(&b, "\n  ... (%d unchanged line(s) omitted) ...", skipped)
			skipped = 0
		}
		switch op.kind {
		case 'e':
			fmt.Fprintf(&b, "\n  %d | %s", op.origN, op.line)
		case 'd':
			fmt.Fprintf(&b, "\n- %d | %s", op.origN, op.line)
		case 'i':
			fmt.Fprintf(&b, "\n+ %d | %s", op.newN, op.line)
		}
	}
	if skipped > 0 {
		fmt.Fprintf(&b, "\n  ... (%d unchanged line(s) omitted) ...", skipped)
	}
	return b.String()
}
