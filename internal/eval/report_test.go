package eval

import "testing"

func TestReportFlagsMiscalibration(t *testing.T) {
	m := Manifest{CorpusVersion: "v1", Targets: []Target{
		{ID: "thorough-ok", ExpectedAdequacy: "thorough"},
		{ID: "gappy-ok", ExpectedAdequacy: "gappy", ExpectedSurvivors: 1},
		{ID: "gappy-BROKEN", ExpectedAdequacy: "gappy", ExpectedSurvivors: 1},
		{ID: "thorough-BROKEN", ExpectedAdequacy: "thorough"},
	}}
	res := []RunResult{
		{TargetID: "thorough-ok", Survivors: 0},
		{TargetID: "gappy-ok", Survivors: 2},        // has the gap → calibrated
		{TargetID: "gappy-BROKEN", Survivors: 0},    // gappy but pool found NO gap → miscalibrated
		{TargetID: "thorough-BROKEN", Survivors: 3}, // thorough but riddled with survivors → miscalibrated
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
