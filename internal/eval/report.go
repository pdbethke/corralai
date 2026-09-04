// SPDX-License-Identifier: Elastic-2.0
package eval

import (
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

type TargetReport struct {
	ID               string
	ExpectedAdequacy string
	// Runs is every run attempted for this target. GradedRuns is the subset
	// that actually measured something; Ungraded = Runs - GradedRuns. The
	// means below are over GradedRuns ONLY: a run whose baseline failed or
	// whose check command never read the file reports a meaningless 0, and
	// averaging that in would let an environment failure declare a target
	// miscalibrated. Ungraded is reported separately so the exclusion is
	// VISIBLE rather than silently dropped.
	Runs             int
	GradedRuns       int
	Ungraded         int
	MeanKillRate     float64
	MeanSurvivors    float64
	MeanMutantsTotal float64
	// MeanProvenMissed is over WriterGradedRuns — the runs whose writer
	// half measured anything — never over runs whose 0 means "not tried".
	MeanProvenMissed float64
	WriterGradedRuns int
	Calibrated       bool
	Note             string
}

// NotRun lists the manifest targets no result named, so a report over a
// subset can never read as a report over the corpus.
func NotRun(m Manifest, results []RunResult) []string {
	seen := map[string]bool{}
	for _, r := range results {
		seen[r.TargetID] = true
	}
	var out []string
	for _, t := range m.Targets {
		if !seen[t.ID] {
			out = append(out, t.ID)
		}
	}
	return out
}

// thoroughSurvivorTolerance: a thorough target may occasionally leave a stray
// survivor (LLM mutant variance); above this mean it's over-sensitive/miscalibrated.
const thoroughSurvivorTolerance = 0.5

func Report(m Manifest, results []RunResult) []TargetReport {
	adeq := map[string]Target{}
	for _, t := range m.Targets {
		adeq[t.ID] = t
	}
	agg := map[string]*TargetReport{}
	unmatched := map[string]bool{}
	order := []string{}
	for _, r := range results {
		rep, ok := agg[r.TargetID]
		if !ok {
			t, found := adeq[r.TargetID]
			if !found {
				unmatched[r.TargetID] = true
			}
			rep = &TargetReport{ID: r.TargetID, ExpectedAdequacy: t.ExpectedAdequacy}
			agg[r.TargetID] = rep
			order = append(order, r.TargetID)
		}
		rep.Runs++
		if !r.Graded() {
			// Could not grade: no kill rate, no survivors, no mutant tally.
			// Account it, never average it.
			rep.Ungraded++
			continue
		}
		rep.GradedRuns++
		rep.MeanKillRate += r.DevKillRate
		rep.MeanSurvivors += float64(r.Survivors)
		rep.MeanMutantsTotal += float64(r.MutantsTotal)
		if r.WriterGraded() {
			rep.WriterGradedRuns++
			rep.MeanProvenMissed += float64(r.ProvenMissed)
		}
	}
	var out []TargetReport
	for _, id := range order {
		rep := agg[id]
		if rep.GradedRuns > 0 {
			rep.MeanKillRate /= float64(rep.GradedRuns)
			rep.MeanSurvivors /= float64(rep.GradedRuns)
			rep.MeanMutantsTotal /= float64(rep.GradedRuns)
		}
		if rep.WriterGradedRuns > 0 {
			rep.MeanProvenMissed /= float64(rep.WriterGradedRuns)
		}
		t := adeq[id]
		if rep.GradedRuns == 0 {
			// Every run for this target could not be graded. The means are
			// zero because nothing was measured, not because the pool
			// under-performed — calibration is unvalidatable, and saying so is
			// the only honest verdict. Checked BEFORE the adequacy switch so a
			// zeroed mean can never be read as evidence either way.
			rep.Note = fmt.Sprintf("all %d run(s) could not be graded (failed baseline or a check command that never reads the file) — nothing was measured; cannot validate calibration", rep.Runs)
		} else if unmatched[id] {
			// The run reported a target that doesn't exist in this manifest.
			// Never let this fall through to the default "unknown adequacy"
			// branch below, which would silently mark it Calibrated=true.
			rep.Note = fmt.Sprintf("target %q not in manifest — cannot validate calibration", id)
		} else {
			switch rep.ExpectedAdequacy {
			case "thorough":
				if rep.MeanMutantsTotal == 0 {
					rep.Note = "no mutants generated — the target was not actually exercised; cannot validate"
				} else if rep.MeanSurvivors <= thoroughSurvivorTolerance {
					rep.Calibrated = true
				} else {
					rep.Note = fmt.Sprintf("thorough target has mean %.2f survivors — pool is inventing gaps (over-sensitive)", rep.MeanSurvivors)
				}
			case "gappy":
				switch {
				case t.ExpectedSurvivors < 1:
					// `MeanSurvivors >= 0` is always true; a gappy target
					// that does not say how many gaps it has cannot be
					// validated, and used to read as calibrated by that
					// arithmetic.
					rep.Note = "gappy target declares no expected_survivors — nothing to validate against"
				case rep.MeanSurvivors < float64(t.ExpectedSurvivors):
					rep.Note = fmt.Sprintf("gappy target has mean %.2f survivors (< expected %d) — pool MISSED a known gap (under-sensitive)", rep.MeanSurvivors, t.ExpectedSurvivors)
				case rep.WriterGradedRuns == 0:
					// The dev-suite half found the gap; the writer half —
					// the column the scorecard's headline rests on — never
					// graded in any run. That is not calibration of the
					// signal being published.
					rep.Note = fmt.Sprintf("gappy target's known gap was found by the dev pass, but the writer half never graded in any of %d run(s) — the proven column is unvalidated", rep.GradedRuns)
				case rep.MeanProvenMissed < 1:
					rep.Note = fmt.Sprintf("gappy target's known gap was never proven catchable: mean %.2f proven over %d writer-graded run(s) — the proven column, which the scorecard's headline rests on, does not confirm this gap", rep.MeanProvenMissed, rep.WriterGradedRuns)
				default:
					rep.Calibrated = true
				}
			default:
				rep.Calibrated = true // "unknown" adequacy isn't a calibration target
			}
		}
		out = append(out, *rep)
	}
	return out
}

func WriteReport(out io.Writer, reps []TargetReport) { WriteReportWithScope(out, reps, nil) }

// WriteReportWithScope is WriteReport plus the manifest targets that were
// NOT run, so the headline says "N of M target(s)" rather than letting a
// subset read as the corpus.
func WriteReportWithScope(out io.Writer, reps []TargetReport, notRun []string) {
	bad := 0
	totalRuns := 0
	totalUngraded := 0
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	// GRADED and UNGRADED are both printed: the means are over GRADED runs
	// only, so a reader must be able to see how much of the attempted work
	// was excluded before trusting (or distrusting) the numbers next to it.
	fmt.Fprintln(tw, "TARGET\tEXPECTED\tRUNS\tGRADED\tUNGRADED\tKILL-RATE\tSURVIVORS\tMUTANTS\tPROVEN\tCALIBRATED\t")
	for _, r := range reps {
		cal := "yes"
		if !r.Calibrated {
			cal = "NO — " + r.Note
			bad++
		}
		totalRuns += r.Runs
		totalUngraded += r.Ungraded
		fmt.Fprintf(tw, "%s\t%s\t%d\t%d\t%d\t%.2f\t%.2f\t%.2f\t%.2f\t%s\t\n",
			r.ID, r.ExpectedAdequacy, r.Runs, r.GradedRuns, r.Ungraded,
			r.MeanKillRate, r.MeanSurvivors, r.MeanMutantsTotal, r.MeanProvenMissed, cal)
	}
	tw.Flush()
	if totalUngraded > 0 {
		fmt.Fprintf(out, "\n%d of %d run(s) COULD NOT BE GRADED and are excluded from every mean above (failed baseline, or a check command that never compiles or imports the audited file).\n", totalUngraded, totalRuns)
	}
	// SCOPE must always be visible: a reader must never mistake "nothing ran"
	// or "only some targets ran" for "the whole corpus passed."
	scope := fmt.Sprintf("%d target(s)", len(reps))
	if len(notRun) > 0 {
		scope = fmt.Sprintf("%d of %d target(s)", len(reps), len(reps)+len(notRun))
	}
	if len(reps) == 0 || totalRuns == 0 {
		fmt.Fprintln(out, "\nNOT EVALUATED — no runs to evaluate; this is not a pass. Do NOT treat this as CALIBRATED.")
	} else if bad == 0 {
		fmt.Fprintf(out, "\nCALIBRATED — %s over %d run(s) behave as their known adequacy predicts; the scorecard's signal is sound for this scope only.\n", scope, totalRuns)
	} else {
		fmt.Fprintf(out, "\nMISCALIBRATED — %d of %s (over %d run(s)) violated their known adequacy. Do NOT publish the scorecard until resolved.\n", bad, scope, totalRuns)
	}
	if len(notRun) > 0 {
		fmt.Fprintf(out, "NOT RUN — %d manifest target(s) have no result and are absent from every line above: %s\n", len(notRun), strings.Join(notRun, ", "))
	}
}
