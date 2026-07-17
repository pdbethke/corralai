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
	Runs             int
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
		rep.MeanKillRate += r.DevKillRate
		rep.MeanSurvivors += float64(r.Survivors)
		rep.MeanMutantsTotal += float64(r.MutantsTotal)
		rep.MeanProvenMissed += float64(r.ProvenMissed)
	}
	var out []TargetReport
	for _, id := range order {
		rep := agg[id]
		rep.MeanKillRate /= float64(rep.Runs)
		rep.MeanSurvivors /= float64(rep.Runs)
		rep.MeanMutantsTotal /= float64(rep.Runs)
		rep.MeanProvenMissed /= float64(rep.Runs)
		t := adeq[id]
		if unmatched[id] {
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
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "TARGET\tEXPECTED\tRUNS\tKILL-RATE\tSURVIVORS\tMUTANTS\tPROVEN\tCALIBRATED\t")
	for _, r := range reps {
		cal := "yes"
		if !r.Calibrated {
			cal = "NO — " + r.Note
			bad++
		}
		totalRuns += r.Runs
		fmt.Fprintf(tw, "%s\t%s\t%d\t%.2f\t%.2f\t%.2f\t%.2f\t%s\t\n",
			r.ID, r.ExpectedAdequacy, r.Runs, r.MeanKillRate, r.MeanSurvivors, r.MeanMutantsTotal, r.MeanProvenMissed, cal)
	}
	tw.Flush()
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
