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
	order := []string{}
	for _, r := range results {
		rep, ok := agg[r.TargetID]
		if !ok {
			t := adeq[r.TargetID]
			rep = &TargetReport{ID: r.TargetID, ExpectedAdequacy: t.ExpectedAdequacy}
			agg[r.TargetID] = rep
			order = append(order, r.TargetID)
		}
		rep.Runs++
		rep.MeanKillRate += r.DevKillRate
		rep.MeanSurvivors += float64(r.Survivors)
		rep.MeanProvenMissed += float64(r.ProvenMissed)
	}
	var out []TargetReport
	for _, id := range order {
		rep := agg[id]
		if rep.Runs > 0 {
			rep.MeanKillRate /= float64(rep.Runs)
			rep.MeanSurvivors /= float64(rep.Runs)
			rep.MeanProvenMissed /= float64(rep.Runs)
		}
		t := adeq[id]
		switch rep.ExpectedAdequacy {
		case "thorough":
			if rep.MeanSurvivors <= thoroughSurvivorTolerance {
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
		out = append(out, *rep)
	}
	return out
}

func WriteReport(out io.Writer, reps []TargetReport) {
	bad := 0
	tw := tabwriter.NewWriter(out, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "TARGET\tEXPECTED\tRUNS\tKILL-RATE\tSURVIVORS\tPROVEN\tCALIBRATED\t")
	for _, r := range reps {
		cal := "yes"
		if !r.Calibrated {
			cal = "NO — " + r.Note
			bad++
		}
		fmt.Fprintf(tw, "%s\t%s\t%d\t%.2f\t%.2f\t%.2f\t%s\t\n",
			r.ID, r.ExpectedAdequacy, r.Runs, r.MeanKillRate, r.MeanSurvivors, r.MeanProvenMissed, cal)
	}
	tw.Flush()
	if bad == 0 {
		fmt.Fprintln(out, "\nCALIBRATED — the corpus behaves as its known adequacy predicts; the scorecard's signal is sound.")
	} else {
		fmt.Fprintf(out, "\nMISCALIBRATED — %d target(s) violated their known adequacy. Do NOT publish the scorecard until resolved.\n", bad)
	}
}
