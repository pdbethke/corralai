// SPDX-License-Identifier: Elastic-2.0

package gate

import (
	"path/filepath"
	"testing"
	"time"
)

func TestGateStoreSaveAndGetByHead(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "gate.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, ok, _ := s.GetByHead("o/r", "abc", ""); ok {
		t.Fatal("expected not found before save")
	}
	if err := s.Save(Run{Repo: "o/r", HeadSHA: "abc", PR: 7, Passed: true, RecordID: 42}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetByHead("o/r", "abc", "")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if !got.Passed || got.PR != 7 || got.RecordID != 42 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

// TestListBySHAReturnsEveryCheck: a head gated by two policies has two
// answers, and ListBySHA returns both — never "whichever ran last", which is
// what GetBySHA, its predecessor, did. (Review 8be2189163b0, R7.)
func TestListBySHAReturnsEveryCheck(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "gate.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if runs, err := s.ListBySHA("o/r", "abc"); err != nil || len(runs) != 0 {
		t.Fatalf("an ungated head lists nothing: %v %v", runs, err)
	}
	_ = s.Save(Run{Repo: "o/r", HeadSHA: "abc", Context: "corral/test", Passed: true, RecordID: 2, RanAt: time.Unix(20, 0)})
	_ = s.Save(Run{Repo: "o/r", HeadSHA: "abc", Context: "corral/lint", Passed: false, RecordID: 1, RanAt: time.Unix(10, 0)})
	runs, err := s.ListBySHA("o/r", "abc")
	if err != nil || len(runs) != 2 || runs[0].Context != "corral/lint" || runs[0].Passed || runs[1].Context != "corral/test" || !runs[1].Passed {
		t.Fatalf("ListBySHA = %+v, %v; want both checks, ordered by context", runs, err)
	}
}
