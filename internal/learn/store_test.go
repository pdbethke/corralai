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
	p2, created2, _ := s.Upsert("missing-req|go.mod", "finding", "builder", []string{"e4"})
	if created2 || p2.ID != p.ID || p2.Count != 4 {
		t.Fatalf("second upsert should bump, not create: %+v created=%v", p2, created2)
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
