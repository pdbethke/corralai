// SPDX-License-Identifier: Elastic-2.0

package review

import (
	"slices"
	"strings"
	"time"
)

// Churn answers the question the review loop raises about itself: does it
// drain the code of defects, or do the fixes for one round's findings make
// the next round's? For every finding, git blame names the commit that last
// wrote its line, at the commit the review saw. If the line was written after
// the previous review of that file, the finding is CHURN — made since that
// review looked; if it was already there, DRAIN — an old defect, finally
// found. A file no earlier review covered is a FIRST LOOK, drain by
// necessity. This is the method of docs/design/adversarial-review.md ("Is the
// loop converging?"), computed from the ledger instead of by hand, with one
// difference stated rather than hidden: "written since the previous review"
// counts every change in between, not only the commits that fixed its
// findings, because nothing on the record says which commits those were.
//
// A finding that FELL (refuted by a person, or by execution — OutcomeOf) is
// no defect and is not classified; one that cannot be placed (no file:line,
// a blame that fails) is UNKNOWN with the reason, never guessed.

// Origins.
const (
	OriginFirstLook = "first-look"
	OriginDrain     = "drain"
	OriginChurn     = "churn"
	OriginUnknown   = "unknown"
)

// FindingOrigin is one finding's place in the churn count.
type FindingOrigin struct {
	ID     string `json:"id"`
	File   string `json:"file,omitempty"`
	Line   int    `json:"line,omitempty"`
	Origin string `json:"origin"`
	// Blamed is the commit that last wrote the line, when blame ran.
	Blamed string `json:"blamed,omitempty"`
	// Held is whether the finding held (by a person or by execution);
	// false with no outcome yet.
	Held   bool   `json:"held"`
	Reason string `json:"reason,omitempty"`
}

// Round is one review's churn count.
type Round struct {
	Scope  string    `json:"scope"`
	Commit string    `json:"commit"`
	When   time.Time `json:"when"`
	// Since is the commit of the review this round is measured against —
	// the newest earlier review covering the round's scope whose commit is
	// an ancestor of this one; "" when there was none.
	Since     string          `json:"since,omitempty"`
	FirstLook int             `json:"first_look"`
	Drain     int             `json:"drain"`
	Churn     int             `json:"churn"`
	Unknown   int             `json:"unknown"`
	Fell      int             `json:"fell"`
	Findings  []FindingOrigin `json:"findings"`
}

// Churn classifies every review's findings, oldest review first. blame names
// the commit that last wrote file:line as of commit; isAncestor reports
// whether a is an ancestor of b (a commit is its own ancestor).
func Churn(reviews []Reviewed, blame func(commit, file string, line int) (string, error), isAncestor func(a, b string) bool) []Round {
	sorted := slices.Clone(reviews)
	slices.SortStableFunc(sorted, func(a, b Reviewed) int { return a.When.Compare(b.When) })
	var rounds []Round
	for i, r := range sorted {
		round := Round{Scope: r.Scope, Commit: r.Commit, When: r.When}
		if p, ok := previousReview(sorted[:i], r.Scope, r.Commit, isAncestor); ok {
			round.Since = p.Commit
		}
		for _, f := range r.Findings {
			out := OutcomeOf(f, r.Adjudications[f.ID])
			if out.Known && !out.Held {
				round.Fell++
				continue
			}
			fo := FindingOrigin{ID: f.ID, File: f.File, Line: f.Line, Held: out.Known && out.Held}
			classify(&fo, sorted[:i], r.Commit, blame, isAncestor)
			switch fo.Origin {
			case OriginFirstLook:
				round.FirstLook++
			case OriginDrain:
				round.Drain++
			case OriginChurn:
				round.Churn++
			default:
				round.Unknown++
			}
			round.Findings = append(round.Findings, fo)
		}
		rounds = append(rounds, round)
	}
	return rounds
}

// classify sets one finding's origin against the newest earlier review that
// covered its file.
func classify(fo *FindingOrigin, earlier []Reviewed, commit string, blame func(string, string, int) (string, error), isAncestor func(a, b string) bool) {
	file := strings.Trim(strings.TrimSpace(fo.File), "/")
	if file == "" || fo.Line <= 0 {
		fo.Origin, fo.Reason = OriginUnknown, "the finding names no file:line"
		return
	}
	prev, ok := previousReview(earlier, file, commit, isAncestor)
	if !ok {
		fo.Origin = OriginFirstLook
		return
	}
	c, err := blame(commit, file, fo.Line)
	if err != nil {
		fo.Origin, fo.Reason = OriginUnknown, "git blame: "+err.Error()
		return
	}
	fo.Blamed = c
	if isAncestor(c, prev.Commit) {
		fo.Origin = OriginDrain
		return
	}
	fo.Origin, fo.Reason = OriginChurn, "written after the review at "+short(prev.Commit)
}

// previousReview is the newest of earlier whose scope covers path and whose
// commit is an ancestor of commit. A review of another branch says nothing
// about what this one wrote.
func previousReview(earlier []Reviewed, path, commit string, isAncestor func(a, b string) bool) (Reviewed, bool) {
	for i := len(earlier) - 1; i >= 0; i-- {
		p := earlier[i]
		if covers(p.Scope, path) && isAncestor(p.Commit, commit) {
			return p, true
		}
	}
	return Reviewed{}, false
}

func short(c string) string {
	if len(c) > 12 {
		return c[:12]
	}
	return c
}
