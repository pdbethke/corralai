// SPDX-License-Identifier: Elastic-2.0

package advpool

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/pdbethke/corralai/internal/modelcorr"
	"github.com/pdbethke/corralai/internal/queue"
)

// fullVerdict is a Verdict with EVERY field set to a distinguishable non-zero
// value. It exists so the round-trip below is a statement about the whole
// type rather than about the handful of fields someone remembered.
//
// Durations are whole milliseconds: the wire form for Timing and the
// mutant-duration summaries is milliseconds on purpose (a nanosecond count
// under a Go field name is a number no other reader of verdict_json could
// interpret), so a sub-millisecond value would not survive by design.
func fullVerdict() Verdict {
	retries := 2
	return Verdict{
		Repo: "o/r", Commit: "deadbeef", Lang: "python",
		DevKillRate:      0.625,
		BaselineDuration: 3 * time.Second,
		MutantsTotal:     8, MutantsInvalid: 2, Survivors: 3, ProvenMissed: 1,
		ProvenMutantIDs: []string{"m7"},
		AuthoredTest:    "def test_x(): assert True\n",
		RegionsTotal:    4, RegionsProbed: 3,
		DroppedRegions: []string{"shard-2"},
		VacuousFindings: []queue.Finding{{
			ID: 4, MissionID: 1, Reporter: "test-critic", Type: "note",
			Severity: "medium", Scope: "whole-test", Evidence: "asserts nothing",
			Status: "open", CreatedTS: 1.5,
		}},
		ModelsByRole:     map[string]string{"mutant-generator": "m-1"},
		Status:           StatusNeedsReview,
		TestWriterFailed: true, PoolTestUnsound: true,
		AuthoredTestNotCollected: true,
		BaselineFailed:           true, BaselineOutput: "E   ImportError",
		SuiteIgnoresFile: true,
		TestSelection: TestSelection{
			Method: "coverage-context", Selected: 14, Of: 1431, Fallback: "",
			PerMutant:      true,
			TestsPerMutant: &TestsPerMutantSpread{Min: 3, Median: 9, Max: 41},
			Rules:          map[string]int{"span": 6},
			AuthoredAlone:  true,
		},
		Concurrency: Concurrency{Trees: 6, Note: "bounded by cores", Shared: []string{".venv"}},
		Uncovered:   true,
		RecordID:    91, RecordHead: "head-91",
		TimedOut: true, DevScored: true,
		DevKilledMutants: []MutantRef{
			{ID: "m1", ParentSHA256: "aa", TestsRun: 4, Rule: "span", Duration: 250 * time.Millisecond, KilledBy: "a.py::test_x"},
		},
		DevSurvivedMutants: []MutantRef{
			{ID: "m2", ParentSHA256: "aa", TestsRun: 4, Rule: "span", Duration: 300 * time.Millisecond},
		},
		Timing: Timing{
			Selection: 92 * time.Second, Generation: 4 * time.Minute, Pool: 20 * time.Second,
			DevPass: 35 * time.Minute, AuthoredPass: time.Minute, Critic: 15 * time.Second,
			Total: 41 * time.Minute,
		},
		ModelCalls: []ModelCall{
			{Role: RoleMutantGenerator, Model: "m-1", Calls: 3, Retries: &retries,
				InputTokens: 900, OutputTokens: 210, Wall: 4100 * time.Millisecond},
		},
		MutantDurationMedian: 54 * time.Second,
		MutantDurationMax:    3 * time.Minute,
		ChallengerAgreement: &modelcorr.Pair{
			ModelA: "w-1", ModelB: "w-2", Mutants: 8,
			SurvivedA: 3, SurvivedB: 4, SharedSurvivors: 2, UnionSurvivors: 5,
			Jaccard: 0.4, Kappa: 0.25, Sufficient: true, KappaDefined: true,
		},
	}
}

// TestVerdictRoundTripsEveryField is the guard on the whole verdict wire
// form, not on one field of it.
//
// A Verdict is marshalled into the ledger's verdict_json, read back out of it
// on a cache hit, pushed to the warehouse and hashed into the signed
// statement. Any field that does not survive the trip is a measurement lost
// silently — and the failure that motivated this test was quieter still:
// MutantDurationMedian/Max had no JSON tag, so they went out as raw
// NANOSECONDS under their Go names, beside a Timing that had spelled its
// milliseconds out for exactly that reason. They round-tripped fine into Go
// and were uninterpretable to everything else.
//
// Field-by-field equality over a fully populated value is the only assertion
// that cannot rot as fields are added: a new field with a bad wire form fails
// here the moment fullVerdict sets it.
func TestVerdictRoundTripsEveryField(t *testing.T) {
	want := fullVerdict()
	b, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got Verdict
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	wv, gv := reflect.ValueOf(want), reflect.ValueOf(got)
	for i := 0; i < wv.NumField(); i++ {
		name := wv.Type().Field(i).Name
		if !reflect.DeepEqual(wv.Field(i).Interface(), gv.Field(i).Interface()) {
			t.Errorf("%s did not survive verdict_json: %#v -> %#v",
				name, wv.Field(i).Interface(), gv.Field(i).Interface())
		}
	}
}

// TestVerdictMutantDurationsAreMilliseconds pins the WIRE NAMES, which is the
// half a Go-to-Go round trip cannot see: the warehouse and every SQL reader
// of verdict_json see these keys, not the Go field names, and a bare
// nanosecond integer under "MutantDurationMedian" is not a number any of them
// can read as a duration.
func TestVerdictMutantDurationsAreMilliseconds(t *testing.T) {
	b, err := json.Marshal(Verdict{
		MutantDurationMedian: 54 * time.Second,
		MutantDurationMax:    3 * time.Minute,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	js := string(b)
	for _, want := range []string{`"mutant_duration_median_ms":54000`, `"mutant_duration_max_ms":180000`} {
		if !strings.Contains(js, want) {
			t.Errorf("verdict JSON is missing %s: %s", want, js)
		}
	}
	for _, bad := range []string{"MutantDurationMedian", "MutantDurationMax", "54000000000"} {
		if strings.Contains(js, bad) {
			t.Errorf("verdict JSON still carries %q (the Go name / raw nanoseconds): %s", bad, js)
		}
	}
}
