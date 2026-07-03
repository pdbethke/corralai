// SPDX-License-Identifier: Elastic-2.0

package artifacts

import (
	"path/filepath"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "a.sqlite3"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPutPullRoundTrip(t *testing.T) {
	s := open(t)
	if s.HeadRev() != 0 || s.Count() != 0 {
		t.Fatal("fresh store should be empty at rev 0")
	}
	r1, sha1, err := s.Put("skills/deploy/SKILL.md", []byte("# deploy"), "boss@x.com", 0)
	if err != nil || r1 != 1 {
		t.Fatalf("put1: rev=%d err=%v", r1, err)
	}
	if sha1 != Sha([]byte("# deploy")) {
		t.Fatal("sha mismatch")
	}
	r2, _, _ := s.Put("hooks/branch-guard.sh", []byte("#!/bin/sh"), "boss@x.com", 0)
	if r2 != 2 {
		t.Fatalf("put2 rev=%d", r2)
	}
	if s.Count() != 2 {
		t.Fatalf("count=%d", s.Count())
	}
	// Kind derived from path prefix.
	a, _ := s.Get("hooks/branch-guard.sh")
	if a == nil || a.Kind != "hook" {
		t.Fatalf("expected hook kind, got %#v", a)
	}
	// Incremental pull: only changes after rev 1.
	ch, _ := s.Changes(1)
	if len(ch) != 1 || ch[0].Path != "hooks/branch-guard.sh" {
		t.Fatalf("incremental pull wrong: %#v", ch)
	}
}

func TestPutUnchangedKeepsRev(t *testing.T) {
	s := open(t)
	r1, _, _ := s.Put("skills/a/SKILL.md", []byte("x"), "me", 0)
	r2, _, _ := s.Put("skills/a/SKILL.md", []byte("x"), "me", 0) // identical
	if r1 != r2 {
		t.Fatalf("identical re-put should keep rev: %d vs %d", r1, r2)
	}
	r3, _, _ := s.Put("skills/a/SKILL.md", []byte("y"), "me", 0) // changed
	if r3 <= r2 {
		t.Fatalf("changed content should bump rev: %d -> %d", r2, r3)
	}
}

func TestChangesPrefix(t *testing.T) {
	s := open(t)
	s.Put("skills/roles/tester/a/SKILL.md", []byte("a"), "me", 0)
	s.Put("skills/roles/deployer/b/SKILL.md", []byte("b"), "me", 0)
	s.Put("skills/global/c/SKILL.md", []byte("c"), "me", 0)
	ch, _ := s.ChangesPrefix(0, "skills/roles/tester/")
	if len(ch) != 1 || ch[0].Path != "skills/roles/tester/a/SKILL.md" {
		t.Fatalf("role prefix should isolate one file: %#v", ch)
	}
	if all, _ := s.ChangesPrefix(0, ""); len(all) != 3 {
		t.Fatalf("empty prefix should return all, got %d", len(all))
	}
}

func TestDeleteTombstone(t *testing.T) {
	s := open(t)
	s.Put("skills/x/SKILL.md", []byte("x"), "me", 0)
	rev, ok, _ := s.Delete("skills/x/SKILL.md", "me")
	if !ok || rev != 2 {
		t.Fatalf("delete: ok=%v rev=%d", ok, rev)
	}
	if a, _ := s.Get("skills/x/SKILL.md"); a != nil {
		t.Fatal("deleted artifact should not Get")
	}
	if s.Count() != 0 {
		t.Fatalf("live count after delete=%d", s.Count())
	}
	// Tombstone still propagates via Changes.
	ch, _ := s.Changes(0)
	if len(ch) != 1 || !ch[0].Deleted {
		t.Fatalf("tombstone should appear in pull: %#v", ch)
	}
	// Deleting again is a no-op.
	if _, ok, _ := s.Delete("skills/x/SKILL.md", "me"); ok {
		t.Fatal("re-delete should be a no-op")
	}
}
