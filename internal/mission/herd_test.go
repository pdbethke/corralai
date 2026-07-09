// SPDX-License-Identifier: Elastic-2.0

package mission

import (
	"path/filepath"
	"testing"

	"github.com/pdbethke/corralai/internal/rolemodel"
)

func TestHerdRoundTrip(t *testing.T) {
	m, err := Open(filepath.Join(t.TempDir(), "m.sqlite3"))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()

	// Absent herd → not found, no error.
	if _, ok, err := m.Herd(7); err != nil || ok {
		t.Fatalf("absent herd: ok=%v err=%v, want false/nil", ok, err)
	}

	want := Herd{
		RoleModels:  map[string]rolemodel.ModelRef{"builder": {Backend: "anthropic", Model: "claude-opus"}},
		Endpoints:   []string{"prod-db"},
		LookbookIDs: []int64{3, 9},
	}
	if err := m.SaveHerd(7, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := m.Herd(7)
	if err != nil || !ok {
		t.Fatalf("Herd(7): ok=%v err=%v", ok, err)
	}
	if got.RoleModels["builder"].Backend != "anthropic" || got.RoleModels["builder"].Model != "claude-opus" {
		t.Fatalf("role_models round-trip wrong: %+v", got.RoleModels)
	}
	if len(got.Endpoints) != 1 || got.Endpoints[0] != "prod-db" {
		t.Fatalf("endpoints round-trip wrong: %+v", got.Endpoints)
	}
	if len(got.LookbookIDs) != 2 || got.LookbookIDs[0] != 3 || got.LookbookIDs[1] != 9 {
		t.Fatalf("lookbook_ids round-trip wrong: %+v", got.LookbookIDs)
	}

	// Save again (upsert) → overwrites, still one row.
	if err := m.SaveHerd(7, Herd{Endpoints: []string{"other"}}); err != nil {
		t.Fatal(err)
	}
	got2, _, _ := m.Herd(7)
	if len(got2.Endpoints) != 1 || got2.Endpoints[0] != "other" || len(got2.LookbookIDs) != 0 {
		t.Fatalf("upsert did not overwrite: %+v", got2)
	}
}

func TestHerdIsEmpty(t *testing.T) {
	if !(Herd{}).IsEmpty() {
		t.Fatal("zero-value herd should be empty")
	}
	if (Herd{RoleModels: map[string]rolemodel.ModelRef{"builder": {Backend: "anthropic", Model: "claude-opus"}}}).IsEmpty() {
		t.Fatal("herd with role models should not be empty")
	}
	if (Herd{Endpoints: []string{"prod-db"}}).IsEmpty() {
		t.Fatal("herd with endpoints should not be empty")
	}
	if (Herd{LookbookIDs: []int64{3}}).IsEmpty() {
		t.Fatal("herd with lookbook ids should not be empty")
	}
}
