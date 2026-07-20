// SPDX-License-Identifier: Elastic-2.0

package matrix

import (
	"context"
	"testing"
)

func TestBuild(t *testing.T) {
	tests := []TestRef{
		{Selector: "T::a", TestFile: "t.py"}, // load-bearing: kills 2
		{Selector: "T::b", TestFile: "t.py"}, // vacuous: scored, kills 0 -> delete-candidate
		{Selector: "T::c", TestFile: "t.py"}, // baseline-fail: not scored
	}
	score := func(_ context.Context, tr TestRef) (int, []string, bool) {
		switch tr.Selector {
		case "T::a":
			return 2, []string{"m1", "m2"}, true
		case "T::b":
			return 0, nil, true
		default:
			return 0, nil, false
		}
	}
	res := Build(context.Background(), tests, 3, 2, score)
	if len(res.Rows) != 3 {
		t.Fatalf("rows=%d", len(res.Rows))
	}
	if res.Rows[0].Selector != "T::a" || res.Rows[2].Selector != "T::c" {
		t.Fatalf("order not preserved: %+v", res.Rows)
	}
	if res.Rows[0].DeleteCandidate {
		t.Error("load-bearing test must not be a delete-candidate")
	}
	if !res.Rows[1].DeleteCandidate {
		t.Error("scored zero-kill test MUST be a delete-candidate")
	}
	if res.Rows[2].DeleteCandidate {
		t.Error("baseline-fail (not scored) must NOT be a delete-candidate")
	}
	if res.Rows[2].Scored {
		t.Error("T::c should be scored=false")
	}
	if !res.Catchable {
		t.Error("Catchable must be true — some test killed a mutant")
	}
}

func TestBuildAllUncatchable(t *testing.T) {
	tests := []TestRef{{Selector: "x", TestFile: "t.py"}}
	res := Build(context.Background(), tests, 4, 1, func(context.Context, TestRef) (int, []string, bool) {
		return 0, nil, true // scored, but killed nothing
	})
	if res.Catchable {
		t.Error("Catchable must be FALSE when no test killed anything")
	}
	if !res.Rows[0].DeleteCandidate {
		t.Error("a scored zero-kill test is still a delete-candidate")
	}
}
