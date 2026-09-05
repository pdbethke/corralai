// SPDX-License-Identifier: Elastic-2.0

package adequacy

import (
	"regexp"
	"strings"
)

// The SHAPE of a mutant: what kind of fault a hunk plants, named from the
// hunk itself and never from the model that wrote it. A generator asked to
// label its own mutation would be grading its own work; the SEARCH → REPLACE
// diff is the ground truth, and this classifier reads only that.
//
// Why it exists: "prone to" is only measurable with a category on every
// row. With shape and generator model recorded per mutant, the warehouse
// can answer which shapes each generator plants, which shapes a suite lets
// through (its blind spots by KIND, not just by line), which shapes each
// writer proves, and whether two generators differ — and the ledger prior
// can tell the next generator "this shape at this place was tried". The
// vocabulary is deliberately small and the rules deliberately dumb: a
// classifier a reader can check by eye is worth more than a clever one.
//
// Shapes are exclusive and checked in order; the first that fits wins.
const (
	ShapeConditionNegated = "condition-negated" // a comparison or boolean flipped: == → !=, < → >=, not/! added or removed, and ↔ or
	ShapeBoundaryShifted  = "boundary-shifted"  // an off-by-one: < ↔ <=, a ±1 on an index or bound, > ↔ >=
	ShapeReturnChanged    = "return-changed"    // the returned value changed
	ShapeCallRemoved      = "call-removed"      // a statement dropped: a call, an assignment, a raise/throw
	ShapeExceptionDropped = "exception-dropped" // a raise/throw/panic removed or its condition widened/narrowed
	ShapeConstantChanged  = "constant-changed"  // a literal changed: a number, a string, True/False, None/nil
	ShapeBranchRemoved    = "branch-removed"    // an if/else/loop body emptied or a branch made unconditional
	ShapeArgumentChanged  = "argument-changed"  // a call's arguments changed (swapped, dropped, altered)
	ShapeOther            = "other"
)

var (
	comparisonOp = regexp.MustCompile(`(==|!=|<=|>=|<|>|\bis not\b|\bis\b|\bnot in\b|\bin\b)`)
	boolOp       = regexp.MustCompile(`(\band\b|\bor\b|\bnot\b|&&|\|\||!)`)
	numberLit    = regexp.MustCompile(`\b\d+(\.\d+)?\b`)
	stringLit    = regexp.MustCompile(`("([^"\\]|\\.)*"|'([^'\\]|\\.)*')`)
	returnKw     = regexp.MustCompile(`\breturn\b`)
	raiseKw      = regexp.MustCompile(`\b(raise|throw|panic)\b`)
	branchKw     = regexp.MustCompile(`\b(if|elif|else|for|while|switch|case|unless)\b`)
	callExpr     = regexp.MustCompile(`\b[A-Za-z_][\w.]*\s*\(`)
	keywordLit   = regexp.MustCompile(`\b(True|False|None|true|false|nil|null)\b`)
)

// ShapeOf classifies m from its hunk. A mutant with no anchor (a whole-file
// replacement, or a recorded set from before hunks existed) is "other": there
// is no diff to read.
func ShapeOf(m Mutant) string {
	return ShapeOfHunk(m.Search, m.Replace)
}

// ShapeOfHunk is ShapeOf over the raw pair, for callers holding a hunk and
// no Mutant.
func ShapeOfHunk(search, replace string) string {
	s, r := strings.TrimSpace(search), strings.TrimSpace(replace)
	if s == "" {
		return ShapeOther
	}
	// Reduce both sides to the lines that actually differ, so a hunk that
	// anchors on three lines and changes one is judged on the one.
	sLines, rLines, prefix := diffLines(strings.Split(s, "\n"), strings.Split(r, "\n"))
	sd, rd := strings.Join(sLines, "\n"), strings.Join(rLines, "\n")
	// The line above the change, when the hunk anchored on it: the branch
	// whose body was emptied lives there, not on the emptied lines.
	above := ""
	if len(prefix) > 0 {
		above = strings.TrimSpace(prefix[len(prefix)-1])
	}
	opensBranch := branchKw.MatchString(above) && (strings.HasSuffix(above, ":") || strings.HasSuffix(above, "{"))

	switch {
	case rd == "" && len(sLines) > 0 && raiseKw.MatchString(sd):
		return ShapeExceptionDropped
	case raiseKw.MatchString(sd) != raiseKw.MatchString(rd):
		return ShapeExceptionDropped
	case rd == "" || strings.TrimSpace(rd) == "pass":
		if branchKw.MatchString(sd) || opensBranch {
			return ShapeBranchRemoved
		}
		return ShapeCallRemoved
	case branchKw.MatchString(sd) && !branchKw.MatchString(rd):
		return ShapeBranchRemoved
	case boundaryShift(sd, rd):
		return ShapeBoundaryShifted
	case negated(sd, rd):
		return ShapeConditionNegated
	case literalOnlyChange(sd, rd):
		// Before return/argument: `return request("get", …)` → `"post"` is a
		// constant change that happens to sit in a return and a call, and
		// the most specific name is the useful one.
		return ShapeConstantChanged
	case returnKw.MatchString(sd) && returnKw.MatchString(rd) && sd != rd:
		return ShapeReturnChanged
	case callExpr.MatchString(sd) && callExpr.MatchString(rd) && sameCallees(sd, rd):
		return ShapeArgumentChanged
	case !callExpr.MatchString(sd) && !callExpr.MatchString(rd) && sameCallees(s, r):
		// The changed line is inside a call whose name sits on an anchor
		// line above it (a multi-line argument list): the same call, with
		// different arguments.
		return ShapeArgumentChanged
	case callExpr.MatchString(sd) && !callExpr.MatchString(rd):
		return ShapeCallRemoved
	}
	return ShapeOther
}

// diffLines drops the common prefix and suffix lines of a and b, returning
// the differing lines of each and the common prefix that was dropped.
func diffLines(a, b []string) (da, db, prefix []string) {
	for len(a) > 0 && len(b) > 0 && strings.TrimSpace(a[0]) == strings.TrimSpace(b[0]) {
		prefix = append(prefix, a[0])
		a, b = a[1:], b[1:]
	}
	for len(a) > 0 && len(b) > 0 && strings.TrimSpace(a[len(a)-1]) == strings.TrimSpace(b[len(b)-1]) {
		a, b = a[:len(a)-1], b[:len(b)-1]
	}
	return a, b, prefix
}

// boundaryShift: the same comparison with strictness flipped (< ↔ <=,
// > ↔ >=), or a ±1 introduced or removed on a bound.
func boundaryShift(s, r string) bool {
	so, ro := comparisonOp.FindAllString(s, -1), comparisonOp.FindAllString(r, -1)
	if len(so) == len(ro) && len(so) > 0 {
		for i := range so {
			a, b := so[i], ro[i]
			if a == b {
				continue
			}
			switch a + b {
			case "<<=", "<=<", ">>=", ">=>":
				return true
			}
			return false
		}
	}
	strip := func(x string) string {
		return strings.NewReplacer(" + 1", "", " - 1", "", "+1", "", "-1", "").Replace(x)
	}
	return s != r && strip(s) == strip(r) && (strings.Contains(s+r, "+ 1") || strings.Contains(s+r, "- 1") || strings.Contains(s+r, "+1") || strings.Contains(s+r, "-1"))
}

// negated: a comparison inverted (== ↔ !=, < ↔ >=, is ↔ is not, in ↔ not in),
// a `not`/`!` added or removed, or and ↔ or.
func negated(s, r string) bool {
	so, ro := comparisonOp.FindAllString(s, -1), comparisonOp.FindAllString(r, -1)
	if len(so) == len(ro) && len(so) > 0 {
		for i := range so {
			if so[i] == ro[i] {
				continue
			}
			switch so[i] + "|" + ro[i] {
			case "==|!=", "!=|==", "<|>=", ">=|<", ">|<=", "<=|>", "is|is not", "is not|is", "in|not in", "not in|in":
				return true
			}
			return false
		}
	}
	sb, rb := boolOp.FindAllString(s, -1), boolOp.FindAllString(r, -1)
	if len(sb) != len(rb) {
		// a not/! added or removed, with the rest intact
		strip := func(x string) string { return strings.NewReplacer("not ", "", "!", "").Replace(x) }
		return strip(s) == strip(r)
	}
	for i := range sb {
		switch sb[i] + "|" + rb[i] {
		case "and|or", "or|and", "&&|||", "|||&&":
			return true
		}
	}
	return false
}

// literalOnlyChange: the two sides are identical once every literal is
// masked, and at least one literal differed.
func literalOnlyChange(s, r string) bool {
	mask := func(x string) string {
		x = stringLit.ReplaceAllString(x, `"…"`)
		x = keywordLit.ReplaceAllString(x, "L")
		x = numberLit.ReplaceAllString(x, "N")
		return x
	}
	return s != r && mask(s) == mask(r)
}

// sameCallees: both sides make the same FIRST call — the statement is the
// same call with different arguments, whether an argument was replaced by
// a literal, a name, or a nested call (`verify` → `bool(verify)`).
func sameCallees(s, r string) bool {
	sc, rc := callExpr.FindAllString(s, -1), callExpr.FindAllString(r, -1)
	if len(sc) == 0 || len(rc) == 0 {
		return false
	}
	return strings.TrimSpace(sc[0]) == strings.TrimSpace(rc[0]) && s != r
}
