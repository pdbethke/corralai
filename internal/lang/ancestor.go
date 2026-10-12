// SPDX-License-Identifier: Elastic-2.0

package lang

import (
	"os"
	"path/filepath"
)

// nearestAncestorWith walks up from start (inclusive) and returns the first
// directory that holds any of names, looking at no more than maxDepth
// directories (start counts as the first). It is the one upward walk the
// language plugins share: JavaScript looks for a lint config, Rust for the
// Cargo.toml that owns a file. The bound is a parameter because each caller
// knows its own limit — a fixed constant here would be the wrong limit for one
// of them. ok is false when none of the directories examined holds a name.
func nearestAncestorWith(start string, names []string, maxDepth int) (dir string, ok bool) {
	dir = start
	for depth := 0; depth < maxDepth; depth++ {
		for _, n := range names {
			if _, err := os.Stat(filepath.Join(dir, n)); err == nil {
				return dir, true
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", false
}
