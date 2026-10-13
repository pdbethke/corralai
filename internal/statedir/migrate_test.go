// SPDX-License-Identifier: Elastic-2.0

package statedir

import (
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"testing"
)

func treeHash(t *testing.T, root string) string {
	t.Helper()
	var lines []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, _ := os.ReadFile(p)
		info, _ := d.Info()
		lines = append(lines, p+" "+info.Mode().String()+" "+string(b))
		return nil
	})
	sort.Strings(lines)
	h := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return string(h[:])
}

func setupMigration(t *testing.T) (home, legacy, state string) {
	home = t.TempDir()
	legacy = filepath.Join(home, ".claude")
	state = filepath.Join(home, "st", "corral") // does not exist yet
	fakeHost(t, "linux", map[string]string{"CORRALAI_STATE_DIR": state}, home)
	writeFile(t, filepath.Join(legacy, "corralai_certify_key"))
	writeFile(t, filepath.Join(legacy, "corralai_coord.sqlite3"))
	writeFile(t, filepath.Join(legacy, "corralai_coord.sqlite3-wal"))
	writeFile(t, filepath.Join(legacy, "unrelated.txt"))
	return
}

func TestMigrateMovesRowsAndSidecarsOnly(t *testing.T) {
	_, legacy, state := setupMigration(t)
	moves, conflicts, err := PlanMigration(legacy)
	if err != nil || len(conflicts) != 0 || len(moves) != 2 {
		t.Fatalf("plan: %d moves, %d conflicts, %v", len(moves), len(conflicts), err)
	}
	if err := ApplyMigration(moves); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"corralai_certify_key", "corralai_coord.sqlite3", "corralai_coord.sqlite3-wal"} {
		if _, err := os.Stat(filepath.Join(state, n)); err != nil {
			t.Errorf("%s not moved: %v", n, err)
		}
		if _, err := os.Stat(filepath.Join(legacy, n)); !os.IsNotExist(err) {
			t.Errorf("%s still at the legacy path", n)
		}
	}
	if _, err := os.Stat(filepath.Join(legacy, "unrelated.txt")); err != nil {
		t.Error("migrate touched a file that is not a table row")
	}
	info, _ := os.Stat(filepath.Join(state, "corralai_certify_key"))
	if info.Mode().Perm() != 0o600 {
		t.Errorf("key mode %v, want 0600", info.Mode().Perm())
	}
	dinfo, _ := os.Stat(state)
	if dinfo.Mode().Perm() != 0o700 {
		t.Errorf("state dir mode %v, want 0700", dinfo.Mode().Perm())
	}
	again, _, _ := PlanMigration(legacy)
	if len(again) != 0 {
		t.Errorf("second plan should move nothing, got %d", len(again))
	}
}

func TestMigrateCrossFilesystemCopiesVerifiesAndRemoves(t *testing.T) {
	_, legacy, state := setupMigration(t)
	old := rename
	rename = func(a, b string) error { return &os.LinkError{Op: "rename", Old: a, New: b, Err: syscall.EXDEV} }
	t.Cleanup(func() { rename = old })
	moves, _, _ := PlanMigration(legacy)
	if err := ApplyMigration(moves); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(filepath.Join(state, "corralai_certify_key")); string(b) != "x" {
		t.Fatalf("copied content %q", b)
	}
	if _, err := os.Stat(filepath.Join(legacy, "corralai_certify_key")); !os.IsNotExist(err) {
		t.Fatal("source not removed after a verified copy")
	}
}

func TestMigrateConflictTouchesNothing(t *testing.T) {
	home, legacy, state := setupMigration(t)
	writeFile(t, filepath.Join(state, "corralai_certify_key"))
	before := treeHash(t, home)
	_, conflicts, err := PlanMigration(legacy)
	if err != nil || len(conflicts) != 1 || conflicts[0].File != CertifyKey {
		t.Fatalf("want one CertifyKey conflict, got %+v, %v", conflicts, err)
	}
	if treeHash(t, home) != before {
		t.Fatal("planning changed the tree")
	}
}

func TestMigrateRefusesASymlinkedSource(t *testing.T) {
	home := t.TempDir()
	legacy := filepath.Join(home, ".claude")
	fakeHost(t, "linux", map[string]string{"CORRALAI_STATE_DIR": filepath.Join(home, "st")}, home)
	real := filepath.Join(home, "dotfiles", "key")
	writeFile(t, real)
	if err := os.MkdirAll(legacy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, filepath.Join(legacy, "corralai_certify_key")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := PlanMigration(legacy); err == nil || !strings.Contains(err.Error(), real) {
		t.Fatalf("want a refusal naming the link target, got %v", err)
	}
}

func TestPlanChangesNothing(t *testing.T) {
	home, legacy, _ := setupMigration(t)
	before := treeHash(t, home)
	if _, _, err := PlanMigration(legacy); err != nil {
		t.Fatal(err)
	}
	if treeHash(t, home) != before {
		t.Fatal("planning changed the tree")
	}
}

func TestMigrateStaleDestinationSidecarIsAConflict(t *testing.T) {
	_, legacy, state := setupMigration(t)
	writeFile(t, filepath.Join(state, "corralai_coord.sqlite3-wal"))
	moves, conflicts, err := PlanMigration(legacy)
	if err != nil || len(conflicts) != 1 {
		t.Fatalf("want one sidecar conflict, got %+v, %v", conflicts, err)
	}
	for _, m := range moves {
		if m.File.Name == "corralai_coord.sqlite3" {
			t.Fatal("the database row must not move while its destination WAL is stale")
		}
	}
}
