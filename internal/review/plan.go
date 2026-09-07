// SPDX-License-Identifier: Elastic-2.0

package review

import (
	"sort"
	"strings"
	"time"
)

// The round planner (docs/design/adversarial-review.md, "Rounds"). Two
// mechanical rules from the week the loop was run by hand: aim at what
// the last round did not touch, and re-attack every fix batch cold. The
// planner keeps the ledger of scopes — covered, uncovered, changed since
// they were reviewed — and PROPOSES the next one. A person names it; a
// model that chose its own scope would be choosing its own exam.

// ScopeState is one scope's standing in the plan.
type ScopeState struct {
	Scope string `json:"scope"`
	Files int    `json:"files"`
	// The newest review that covered this scope (its own scope equal to
	// this one, a file inside it, or a directory above it), if any.
	Reviews      int       `json:"reviews"`
	LastReviewed time.Time `json:"last_reviewed,omitempty"`
	LastCommit   string    `json:"last_commit,omitempty"`
	LastScope    string    `json:"last_scope,omitempty"`
	// Findings across every review of the scope, by outcome.
	Held int `json:"held"`
	Fell int `json:"fell"`
	Open int `json:"open"`
	// ChangedSince is how many files under the scope changed between the
	// last review's commit and HEAD — a fix batch nobody has re-attacked.
	ChangedSince int `json:"changed_since"`
	// Why the planner ranks it where it does.
	Reason string `json:"reason"`
}

// Reviewed is what the planner needs from one review entry.
type Reviewed struct {
	Scope         string
	Commit        string
	When          time.Time
	Findings      []Finding
	Adjudications map[string]*Adjudicated // by finding id
}

// covers reports whether a review of reviewScope speaks for scope.
func covers(reviewScope, scope string) bool {
	rs, s := strings.Trim(reviewScope, "/"), strings.Trim(scope, "/")
	if rs == s || rs == "." || rs == "" {
		return true
	}
	// A file inside the scope, or a directory above it.
	return strings.HasPrefix(rs, s+"/") || strings.HasPrefix(s, rs+"/")
}

// Plan ranks scopes for the next round. scopes are the candidate scopes
// with their file counts; reviews every review entry; changed reports how
// many files under a scope changed since a commit (nil when no git).
func Plan(scopes map[string]int, reviews []Reviewed, changed func(scope, sinceCommit string) int) []ScopeState {
	var out []ScopeState
	for scope, files := range scopes {
		st := ScopeState{Scope: scope, Files: files}
		for _, r := range reviews {
			if !covers(r.Scope, scope) {
				continue
			}
			st.Reviews++
			if r.When.After(st.LastReviewed) {
				st.LastReviewed, st.LastCommit, st.LastScope = r.When, r.Commit, r.Scope
			}
			for _, f := range r.Findings {
				o := OutcomeOf(f, r.Adjudications[f.ID])
				switch {
				case !o.Known:
					st.Open++
				case o.Held:
					st.Held++
				default:
					st.Fell++
				}
			}
		}
		if st.Reviews > 0 && changed != nil && st.LastCommit != "" {
			st.ChangedSince = changed(scope, st.LastCommit)
		}
		st.Reason = reasonFor(st)
		out = append(out, st)
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if ra, rb := rank(a), rank(b); ra != rb {
			return ra < rb
		}
		switch rank(a) {
		case 0: // never reviewed: the biggest first
			return a.Files > b.Files
		case 1: // changed since: the most changed first
			return a.ChangedSince > b.ChangedSince
		}
		if !a.LastReviewed.Equal(b.LastReviewed) {
			return a.LastReviewed.Before(b.LastReviewed) // the stalest first
		}
		return a.Scope < b.Scope
	})
	return out
}

// rank is the planner's order: never reviewed, then changed since its
// review (a fix batch to re-attack), then reviewed and unchanged, stalest
// first.
func rank(s ScopeState) int {
	switch {
	case s.Reviews == 0:
		return 0
	case s.ChangedSince > 0:
		return 1
	}
	return 2
}

func reasonFor(s ScopeState) string {
	switch {
	case s.Reviews == 0:
		return "never reviewed"
	case s.ChangedSince > 0 && s.Held > 0:
		return "changed since its review, with findings that held — a fix batch nobody has re-attacked"
	case s.ChangedSince > 0:
		return "changed since its review"
	case s.Open > 0:
		return "reviewed; findings awaiting a person's verdict"
	}
	return "reviewed and unchanged since"
}
