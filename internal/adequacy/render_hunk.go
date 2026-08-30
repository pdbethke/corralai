// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"fmt"
	"strings"
)

// RenderHunk renders one mutant as a unified-diff block sized for a prompt:
// a header naming the survivor, `context` lines of unchanged code before
// and after, the removed lines prefixed "-" and the added lines prefixed
// "+", every line numbered ("  12 | ...", "- 13 | ...", "+ 13 | ...").
// It exists so a prompt that must show a model what a mutant DID never
// needs the whole mutated file to do it — see Mutant's doc comment for the
// cost that materialising the whole file was measured to have (0.5M tokens
// on one writer seat).
//
// The numbering is a unified diff's DUAL numbering, not one shared line
// count: before-context and the removed "-" lines are numbered against
// ORIGINAL, because that is the file a test actually runs against and
// where a reviewer must go to see the mutant's anchor. The added "+" lines
// and after-context are numbered against the MUTATED file instead — the
// file Replace actually produces — because nothing constrains a hunk's
// SEARCH and REPLACE to the same line count, and reusing ORIGINAL's
// numbers for the "+" side when they differ makes two different physical
// lines claim the same number (measured: a 2-line SEARCH replaced by a
// 3-line REPLACE put the 3rd added line and the first after-context line
// both at the SEARCH span's original end+1). ON THE ANCHORED-HUNK PATH the
// header's line range ("lines <start>-<end>") names this same MUTATED
// region: <start> is shared between both files (the splice point does not
// move), but <end> is the last line REPLACE produces, which only equals the
// original span's end when the two are the same length. The whole-file (v1)
// path below carries a header too, and it is NOT the same claim — its range
// is in ORIGINAL numbering (the first and last original lines the diff
// covers), because a v1 mutant has no anchored splice point to share.
//
// A whole-file (v1) mutant (m.IsWholeFile()) has no anchor to hunk against —
// Replace IS the whole mutated file — so it renders as a line diff of
// Replace against original instead: a simple LCS (correctness over
// prettiness), never the file verbatim, following the same dual-numbering
// rule (original numbers before the first change, mutated numbers from the
// first change onward).
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
// context are computed directly, with no diffing needed — but the removed
// side and the added side are numbered against DIFFERENT files (see
// RenderHunk's doc comment), so their line counts are tracked separately.
func renderAnchoredHunk(m Mutant, original string, context int) string {
	origSpan := HunkSpan(original, m.Search)
	if origSpan.IsZero() {
		// The anchor is not IN original (a mutant graded against different
		// bytes than it was cut from, or a hand-built fixture). Apply would
		// refuse this; RenderHunk instead says so plainly rather than
		// indexing into lines that do not exist.
		return renderUnanchoredHunk(m)
	}
	start, origEnd := origSpan.Start, origSpan.End
	origLines := splitLines(original)
	replaceLines := splitLines(m.Replace)

	// The REPLACE region's end in the MUTATED file: the splice starts at
	// the same `start` (the prefix before it is byte-identical in both
	// files), but runs for len(replaceLines) lines rather than the
	// SEARCH span's line count — that is exactly where the two numberings
	// diverge. A pure deletion (no replace lines) has no "+" side to
	// number; end stays start-1 so the range reads empty rather than
	// negative or bogus.
	mutEnd := start - 1
	if len(replaceLines) > 0 {
		mutEnd = start + len(replaceLines) - 1
	}
	// m.Span, when a producer set it, is computed the same way (HunkSpan
	// against the SAME original) as origSpan here, so its Start must agree
	// with this hunk's start — the splice point is one integer, read the
	// same way regardless of which file's line numbers you're using. A
	// disagreement means m.Span was computed against DIFFERENT bytes than
	// `original` here (a caller bug: rendering a mutant against the wrong
	// source) and is surfaced in the header rather than silently trusted or
	// silently dropped.
	header := fmt.Sprintf("--- SURVIVOR %s (lines %d-%d) ---", m.ID, start, mutEnd)
	if !m.Span.IsZero() && m.Span.Start != start {
		header = fmt.Sprintf("--- SURVIVOR %s (lines %d-%d) (SPAN MISMATCH: recorded Span.Start=%d, computed=%d — rendered against the wrong source?) ---",
			m.ID, start, mutEnd, m.Span.Start, start)
	}

	var b strings.Builder
	b.WriteString(header)

	beforeFrom := start - context
	if beforeFrom < 1 {
		beforeFrom = 1
	}
	for n := beforeFrom; n < start; n++ {
		fmt.Fprintf(&b, "\n  %d | %s", n, origLines[n-1])
	}
	for n := start; n <= origEnd; n++ {
		fmt.Fprintf(&b, "\n- %d | %s", n, origLines[n-1])
	}
	for i, line := range replaceLines {
		fmt.Fprintf(&b, "\n+ %d | %s", start+i, line)
	}
	// After-context is ORIGINAL content (the lines following the SEARCH
	// span never changed) but MUTATED numbers — offset by however many
	// lines longer or shorter REPLACE made the file, so numbering stays
	// contiguous with the "+" side above instead of rewinding into it.
	delta := mutEnd - origEnd
	afterTo := origEnd + context
	if afterTo > len(origLines) {
		afterTo = len(origLines)
	}
	for n := origEnd + 1; n <= afterTo; n++ {
		fmt.Fprintf(&b, "\n  %d | %s", n+delta, origLines[n-1])
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
	// changed tracks whether any 'd'/'i' op has been emitted yet: equal
	// lines strictly before the first change are numbered against
	// ORIGINAL (they haven't diverged from it yet, so the two numberings
	// agree anyway); equal lines from the first change onward are
	// numbered against MUTATED, the same dual-numbering rule the anchored
	// hunk uses, so a post-change context line never re-claims a line
	// number an added line already used (the bug this fixes: a whole-file
	// edit that grows the file left a later equal line stamped with its
	// stale original number, colliding with an added line at that number).
	changed := false
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
			n := op.origN
			if changed {
				n = op.newN
			}
			fmt.Fprintf(&b, "\n  %d | %s", n, op.line)
		case 'd':
			changed = true
			fmt.Fprintf(&b, "\n- %d | %s", op.origN, op.line)
		case 'i':
			changed = true
			fmt.Fprintf(&b, "\n+ %d | %s", op.newN, op.line)
		}
	}
	if skipped > 0 {
		fmt.Fprintf(&b, "\n  ... (%d unchanged line(s) omitted) ...", skipped)
	}
	return b.String()
}
