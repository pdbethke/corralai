// SPDX-License-Identifier: Elastic-2.0

package learn

import (
	"path/filepath"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "learn.sqlite3"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestUpsertDedupsBySignature(t *testing.T) {
	s := open(t)
	p, created, err := s.Upsert("missing-req|go.mod", "finding", "builder", []string{"e1", "e2", "e3"})
	if err != nil || !created || p.Status != StatusPending || p.Count != 3 {
		t.Fatalf("first upsert: %+v created=%v err=%v", p, created, err)
	}
	// Upsert is SNAPSHOT-based, not cumulative: the ticker re-feeds the full
	// finding/lesson history every tick, so the incoming evidence IS the
	// cluster's current state, not a delta to add. A re-feed of a grown
	// snapshot (e1..e4) must bump the existing pending row (not create a
	// duplicate) to count==4 — the size of the snapshot, not 3+1.
	p2, created2, _ := s.Upsert("missing-req|go.mod", "finding", "builder", []string{"e1", "e2", "e3", "e4"})
	if created2 || p2.ID != p.ID || p2.Count != 4 {
		t.Fatalf("second upsert should bump, not create: %+v created=%v", p2, created2)
	}
}

// TestSweepReFeedDoesNotInflateCount reproduces review finding #1(a): the
// ticker re-feeds the ENTIRE cumulative finding history every tick. Three
// identical sweeps of the same 3 findings must leave the proposal's count at
// 3 (the snapshot size), never 9 (3 sweeps x 3, if Upsert summed).
func TestSweepReFeedDoesNotInflateCount(t *testing.T) {
	s := open(t)
	findings := []FindingSignal{
		{Type: "missing-req", Target: "go.mod", Role: "builder", Evidence: "a"},
		{Type: "missing-req", Target: "go.mod", Role: "builder", Evidence: "b"},
		{Type: "missing-req", Target: "go.mod", Role: "builder", Evidence: "c"},
	}
	var lastID int64
	for i := 0; i < 3; i++ {
		opened, err := s.Sweep(findings, nil)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			if len(opened) != 1 {
				t.Fatalf("first sweep should open exactly 1 proposal, got %d", len(opened))
			}
			lastID = opened[0].ID
		} else if len(opened) != 0 {
			t.Fatalf("re-sweep of identical findings must not open a new proposal, got %d", len(opened))
		}
	}
	all, err := s.List("")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("want exactly 1 proposal row after 3 identical sweeps, got %d: %+v", len(all), all)
	}
	got, err := s.ByID(lastID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Count != 3 {
		t.Fatalf("count after 3 identical sweeps = %d, want 3 (snapshot, not cumulative)", got.Count)
	}
}

// TestApprovedSignatureUpsertNoOp reproduces review finding #1(b): once a
// proposal is approved, the next sweep re-feed must NOT open a fresh pending
// duplicate for the same signature — that job belongs exclusively to the
// efficacy hook (RecordRecurrence).
func TestApprovedSignatureUpsertNoOp(t *testing.T) {
	s := open(t)
	p, _, err := s.Upsert("missing-req|go.mod", "finding", "builder", []string{"a", "b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Approve(p.ID); err != nil {
		t.Fatal(err)
	}
	got, created, err := s.Upsert("missing-req|go.mod", "finding", "builder", []string{"a", "b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("re-sweep of an approved signature must not open a new pending proposal")
	}
	if got != nil {
		t.Fatalf("approved-signature no-op should return a nil proposal, got %+v", got)
	}
	pending, err := s.List(StatusPending)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Fatalf("want 0 pending proposals after re-sweeping an approved signature, got %d: %+v", len(pending), pending)
	}
}

func TestRejectionSuppressesUntilDoubled(t *testing.T) {
	s := open(t)
	p, _, _ := s.Upsert("bug|build.sh", "finding", "builder", []string{"a", "b", "c"})
	if err := s.Reject(p.ID, "not actionable"); err != nil {
		t.Fatal(err)
	}
	// Same volume again: suppressed (no new pending proposal).
	if _, created, _ := s.Upsert("bug|build.sh", "finding", "builder", []string{"d"}); created {
		t.Fatal("rejected signature must stay suppressed below 2x")
	}
	// Cluster grows to 2x the count at rejection (3 -> 6): reopens.
	if _, created, _ := s.Upsert("bug|build.sh", "finding", "builder", []string{"e", "f", "g", "h", "i", "j"}); !created {
		t.Fatal("2x growth should reopen a rejected signature")
	}
}

// TestRejectSuppressionSurvivesReFeed reproduces review finding #1(c): reject
// at count 3, then a ticker re-feed of the SAME 3 findings (not cumulative
// growth) must stay suppressed — under the old +=-based Upsert, re-feeding
// the full history computed 3+3=6 >= 2x3 and wrongly reopened with zero new
// evidence.
func TestRejectSuppressionSurvivesReFeed(t *testing.T) {
	s := open(t)
	p, _, err := s.Upsert("bug|build.sh", "finding", "builder", []string{"a", "b", "c"})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Reject(p.ID, "not actionable"); err != nil {
		t.Fatal(err)
	}
	// Re-feed the identical 3-item snapshot repeatedly: no new evidence, so
	// it must stay suppressed every time.
	for i := 0; i < 3; i++ {
		if _, created, err := s.Upsert("bug|build.sh", "finding", "builder", []string{"a", "b", "c"}); err != nil {
			t.Fatal(err)
		} else if created {
			t.Fatalf("re-feed #%d of the same 3-item snapshot reopened a rejected signature with no new evidence", i+1)
		}
	}
	// A genuinely bigger snapshot (6 >= 2x3) reopens.
	if _, created, err := s.Upsert("bug|build.sh", "finding", "builder", []string{"a", "b", "c", "d", "e", "f"}); err != nil {
		t.Fatal(err)
	} else if !created {
		t.Fatal("a 6-item snapshot (2x the rejection baseline) should reopen")
	}
}

func TestApproveAndEfficacyReopen(t *testing.T) {
	s := open(t)
	p, _, _ := s.Upsert("missing-req|go.mod", "finding", "builder", []string{"a", "b", "c"})
	if err := s.SetDraft(p.ID, "run go mod init first", "init-go-workspace", "# init\nsteps"); err != nil {
		t.Fatal(err)
	}
	ap, err := s.Approve(p.ID)
	if err != nil || ap.Status != StatusApproved || ap.Guidance == "" {
		t.Fatalf("approve: %+v err=%v", ap, err)
	}
	if r, _ := s.RecordRecurrence("missing-req|go.mod"); r != nil {
		t.Fatal("first post-promotion recurrence must not reopen")
	}
	r, err := s.RecordRecurrence("missing-req|go.mod")
	if err != nil || r == nil || r.Status != StatusPending || r.Supersedes != ap.ID {
		t.Fatalf("second recurrence should reopen as revision: %+v err=%v", r, err)
	}
	if r2, _ := s.RecordRecurrence("nope|nothing"); r2 != nil {
		t.Fatal("unknown signature must be a no-op")
	}
}

// TestRecordRecurrenceDoesNotStackPendingRevisions reproduces review finding
// #4: RecordRecurrence used to clone a new pending revision every 2
// recurrences with no check for an existing pending revision, so an approved
// proposal that kept recurring accumulated an unbounded pile of duplicate
// pending rows. Driving 4+ recurrences (two threshold-crossings) must leave
// exactly ONE pending revision.
func TestRecordRecurrenceDoesNotStackPendingRevisions(t *testing.T) {
	s := open(t)
	p, _, _ := s.Upsert("missing-req|go.mod", "finding", "builder", []string{"a"})
	if _, err := s.Approve(p.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := s.RecordRecurrence("missing-req|go.mod"); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := s.List(StatusPending)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("want exactly 1 pending revision after 5 recurrences, got %d: %+v", len(pending), pending)
	}
	if pending[0].Supersedes != p.ID {
		t.Fatalf("pending revision supersedes = %d, want %d", pending[0].Supersedes, p.ID)
	}
}
