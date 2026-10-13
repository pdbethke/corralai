// SPDX-License-Identifier: Elastic-2.0

package memory

import (
	"path/filepath"
	"testing"
)

// isolate points every default-path input at temp dirs so a developer's real
// HOME (and any legacy state under it) is never consulted.
func isolate(t *testing.T) string {
	t.Helper()
	st := t.TempDir()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("XDG_CACHE_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("CORRALAI_MEMORY_DIR", "")
	t.Setenv("CORRALAI_STATE_DIR", st)
	return st
}

// An entry written with no CORRALAI_MEMORY_DIR used to land in a directory
// Build(nil) never indexed: saved, then unfindable. Add and Build now share
// one rule.
func TestAddedEntryIsFoundWithNoMemoryDirSet(t *testing.T) {
	isolate(t)
	s, err := Open(filepath.Join(t.TempDir(), "m.duckdb"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, _, _, err := s.Add("zebracorn-note", "a note about zebracorn", "d", "reference", "default", "", true, "tester"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Build(nil); err != nil {
		t.Fatal(err)
	}
	hits, err := s.Search("zebracorn", "", "", 10, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 {
		t.Fatal("an entry added with no CORRALAI_MEMORY_DIR is not found by search")
	}
}

func TestDirDefaultsUnderState(t *testing.T) {
	st := isolate(t)
	if d, err := Dir(); err != nil || d != filepath.Join(st, "memory") {
		t.Fatalf("got %q, %v", d, err)
	}
	t.Setenv("CORRALAI_MEMORY_DIR", "/m")
	if d, _ := Dir(); d != "/m" {
		t.Fatalf("override: got %q", d)
	}
}
