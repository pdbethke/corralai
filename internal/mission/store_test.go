// SPDX-License-Identifier: Elastic-2.0

package mission

import (
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/queue"
)

// openQueue is a test helper that opens a queue.Store backed by a temp SQLite
// file inside dir and registers cleanup on t.
func openQueue(t *testing.T, dir string) *queue.Store {
	t.Helper()
	q, err := queue.Open(filepath.Join(dir, "q.sqlite3"))
	if err != nil {
		t.Fatalf("openQueue: %v", err)
	}
	t.Cleanup(func() { q.Close() })
	return q
}

// openMissionStore is a test helper that opens a mission.Store backed by a temp
// SQLite file inside dir and registers cleanup on t.
func openMissionStore(t *testing.T, dir string) *Store {
	t.Helper()
	s, err := Open(filepath.Join(dir, "m.sqlite3"))
	if err != nil {
		t.Fatalf("openMissionStore: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// queueHasTaskForPhase reports whether the queue holds at least one task for
// missionID whose Title equals phaseName (planToTasks sets Title = phase name).
func queueHasTaskForPhase(t *testing.T, q *queue.Store, missionID int64, phaseName string) bool {
	t.Helper()
	tasks, err := q.List(missionID)
	if err != nil {
		t.Fatalf("queueHasTaskForPhase: List: %v", err)
	}
	for _, tk := range tasks {
		if tk.Title == phaseName {
			return true
		}
	}
	return false
}

func TestMissionRepoFields(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "m.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	q := openQueue(t, dir)
	id, err := CreateMission(s, q, "build calc", DefaultPlan("build calc"), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetRepo(id, "https://github.com/o/r", "main", "corralai/m1"); err != nil {
		t.Fatal(err)
	}
	m, err := s.Mission(id)
	if err != nil || m.Repo != "https://github.com/o/r" || m.Base != "main" || m.Branch != "corralai/m1" {
		t.Fatalf("repo fields not persisted: %+v err=%v", m, err)
	}
	if err := s.SetPRURL(id, "https://github.com/o/r/pull/7"); err != nil {
		t.Fatal(err)
	}
	m, _ = s.Mission(id)
	if m.PRURL != "https://github.com/o/r/pull/7" {
		t.Fatalf("PRURL not persisted: %+v", m)
	}
	if got := MissionDir("/ws", 42); got != "/ws/m42" {
		t.Fatalf("MissionDir = %q, want /ws/m42", got)
	}
}

func TestReviewStateAndOpenPRFilter(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(filepath.Join(dir, "m.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	q := openQueue(t, dir)

	id, err := CreateMission(s, q, "build calc", DefaultPlan("build calc"), false)
	if err != nil {
		t.Fatal(err)
	}
	s.SetMissionStatus(id, "done")
	s.SetRepo(id, "https://github.com/o/r", "main", "corralai/m1")
	s.SetPRURL(id, "https://github.com/o/r/pull/7")

	// mission is done + has pr_url + not parked → must appear in the open-PR set
	got, err := s.MissionsWithOpenPR()
	if err != nil || len(got) != 1 || got[0].ID != id {
		t.Fatalf("open-PR set: %v err=%v", got, err)
	}

	// set review state and read it back
	if err := s.SetReviewState(id, 2, "2026-07-01T10:00:00Z", false); err != nil {
		t.Fatal(err)
	}
	m, err := s.Mission(id)
	if err != nil {
		t.Fatal(err)
	}
	if m.ReviewRounds != 2 || m.ReviewWatermark != "2026-07-01T10:00:00Z" || m.ReviewParked {
		t.Fatalf("review state not persisted: %+v", m)
	}

	// parking removes it from the open-PR set
	s.SetReviewState(id, 3, "x", true)
	if got, _ := s.MissionsWithOpenPR(); len(got) != 0 {
		t.Fatalf("parked mission must leave the open-PR set, got %v", got)
	}

	// ParsePRNumber
	if n, err := ParsePRNumber("https://github.com/o/r/pull/7"); err != nil || n != 7 {
		t.Fatalf("ParsePRNumber = %d err=%v", n, err)
	}
	if n, err := ParsePRNumber("https://github.com/o/r/pull/7/"); err != nil || n != 7 {
		t.Fatalf("ParsePRNumber trailing slash = %d err=%v", n, err)
	}
	if _, err := ParsePRNumber("https://github.com/o/r/issues/7"); err == nil {
		t.Fatal("ParsePRNumber: expected error for URL without /pull/")
	}

	// verify queryMissions (via ListMissions) also scans the new columns
	all, err := s.ListMissions()
	if err != nil || len(all) != 1 || all[0].ReviewRounds != 3 || !all[0].ReviewParked {
		t.Fatalf("queryMissions review fields: %+v err=%v", all, err)
	}

	// migration idempotency: re-open the same DB
	s2, err := Open(filepath.Join(dir, "m.duckdb"))
	if err != nil {
		t.Fatalf("re-open: %v", err)
	}
	defer s2.Close()
	m2, err := s2.Mission(id)
	if err != nil || m2 == nil || m2.ReviewRounds != 3 {
		t.Fatalf("re-open mission: %+v err=%v", m2, err)
	}
}

func TestDefaultPlanVerify(t *testing.T) {
	want := map[string]string{"build-core": "go build", "build": "go build", "test": "go test", "integrate": "go build"}
	for _, p := range DefaultPlan("a calc package") {
		if v, gated := want[p.Name]; gated {
			if p.Verify != v {
				t.Fatalf("phase %s Verify = %q, want %q", p.Name, p.Verify, v)
			}
		} else if p.Verify != "" {
			t.Fatalf("phase %s should be ungated, got Verify=%q", p.Name, p.Verify)
		}
	}
}

func TestReopenForReview(t *testing.T) {
	dir := t.TempDir()
	s := openMissionStore(t, dir)
	q := openQueue(t, dir)
	id, _ := CreateMission(s, q, "build calc", DefaultPlan("build calc"), false)
	s.SetMissionStatus(id, "done")

	before, _ := s.Phases(id)
	phases := []PhaseSpec{{Name: "review-r1-fix", Instruction: "address: rename Foo", Count: 1, Verify: "go build ./..."}}
	if err := s.ReopenForReview(q, id, phases, "2026-07-01T10:00:00Z"); err != nil {
		t.Fatal(err)
	}
	m, _ := s.Mission(id)
	if m.Status != "running" || m.ReviewRounds != 1 || m.ReviewWatermark != "2026-07-01T10:00:00Z" {
		t.Fatalf("reopen state: %+v", m)
	}
	after, _ := s.Phases(id)
	if len(after) != len(before)+1 {
		t.Fatalf("expected one appended phase, before=%d after=%d", len(before), len(after))
	}
	// the appended phase's tasks are enqueued (a ready/pending task exists for it)
	if !queueHasTaskForPhase(t, q, id, "review-r1-fix") {
		t.Fatal("review phase tasks were not enqueued")
	}
}

// TestParsePRNumberByForge verifies ParsePRNumber handles both GitHub/Gitea
// (/pull/) and GitLab (/merge_requests/) URL forms.
func TestParsePRNumberByForge(t *testing.T) {
	cases := []struct {
		url     string
		want    int
		wantErr bool
	}{
		{"https://github.com/o/r/pull/7", 7, false},
		{"https://gitea.example.com/o/r/pull/42", 42, false},
		{"https://gitlab.com/o/r/-/merge_requests/7", 7, false},
		{"https://gitlab.example.com/o/r/-/merge_requests/99/", 99, false},
		{"https://github.com/o/r/issues/7", 0, true},
		{"not-a-url", 0, true},
	}
	for _, tc := range cases {
		n, err := ParsePRNumber(tc.url)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParsePRNumber(%q): want error, got n=%d", tc.url, n)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParsePRNumber(%q): unexpected error: %v", tc.url, err)
			continue
		}
		if n != tc.want {
			t.Errorf("ParsePRNumber(%q) = %d, want %d", tc.url, n, tc.want)
		}
	}
}
