// SPDX-License-Identifier: Elastic-2.0

package eval

import (
	"bytes"
	"strings"
	"testing"
)

func TestReportFlagsMiscalibration(t *testing.T) {
	m := Manifest{CorpusVersion: "v1", Targets: []Target{
		{ID: "thorough-ok", ExpectedAdequacy: "thorough"},
		{ID: "gappy-ok", ExpectedAdequacy: "gappy", ExpectedSurvivors: 1},
		{ID: "gappy-BROKEN", ExpectedAdequacy: "gappy", ExpectedSurvivors: 1},
		{ID: "thorough-BROKEN", ExpectedAdequacy: "thorough"},
	}}
	res := []RunResult{
		{TargetID: "thorough-ok", Survivors: 0, MutantsTotal: 8},
		{TargetID: "gappy-ok", Survivors: 2, ProvenMissed: 1},        // has the gap, and the writer PROVED it → calibrated
		{TargetID: "gappy-BROKEN", Survivors: 0},                     // gappy but pool found NO gap → miscalibrated
		{TargetID: "thorough-BROKEN", Survivors: 3, MutantsTotal: 8}, // thorough but riddled with survivors → miscalibrated
	}
	reps := Report(m, res)
	byID := map[string]TargetReport{}
	for _, r := range reps {
		byID[r.ID] = r
	}
	if !byID["thorough-ok"].Calibrated || !byID["gappy-ok"].Calibrated {
		t.Fatal("well-behaved targets must be calibrated")
	}
	if byID["gappy-BROKEN"].Calibrated {
		t.Fatal("a gappy target with 0 survivors must be flagged miscalibrated (metric under-sensitive)")
	}
	if byID["thorough-BROKEN"].Calibrated {
		t.Fatal("a thorough target riddled with survivors must be flagged (metric over-sensitive)")
	}
}

// TestReportFlagsUnmatchedTarget ensures a RunResult referencing a TargetID
// that isn't in the manifest is flagged miscalibrated with an explanatory
// note, NOT silently reported as calibrated via the "unknown adequacy"
// default branch (Hole 1: dangerous zero-value default).
func TestReportFlagsUnmatchedTarget(t *testing.T) {
	m := Manifest{CorpusVersion: "v1", Targets: []Target{
		{ID: "thorough-ok", ExpectedAdequacy: "thorough"},
	}}
	res := []RunResult{
		{TargetID: "thorough-ok", Survivors: 0, MutantsTotal: 8},
		{TargetID: "does-not-exist-in-manifest", Survivors: 0},
	}
	reps := Report(m, res)
	byID := map[string]TargetReport{}
	for _, r := range reps {
		byID[r.ID] = r
	}
	if !byID["thorough-ok"].Calibrated {
		t.Fatal("well-behaved matched target must remain calibrated")
	}
	got, ok := byID["does-not-exist-in-manifest"]
	if !ok {
		t.Fatal("unmatched target must still appear in the report")
	}
	if got.Calibrated {
		t.Fatal("a target absent from the manifest must be flagged miscalibrated, never silently calibrated")
	}
	if !strings.Contains(got.Note, "not in manifest") {
		t.Fatalf("expected a clear 'not in manifest' note, got: %q", got.Note)
	}
}

// TestReportThoroughRequiresMutants ensures a thorough target that converges
// with zero survivors AND zero mutants generated is flagged miscalibrated,
// not silently CALIBRATED — "0 survivors because the tests are thorough" is
// indistinguishable from "0 survivors because nothing was mutated" unless the
// gate also checks that mutants were actually generated.
func TestReportThoroughRequiresMutants(t *testing.T) {
	m := Manifest{CorpusVersion: "v1", Targets: []Target{
		{ID: "thorough-degenerate", ExpectedAdequacy: "thorough"},
		{ID: "thorough-normal", ExpectedAdequacy: "thorough"},
	}}
	res := []RunResult{
		{TargetID: "thorough-degenerate", Survivors: 0, MutantsTotal: 0},
		{TargetID: "thorough-normal", Survivors: 0, MutantsTotal: 8},
	}
	reps := Report(m, res)
	byID := map[string]TargetReport{}
	for _, r := range reps {
		byID[r.ID] = r
	}
	if byID["thorough-degenerate"].Calibrated {
		t.Fatalf("a thorough target with 0 mutants generated must be flagged miscalibrated, not CALIBRATED: %+v", byID["thorough-degenerate"])
	}
	if !strings.Contains(byID["thorough-degenerate"].Note, "no mutants generated") {
		t.Fatalf("expected a 'no mutants generated' note, got: %q", byID["thorough-degenerate"].Note)
	}
	if !byID["thorough-normal"].Calibrated {
		t.Fatal("a thorough target with mutants generated and 0 survivors must remain calibrated")
	}
}

// TestWriteReportEmptyIsNotCalibrated ensures an empty result set never
// prints the bare "CALIBRATED" clean-pass headline (Hole 2: silent partial/
// empty scope read as a full-corpus pass).
func TestWriteReportEmptyIsNotCalibrated(t *testing.T) {
	var buf bytes.Buffer
	WriteReport(&buf, nil)
	output := buf.String()
	if strings.Contains(output, "CALIBRATED") && !strings.Contains(output, "NOT EVALUATED") {
		t.Fatalf("empty report must not read as a clean CALIBRATED pass: %q", output)
	}
	if !strings.Contains(output, "NOT EVALUATED") && !strings.Contains(output, "no runs to evaluate") {
		t.Fatalf("empty report must clearly say nothing ran, got: %q", output)
	}
}

// TestWriteReportShowsScope ensures a genuine clean pass states its scope
// (N targets over M runs) so a partial run can't be mistaken for the whole
// corpus passing.
func TestWriteReportShowsScope(t *testing.T) {
	var buf bytes.Buffer
	reps := []TargetReport{
		{ID: "thorough-ok", ExpectedAdequacy: "thorough", Runs: 2, Calibrated: true},
	}
	WriteReport(&buf, reps)
	output := buf.String()
	if !strings.Contains(output, "CALIBRATED") {
		t.Fatalf("expected a CALIBRATED headline for an all-passing report, got: %q", output)
	}
	if !strings.Contains(output, "1 target") || !strings.Contains(output, "2 run") {
		t.Fatalf("expected the headline to state scope (targets/runs), got: %q", output)
	}
}

// TestReportExcludesUngradedRunsFromTheMeans is the never-fabricate-a-score
// invariant at the soundness report — the report whose entire job is to say
// "do NOT publish". A run that could not be graded (failed baseline, or a
// check command that provably never compiles or imports the audited file)
// carries a meaningless DevKillRate 0 with an empty survivor set. Averaging
// that in lets a build failure declare a well-behaved target miscalibrated.
func TestReportExcludesUngradedRunsFromTheMeans(t *testing.T) {
	m := Manifest{CorpusVersion: "v1", Targets: []Target{
		{ID: "gappy-ok", ExpectedAdequacy: "gappy", ExpectedSurvivors: 2},
	}}
	res := []RunResult{
		{TargetID: "gappy-ok", DevKillRate: 0.6, Survivors: 2, MutantsTotal: 5, ProvenMissed: 2},
		{TargetID: "gappy-ok", DevKillRate: 0, Survivors: 0, MutantsTotal: 0, SuiteIgnoresFile: true},
		{TargetID: "gappy-ok", DevKillRate: 0, Survivors: 0, MutantsTotal: 0, BaselineFailed: true},
	}
	reps := Report(m, res)
	if len(reps) != 1 {
		t.Fatalf("want 1 target report, got %d", len(reps))
	}
	got := reps[0]
	if got.Runs != 3 || got.GradedRuns != 1 || got.Ungraded != 2 {
		t.Errorf("Runs/GradedRuns/Ungraded = %d/%d/%d, want 3/1/2", got.Runs, got.GradedRuns, got.Ungraded)
	}
	if got.MeanKillRate != 0.6 {
		t.Errorf("MeanKillRate = %.2f, want 0.60 — ungraded runs must not average in", got.MeanKillRate)
	}
	if got.MeanSurvivors != 2 {
		t.Errorf("MeanSurvivors = %.2f, want 2.00 — ungraded runs must not average in", got.MeanSurvivors)
	}
	if !got.Calibrated {
		t.Errorf("a target whose ONE graded run behaves as predicted must stay calibrated; note = %q", got.Note)
	}

	var buf bytes.Buffer
	WriteReport(&buf, reps)
	out := buf.String()
	if !strings.Contains(out, "COULD NOT BE GRADED") {
		t.Errorf("the excluded runs must be VISIBLE in the report, not silently dropped:\n%s", out)
	}
	if strings.Contains(out, "MISCALIBRATED") {
		t.Errorf("ungraded runs pushed a sound target into MISCALIBRATED:\n%s", out)
	}
}

// TestReportAllRunsUngradedCannotValidate: when NOTHING was graded, the means
// are zero because nothing was measured. That must read as "cannot validate",
// never as evidence in either direction.
func TestReportAllRunsUngradedCannotValidate(t *testing.T) {
	m := Manifest{CorpusVersion: "v1", Targets: []Target{
		{ID: "thorough-ok", ExpectedAdequacy: "thorough"},
	}}
	res := []RunResult{
		{TargetID: "thorough-ok", SuiteIgnoresFile: true},
		{TargetID: "thorough-ok", BaselineFailed: true},
	}
	reps := Report(m, res)
	if len(reps) != 1 {
		t.Fatalf("want 1 target report, got %d", len(reps))
	}
	got := reps[0]
	if got.Calibrated {
		t.Fatalf("a target with zero graded runs must never be reported CALIBRATED: %+v", got)
	}
	if !strings.Contains(got.Note, "could not be graded") {
		t.Errorf("note = %q, want it to name the could-not-grade cause", got.Note)
	}
	if got.MeanKillRate != 0 || got.MeanSurvivors != 0 {
		t.Errorf("means over zero graded runs must stay 0 (and must not be NaN): %+v", got)
	}
}

// TestReportValidatesTheProvenColumnAndEveryUngradedCause pins the review's
// three ways the report said CALIBRATED about nothing: a thorough target
// whose only run TIMED OUT (0 survivors because nothing graded), a gappy
// target with expected_survivors unset (MeanSurvivors >= 0 is always true),
// and a gappy target whose known gap the writer never proved — the proven
// column is the one the scorecard's headline rests on, and the report never
// looked at it. Plus: a manifest target with no result is named, so a
// subset never reads as the corpus.
func TestReportValidatesTheProvenColumnAndEveryUngradedCause(t *testing.T) {
	m := Manifest{CorpusVersion: "v1", Targets: []Target{
		{ID: "thorough-timeout", ExpectedAdequacy: "thorough"},
		{ID: "gappy-unset", ExpectedAdequacy: "gappy"},
		{ID: "gappy-writer-failed", ExpectedAdequacy: "gappy", ExpectedSurvivors: 1},
		{ID: "gappy-unproven", ExpectedAdequacy: "gappy", ExpectedSurvivors: 1},
		{ID: "gappy-proven", ExpectedAdequacy: "gappy", ExpectedSurvivors: 1},
		{ID: "never-ran", ExpectedAdequacy: "thorough"},
	}}
	res := []RunResult{
		{TargetID: "thorough-timeout", Survivors: 0, MutantsTotal: 8, TimedOut: true},
		{TargetID: "gappy-unset", Survivors: 0},
		{TargetID: "gappy-writer-failed", Survivors: 5, ProvenMissed: 0, TestWriterFailed: true},
		{TargetID: "gappy-unproven", Survivors: 5, ProvenMissed: 0},
		{TargetID: "gappy-proven", Survivors: 5, ProvenMissed: 2},
	}
	reps := Report(m, res)
	byID := map[string]TargetReport{}
	for _, r := range reps {
		byID[r.ID] = r
	}
	for id, wantNote := range map[string]string{
		"thorough-timeout":    "could not be graded",
		"gappy-unset":         "declares no expected_survivors",
		"gappy-writer-failed": "the writer half never graded",
		"gappy-unproven":      "never proven catchable",
	} {
		r := byID[id]
		if r.Calibrated {
			t.Errorf("%s: CALIBRATED — %q", id, r.Note)
		} else if !strings.Contains(r.Note, wantNote) {
			t.Errorf("%s: note = %q, want it to say %q", id, r.Note, wantNote)
		}
	}
	if !byID["gappy-proven"].Calibrated {
		t.Errorf("gappy-proven: not calibrated: %q", byID["gappy-proven"].Note)
	}
	if byID["gappy-writer-failed"].MeanProvenMissed != 0 || byID["gappy-writer-failed"].WriterGradedRuns != 0 {
		t.Errorf("a writer-failed run contributed to the proven mean: %+v", byID["gappy-writer-failed"])
	}

	var buf bytes.Buffer
	WriteReportWithScope(&buf, reps, NotRun(m, res))
	out := buf.String()
	if !strings.Contains(out, "5 of 6 target(s)") || !strings.Contains(out, "NOT RUN — 1 manifest target(s) have no result and are absent from every line above: never-ran") {
		t.Errorf("the headline must say which targets ran:\n%s", out)
	}
}
