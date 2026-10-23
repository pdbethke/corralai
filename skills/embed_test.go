// SPDX-License-Identifier: Elastic-2.0

package skills

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestCorralSkillMatchesTree: the embedded copy is the tree on disk, file for
// file and byte for byte. It is walked from disk, not from a list, so a file
// added to skills/corral is covered without anyone remembering to add it here.
func TestCorralSkillMatchesTree(t *testing.T) {
	got, err := Corral()
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	err = filepath.WalkDir("corral", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel("corral", p)
		rel = filepath.ToSlash(rel)
		want, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		seen++
		b, ok := got[rel]
		if !ok {
			t.Errorf("%s is on disk but not embedded", rel)
		} else if !bytes.Equal(b, want) {
			t.Errorf("%s differs between disk and the embedded copy", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if seen == 0 {
		t.Fatal("walked no files under skills/corral; the test would pass on nothing")
	}
	if len(got) != seen {
		t.Errorf("embedded %d files, disk has %d", len(got), seen)
	}
	if _, ok := got["SKILL.md"]; !ok {
		t.Error("SKILL.md missing from the embedded skill")
	}
}
