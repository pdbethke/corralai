// SPDX-License-Identifier: Elastic-2.0

package queue

import (
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestMissionPassedVerify(t *testing.T) {
	s := openTestStore(t)
	rec := func(mid int64, cmd string, code int) {
		if err := s.RecordExecution(Execution{MissionID: mid, Agent: "Bob", Role: "builder", Command: cmd, ExitCode: code, OK: code == 0, TS: 1}); err != nil {
			t.Fatal(err)
		}
	}
	// mission 1: a failing then a passing `go test`
	rec(1, "cd calc && go test ./...", 1)
	rec(1, "cd calc && go test ./...", 0) // substring "go test" + exit 0
	// mission 2: only a failing build
	rec(2, "go build ./...", 1)

	pass := func(mid int64, v string) bool {
		ok, err := s.MissionPassedVerify(mid, v)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !pass(1, "go test") {
		t.Fatal("mission 1 should have a passing 'go test'")
	}
	if pass(2, "go build") {
		t.Fatal("mission 2's go build only ever failed — must not pass")
	}
	if pass(1, "go vet") {
		t.Fatal("no 'go vet' was ever run — must not pass")
	}
	if pass(2, "go test") {
		t.Fatal("mission 2 never ran 'go test' — must not pass")
	}
}

func TestExecutionsByMission(t *testing.T) {
	s := openTestStore(t)
	if err := s.RecordExecution(Execution{MissionID: 1, Agent: "bee1", Command: "go build", OK: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordExecution(Execution{MissionID: 2, Agent: "bee2", Command: "go test", OK: true}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ExecutionsByMission(1)
	if err != nil || len(got) != 1 || got[0].Command != "go build" {
		t.Fatalf("got %v err=%v", got, err)
	}
}
