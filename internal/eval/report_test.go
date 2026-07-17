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
		{TargetID: "gappy-ok", Survivors: 2},                         // has the gap → calibrated
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
