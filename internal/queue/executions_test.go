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
	rec := func(mid int64, cmd string, code int, ts int64) {
		if err := s.RecordExecution(Execution{MissionID: mid, Agent: "Bob", Role: "builder", Command: cmd, ExitCode: code, OK: code == 0, TS: ts}); err != nil {
			t.Fatal(err)
		}
	}
	// mission 1: pass then fail of same verify command; latest state must fail.
	rec(1, "go test ./...", 0, 1)
	rec(1, "go test ./...", 1, 2)
	// mission 2: only a failing build
	rec(2, "go build ./...", 1, 1)
	// mission 3: non-prefix command must not satisfy verify.
	rec(3, "cd calc && go test ./...", 0, 1)

	pass := func(mid int64, v string) bool {
		ok, err := s.MissionPassedVerify(mid, v)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if pass(1, "go test") {
		t.Fatal("mission 1 latest 'go test' failed — must not pass final-state gate")
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
	if pass(3, "go test") {
		t.Fatal("non-prefix command should not match verify string")
	}
}

func TestMissionPassedVerifySince(t *testing.T) {
	s := openTestStore(t)
	rec := func(mid int64, cmd string, code int, ts int64) {
		if err := s.RecordExecution(Execution{
			MissionID: mid, Agent: "Tess", Role: "tester", Command: cmd,
			ExitCode: code, OK: code == 0, TS: ts,
		}); err != nil {
			t.Fatal(err)
		}
	}
	rec(9, "go test ./...", 0, 10) // before claim window
	rec(9, "go test ./...", 1, 20) // in claim window, latest fails
	rec(9, "go test ./...", 0, 30) // in claim window, latest passes

	ok, err := s.MissionPassedVerifySince(9, "go test", 15)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected passing latest verify in claim window")
	}

	ok, err = s.MissionPassedVerifySince(9, "go test", 25)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("window at ts>=25 should see the pass at ts=30")
	}

	ok, err = s.MissionPassedVerifySince(9, "go test", 31)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("no matching verify executions in window should fail")
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
