// SPDX-License-Identifier: Elastic-2.0

package queue

import "testing"

// TestFootprintCountsAndPruneMission covers the DB relief valve's (#66)
// per-mission counts and destructive delete: FootprintCounts must count only
// the target mission's tasks/findings/executions (not a sibling mission's),
// and PruneMission must remove exactly that mission's rows and nothing else.
func TestFootprintCountsAndPruneMission(t *testing.T) {
	s := openTestStore(t)

	if err := s.Enqueue(1, []TaskSpec{{Key: "build#1", Title: "build"}, {Key: "test#1", Title: "test"}}); err != nil {
		t.Fatalf("Enqueue mission 1: %v", err)
	}
	if err := s.Enqueue(2, []TaskSpec{{Key: "build#1", Title: "build"}}); err != nil {
		t.Fatalf("Enqueue mission 2: %v", err)
	}
	if _, err := s.AddFinding(Finding{MissionID: 1, Reporter: "Hawk", Type: "bug", Severity: "low"}); err != nil {
		t.Fatalf("AddFinding mission 1: %v", err)
	}
	if _, err := s.AddFinding(Finding{MissionID: 2, Reporter: "Hawk", Type: "bug", Severity: "low"}); err != nil {
		t.Fatalf("AddFinding mission 2: %v", err)
	}
	if err := s.RecordExecution(Execution{MissionID: 1, Agent: "bob", Command: "go build", ExitCode: 0, OK: true, TS: 1}); err != nil {
		t.Fatalf("RecordExecution mission 1: %v", err)
	}

	tasks, findings, execs, err := s.FootprintCounts(1)
	if err != nil {
		t.Fatal(err)
	}
	if tasks != 2 || findings != 1 || execs != 1 {
		t.Fatalf("mission 1 footprint = tasks=%d findings=%d executions=%d, want 2/1/1", tasks, findings, execs)
	}

	if err := s.PruneMission(1); err != nil {
		t.Fatalf("PruneMission(1): %v", err)
	}

	tasks, findings, execs, err = s.FootprintCounts(1)
	if err != nil {
		t.Fatal(err)
	}
	if tasks != 0 || findings != 0 || execs != 0 {
		t.Fatalf("mission 1 not fully pruned: tasks=%d findings=%d executions=%d", tasks, findings, execs)
	}

	// Mission 2's rows must survive untouched — prune is scoped to one mission.
	tasks, findings, execs, err = s.FootprintCounts(2)
	if err != nil {
		t.Fatal(err)
	}
	if tasks != 1 || findings != 1 || execs != 0 {
		t.Fatalf("mission 2 was affected by mission 1's prune: tasks=%d findings=%d executions=%d, want 1/1/0", tasks, findings, execs)
	}
}
