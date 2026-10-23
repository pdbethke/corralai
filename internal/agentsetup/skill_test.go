// SPDX-License-Identifier: Elastic-2.0

package agentsetup

import (
	"os"
	"path/filepath"
	"testing"
)

var testFiles = map[string][]byte{"SKILL.md": []byte("hello"), "ref/a.md": []byte("a")}

func TestWriteSkillThenManaged(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "corral")
	if got := skillState(dir); got != "absent" {
		t.Fatalf("before: %s", got)
	}
	if err := WriteSkill(dir, "v1", testFiles); err != nil {
		t.Fatal(err)
	}
	if got := skillState(dir); got != "managed" {
		t.Fatalf("after: %s", got)
	}
	m, ok := readMarker(dir)
	if !ok || m.Version != "v1" || len(m.Files) != 2 {
		t.Fatalf("marker %+v", m)
	}
	for rel, b := range testFiles {
		if m.Files[rel] != sum(b) {
			t.Errorf("hash of %s", rel)
		}
	}
	// refresh drops files the new version no longer carries
	if err := WriteSkill(dir, "v2", map[string][]byte{"SKILL.md": []byte("new")}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ref")); !os.IsNotExist(err) {
		t.Errorf("stale file left: %v", err)
	}
	if skillState(dir) != "managed" {
		t.Error("refreshed copy not managed")
	}
}

func TestEditedSkillIsUnmanagedAndNotOverwritten(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "corral")
	if err := WriteSkill(dir, "v1", testFiles); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "SKILL.md")
	if err := os.WriteFile(p, []byte("edited"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := skillState(dir); got != "unmanaged" {
		t.Fatalf("state %s", got)
	}
	if err := WriteSkill(dir, "v2", testFiles); err == nil {
		t.Fatal("overwrote an edited copy")
	}
	if err := RemoveSkill(dir); err == nil {
		t.Fatal("removed an edited copy")
	}
	if b, _ := os.ReadFile(p); string(b) != "edited" {
		t.Errorf("edit lost: %q", b)
	}
}

func TestUnmarkedDirIsUnmanaged(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("mine"), 0o600); err != nil {
		t.Fatal(err)
	}
	if skillState(dir) != "unmanaged" || WriteSkill(dir, "v", testFiles) == nil {
		t.Fatal("took over someone else's skill")
	}
}

func TestSymlinkSkillDirRefused(t *testing.T) {
	base := t.TempDir()
	target := filepath.Join(base, "elsewhere")
	if err := os.Mkdir(target, 0o750); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(base, "corral")
	if err := os.Symlink(target, dir); err != nil {
		t.Fatal(err)
	}
	if got := skillState(dir); got != "refused-symlink" {
		t.Fatalf("state %s", got)
	}
	if err := WriteSkill(dir, "v", testFiles); err == nil {
		t.Fatal("wrote through a symlink")
	}
	if es, _ := os.ReadDir(target); len(es) != 0 {
		t.Errorf("target touched: %v", es)
	}
}

func TestRemoveSkillManaged(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "corral")
	if err := WriteSkill(dir, "v1", testFiles); err != nil {
		t.Fatal(err)
	}
	if err := RemoveSkill(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("dir remains: %v", err)
	}
	if err := RemoveSkill(dir); err != nil {
		t.Errorf("absent dir should be a no-op: %v", err)
	}
}

func TestWriteSkillRefusesBadNames(t *testing.T) {
	for _, n := range []string{"../x", "/abs", MarkerName, "a/../b"} {
		if err := WriteSkill(filepath.Join(t.TempDir(), "c"), "v", map[string][]byte{n: nil}); err == nil {
			t.Errorf("accepted %q", n)
		}
	}
}
