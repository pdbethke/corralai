// SPDX-License-Identifier: Elastic-2.0

package review

import (
	"errors"
	"testing"
	"time"
)

// A fake history: c1 → c2 → c3. blame answers which commit last wrote a line.
func churnGit(blame map[string]string) (func(commit, file string, line int) (string, error), func(a, b string) bool) {
	order := map[string]int{"c1": 1, "c2": 2, "c3": 3}
	b := func(commit, file string, line int) (string, error) {
		if c, ok := blame[commit+":"+file+":"+string(rune('0'+line))]; ok {
			return c, nil
		}
		return "", errors.New("no such line")
	}
	anc := func(a, d string) bool { return order[a] > 0 && order[a] <= order[d] }
	return b, anc
}

func held(id, file string, line int) Finding {
	zero := 0
	return Finding{ID: id, File: file, Line: line, Declared: TierReproduced, Tier: TierReproduced, ExitCode: &zero}
}

// The field note's method, from the record: a finding whose line was last
// written after the previous review of its file is churn; one already there
// is drain; a file no earlier review covered is a first look; a finding that
// fell is no defect and is not counted; one that cannot be placed is unknown,
// with the reason.
func TestChurnClassifiesEachFindingByWhenItsLineWasWritten(t *testing.T) {
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	refutedByPerson := held("R3", "pkg/a.go", 3)
	reviews := []Reviewed{
		{Scope: "pkg", Commit: "c1", When: t0, Findings: []Finding{held("R1", "pkg/a.go", 1)}},
		{Scope: "pkg", Commit: "c3", When: t0.Add(48 * time.Hour), Findings: []Finding{
			held("R1", "pkg/a.go", 1), // written at c1, before the last review: drain
			held("R2", "pkg/a.go", 2), // written at c2, after it: churn
			refutedByPerson,           // refuted by a person: not counted
			held("R4", "other/b.go", 1),
			{ID: "R5", Claim: "no place", Declared: TierHypothesis, Tier: TierHypothesis},
			held("R6", "pkg/a.go", 9), // blame fails
		}, Adjudications: map[string]*Adjudicated{"R3": {Verdict: "refuted", By: "pdb"}}},
	}
	blame, anc := churnGit(map[string]string{
		"c1:pkg/a.go:1":   "c1",
		"c3:pkg/a.go:1":   "c1",
		"c3:pkg/a.go:2":   "c2",
		"c3:other/b.go:1": "c3",
	})
	rounds := Churn(reviews, blame, anc)
	if len(rounds) != 2 {
		t.Fatalf("%d rounds", len(rounds))
	}
	first, second := rounds[0], rounds[1]
	if first.FirstLook != 1 || first.Drain+first.Churn != 0 {
		t.Errorf("round one: %+v", first)
	}
	if second.Since != "c1" {
		t.Errorf("round two's previous review = %q, want c1", second.Since)
	}
	want := map[string]string{"R1": OriginDrain, "R2": OriginChurn, "R4": OriginFirstLook, "R5": OriginUnknown, "R6": OriginUnknown}
	for _, f := range second.Findings {
		if f.Origin != want[f.ID] {
			t.Errorf("%s: %s (%s), want %s", f.ID, f.Origin, f.Reason, want[f.ID])
		}
		if f.ID == "R3" {
			t.Errorf("a refuted finding was classified: %+v", f)
		}
	}
	if second.Drain != 1 || second.Churn != 1 || second.FirstLook != 1 || second.Unknown != 2 || second.Fell != 1 {
		t.Errorf("round two counts: %+v", second)
	}
	if second.Findings[1].Blamed != "c2" {
		t.Errorf("the churn finding should name the commit that wrote its line: %+v", second.Findings[1])
	}
}

// The previous review must be an ANCESTOR of this one's commit: a review of a
// sibling branch says nothing about what this branch's fixes wrote. And a
// review at the same commit is a second look, not a later round.
func TestChurnComparesOnlyAgainstAnAncestorReview(t *testing.T) {
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	reviews := []Reviewed{
		{Scope: "pkg", Commit: "x9", When: t0, Findings: []Finding{held("R1", "pkg/a.go", 1)}}, // not in the history
		{Scope: "pkg", Commit: "c2", When: t0.Add(time.Hour), Findings: []Finding{held("R1", "pkg/a.go", 1)}},
		{Scope: "pkg", Commit: "c2", When: t0.Add(2 * time.Hour), Findings: []Finding{held("R1", "pkg/a.go", 1)}},
	}
	blame, anc := churnGit(map[string]string{"c2:pkg/a.go:1": "c2"})
	rounds := Churn(reviews, blame, anc)
	if got := rounds[1].Findings[0].Origin; got != OriginFirstLook {
		t.Errorf("a review on an unrelated commit was taken as the previous one: %s", got)
	}
	if got := rounds[2].Findings[0].Origin; got != OriginDrain {
		t.Errorf("a second look at the same commit: %s, want drain (nothing was written since)", got)
	}
}
