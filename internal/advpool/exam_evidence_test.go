// SPDX-License-Identifier: Elastic-2.0

package advpool

// Two ways a verdict certified on less evidence than the exam-size rule
// demands, both found by an outside review (Codex, 2026-10-17) and reproduced
// with real shell executions through adequacy.Score: seven of eight mutants
// UNMEASURED (their grading command fails on compliant code) were counted as
// graded, and eight copies of ONE edit were counted as eight observations in
// the certification interval. Each test below was a passing counterexample
// before the fix.

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/adequacy"
)

type examEvidenceScorer struct {
	fakeScorer
	jail       *adequacy.WorkspaceRunner
	unmeasured bool
	report     adequacy.Report
}

func (s *examEvidenceScorer) ScoreReport(ctx context.Context, _, _, _ string, mutants []adequacy.Mutant, _ string) (adequacy.Report, error) {
	cmd := []string{"sh", "-c", `. ./value.sh; test "$v" -eq 1`}
	var options []adequacy.ScoreOption
	if s.unmeasured {
		options = append(options, adequacy.WithCommandFor(func(m adequacy.Mutant) adequacy.MutantCommand {
			if m.ID == "m0" {
				return adequacy.MutantCommand{Cmd: cmd, Tests: 1}
			}
			return adequacy.MutantCommand{Cmd: []string{"sh", "-c", "exit 1"}, Tests: 1}
		}))
	}
	rep, err := adequacy.Score(ctx, s.jail, nil, "value.sh", "v=1\n", mutants, cmd, options...)
	s.report = rep
	return rep, err
}

func examEvidenceVerdict(t *testing.T, unmeasured bool) (*Verdict, adequacy.Report) {
	t.Helper()
	mutants := make([]adequacy.Mutant, 8)
	for i := range mutants {
		replacement := "v=2\n"
		if unmeasured {
			replacement = fmt.Sprintf("v=%d\n", i+2)
		}
		mutants[i] = adequacy.Mutant{ID: fmt.Sprintf("m%d", i), Replace: replacement}
	}
	scorer := &examEvidenceScorer{jail: adequacy.NewWorkspaceRunner(t.TempDir(), time.Second*10), unmeasured: unmeasured}
	q := newTestQueue(t)
	d, err := NewDriver(q, scorer, &fakeValidator{mutants: mutants}, decorrelatedAssign(), 0.8)
	if err != nil {
		t.Fatal(err)
	}
	const mission int64 = 601
	if err := d.StartRun(mission, testRunSpec(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := q.PromoteReady(mission); err != nil {
		t.Fatal(err)
	}
	ready := claimAllReady(t, q)
	mustComplete(t, q, ready[RoleTestCritic].ID, "No findings")
	mustComplete(t, q, ready[RoleMutantGenerator].ID, "raw mutants")
	v, err := d.Tick(context.Background(), mission)
	if err != nil || v == nil {
		t.Fatalf("Tick: verdict=%+v error=%v", v, err)
	}
	return v, scorer.report
}

// Eight copies of one fault are one observation. The kill rate keeps its
// output weighting (all eight are still killed, MutantsTotal stays 8), but
// the interval that decides whether the exam can certify is taken over
// DISTINCT edits, and one distinct edit cannot certify anything.
func TestDuplicateMutantsDoNotWidenTheCertificationSample(t *testing.T) {
	v, rep := examEvidenceVerdict(t, false)
	if rep.DuplicateMutants != 7 || v.DuplicateMutants != 7 {
		t.Fatalf("fixture: want 7 duplicates, report=%d verdict=%d", rep.DuplicateMutants, v.DuplicateMutants)
	}
	if v.MutantsTotal != 8 {
		t.Errorf("MutantsTotal = %d, want 8: duplicates keep their place in the kill rate", v.MutantsTotal)
	}
	if v.Status == StatusCertified || !v.ExamIndicative {
		t.Fatalf("eight copies of one edit certified: status=%s indicative=%v reason=%q", v.Status, v.ExamIndicative, v.IndicativeReason)
	}
	if !strings.Contains(v.IndicativeReason, "1 distinct") {
		t.Errorf("IndicativeReason = %q, want it to name the 1 distinct mutant the interval was taken over", v.IndicativeReason)
	}
}

// A mutant whose grading command fails on the compliant code was never
// measured. It is neither killed nor survived, so it cannot be in the graded
// count, the interval's sample or the "killed N of M" line.
func TestUnmeasuredMutantsAreNotCountedAsGraded(t *testing.T) {
	v, rep := examEvidenceVerdict(t, true)
	if rep.Total != 1 || len(rep.Unmeasured) != 7 || len(rep.Killed) != 1 {
		t.Fatalf("fixture: scorer should grade 1 and leave 7 unmeasured: %+v", rep)
	}
	if v.MutantsTotal != 1 {
		t.Errorf("MutantsTotal = %d, want 1 (the one mutant that was measured)", v.MutantsTotal)
	}
	if v.MutantsUnmeasured != 7 {
		t.Errorf("MutantsUnmeasured = %d, want 7", v.MutantsUnmeasured)
	}
	if v.Status == StatusCertified {
		t.Fatalf("certified on one measured mutant: %+v", v)
	}
	if got := len(v.DevKilledMutants) + len(v.DevSurvivedMutants); got != v.MutantsTotal {
		t.Errorf("evidence lists hold %d mutants, MutantsTotal says %d", got, v.MutantsTotal)
	}
}
