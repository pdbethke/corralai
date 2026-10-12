// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
	"testing"
)

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestNearestAncestorWith(t *testing.T) {
	root := t.TempDir()
	touch(t, filepath.Join(root, "a", "marker"))
	touch(t, filepath.Join(root, "a", "b", "c", "other"))
	start := filepath.Join(root, "a", "b", "c")

	cases := []struct {
		name     string
		names    []string
		maxDepth int
		want     string
		ok       bool
	}{
		{"found in the start dir", []string{"other"}, 8, start, true},
		{"found in an ancestor", []string{"marker"}, 8, filepath.Join(root, "a"), true},
		{"any listed name matches", []string{"nope", "marker"}, 8, filepath.Join(root, "a"), true},
		{"the walk is bounded: start + 2 parents is 3 dirs", []string{"marker"}, 3, filepath.Join(root, "a"), true},
		{"a bound too small to reach it finds nothing", []string{"marker"}, 2, "", false},
		{"a zero bound checks nothing", []string{"other"}, 0, "", false},
		{"absent everywhere", []string{"missing"}, 64, "", false},
	}
	for _, c := range cases {
		got, ok := nearestAncestorWith(start, c.names, c.maxDepth)
		if ok != c.ok || got != c.want {
			t.Errorf("%s: got (%q,%v), want (%q,%v)", c.name, got, ok, c.want, c.ok)
		}
	}
}
