// SPDX-License-Identifier: Elastic-2.0

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/auditpush"
	"github.com/pdbethke/corralai/internal/lang"
	"github.com/pdbethke/corralai/internal/reposcan"
	"github.com/pdbethke/corralai/internal/scanstore"
)

// TestTheUnitPassReachesTheLedgerWhole: five units, one of each kind the
// pass can end in, and the bundle's UnitPass says the same numbers the
// scan's "units:" lines print. The two are asserted against each other, so
// neither can be recounted without this failing.
func TestTheUnitPassReachesTheLedgerWhole(t *testing.T) {
	root := unitGradingFixture(t)
	cov := map[string]lang.LineCounts{"src/a.ts": {Executed: 1, Executable: 1}}
	ev := reposcan.UnitEvidence{Ran: true, Units: []reposcan.UnitResult{
		unitRun("tests/one.test.ts", cov),
		{Unit: "tests/two.test.ts", Err: "run failed: exit 1"},
		{Unit: "tests/three.test.ts", Err: "not run (time limit)"},
		{Unit: "tests/four.test.ts", Err: fmt.Sprintf("not measured: red with the evidence pass's change (%s), green as the operator's command runs it, which took no coverage", "threads pool")},
		{Unit: "tests/five.test.ts", Err: "could not run: jail refused"},
	}}
	ex := unitGradedExecutor(t, root, ev)
	ex.evidenceTimeout = 2 * time.Minute
	ex.unitsDuration = 1500 * time.Millisecond

	var screen bytes.Buffer
	printUnitPass(&screen, ex)
	printUnitGrading(&screen, ex, "typescript")

	b := buildBundle(scanstore.Scan{Repo: "o/r", Commit: "c", Substrate: substrateWorkspace, UnitPass: ex.unitPassJSON()},
		1, nil, nil, nil, nil, auditpush.Link{}, false, "o/r", "c", "", bundleMeta{})
	var got struct {
		Listed, Runnable, Run, Failed, Cut, Unreported int
		GreenAsWritten                                 int   `json:"green_as_written"`
		Millis                                         int64 `json:"millis"`
		Reused                                         bool  `json:"reused"`
	}
	if b.Scan.UnitPass == "" {
		t.Fatal("the bundle carries no UnitPass")
	}
	dec := json.NewDecoder(strings.NewReader(b.Scan.UnitPass))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&got); err != nil {
		t.Fatalf("UnitPass %q: %v", b.Scan.UnitPass, err)
	}
	if got.Listed != 5 || got.Runnable != 5 || got.Run != 5 || got.Failed != 4 || got.Cut != 1 ||
		got.GreenAsWritten != 1 || got.Unreported != 4 || got.Millis != 1500 || got.Reused {
		t.Errorf("UnitPass = %s", b.Scan.UnitPass)
	}
	// The screen says the same numbers.
	s := screen.String()
	for _, want := range []string{
		fmt.Sprintf("units: %d run, %d failed", got.Run, got.Failed),
		fmt.Sprintf("%d unit(s) cut short", got.Cut),
		fmt.Sprintf("%d unit(s) red only with", got.GreenAsWritten),
		fmt.Sprintf("the %d that did not are added", got.Unreported),
	} {
		if !strings.Contains(s, want) {
			t.Errorf("the screen lacks %q, so it and the record disagree:\n%s", want, s)
		}
	}
}

// A scan that ran no unit pass records none, not a zeroed one.
func TestNoUnitPassRecordsNoUnitPass(t *testing.T) {
	if got := (*localExecutor)(nil).unitPassJSON(); got != "" {
		t.Errorf("nil executor: %q", got)
	}
	if got := (&localExecutor{}).unitPassJSON(); got != "" {
		t.Errorf("no pass: %q", got)
	}
}

// A key nothing measured is ABSENT from the record, never a zero standing
// in for it: runnable on a pass that failed to seed (it announced nothing),
// millis on a reused pass (no unit ran), unreported where the whole suite
// grades (the screen prints no such text).
func TestUnitPassOmitsWhatNothingMeasured(t *testing.T) {
	keys := func(ex *localExecutor) map[string]any {
		var m map[string]any
		if err := json.Unmarshal([]byte(ex.unitPassJSON()), &m); err != nil {
			t.Fatalf("%q: %v", ex.unitPassJSON(), err)
		}
		return m
	}
	// Ran:false with units: the seed-failure early return.
	failed := &localExecutor{units: reposcan.UnitEvidence{Units: []reposcan.UnitResult{
		{Unit: "a", Err: "could not run: seed"}, {Unit: "b", Err: "could not run: seed"}}}}
	m := keys(failed)
	for _, k := range []string{"runnable", "unreported"} {
		if _, ok := m[k]; ok {
			t.Errorf("a pass that never announced records %s: %v", k, m)
		}
	}
	if m["run"] != float64(2) || m["failed"] != float64(2) {
		t.Errorf("counts lost: %v", m)
	}
	// Reused: no duration, but runnable is known.
	n := 2
	reused := &localExecutor{unitsReused: true, unitsRunnable: &n, units: reposcan.UnitEvidence{Ran: true, Units: []reposcan.UnitResult{{Unit: "a"}, {Unit: "b"}}}}
	m = keys(reused)
	if _, ok := m["millis"]; ok {
		t.Errorf("a reused pass records a duration: %v", m)
	}
	if m["runnable"] != float64(2) || m["reused"] != true {
		t.Errorf("reused pass: %v", m)
	}
}
