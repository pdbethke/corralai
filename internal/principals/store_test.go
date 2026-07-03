// SPDX-License-Identifier: Elastic-2.0

package principals

import (
	"path/filepath"
	"testing"
)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "p.sqlite3"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestEmptyIsOpen(t *testing.T) {
	s := open(t)
	if !s.Allowed("anyone@x.com") {
		t.Fatal("empty table should allow anyone (dev-open)")
	}
	if !s.IsSuperuser("anyone@x.com") {
		t.Fatal("no superuser yet should be open (pre-bootstrap)")
	}
}

func TestSeedAndStrict(t *testing.T) {
	s := open(t)
	n, err := s.Seed([]string{"Boss@X.com"}, []string{"member@x.com"})
	if err != nil || n != 2 {
		t.Fatalf("seed: n=%d err=%v", n, err)
	}
	if s.Count() != 2 || s.SuperuserCount() != 1 {
		t.Fatalf("counts: total=%d su=%d", s.Count(), s.SuperuserCount())
	}
	// Case-insensitive + strict now that the table is populated.
	if !s.Allowed("boss@x.com") || !s.IsSuperuser("BOSS@x.com") {
		t.Fatal("seeded superuser should be allowed + superuser (case-insensitive)")
	}
	if !s.Allowed("member@x.com") || s.IsSuperuser("member@x.com") {
		t.Fatal("member should be allowed but NOT superuser")
	}
	if s.Allowed("stranger@x.com") {
		t.Fatal("stranger must be rejected once table is populated")
	}
}

func TestSeedPromotesExistingMember(t *testing.T) {
	s := open(t)
	// Seed a real superuser first so the admin gate is strict (otherwise, with zero
	// superusers, IsSuperuser is open by design — the pre-bootstrap state).
	if _, err := s.Seed([]string{"boss@x.com"}, []string{"u@x.com"}); err != nil {
		t.Fatal(err)
	}
	if s.IsSuperuser("u@x.com") {
		t.Fatal("member should not be superuser while a superuser exists")
	}
	if _, err := s.Seed([]string{"u@x.com"}, nil); err != nil {
		t.Fatal(err)
	}
	if !s.IsSuperuser("u@x.com") {
		t.Fatal("re-seeding as superuser should promote the existing member")
	}
}

func TestCreateSuperuserAndManage(t *testing.T) {
	s := open(t)
	if err := s.CreateSuperuser("a@x.com", "bootstrap"); err != nil {
		t.Fatal(err)
	}
	if !s.IsSuperuser("a@x.com") || s.SuperuserCount() != 1 {
		t.Fatal("create_superuser should make a@x.com a superuser")
	}
	// Add a member, promote, demote, remove.
	if err := s.AddMember("b@x.com", "a@x.com"); err != nil {
		t.Fatal(err)
	}
	if s.IsSuperuser("b@x.com") {
		t.Fatal("new member is not a superuser")
	}
	if ok, _ := s.SetSuperuser("b@x.com", true); !ok || !s.IsSuperuser("b@x.com") {
		t.Fatal("promote failed")
	}
	if ok, _ := s.SetSuperuser("b@x.com", false); !ok || s.IsSuperuser("b@x.com") {
		t.Fatal("demote failed")
	}
	if ok, _ := s.SetSuperuser("ghost@x.com", true); ok {
		t.Fatal("set_superuser on a non-existent principal should report false")
	}
	if ok, _ := s.Remove("b@x.com"); !ok || s.Allowed("b@x.com") {
		t.Fatal("remove failed")
	}
}
