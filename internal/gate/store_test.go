// SPDX-License-Identifier: Elastic-2.0

package gate

import (
	"path/filepath"
	"testing"
)

func TestGateStoreSaveAndGetBySHA(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "gate.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if _, ok, _ := s.GetBySHA("o/r", "abc"); ok {
		t.Fatal("expected not found before save")
	}
	if err := s.Save(Run{Repo: "o/r", HeadSHA: "abc", PR: 7, Passed: true, RecordID: 42}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetBySHA("o/r", "abc")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	if !got.Passed || got.PR != 7 || got.RecordID != 42 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}
