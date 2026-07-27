// SPDX-License-Identifier: Elastic-2.0
package eval

import (
	"fmt"
	"io"
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
	MeanProvenMissed float64
	Calibrated       bool
	Note             string
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
		rep.MeanProvenMissed += float64(r.ProvenMissed)
	}
	var out []TargetReport
	for _, id := range order {
		rep := agg[id]
		if rep.GradedRuns > 0 {
			rep.MeanKillRate /= float64(rep.GradedRuns)
			rep.MeanSurvivors /= float64(rep.GradedRuns)
			rep.MeanMutantsTotal /= float64(rep.GradedRuns)
			rep.MeanProvenMissed /= float64(rep.GradedRuns)
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
				if rep.MeanSurvivors >= float64(t.ExpectedSurvivors) {
					rep.Calibrated = true
				} else {
					rep.Note = fmt.Sprintf("gappy target has mean %.2f survivors (< expected %d) — pool MISSED a known gap (under-sensitive)", rep.MeanSurvivors, t.ExpectedSurvivors)
				}
			default:
				rep.Calibrated = true // "unknown" adequacy isn't a calibration target
			}
		}
		out = append(out, *rep)
	}
	return out
}

func WriteReport(out io.Writer, reps []TargetReport) {
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
	if len(reps) == 0 || totalRuns == 0 {
		fmt.Fprintln(out, "\nNOT EVALUATED — no runs to evaluate; this is not a pass. Do NOT treat this as CALIBRATED.")
	} else if bad == 0 {
		fmt.Fprintf(out, "\nCALIBRATED — %d target(s) over %d run(s) behave as their known adequacy predicts; the scorecard's signal is sound for this scope only.\n", len(reps), totalRuns)
	} else {
		fmt.Fprintf(out, "\nMISCALIBRATED — %d of %d target(s) (over %d run(s)) violated their known adequacy. Do NOT publish the scorecard until resolved.\n", bad, len(reps), totalRuns)
	}
}
